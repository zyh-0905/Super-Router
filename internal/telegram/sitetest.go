package telegram

// 站点直达测试（测试台「站点测试」的 Telegram 版）：
// 余额前 → 非流式聊天 → 余额中 → 流式聊天 → 余额后 → 余额差反推实测倍率。
//
// 与 Gateway 的 SiteTestHandler（internal/api/sitetest.go）同口径：
//   - 复用 quality.RunChat（协议转换/超时/SSE 解析）与 ProbeChecker 余额多协议探测
//     （含 Sub2API 自动登录、GET→POST 回退等）；
//   - 结果仅展示、不落库、不占探测预算、不更新熔断样本；
//   - 与定时/手动探针共用同一把站点级 advisory lock，余额差测量串行化；
//   - 凭据只在进程内存中使用，错误信息不包含上游响应体与凭据（quality.RunChat 已保证）。
//
// 执行成本：两次小型聊天请求（非流式+流式），可能产生少量上游费用，
// 与 Web 测试台一致；执行由 Telegram 命令触发、异步推送结果。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/quality"
	"smart-router/internal/safenet"
	"smart-router/internal/store"

	"go.uber.org/zap"
)

const (
	siteTestChatTimeout    = 60 * time.Second // 站点未配置超时时的兜底
	siteTestMaxChatTimeout = 2 * time.Minute  // 站点配置值的安全上限
	siteTestDefaultTokens  = 128              // max_tokens 未指定时的默认值
	siteTestMaxTokens      = 512              // max_tokens 安全上限
	siteTestTotalBudget    = 6 * time.Minute  // 异步执行总闸（余额×3 + 聊天×2 的上界，防 goroutine 悬挂）
	siteTestSendBudget     = 15 * time.Second // 异步结果推送预算（独立于执行 ctx）
)

// SiteTestRunner 站点测试执行器。
type SiteTestRunner struct {
	db        *store.DB
	probe     *checker.ProbeChecker // 复用 Gateway 同款的余额读取 + 站点探测锁
	cryptoKey string                // 上游凭据信封加密密钥（enc:v1:，可为空 = 明文）
	logger    *zap.Logger
	opts      safenet.Options
	once      sync.Once
	client    *http.Client
}

// NewSiteTestRunner 创建站点测试执行器（nil logger 退化为 Nop）。
func NewSiteTestRunner(db *store.DB, probe *checker.ProbeChecker, cryptoKey string, logger *zap.Logger, opts safenet.Options) *SiteTestRunner {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SiteTestRunner{db: db, probe: probe, cryptoKey: cryptoKey, logger: logger, opts: opts}
}

// testClient 返回带 SSRF 重定向校验的懒构建客户端（与 quality.Executor 同口径）。
func (r *SiteTestRunner) testClient() *http.Client {
	r.once.Do(func() {
		c := &http.Client{Timeout: siteTestMaxChatTimeout, Transport: http.DefaultTransport}
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return safenet.ValidateRedirect(req.URL.String(), r.opts)
		}
		r.client = c
	})
	return r.client
}

// Preflight 快速校验（命令同步阶段调用，失败立即回复，不启动异步任务）：
// 站点存在且启用 → 分组授权 → 模型解析（参数 → test_model → 映射首键）。
// 返回站点展示名、解析后的模型与上游侧模型名（供确认消息与重测按钮使用）。
func (r *SiteTestRunner) Preflight(ctx context.Context, channelID int, model string, groupIDs []int) (name, resolvedModel, upstreamModel string, err error) {
	upstream, mapping, model, err := r.validate(ctx, channelID, model, groupIDs)
	if err != nil {
		return "", "", "", err
	}
	um := mapping[model]
	if um == "" {
		um = model
	}
	return upstream.Name, model, um, nil
}

// Run 执行一次站点测试，返回格式化好的 Telegram HTML 消息。
// 模型与 max_tokens 来自 /sitetest 命令参数（默认 test_model/128）。
func (r *SiteTestRunner) Run(ctx context.Context, channelID int, model string, maxTokens int, groupIDs []int) (string, error) {
	upstream, mapping, model, err := r.validate(ctx, channelID, model, groupIDs)
	if err != nil {
		return "", err
	}
	upstreamModel := mapping[model]
	if upstreamModel == "" {
		upstreamModel = model
	}
	maxTokens = clampSiteTestTokens(maxTokens)

	// 与定时/手动探针共用站点级 advisory lock：余额差测量串行化，防并发扣费串扰
	unlock, err := r.probe.AcquireChannelLock(ctx, channelID)
	if err != nil {
		return "", fmt.Errorf("获取站点测试锁失败，请稍后重试")
	}
	defer unlock()

	rep := r.runTest(ctx, upstream, model, upstreamModel, maxTokens)
	return formatSiteTestReport(rep), nil
}

// validate 站点加载/授权/模型解析公共校验（Preflight 与 Run 共用）。
func (r *SiteTestRunner) validate(ctx context.Context, channelID int, model string, groupIDs []int) (*checker.Upstream, map[string]string, string, error) {
	upstream, mapping, err := r.loadUpstream(ctx, channelID)
	if err != nil {
		return nil, nil, "", fmt.Errorf("站点不存在")
	}
	if !upstream.Enabled {
		return nil, nil, "", fmt.Errorf("站点已禁用")
	}
	if !r.channelInGroups(ctx, channelID, groupIDs) {
		return nil, nil, "", fmt.Errorf("无权测试该站点")
	}
	model = resolveSiteTestModel(model, upstream, mapping)
	if _, ok := mapping[model]; !ok {
		return nil, nil, "", fmt.Errorf("模型 %q 不在该站点的模型映射中", model)
	}
	if err := safenet.ValidateUpstreamURL(upstream.BaseURL, r.opts); err != nil {
		return nil, nil, "", fmt.Errorf("站点地址校验失败: %v", err)
	}
	return upstream, mapping, model, nil
}

// loadUpstream 加载站点探测所需字段（含凭据解密；字段与 api.SiteTestHandler 同口径）。
func (r *SiteTestRunner) loadUpstream(ctx context.Context, channelID int) (*checker.Upstream, map[string]string, error) {
	var u checker.Upstream
	var mmJSON []byte
	err := r.db.Pool.QueryRow(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol, relay_type, test_model,
		       daily_probe_budget, balance_api_url, balance_api_token,
		       balance_login_email, balance_login_password,
		       timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms,
		       COALESCE(model_mapping::text, '{}')
		FROM upstreams WHERE id = $1
	`, channelID).Scan(
		&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol, &u.RelayType, &u.TestModel,
		&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken,
		&u.BalanceLoginEmail, &u.BalanceLoginPassword,
		&u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
		&mmJSON,
	)
	if err != nil {
		return nil, nil, err
	}
	// 凭据解密（P1-07）：失败即不可测试（避免以错误凭据反复请求上游）
	if err := checker.DecryptCreds(&u, r.cryptoKey); err != nil {
		return nil, nil, fmt.Errorf("decrypt credentials: %w", err)
	}
	mapping := map[string]string{}
	_ = json.Unmarshal(mmJSON, &mapping)
	return &u, mapping, nil
}

// ListModels 返回该站点映射中的模型 key 列表（排序后），供向导点选模型。
// 授权与启用校验与 validate 一致（站点不存在/禁用/无权 → 返回错误）。
func (r *SiteTestRunner) ListModels(ctx context.Context, channelID int, groupIDs []int) ([]string, error) {
	upstream, mapping, err := r.loadUpstream(ctx, channelID)
	if err != nil {
		return nil, fmt.Errorf("站点不存在")
	}
	if !upstream.Enabled {
		return nil, fmt.Errorf("站点已禁用")
	}
	if !r.channelInGroups(ctx, channelID, groupIDs) {
		return nil, fmt.Errorf("无权测试该站点")
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// channelInGroups 校验站点是否在授权分组内（空 = 全部可见，与 query.go 同语义）。
func (r *SiteTestRunner) channelInGroups(ctx context.Context, channelID int, groupIDs []int) bool {
	if len(groupIDs) == 0 {
		return true
	}
	var ok bool
	err := r.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM channel_group_members
			WHERE channel_id = $1 AND group_id = ANY($2)
		)
	`, channelID, groupIDs).Scan(&ok)
	return err == nil && ok
}

// resolveSiteTestModel 模型回退链：请求显式 → 站点 test_model → 映射首个键（字典序最小保证确定性）。
func resolveSiteTestModel(model string, upstream *checker.Upstream, mapping map[string]string) string {
	if model != "" {
		return model
	}
	if upstream.TestModel != "" {
		return upstream.TestModel
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// clampSiteTestTokens 归一化 max_tokens（≤0 → 128，>512 → 512）。
func clampSiteTestTokens(n int) int {
	if n <= 0 {
		return siteTestDefaultTokens
	}
	if n > siteTestMaxTokens {
		return siteTestMaxTokens
	}
	return n
}

// chatTimeout 单次聊天超时：站点 timeout_total_ms 优先（上限 2 分钟），未配置兜底 60s。
func chatTimeout(upstream *checker.Upstream) time.Duration {
	if upstream.TimeoutTotalMS > 0 {
		if t := time.Duration(upstream.TimeoutTotalMS) * time.Millisecond; t > 0 {
			if t > siteTestMaxChatTimeout {
				return siteTestMaxChatTimeout
			}
			return t
		}
	}
	return siteTestChatTimeout
}

// chatOutcome 单次聊天结果（ev 为 nil 时看 err）。
type chatOutcome struct {
	ev  *quality.ChatEvidence
	err error
}

// siteTestReport 站点测试完整结果（格式化前的中性结构，便于测试）。
type siteTestReport struct {
	channelName   string
	protocol      string
	model         string
	upstreamModel string
	maxTokens     int

	balanceOK  bool
	balanceErr string
	before     float64
	mid        float64
	after      float64

	nonStream chatOutcome
	stream    chatOutcome

	costTotal  float64
	ratioOK    bool
	ratioErr   string
	ratio      float64
	basis      string
	estInPerM  float64
	estOutPerM float64
}

// runTest 执行测试序列：各阶段独立容错，任何单阶段失败不阻断其余阶段。
func (r *SiteTestRunner) runTest(ctx context.Context, upstream *checker.Upstream, model, upstreamModel string, maxTokens int) siteTestReport {
	ch := &quality.Channel{
		ID:                 upstream.ID,
		Name:               upstream.Name,
		BaseURL:            upstream.BaseURL,
		Protocol:           upstream.Protocol,
		RelayType:          upstream.RelayType,
		TestModel:          upstream.TestModel,
		APIKey:             upstream.APIKey,
		AccessToken:        upstream.AccessToken,
		TimeoutConnectMS:   upstream.TimeoutConnectMS,
		TimeoutFirstByteMS: upstream.TimeoutFirstByteMS,
		TimeoutTotalMS:     upstream.TimeoutTotalMS,
	}
	scenario := quality.ProbeScenario{
		Model:     upstreamModel,
		Messages:  []quality.ProbeMessage{{Role: "user", Content: "hi"}}, // 与测试台默认消息一致
		MaxTokens: maxTokens,
	}
	timeout := chatTimeout(upstream)

	rep := siteTestReport{
		channelName:   upstream.Name,
		protocol:      upstream.Protocol,
		model:         model,
		upstreamModel: upstreamModel,
		maxTokens:     maxTokens,
	}

	// 余额前
	if v, _, err := r.readBalance(ctx, upstream); err != nil {
		rep.balanceErr = err.Error()
	} else {
		rep.balanceOK = true
		rep.before = v
	}

	// 非流式聊天
	sc := scenario
	sc.Stream = false
	ev, err := quality.RunChat(ctx, ch, sc, timeout, r.testClient())
	rep.nonStream = chatOutcome{ev: ev, err: err}

	// 余额中
	if rep.balanceOK {
		if v, _, err := r.readBalance(ctx, upstream); err != nil {
			rep.balanceOK = false
			rep.balanceErr = err.Error()
		} else {
			rep.mid = v
		}
	}

	// 流式聊天
	sc = scenario
	sc.Stream = true
	ev, err = quality.RunChat(ctx, ch, sc, timeout, r.testClient())
	rep.stream = chatOutcome{ev: ev, err: err}

	// 余额后
	if rep.balanceOK {
		if v, _, err := r.readBalance(ctx, upstream); err != nil {
			rep.balanceOK = false
			rep.balanceErr = err.Error()
		} else {
			rep.after = v
		}
	}

	rep.finalize(ctx, r)
	return rep
}

// readBalance 读取一次余额（多协议探测链；失败只影响余额/倍率区，聊天照跑）。
func (r *SiteTestRunner) readBalance(ctx context.Context, upstream *checker.Upstream) (float64, string, error) {
	res, err := r.probe.ChannelBalance(ctx, *upstream)
	if err != nil {
		return 0, "", err
	}
	currency := res.Currency
	if currency == "" {
		currency = "USD"
	}
	return res.Balance, currency, nil
}

// finalize 计算余额差合计与实测倍率（usage 缺失/余额差为 0 时给出原因而非数字）。
func (rep *siteTestReport) finalize(ctx context.Context, r *SiteTestRunner) {
	if rep.balanceOK {
		rep.costTotal = round4(rep.before - rep.after)
	}
	if !rep.balanceOK {
		rep.ratioErr = "余额读取失败，无法计算倍率"
		return
	}
	if rep.costTotal <= 0 {
		rep.ratioErr = "余额差为 0，无法计算倍率（建议提高 max_tokens 后重试）"
		return
	}
	prompt, completion := 0, 0
	for _, sec := range []chatOutcome{rep.nonStream, rep.stream} {
		if sec.ev != nil && sec.ev.Usage.Present {
			prompt += sec.ev.Usage.PromptTokens
			completion += sec.ev.Usage.CompletionTokens
		}
	}
	if prompt+completion <= 0 {
		rep.ratioErr = "上游未返回 usage，无法计算倍率"
		return
	}
	inPerM, outPerM := r.modelPriceValues(ctx, rep.model)
	rep.ratio, rep.basis = checker.ComputeRealRatio(rep.costTotal, prompt, completion, inPerM, outPerM)
	if rep.basis == checker.BasisOfficial {
		rep.estInPerM = round4(rep.ratio * inPerM)
		rep.estOutPerM = round4(rep.ratio * outPerM)
	}
	rep.ratioOK = true
}

// modelPriceValues 查询模型官方价格（$/1M），未收录返回 0。
func (r *SiteTestRunner) modelPriceValues(ctx context.Context, model string) (float64, float64) {
	var in, out float64
	if r.db == nil || r.db.Pool == nil {
		return 0, 0
	}
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT input_price_per_m, output_price_per_m FROM model_prices WHERE model = $1
	`, model).Scan(&in, &out); err != nil {
		return 0, 0
	}
	return in, out
}

// formatSiteTestReport 渲染 Telegram HTML 报告（动态字段全部转义）。
func formatSiteTestReport(rep siteTestReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧪 <b>站点测试</b>：%s\n", EscapeHTML(rep.channelName))
	b.WriteString("━━━━━━━━━━━━\n")
	fmt.Fprintf(&b, "🔌 协议：%s ｜ 🤖 模型：%s ｜ 🎯 max_tokens：%d\n",
		EscapeHTML(rep.protocol), EscapeHTML(rep.upstreamModel), rep.maxTokens)

	if rep.balanceOK {
		fmt.Fprintf(&b, "💰 余额：$%.4f → $%.4f → $%.4f\n", rep.before, rep.mid, rep.after)
	} else {
		fmt.Fprintf(&b, "💰 余额：✗ %s\n", EscapeHTML(truncateRunesSafe(rep.balanceErr, 120)))
	}
	b.WriteString(formatChatLine("非流式", rep.nonStream))
	b.WriteString("\n")
	b.WriteString(formatChatLine("流式", rep.stream))
	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━\n")

	if rep.balanceOK {
		fmt.Fprintf(&b, "💵 余额差合计：$%.4f\n", rep.costTotal)
	}
	if rep.ratioOK {
		basisLabel := "官网价基准"
		if rep.basis == checker.BasisBaseline {
			basisLabel = "$10/1M 基准估测"
		}
		fmt.Fprintf(&b, "📐 实测倍率：<b>%.2fx</b>（%s）\n", rep.ratio, basisLabel)
		if rep.basis == checker.BasisOfficial {
			fmt.Fprintf(&b, "💱 估算单价：输入 $%.2f/1M ｜ 输出 $%.2f/1M\n", rep.estInPerM, rep.estOutPerM)
		} else {
			b.WriteString("⚠️ 官方价格库未收录该模型，倍率按混合基准估算\n")
		}
	} else {
		fmt.Fprintf(&b, "📐 实测倍率：✗ %s\n", EscapeHTML(rep.ratioErr))
	}
	b.WriteString("💡 再测一次点下方按钮")
	return b.String()
}

// formatChatLine 渲染单次聊天结果行。
func formatChatLine(label string, out chatOutcome) string {
	if out.err != nil {
		return fmt.Sprintf("%s：✗ %s", label, EscapeHTML(truncateRunesSafe(out.err.Error(), 100)))
	}
	ev := out.ev
	if ev == nil {
		return fmt.Sprintf("%s：✗ 无结果", label)
	}
	if ev.HTTPStatus != http.StatusOK {
		return fmt.Sprintf("%s：✗ upstream %d", label, ev.HTTPStatus)
	}
	s := fmt.Sprintf("%s：✅ ⏱ TTFT %dms ｜ 总耗时 %dms", label, ev.TTFBMS, ev.TotalMS)
	if ev.Usage.Present {
		s += fmt.Sprintf(" ｜ tokens %d+%d", ev.Usage.PromptTokens, ev.Usage.CompletionTokens)
	}
	if ev.StreamEvents > 0 {
		s += fmt.Sprintf(" ｜ SSE %d 事件", ev.StreamEvents)
		if ev.DoneReceived {
			s += " ｜ [DONE] ✓"
		} else {
			s += " ｜ [DONE] ✗"
		}
	}
	return s
}

// truncateRunesSafe 按 rune 截断（防多字节字符被截断产生非法 HTML）。
func truncateRunesSafe(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

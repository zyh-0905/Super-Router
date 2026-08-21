// 站点直达测试（测试台 · 站点测试页签）：
// 绕过路由引擎，直达指定站点做两次真实推理（非流式 + 流式），
// 展示延迟/TTFT、余额差（所花余额）与实测倍率。
//
// 设计约束：
//   - 结果仅展示、不落库：不写 request_history / probe_results / 预算 / 熔断样本，
//     不污染路由决策数据；倍率实测仍走「倍率检测」功能。
//   - 与定时/手动探针共用同一把站点级 advisory lock（AcquireChannelLock），
//     余额差测量串行化，防并发扣费串扰。
//   - 各阶段独立容错：余额读取失败只影响余额/倍率区，聊天照跑；
//     聊天失败只影响对应模式区，HTTP 恒 200（诊断型接口，结果即 payload）。
//   - 凭据只在进程内存解密使用，响应不含凭据字段；
//     聊天错误信息不透传上游响应体（quality.RunChat 已保证）。
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/quality"
	"smart-router/internal/safenet"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 站点测试聊天超时与 token 上限（与 quality 检测同口径）。
const (
	siteTestChatTimeout    = 60 * time.Second // 渠道未配置超时时的兜底
	siteTestMaxChatTimeout = 2 * time.Minute  // 渠道配置值的安全上限
	siteTestMaxTokens      = 512              // max_tokens 安全上限
)

// SiteTestHandler 站点直达测试管理接口。
type SiteTestHandler struct {
	db          *store.DB
	probe       *checker.ProbeChecker // 复用 Gateway 内的 ratioProbe（含 BalanceChecker）
	cryptoKey   string                // 上游凭据信封加密密钥（enc:v1:）
	logger      *zap.Logger
	safenetOpts safenet.Options
	clientOnce  sync.Once
	client      *http.Client
}

// NewSiteTestHandler 创建站点测试 Handler。
func NewSiteTestHandler(db *store.DB, probe *checker.ProbeChecker, cryptoKey string, logger *zap.Logger, opts safenet.Options) *SiteTestHandler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &SiteTestHandler{db: db, probe: probe, cryptoKey: cryptoKey, logger: logger, safenetOpts: opts}
}

// siteTestRequest 站点测试请求体（model/message 均可空）。
type siteTestRequest struct {
	Model     string `json:"model"`
	Message   string `json:"message"`
	MaxTokens int    `json:"max_tokens"`
}

// chatSection 单次聊天结果（非流式/流式同构）。
type chatSection struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	Status           int    `json:"status,omitempty"`
	TTFBMS           int    `json:"ttfb_ms,omitempty"`
	TotalMS          int    `json:"total_ms,omitempty"`
	PromptTokens     int    `json:"prompt_tokens,omitempty"`
	CompletionTokens int    `json:"completion_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	UsagePresent     bool   `json:"usage_present"`
	ActualModel      string `json:"actual_model,omitempty"`
	Text             string `json:"text,omitempty"`
	StreamEvents     int    `json:"stream_events,omitempty"` // 仅流式
	DoneReceived     bool   `json:"done_received,omitempty"` // 仅流式
}

// balanceSection 余额差分段结果。
type balanceSection struct {
	OK            bool    `json:"ok"`
	Error         string  `json:"error,omitempty"`
	Currency      string  `json:"currency,omitempty"`
	Before        float64 `json:"before"`
	Mid           float64 `json:"mid"`
	After         float64 `json:"after"`
	CostNonStream float64 `json:"cost_non_stream"`
	CostStream    float64 `json:"cost_stream"`
	CostTotal     float64 `json:"cost_total"`
	Warning       string  `json:"warning,omitempty"`
}

// ratioSection 实测倍率结果。
type ratioSection struct {
	OK                 bool    `json:"ok"`
	Error              string  `json:"error,omitempty"`
	RealRatio          float64 `json:"real_ratio"`
	Basis              string  `json:"basis,omitempty"` // official | baseline
	OfficialInputPerM  float64 `json:"official_input_per_m,omitempty"`
	OfficialOutputPerM float64 `json:"official_output_per_m,omitempty"`
	EstimatedInputPerM float64 `json:"estimated_input_per_m,omitempty"`
	EstimatedOutputPerM float64 `json:"estimated_output_per_m,omitempty"`
	Warning            string  `json:"warning,omitempty"`
}

// testClient 返回带 SSRF 重定向校验的懒构建客户端（与 quality.Executor 同口径）。
func (h *SiteTestHandler) testClient() *http.Client {
	h.clientOnce.Do(func() {
		c := &http.Client{Timeout: siteTestMaxChatTimeout, Transport: http.DefaultTransport}
		c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return safenet.ValidateRedirect(req.URL.String(), h.safenetOpts)
		}
		h.client = c
	})
	return h.client
}

// RunSiteTest POST /admin/channels/:id/site-test
func (h *SiteTestHandler) RunSiteTest(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(400, gin.H{"error": "invalid channel id"})
		return
	}
	var req siteTestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	req.Message = defaultSiteTestMessage(req.Message)
	req.MaxTokens = clampSiteTestTokens(req.MaxTokens)

	upstream, mapping, err := h.loadUpstream(c.Request.Context(), channelID)
	if err != nil {
		c.JSON(404, gin.H{"error": "channel not found"})
		return
	}
	if !upstream.Enabled {
		c.JSON(400, gin.H{"error": "channel disabled"})
		return
	}

	// 模型回退链：请求显式 → 站点 test_model → 映射首个键（与前端预填逻辑一致）
	model := req.Model
	if model == "" {
		model = upstream.TestModel
	}
	if model == "" {
		model = firstMappingKey(mapping)
	}
	if model == "" {
		c.JSON(400, gin.H{"error": "站点未配置默认测试模型，请在请求中指定 model"})
		return
	}
	// 上游模型：有映射则替换，否则原样发送（宽容）
	upstreamModel := model
	if m, ok := mapping[model]; ok && m != "" {
		upstreamModel = m
	}

	// 出站前 SSRF 校验（与网关写入/质量检测同口径）
	if err := safenet.ValidateUpstreamURL(upstream.BaseURL, h.safenetOpts); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	start := time.Now()

	// 站点级 advisory lock：与定时/手动探针共用（余额差测量串行化）
	unlock, err := h.probe.AcquireChannelLock(c.Request.Context(), channelID)
	if err != nil {
		c.JSON(500, gin.H{"error": "获取站点探测锁失败，请稍后重试"})
		return
	}
	defer unlock()

	resp := h.runSiteTest(c.Request.Context(), upstream, model, upstreamModel, req.Message, req.MaxTokens)
	resp["channel_id"] = channelID
	resp["channel_name"] = upstream.Name
	resp["model"] = model
	resp["upstream_model"] = upstreamModel
	resp["protocol"] = upstream.Protocol
	resp["message"] = req.Message
	resp["elapsed_ms"] = int(time.Since(start).Milliseconds())
	c.JSON(200, resp)
}

// runSiteTest 执行完整测试序列（持锁内调用）。各阶段独立容错，HTTP 恒 200。
func (h *SiteTestHandler) runSiteTest(ctx context.Context, upstream *checker.Upstream, model, upstreamModel, message string, maxTokens int) gin.H {
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
		Messages:  []quality.ProbeMessage{{Role: "user", Content: message}},
		MaxTokens: maxTokens,
	}
	timeout := siteTestChatTimeout
	if upstream.TimeoutTotalMS > 0 {
		if t := time.Duration(upstream.TimeoutTotalMS) * time.Millisecond; t > 0 {
			if t > siteTestMaxChatTimeout {
				t = siteTestMaxChatTimeout
			}
			timeout = t
		}
	}

	// 余额前
	resp := gin.H{}
	balanceOK := true
	var balanceErr string
	balanceBefore, currency, err := h.readBalance(ctx, upstream)
	if err != nil {
		balanceOK = false
		balanceErr = err.Error()
	}

	// 非流式聊天
	sc := scenario
	sc.Stream = false
	nonStreamEv, nsErr := quality.RunChat(ctx, ch, sc, timeout, h.testClient())
	nonStream := evidenceToSection(nonStreamEv, nsErr)

	// 余额中
	var balanceMid float64
	if balanceOK {
		if v, _, err := h.readBalance(ctx, upstream); err != nil {
			balanceOK = false
			balanceErr = err.Error()
		} else {
			balanceMid = v
		}
	}

	// 流式聊天
	sc = scenario
	sc.Stream = true
	streamEv, sErr := quality.RunChat(ctx, ch, sc, timeout, h.testClient())
	stream := evidenceToSection(streamEv, sErr)

	// 余额后
	var balanceAfter float64
	if balanceOK {
		if v, _, err := h.readBalance(ctx, upstream); err != nil {
			balanceOK = false
			balanceErr = err.Error()
		} else {
			balanceAfter = v
		}
	}

	resp["non_stream"] = nonStream
	resp["stream"] = stream

	// 余额区
	bal := balanceSection{OK: balanceOK, Error: balanceErr, Currency: currency,
		Before: balanceBefore, Mid: balanceMid, After: balanceAfter}
	if balanceOK {
		bal.CostNonStream = round4(balanceBefore - balanceMid)
		bal.CostStream = round4(balanceMid - balanceAfter)
		bal.CostTotal = round4(balanceBefore - balanceAfter)
		if bal.CostTotal <= 0 {
			bal.Warning = "余额精度不足以区分本次扣费，建议提高 max_tokens 后重测"
		}
	}
	resp["balance"] = bal

	// 倍率区：总花费 ÷ 官网应扣费
	resp["ratio"] = h.buildRatio(ctx, balanceOK, bal.CostTotal, nonStream, stream, model)

	return resp
}

// readBalance 读取一次余额，返回 (金额, 货币, 错误)。
func (h *SiteTestHandler) readBalance(ctx context.Context, upstream *checker.Upstream) (float64, string, error) {
	res, err := h.probe.ChannelBalance(ctx, *upstream)
	if err != nil {
		return 0, "", err
	}
	if res.Currency == "" {
		res.Currency = "USD"
	}
	return res.Balance, res.Currency, nil
}

// evidenceToSection 把 quality.ChatEvidence 转为响应分区。
func evidenceToSection(ev *quality.ChatEvidence, err error) chatSection {
	s := chatSection{}
	if err != nil {
		s.OK = false
		s.Error = truncateSiteTestErr(err.Error(), 200)
		return s
	}
	if ev == nil {
		s.OK = false
		s.Error = "no evidence"
		return s
	}
	s.OK = ev.HTTPStatus == http.StatusOK
	s.Status = ev.HTTPStatus
	s.TTFBMS = ev.TTFBMS
	s.TotalMS = ev.TotalMS
	s.PromptTokens = ev.Usage.PromptTokens
	s.CompletionTokens = ev.Usage.CompletionTokens
	s.TotalTokens = ev.Usage.TotalTokens
	s.UsagePresent = ev.Usage.Present
	s.ActualModel = ev.ActualModel
	s.Text = ev.Text
	s.StreamEvents = ev.StreamEvents
	s.DoneReceived = ev.DoneReceived
	if !s.OK {
		s.Error = fmt.Sprintf("upstream %d", ev.HTTPStatus)
	}
	return s
}

// buildRatio 计算实测倍率（总花费 ÷ 按官网价应扣费）。
func (h *SiteTestHandler) buildRatio(ctx context.Context, balanceOK bool, costTotal float64, nonStream, stream chatSection, model string) ratioSection {
	r := ratioSection{}
	if !balanceOK {
		r.OK = false
		r.Error = "余额读取失败，无法计算倍率"
		return r
	}
	if costTotal <= 0 {
		r.OK = false
		r.Error = "余额差为 0，无法计算倍率（建议提高 max_tokens 后重测）"
		return r
	}
	prompt := 0
	completion := 0
	for _, sec := range []chatSection{nonStream, stream} {
		if sec.OK && sec.UsagePresent {
			prompt += sec.PromptTokens
			completion += sec.CompletionTokens
		}
	}
	if prompt+completion <= 0 {
		r.OK = false
		r.Error = "上游未返回 usage，无法计算倍率"
		return r
	}
	inPerM, outPerM := h.modelPriceValues(ctx, model)
	r.RealRatio, r.Basis = checker.ComputeRealRatio(costTotal, prompt, completion, inPerM, outPerM)
	r.OfficialInputPerM = round4(inPerM)
	r.OfficialOutputPerM = round4(outPerM)
	if r.Basis == checker.BasisOfficial {
		r.EstimatedInputPerM = round4(r.RealRatio * inPerM)
		r.EstimatedOutputPerM = round4(r.RealRatio * outPerM)
	} else {
		r.Warning = "官方价格库未收录该模型，按 $10/1M 混合基准估算倍率"
	}
	r.OK = true
	return r
}

// modelPriceValues 查询模型官方价格（$/1M），未收录返回 0。
func (h *SiteTestHandler) modelPriceValues(ctx context.Context, model string) (float64, float64) {
	var in, out float64
	if h.db == nil || h.db.Pool == nil {
		return 0, 0
	}
	if err := h.db.Pool.QueryRow(ctx, `
		SELECT input_price_per_m, output_price_per_m FROM model_prices WHERE model = $1
	`, model).Scan(&in, &out); err != nil {
		return 0, 0
	}
	return in, out
}

// loadUpstream 加载站点探测所需字段（含凭据解密）。
func (h *SiteTestHandler) loadUpstream(ctx context.Context, channelID int) (*checker.Upstream, map[string]string, error) {
	var u checker.Upstream
	var mmJSON []byte
	err := h.db.Pool.QueryRow(ctx, `
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
	// 凭据解密（P1-07）
	if err := checker.DecryptCreds(&u, h.cryptoKey); err != nil {
		return nil, nil, fmt.Errorf("decrypt credentials: %w", err)
	}
	mapping := map[string]string{}
	_ = json.Unmarshal(mmJSON, &mapping)
	return &u, mapping, nil
}

// 辅助纯函数（测试覆盖）

// defaultSiteTestMessage 消息默认值（空 → "hi"，保留前导/尾随空白之外的原文）。
func defaultSiteTestMessage(msg string) string {
	if msg == "" {
		return "hi"
	}
	return msg
}

// clampSiteTestTokens 归一化 max_tokens（≤0 → 128，>512 → 512）。
func clampSiteTestTokens(n int) int {
	if n <= 0 {
		return 128
	}
	if n > siteTestMaxTokens {
		return siteTestMaxTokens
	}
	return n
}

// firstMappingKey 返回映射的首个键（map 无序，取字典序最小保证确定性）。
func firstMappingKey(mapping map[string]string) string {
	if len(mapping) == 0 {
		return ""
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0]
}

// truncateSiteTestErr 截断错误信息（长度防异常信息撑爆响应）。
func truncateSiteTestErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/store"

	"go.uber.org/zap"
)

type ProbeChecker struct {
	db         *store.DB
	logger     *zap.Logger
	client     *http.Client
	balance    *BalanceChecker // 余额探测（支持自定义余额接口/多协议）
	probeModel string          // 推理探针使用的模型（默认 gpt-4o，可配置）
}

func NewProbeChecker(db *store.DB, logger *zap.Logger) *ProbeChecker {
	return &ProbeChecker{
		db:         db,
		logger:     logger,
		client:     &http.Client{Timeout: 30 * time.Second},
		balance:    NewBalanceChecker(db, logger),
		probeModel: "gpt-4o",
	}
}

// SetProbeModel 设置推理探针模型（空值保持默认）
func (p *ProbeChecker) SetProbeModel(model string) {
	if model != "" {
		p.probeModel = model
	}
}

// ChatCompletionRequest OpenAI 兼容的请求格式
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Usage OpenAI 兼容的用量统计
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionResponse OpenAI 兼容的响应格式
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int         `json:"index"`
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

// ProbeResult 单次推理探测的结构化结果
type ProbeResult struct {
	Model            string  `json:"model"`
	Success          bool    `json:"success"`
	RealRatio        float64 `json:"real_ratio"`
	Basis            string  `json:"basis,omitempty"` // official（相对官网价）| baseline（$10/1M 混合基准）
	OfficialInputPerM  float64 `json:"official_input_per_m,omitempty"`  // 官网输入价 $/1M
	OfficialOutputPerM float64 `json:"official_output_per_m,omitempty"` // 官网输出价 $/1M
	TTFTMS           int     `json:"ttft_ms"`
	Cost             float64 `json:"cost"`
	BalanceBefore    float64 `json:"balance_before"`
	BalanceAfter     float64 `json:"balance_after"`
	TokensUsed       int     `json:"tokens_used"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Stage            string  `json:"stage,omitempty"` // balance_before | chat | balance_after | ok（失败阶段）
	Error            string  `json:"error,omitempty"`
}

// 计价基准常量（probe_results.basis）
const (
	BasisOfficial = "official" // 相对该模型官网价
	BasisBaseline = "baseline" // $10/1M 混合基准（价格库未收录）
)

// computeRealRatio 按 token 拆分计算真实倍率：
// 有官网价 → 实际扣费 ÷ 按官网价应扣费（official）
// 无官网价 → 实际扣费 ÷ tokens ÷ $10/1M（baseline 兜底）
func computeRealRatio(cost float64, promptTokens, completionTokens int, officialInPerM, officialOutPerM float64) (ratio float64, basis string) {
	totalTokens := promptTokens + completionTokens
	if totalTokens <= 0 || cost <= 0 {
		return 0, BasisBaseline
	}
	if officialInPerM > 0 && officialOutPerM > 0 {
		expected := float64(promptTokens)*officialInPerM/1_000_000 + float64(completionTokens)*officialOutPerM/1_000_000
		if expected > 0 {
			return cost / expected, BasisOfficial
		}
	}
	// 兜底：$10/1M 混合基准
	basePrice := 10.0 / 1_000_000
	return cost / float64(totalTokens) / basePrice, BasisBaseline
}

// nullableFloat 将 0 转为 NULL（官网价未提供时入库为 NULL）
func nullableFloat(v float64) interface{} {
	if v <= 0 {
		return nil
	}
	return v
}

// modelPrice 官方模型价格
type modelPrice struct {
	InputPerM  float64
	OutputPerM float64
}

// lookupModelPrice 查询模型官方价格（未收录返回 nil）
func (p *ProbeChecker) lookupModelPrice(ctx context.Context, model string) *modelPrice {
	var in, out float64
	err := p.db.Pool.QueryRow(ctx, `
		SELECT input_price_per_m, output_price_per_m FROM model_prices WHERE model = $1
	`, model).Scan(&in, &out)
	if err != nil {
		return nil
	}
	return &modelPrice{InputPerM: in, OutputPerM: out}
}

// 探测来源常量（probe_results.source）
const (
	ProbeSourceScheduled = "scheduled"
	ProbeSourceManual    = "manual"
)

// Run 执行一轮推理探针
func (p *ProbeChecker) Run(ctx context.Context, globalDailyBudget float64) error {
	epoch, err := p.db.GetCurrentEpoch(ctx)
	if err != nil {
		return fmt.Errorf("get epoch: %w", err)
	}

	p.logger.Info("Starting probe check", zap.Int64("epoch", epoch))

	// 检查今日全局预算
	todaySpent, err := p.getTodaySpent(ctx)
	if err != nil {
		return fmt.Errorf("get today spent: %w", err)
	}

	if todaySpent >= globalDailyBudget {
		p.logger.Warn("Global daily budget exceeded",
			zap.Float64("spent", todaySpent),
			zap.Float64("budget", globalDailyBudget),
		)
		return nil
	}

	p.logger.Info("Budget check",
		zap.Float64("spent", todaySpent),
		zap.Float64("remaining", globalDailyBudget-todaySpent),
	)

	// 读取所有启用的上游
	upstreams, err := p.loadUpstreams(ctx)
	if err != nil {
		return fmt.Errorf("load upstreams: %w", err)
	}

	probeCount := 0
	totalCost := 0.0

	for _, upstream := range upstreams {
		// 检查单站点今日预算
		upstreamSpent, err := p.getUpstreamTodaySpent(ctx, upstream.ID)
		if err != nil {
			p.logger.Warn("Get upstream spent failed", zap.Error(err))
			continue
		}

		if upstreamSpent >= upstream.DailyProbeBudget {
			p.logger.Debug("Upstream daily budget exceeded",
				zap.String("name", upstream.Name),
				zap.Float64("spent", upstreamSpent),
			)
			continue
		}

		// 执行探针
		res, err := p.probeOne(ctx, upstream, epoch, p.probeModel, 16, ProbeSourceScheduled)
		if err != nil {
			p.logger.Error("Probe failed",
				zap.String("name", upstream.Name),
				zap.Error(err),
			)
		} else {
			probeCount++
			totalCost += res.Cost

			// 检查是否超全局预算
			if todaySpent+totalCost >= globalDailyBudget {
				p.logger.Warn("Global budget limit reached during probing")
				break
			}
		}
	}

	p.logger.Info("Probe check completed",
		zap.Int64("epoch", epoch),
		zap.Int("probed", probeCount),
		zap.Float64("cost", totalCost),
	)

	return nil
}

func (p *ProbeChecker) loadUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := p.db.Pool.Query(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role,
		       daily_probe_budget, balance_api_url, balance_api_token, timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms
		FROM upstreams
		WHERE enabled = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var upstreams []Upstream
	for rows.Next() {
		var u Upstream
		if err := rows.Scan(
			&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role,
			&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken, &u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
		); err != nil {
			return nil, err
		}
		upstreams = append(upstreams, u)
	}

	return upstreams, rows.Err()
}

func (p *ProbeChecker) getTodaySpent(ctx context.Context) (float64, error) {
	var spent float64
	err := p.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost), 0)
		FROM probe_results
		WHERE checked_at >= CURRENT_DATE
	`).Scan(&spent)
	return spent, err
}

func (p *ProbeChecker) getUpstreamTodaySpent(ctx context.Context, upstreamID int) (float64, error) {
	var spent float64
	err := p.db.Pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost), 0)
		FROM probe_results
		WHERE upstream_id = $1 AND checked_at >= CURRENT_DATE
	`, upstreamID).Scan(&spent)
	return spent, err
}

// ProbeChannel 对单个上游执行推理探针（分组调度器使用），返回实际成本
func (p *ProbeChecker) ProbeChannel(ctx context.Context, upstream Upstream, epoch int64) (float64, error) {
	res, err := p.probeOne(ctx, upstream, epoch, p.probeModel, 16, ProbeSourceScheduled)
	if err != nil {
		return 0, err
	}
	return res.Cost, nil
}

// ProbeModel 按需探测指定模型（手动实测入口），返回结构化结果
func (p *ProbeChecker) ProbeModel(ctx context.Context, upstream Upstream, epoch int64, model string, maxTokens int, source string) (*ProbeResult, error) {
	if maxTokens <= 0 {
		maxTokens = 64
	}
	return p.probeOne(ctx, upstream, epoch, model, maxTokens, source)
}

// TodaySpent 今日全局探针总花费（分组调度器使用）
func (p *ProbeChecker) TodaySpent(ctx context.Context) (float64, error) {
	return p.getTodaySpent(ctx)
}

// UpstreamTodaySpent 单站点今日探针花费（分组调度器使用）
func (p *ProbeChecker) UpstreamTodaySpent(ctx context.Context, upstreamID int) (float64, error) {
	return p.getUpstreamTodaySpent(ctx, upstreamID)
}

func (p *ProbeChecker) probeOne(ctx context.Context, upstream Upstream, epoch int64, model string, maxTokens int, source string) (*ProbeResult, error) {
	start := time.Now()
	res := &ProbeResult{Model: model}

	// 1. 读取余额前
	res.Stage = "balance_before"
	balanceBefore, err := p.getBalance(ctx, upstream)
	if err != nil {
		res.Error = fmt.Sprintf("读取余额失败: %v", err)
		return res, fmt.Errorf("get balance before: %w", err)
	}
	res.BalanceBefore = balanceBefore

	// 2. 发起真实推理请求（模型与 token 上限由调用方指定）
	res.Stage = "chat"
	usage, err := p.callChatCompletion(ctx, upstream, model, maxTokens)
	res.TTFTMS = int(time.Since(start).Milliseconds())

	if err != nil {
		res.Error = fmt.Sprintf("推理请求失败: %v", err)
		// 记录失败（含来源）
		_, _ = p.db.Pool.Exec(ctx, `
			INSERT INTO probe_results (upstream_id, epoch, model, success, source, checked_at)
			VALUES ($1, $2, $3, false, $4, NOW())
		`, upstream.ID, epoch, model, source)
		return res, fmt.Errorf("call completion: %w", err)
	}

	// 3. 读取余额后
	res.Stage = "balance_after"
	balanceAfter, err := p.getBalance(ctx, upstream)
	if err != nil {
		res.Error = fmt.Sprintf("读取余额失败: %v", err)
		return res, fmt.Errorf("get balance after: %w", err)
	}
	res.BalanceAfter = balanceAfter

	// 4. 计算实测倍率（按该模型官网输入/输出价与 token 拆分）
	cost := balanceBefore - balanceAfter
	totalTokens := usage.PromptTokens + usage.CompletionTokens
	if totalTokens <= 0 {
		totalTokens = usage.TotalTokens
	}
	res.Cost = cost
	res.TokensUsed = totalTokens
	res.PromptTokens = usage.PromptTokens
	res.CompletionTokens = usage.CompletionTokens

	price := p.lookupModelPrice(ctx, model)
	var officialInPerM, officialOutPerM float64
	if price != nil {
		officialInPerM = price.InputPerM
		officialOutPerM = price.OutputPerM
		res.OfficialInputPerM = officialInPerM
		res.OfficialOutputPerM = officialOutPerM
	}
	res.RealRatio, res.Basis = computeRealRatio(cost, usage.PromptTokens, usage.CompletionTokens, officialInPerM, officialOutPerM)

	// 5. 写入数据库（含 token 拆分、计价基准与官网价快照）
	_, err = p.db.Pool.Exec(ctx, `
		INSERT INTO probe_results (
			upstream_id, epoch, model, success, real_ratio, ttft_ms, cost,
			balance_before, balance_after, tokens_used, prompt_tokens, completion_tokens,
			basis, official_input_per_m, official_output_per_m, source, checked_at
		) VALUES ($1, $2, $3, true, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW())
	`, upstream.ID, epoch, model, res.RealRatio, res.TTFTMS, cost,
		balanceBefore, balanceAfter, totalTokens, usage.PromptTokens, usage.CompletionTokens,
		res.Basis, nullableFloat(officialInPerM), nullableFloat(officialOutPerM), source)

	if err != nil {
		return res, fmt.Errorf("insert probe result: %w", err)
	}
	res.Stage = "ok"
	res.Success = true

	p.logger.Info("Probe succeeded",
		zap.String("name", upstream.Name),
		zap.String("model", model),
		zap.String("source", source),
		zap.String("basis", res.Basis),
		zap.Float64("cost", cost),
		zap.Float64("real_ratio", res.RealRatio),
		zap.Int("ttft_ms", res.TTFTMS),
		zap.Int("tokens", totalTokens),
	)

	return res, nil
}

// getBalance 读取站点余额（复用 BalanceChecker 的多协议探测：
// 站点自定义余额接口 → one-api/new-api → OpenAI 官方）
func (p *ProbeChecker) getBalance(ctx context.Context, upstream Upstream) (float64, error) {
	res, err := p.balance.FetchBalance(ctx, upstream)
	if err != nil {
		return 0, err
	}
	return res.Balance, nil
}

func (p *ProbeChecker) callChatCompletion(ctx context.Context, upstream Upstream, model string, maxTokens int) (*Usage, error) {
	reqBody := ChatCompletionRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "user", Content: "Hi"},
		},
		MaxTokens:   maxTokens,
		Temperature: 0,
		Stream:      false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/v1/chat/completions", upstream.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+upstream.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var chatResp ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	return &chatResp.Usage, nil
}

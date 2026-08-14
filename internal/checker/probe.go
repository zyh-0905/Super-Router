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
	db     *store.DB
	logger *zap.Logger
	client *http.Client
}

func NewProbeChecker(db *store.DB, logger *zap.Logger) *ProbeChecker {
	return &ProbeChecker{
		db:     db,
		logger: logger,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// OneAPIUserSelfResponse one-api 的 /api/user/self 响应
type OneAPIUserSelfResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Username string  `json:"username"`
		Quota    float64 `json:"quota"` // 余额（单位：美元）
	} `json:"data"`
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
		cost, err := p.probeOne(ctx, upstream, epoch)
		if err != nil {
			p.logger.Error("Probe failed",
				zap.String("name", upstream.Name),
				zap.Error(err),
			)
		} else {
			probeCount++
			totalCost += cost

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
	return p.probeOne(ctx, upstream, epoch)
}

// TodaySpent 今日全局探针总花费（分组调度器使用）
func (p *ProbeChecker) TodaySpent(ctx context.Context) (float64, error) {
	return p.getTodaySpent(ctx)
}

// UpstreamTodaySpent 单站点今日探针花费（分组调度器使用）
func (p *ProbeChecker) UpstreamTodaySpent(ctx context.Context, upstreamID int) (float64, error) {
	return p.getUpstreamTodaySpent(ctx, upstreamID)
}

func (p *ProbeChecker) probeOne(ctx context.Context, upstream Upstream, epoch int64) (float64, error) {
	// 1. 读取余额前
	balanceBefore, err := p.getBalance(ctx, upstream)
	if err != nil {
		return 0, fmt.Errorf("get balance before: %w", err)
	}

	// 2. 发起极小推理请求
	model := "gpt-4o" // 默认探测模型，可配置
	start := time.Now()
	usage, err := p.callChatCompletion(ctx, upstream, model)
	ttftMS := int(time.Since(start).Milliseconds())

	if err != nil {
		// 记录失败
		_, _ = p.db.Pool.Exec(ctx, `
			INSERT INTO probe_results (upstream_id, epoch, model, success, checked_at)
			VALUES ($1, $2, $3, false, NOW())
		`, upstream.ID, epoch, model)
		return 0, fmt.Errorf("call completion: %w", err)
	}

	// 3. 读取余额后
	balanceAfter, err := p.getBalance(ctx, upstream)
	if err != nil {
		return 0, fmt.Errorf("get balance after: %w", err)
	}

	// 4. 计算实测倍率
	cost := balanceBefore - balanceAfter
	totalTokens := usage.PromptTokens + usage.CompletionTokens
	var realRatio float64

	if totalTokens > 0 && cost > 0 {
		// 假设 GPT-4o 官方价格：$5/1M input, $15/1M output
		// 这里简化为平均 $10/1M tokens 作为基准
		basePrice := 10.0 / 1_000_000 // 每 token 的基准价格
		realRatio = cost / float64(totalTokens) / basePrice
	}

	// 5. 写入数据库
	_, err = p.db.Pool.Exec(ctx, `
		INSERT INTO probe_results (
			upstream_id, epoch, model, success, real_ratio, ttft_ms, cost,
			balance_before, balance_after, tokens_used, checked_at
		) VALUES ($1, $2, $3, true, $4, $5, $6, $7, $8, $9, NOW())
	`, upstream.ID, epoch, model, realRatio, ttftMS, cost,
		balanceBefore, balanceAfter, totalTokens)

	if err != nil {
		return cost, fmt.Errorf("insert probe result: %w", err)
	}

	p.logger.Info("Probe succeeded",
		zap.String("name", upstream.Name),
		zap.Float64("cost", cost),
		zap.Float64("real_ratio", realRatio),
		zap.Int("ttft_ms", ttftMS),
		zap.Int("tokens", totalTokens),
	)

	return cost, nil
}

func (p *ProbeChecker) getBalance(ctx context.Context, upstream Upstream) (float64, error) {
	url := fmt.Sprintf("%s/api/user/self", upstream.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Authorization", "Bearer "+upstream.AccessToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var userResp OneAPIUserSelfResponse
	if err := json.NewDecoder(resp.Body).Decode(&userResp); err != nil {
		return 0, err
	}

	if !userResp.Success {
		return 0, fmt.Errorf("user API returned success=false")
	}

	return userResp.Data.Quota, nil
}

func (p *ProbeChecker) callChatCompletion(ctx context.Context, upstream Upstream, model string) (*Usage, error) {
	reqBody := ChatCompletionRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "user", Content: "Hi"},
		},
		MaxTokens:   16,
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

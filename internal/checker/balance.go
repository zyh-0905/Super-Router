package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/store"

	"go.uber.org/zap"
)

// BalanceResult 余额检测结果
type BalanceResult struct {
	Balance  float64
	Currency string
	Source   string // oneapi | openai
}

// BalanceChecker 上游余额检测器（多协议自动探测）
type BalanceChecker struct {
	db     *store.DB
	logger *zap.Logger
	client *http.Client
}

func NewBalanceChecker(db *store.DB, logger *zap.Logger) *BalanceChecker {
	return &BalanceChecker{
		db:     db,
		logger: logger,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// CheckChannel 检测单站点余额并写入历史表
func (b *BalanceChecker) CheckChannel(ctx context.Context, upstream Upstream) error {
	res, err := b.FetchBalance(ctx, upstream)
	if err != nil {
		_, _ = b.db.Pool.Exec(ctx, `
			INSERT INTO balance_checks (channel_id, balance, currency, source, error, checked_at)
			VALUES ($1, 0, 'USD', '', $2, NOW())
		`, upstream.ID, truncateStr(err.Error(), 500))
		return err
	}

	_, err = b.db.Pool.Exec(ctx, `
		INSERT INTO balance_checks (channel_id, balance, currency, source, checked_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, upstream.ID, res.Balance, res.Currency, res.Source)
	if err != nil {
		return fmt.Errorf("insert balance check: %w", err)
	}

	b.logger.Debug("Balance checked",
		zap.String("channel", upstream.Name),
		zap.Float64("balance", res.Balance),
		zap.String("source", res.Source),
	)
	return nil
}

// FetchBalance 多协议探测站点余额（不写库，供余额检测与推理探针复用）
func (b *BalanceChecker) FetchBalance(ctx context.Context, upstream Upstream) (*BalanceResult, error) {
	return b.fetch(ctx, upstream)
}

// fetch 多协议探测：站点自定义接口 → one-api/new-api → OpenAI 官方
func (b *BalanceChecker) fetch(ctx context.Context, upstream Upstream) (*BalanceResult, error) {
	var attempts []string

	// 0. 站点自定义余额接口（完整 URL 或相对 base_url 的路径）
	if upstream.BalanceAPIURL != "" {
		url := upstream.BalanceAPIURL
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = strings.TrimRight(upstream.BaseURL, "/") + "/" + strings.TrimLeft(url, "/")
		}
		// 认证：余额接口独立令牌 > API Key > Access Token
		cred := upstream.BalanceAPIToken
		if cred == "" {
			cred = upstream.APIKey
		}
		if cred == "" {
			cred = upstream.AccessToken
		}
		bal, src, err := b.fetchGeneric(ctx, url, cred)
		if err == nil {
			return &BalanceResult{Balance: bal, Currency: "USD", Source: src}, nil
		}
		attempts = append(attempts, fmt.Sprintf("自定义接口: %v", err))
	}

	// 1. one-api / new-api：GET /api/user/self（Access Token）
	if upstream.AccessToken != "" {
		bal, err := b.fetchOneAPI(ctx, upstream.BaseURL, upstream.AccessToken)
		if err == nil {
			return &BalanceResult{Balance: bal, Currency: "USD", Source: "oneapi"}, nil
		}
		attempts = append(attempts, fmt.Sprintf("one-api: %v", err))
	}

	// 2. OpenAI 官方：GET /v1/dashboard/billing/credit_grants（API Key）
	if upstream.APIKey != "" {
		bal, err := b.fetchOpenAI(ctx, upstream.BaseURL, upstream.APIKey)
		if err == nil {
			return &BalanceResult{Balance: bal, Currency: "USD", Source: "openai"}, nil
		}
		attempts = append(attempts, fmt.Sprintf("openai: %v", err))
	}

	if len(attempts) == 0 {
		attempts = append(attempts, "站点未配置 Access Token / API Key")
	}
	return nil, fmt.Errorf("余额接口不可用: %s", strings.Join(attempts, " | "))
}

// fetchGeneric 通用余额接口请求：自动识别响应格式
// 支持：one-api {success,data:{quota}} / OpenAI {total_available} / 直接数字
// 兼容 quota 为字符串的数字（如 "8.20"）
func (b *BalanceChecker) fetchGeneric(ctx context.Context, url, credential string) (float64, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, "", err
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, "", fmt.Errorf("非 JSON 响应")
	}

	// 1. one-api 格式
	if data, ok := raw["data"].(map[string]interface{}); ok {
		for _, k := range []string{"quota", "balance", "remain_quota"} {
			if v, ok := data[k]; ok {
				if f, ok := toFloat(v); ok {
					return f, "oneapi", nil
				}
			}
		}
	}
	// 顶层 quota（无 data 包装）
	for _, k := range []string{"quota", "balance"} {
		if v, ok := raw[k]; ok {
			if f, ok := toFloat(v); ok {
				return f, "oneapi", nil
			}
		}
	}
	// 2. OpenAI 格式
	if v, ok := raw["total_available"]; ok {
		if f, ok := toFloat(v); ok {
			return f, "openai", nil
		}
	}

	return 0, "", fmt.Errorf("响应中未找到余额字段")
}

// toFloat 宽松数字转换（兼容字符串/数字）
func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// fetchOneAPI one-api/new-api：/api/user/self → data.quota（美元）
func (b *BalanceChecker) fetchOneAPI(ctx context.Context, baseURL, accessToken string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(baseURL, "/")+"/api/user/self", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		Success bool `json:"success"`
		Data    struct {
			Quota float64 `json:"quota"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("解析失败")
	}
	if !parsed.Success {
		return 0, fmt.Errorf("success=false")
	}
	return parsed.Data.Quota, nil
}

// fetchOpenAI OpenAI 官方：/v1/dashboard/billing/credit_grants → total_available
func (b *BalanceChecker) fetchOpenAI(ctx context.Context, baseURL, apiKey string) (float64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(baseURL, "/")+"/v1/dashboard/billing/credit_grants", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		TotalGranted   float64 `json:"total_granted"`
		TotalUsed      float64 `json:"total_used"`
		TotalAvailable float64 `json:"total_available"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, fmt.Errorf("解析失败")
	}
	return parsed.TotalAvailable, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/protocol"
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
	db        *store.DB
	logger    *zap.Logger
	client    *http.Client
	cryptoKey string
	redis     *store.RedisClient // Sub2API 自动登录令牌缓存；nil 时每次检测重新登录
}

func NewBalanceChecker(db *store.DB, logger *zap.Logger, cryptoKey string, redis *store.RedisClient) *BalanceChecker {
	return &BalanceChecker{
		db:        db,
		logger:    logger,
		cryptoKey: cryptoKey,
		redis:     redis,
		client:    &http.Client{Timeout: 10 * time.Second},
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

// fetch 多协议探测：站点自定义接口 → 类型默认接口 → one-api/new-api → OpenAI 官方
func (b *BalanceChecker) fetch(ctx context.Context, upstream Upstream) (*BalanceResult, error) {
	// P2-07：渠道 timeout_total_ms 映射为请求上下文超时（10s 客户端上限兜底；
	// 多接口回退探测链共享该总预算）
	reqCtx, cancel := withUpstreamTimeout(ctx, upstream, 10*time.Second)
	defer cancel()
	ctx = reqCtx

	var attempts []string

	// 站点余额凭证：独立令牌 > 自动登录会话（Sub2API）> API Key > Access Token
	cred, auto, loginErr := b.balanceCredential(ctx, upstream)
	if loginErr != nil {
		attempts = append(attempts, loginErr.Error())
	}

	// 0a. 站点自定义余额接口（完整 URL 或相对 base_url 的路径）
	if upstream.BalanceAPIURL != "" {
		url := upstream.BalanceAPIURL
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = strings.TrimRight(upstream.BaseURL, "/") + "/" + strings.TrimLeft(url, "/")
		}
		bal, src, err := b.fetchWithAutoRetry(ctx, url, cred, auto, upstream)
		if err == nil {
			return &BalanceResult{Balance: bal, Currency: "USD", Source: src}, nil
		}
		attempts = append(attempts, fmt.Sprintf("自定义接口: %v", err))
	}

	// 0b. 中转站类型默认余额接口（newapi → /api/user/self，sub2api → /api/v1/auth/me）
	if ep := protocol.DefaultBalanceEndpoint(upstream.RelayType); ep != "" {
		url := strings.TrimRight(upstream.BaseURL, "/") + "/" + strings.TrimLeft(ep, "/")
		bal, src, err := b.fetchWithAutoRetry(ctx, url, cred, auto, upstream)
		if err == nil {
			return &BalanceResult{Balance: bal, Currency: "USD", Source: src}, nil
		}
		attempts = append(attempts, fmt.Sprintf("%s默认接口: %v", protocol.RelayTypeLabel(upstream.RelayType), err))
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

// doBalanceRequest 发起余额接口请求（POST 携带空 JSON 体）
func (b *BalanceChecker) doBalanceRequest(ctx context.Context, url, credential, method string) (*http.Response, error) {
	var body io.Reader
	if method == "POST" {
		body = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	return b.client.Do(req)
}

// fetchGeneric 通用余额接口请求：自动识别响应格式
// 支持：one-api {success,data:{quota}} / OpenAI {total_available} / 直接数字
// 兼容 quota 为字符串的数字（如 "8.20"）。
// GET 返回 404/405 时自动回退 POST（部分站点余额/会话接口仅支持 POST）。
func (b *BalanceChecker) fetchGeneric(ctx context.Context, url, credential string) (float64, string, error) {
	resp, err := b.doBalanceRequest(ctx, url, credential, "GET")
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		resp.Body.Close()
		resp, err = b.doBalanceRequest(ctx, url, credential, "POST")
		if err != nil {
			return 0, "", err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)); len(b) > 0 {
				var em struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(b, &em) == nil && (em.Code != "" || em.Message != "") {
					msg := em.Message
					if msg == "" {
						msg = em.Code
					}
					return 0, "", fmt.Errorf("HTTP %d %s（令牌已过期或无权限，请更新站点「余额接口令牌」，建议使用控制台生成的长期有效系统访问令牌）", resp.StatusCode, msg)
				}
			}
		}
		return 0, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, "", fmt.Errorf("非 JSON 响应")
	}

	// 1. one-api 格式：data.quota / data.remain_quota（quota 单位）/ data.balance（美元）
	if data, ok := raw["data"].(map[string]interface{}); ok {
		for _, k := range []string{"quota", "remain_quota"} {
			if v, ok := data[k]; ok {
				if f, ok := toFloat(v); ok {
					return quotaToUSD(f), "oneapi", nil
				}
			}
		}
		if v, ok := data["balance"]; ok {
			if f, ok := toFloat(v); ok {
				return f, "oneapi", nil
			}
		}
		// 1b. new-api 登录/会话响应嵌套格式：data.user.quota
		if user, ok := data["user"].(map[string]interface{}); ok {
			for _, k := range []string{"quota", "remain_quota"} {
				if v, ok := user[k]; ok {
					if f, ok := toFloat(v); ok {
						return quotaToUSD(f), "oneapi", nil
					}
				}
			}
			if v, ok := user["balance"]; ok {
				if f, ok := toFloat(v); ok {
					return f, "oneapi", nil
				}
			}
		}
	}
	// 顶层 quota（无 data 包装）
	if v, ok := raw["quota"]; ok {
		if f, ok := toFloat(v); ok {
			return quotaToUSD(f), "oneapi", nil
		}
	}
	if v, ok := raw["balance"]; ok {
		if f, ok := toFloat(v); ok {
			return f, "oneapi", nil
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

// 单位换算：new-api 口径 1 美元 = 500,000 quota。
// 部分部署直接返回美元数值，接口无法区分单位，因此采用启发式：
// 数值高于阈值视为 quota 单位 → ÷500,000；低于阈值视为已是美元。
// （个人中转账户的美元余额通常在 $0.1~$100，quota 数值通常在 25 万以上，阈值落在两者之间。）
const (
	quotaPerUSD         = 500000.0
	quotaToUSDThreshold = 100000.0
)

func quotaToUSD(v float64) float64 {
	if v > quotaToUSDThreshold {
		return v / quotaPerUSD
	}
	return v
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
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if bb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096)); len(bb) > 0 {
				var em struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if json.Unmarshal(bb, &em) == nil && (em.Code != "" || em.Message != "") {
					msg := em.Message
					if msg == "" {
						msg = em.Code
					}
					return 0, fmt.Errorf("HTTP %d %s（令牌已过期或无权限，请更新站点凭证）", resp.StatusCode, msg)
				}
			}
		}
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
	return quotaToUSD(parsed.Data.Quota), nil
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

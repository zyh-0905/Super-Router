package checker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/protocol"
	"smart-router/internal/store"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type PricingChecker struct {
	db        *store.DB
	logger    *zap.Logger
	client    *http.Client
	cryptoKey string
}

func NewPricingChecker(db *store.DB, logger *zap.Logger, cryptoKey string) *PricingChecker {
	return &PricingChecker{
		db:        db,
		logger:    logger,
		cryptoKey: cryptoKey,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// OneAPIPricingResponse one-api/new-api 的 /api/pricing 响应
type OneAPIPricingResponse struct {
	Success bool `json:"success"`
	Data    []struct {
		Model           string  `json:"model"`
		ModelRatio      float64 `json:"model_ratio"`
		CompletionRatio float64 `json:"completion_ratio"`
		GroupRatio      float64 `json:"group_ratio"`
	} `json:"data"`
}

// Run 执行一轮价格同步
func (p *PricingChecker) Run(ctx context.Context) error {
	// 获取当前 epoch
	epoch, err := p.db.GetCurrentEpoch(ctx)
	if err != nil {
		return fmt.Errorf("get epoch: %w", err)
	}

	p.logger.Info("Starting pricing sync", zap.Int64("epoch", epoch))

	// 读取所有启用的上游
	upstreams, err := p.loadUpstreams(ctx)
	if err != nil {
		return fmt.Errorf("load upstreams: %w", err)
	}

	syncCount := 0
	for _, upstream := range upstreams {
		if err := p.syncOne(ctx, upstream, epoch); err != nil {
			p.logger.Error("Sync pricing failed",
				zap.Int("upstream_id", upstream.ID),
				zap.String("name", upstream.Name),
				zap.Error(err),
			)
		} else {
			syncCount++
		}
	}

	p.logger.Info("Pricing sync completed",
		zap.Int64("epoch", epoch),
		zap.Int("synced", syncCount),
		zap.Int("total", len(upstreams)),
	)

	return nil
}

func (p *PricingChecker) loadUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := p.db.Pool.Query(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol, relay_type, test_model,
		       daily_probe_budget, balance_api_url, balance_api_token, timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms,
		       balance_login_email, balance_login_password
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
			&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol, &u.RelayType, &u.TestModel,
			&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken, &u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
			&u.BalanceLoginEmail, &u.BalanceLoginPassword,
		); err != nil {
			return nil, err
		}
		// 凭据解密（P1-07）：失败时跳过该渠道
		if err := DecryptCreds(&u, p.cryptoKey); err != nil {
			p.logger.Warn("Decrypt upstream credentials failed, channel skipped",
				zap.Int("channel_id", u.ID), zap.Error(err))
			continue
		}
		upstreams = append(upstreams, u)
	}

	return upstreams, rows.Err()
}

// SyncChannel 对单个上游执行价格同步（分组调度器使用）
func (p *PricingChecker) SyncChannel(ctx context.Context, upstream Upstream, epoch int64) error {
	return p.syncOne(ctx, upstream, epoch)
}

// syncOne 价格同步包装：执行同步并把每次倍率查询计入 request_history
// （is_probe 标记，失败按错误类别归类，供站点请求统计与告警）。
func (p *PricingChecker) syncOne(ctx context.Context, upstream Upstream, epoch int64) error {
	// /api/pricing 是 new-api/one-api 系接口：sub2api 与 custom 类型不支持，
	// 跳过避免每轮产生无意义的 404 失败记录（与余额检测按类型回退一致）。
	if !supportsPricingEndpoint(upstream.RelayType) {
		p.logger.Debug("Pricing sync skipped (relay type does not expose /api/pricing)",
			zap.String("name", upstream.Name),
			zap.String("relay_type", upstream.RelayType),
		)
		return nil
	}

	start := time.Now()
	statusCode, err := p.doSync(ctx, upstream, epoch)
	p.recordSyncHistory(ctx, upstream, start, statusCode, err)
	return err
}

// supportsPricingEndpoint 判断中转站类型是否提供 /api/pricing。
// new-api/one-api 系支持；sub2api 与 custom（含未填类型）不支持。
func supportsPricingEndpoint(relayType string) bool {
	return relayType == protocol.RelayTypeNewAPI
}

func (p *PricingChecker) doSync(ctx context.Context, upstream Upstream, epoch int64) (int, error) {
	// P2-07：渠道 timeout_total_ms 映射为请求上下文超时（15s 客户端上限兜底）
	reqCtx, cancel := withUpstreamTimeout(ctx, upstream, 15*time.Second)
	defer cancel()

	// 尝试 GET /api/pricing（需要 Access Token）
	url := fmt.Sprintf("%s/api/pricing", upstream.BaseURL)
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}

	// 设置认证头（Access Token）
	if upstream.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AccessToken)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return resp.StatusCode, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// 解析响应
	var pricingResp OneAPIPricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&pricingResp); err != nil {
		return 200, fmt.Errorf("%w: %v", pricingErrDecode, err)
	}

	if !pricingResp.Success {
		return 200, fmt.Errorf("pricing API returned success=false")
	}

	// 批量写入数据库
	insertCount := 0
	skipped := 0
	for _, item := range pricingResp.Data {
		// 计算最终倍率（group_ratio 通常是全局倍率）
		promptRatio := item.ModelRatio * item.GroupRatio
		completionRatio := item.CompletionRatio * item.GroupRatio

		// 丢弃无效条目：部分中转站的 /api/pricing 会返回空模型名或零倍率。
		// 零倍率入库后会被换算成 $0 单价，使该渠道在 price_first 下显得免费而永远排第一。
		if item.Model == "" || promptRatio <= 0 || completionRatio <= 0 {
			skipped++
			continue
		}

		_, err := p.db.Pool.Exec(ctx, `
			INSERT INTO declared_prices (upstream_id, epoch, model, prompt_ratio, completion_ratio, checked_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
		`, upstream.ID, epoch, item.Model, promptRatio, completionRatio)

		if err != nil {
			p.logger.Warn("Insert declared_price failed",
				zap.String("model", item.Model),
				zap.Error(err),
			)
			continue
		}
		insertCount++
	}

	if skipped > 0 {
		p.logger.Warn("Skipped invalid pricing entries (empty model or non-positive ratio)",
			zap.String("name", upstream.Name),
			zap.Int("skipped", skipped),
			zap.Int("total", len(pricingResp.Data)),
		)
	}

	p.logger.Debug("Pricing synced",
		zap.String("name", upstream.Name),
		zap.Int("models", insertCount),
	)

	return 200, nil
}

// ==================== 倍率查询记录与错误分类 ====================

// pricingErrDecode 响应解析失败（HTTP 200 但 JSON 无法解码）
var pricingErrDecode = errors.New("decode pricing response")

// classifyPricingError 把倍率查询失败归类为稳定错误类别（写入 request_history.error_class）。
// 类别与网关 classifyError 语义对齐，另含 pricing 特有的解析类错误。
func classifyPricingError(status int, err error) string {
	if err == nil {
		return ""
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth_error"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "upstream_error"
	case status >= 400:
		return "bad_request"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case status == 200:
		// HTTP 成功但业务/解析失败
		if errors.Is(err, pricingErrDecode) {
			return "decode_error"
		}
		return "bad_response"
	default:
		// status == 0：连接/网络层失败
		return "network_error"
	}
}

// recordSyncHistory 把一次倍率查询计入该站点的 request_history（is_probe 标记）。
// 请求统计、成功率、延迟与错误分类统计均聚合该表，模型分布按 is_probe 排除。
func (p *PricingChecker) recordSyncHistory(ctx context.Context, upstream Upstream, start time.Time, statusCode int, err error) {
	dur := int(time.Since(start).Milliseconds())
	success := err == nil
	errorClass := classifyPricingError(statusCode, err)
	if err != nil {
		p.logger.Warn("Pricing sync recorded",
			zap.Int("upstream_id", upstream.ID),
			zap.String("name", upstream.Name),
			zap.String("error_class", errorClass),
			zap.Int("status_code", statusCode),
			zap.Error(err),
		)
	}

	_, ierr := p.db.Pool.Exec(ctx, `
		INSERT INTO request_history (
			request_id, channel_id, model, capability, success, first_byte_commit,
			ttft_ms, total_duration_ms, status_code, error_class, is_probe, created_at
		) VALUES ($1, $2, $3, $4, $5, false, $6, $6, $7, $8, true, NOW())
	`, uuid.NewString(), upstream.ID, "__pricing__", "pricing", success, dur, statusCode, errorClass)
	if ierr != nil {
		p.logger.Warn("Failed to record pricing sync history", zap.Error(ierr))
	}
}

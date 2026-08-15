package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/store"

	"go.uber.org/zap"
)

type PricingChecker struct {
	db     *store.DB
	logger *zap.Logger
	client *http.Client
}

func NewPricingChecker(db *store.DB, logger *zap.Logger) *PricingChecker {
	return &PricingChecker{
		db:     db,
		logger: logger,
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
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol,
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
			&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol,
			&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken, &u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
		); err != nil {
			return nil, err
		}
		upstreams = append(upstreams, u)
	}

	return upstreams, rows.Err()
}

// SyncChannel 对单个上游执行价格同步（分组调度器使用）
func (p *PricingChecker) SyncChannel(ctx context.Context, upstream Upstream, epoch int64) error {
	return p.syncOne(ctx, upstream, epoch)
}

func (p *PricingChecker) syncOne(ctx context.Context, upstream Upstream, epoch int64) error {
	// 尝试 GET /api/pricing（需要 Access Token）
	url := fmt.Sprintf("%s/api/pricing", upstream.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// 设置认证头（Access Token）
	if upstream.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.AccessToken)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// 解析响应
	var pricingResp OneAPIPricingResponse
	if err := json.NewDecoder(resp.Body).Decode(&pricingResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if !pricingResp.Success {
		return fmt.Errorf("pricing API returned success=false")
	}

	// 批量写入数据库
	insertCount := 0
	for _, item := range pricingResp.Data {
		// 计算最终倍率（group_ratio 通常是全局倍率）
		promptRatio := item.ModelRatio * item.GroupRatio
		completionRatio := item.CompletionRatio * item.GroupRatio

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

	p.logger.Debug("Pricing synced",
		zap.String("name", upstream.Name),
		zap.Int("models", insertCount),
	)

	return nil
}

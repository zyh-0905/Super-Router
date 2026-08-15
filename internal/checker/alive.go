package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"smart-router/internal/protocol"
	"smart-router/internal/store"

	"go.uber.org/zap"
)

// Upstream 上游站点配置
type Upstream struct {
	ID                 int
	Name               string
	BaseURL            string
	AccessToken        string
	APIKey             string
	Enabled            bool
	Role               string
	Protocol           string // openai（默认）| anthropic
	DailyProbeBudget   float64
	BalanceAPIURL      string
	BalanceAPIToken    string
	TimeoutConnectMS   int
	TimeoutFirstByteMS int
	TimeoutTotalMS     int
}

type AliveChecker struct {
	db     *store.DB
	logger *zap.Logger
	client *http.Client
}

func NewAliveChecker(db *store.DB, logger *zap.Logger) *AliveChecker {
	return &AliveChecker{
		db:     db,
		logger: logger,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Run 执行一轮存活探测
func (a *AliveChecker) Run(ctx context.Context) error {
	// 递增 epoch
	epoch, err := a.db.IncrementEpoch(ctx)
	if err != nil {
		return fmt.Errorf("increment epoch: %w", err)
	}

	a.logger.Info("Starting alive check", zap.Int64("epoch", epoch))

	// 读取所有启用的上游
	upstreams, err := a.loadUpstreams(ctx)
	if err != nil {
		return fmt.Errorf("load upstreams: %w", err)
	}

	// 并发探测
	for _, upstream := range upstreams {
		if err := a.checkOne(ctx, upstream, epoch); err != nil {
			a.logger.Error("Check upstream failed",
				zap.Int("upstream_id", upstream.ID),
				zap.String("name", upstream.Name),
				zap.Error(err),
			)
		}
	}

	a.logger.Info("Alive check completed",
		zap.Int64("epoch", epoch),
		zap.Int("checked", len(upstreams)),
	)

	return nil
}

func (a *AliveChecker) loadUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := a.db.Pool.Query(ctx, `
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

// CheckChannel 对单个上游执行存活探测（分组调度器使用）
func (a *AliveChecker) CheckChannel(ctx context.Context, upstream Upstream, epoch int64) error {
	return a.checkOne(ctx, upstream, epoch)
}

func (a *AliveChecker) checkOne(ctx context.Context, upstream Upstream, epoch int64) error {
	start := time.Now()
	isAlive := false
	var latencyMS int

	// 尝试 GET /v1/models
	url := protocol.ModelsEndpoint(upstream.BaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// 设置认证头（anthropic 用 x-api-key）
	if protocol.IsAnthropic(upstream.Protocol) {
		for k, v := range protocol.AnthropicHeaders(upstream.APIKey) {
			req.Header.Set(k, v)
		}
	} else if upstream.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+upstream.APIKey)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		// 网络错误，标记为不存活
		a.logger.Debug("Upstream unreachable",
			zap.String("name", upstream.Name),
			zap.Error(err),
		)
	} else {
		defer resp.Body.Close()
		latencyMS = int(time.Since(start).Milliseconds())

		// 只要返回 200 且能解析为 JSON 就认为存活
		if resp.StatusCode == 200 {
			var result map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&result) == nil {
				isAlive = true
			}
		}
	}

	// 写入数据库
	_, err = a.db.Pool.Exec(ctx, `
		INSERT INTO health_checks (upstream_id, epoch, is_alive, latency_ms, checked_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, upstream.ID, epoch, isAlive, latencyMS)

	if err != nil {
		return fmt.Errorf("insert health_check: %w", err)
	}

	a.logger.Debug("Upstream checked",
		zap.String("name", upstream.Name),
		zap.Bool("alive", isAlive),
		zap.Int("latency_ms", latencyMS),
	)

	return nil
}

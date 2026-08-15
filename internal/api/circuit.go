package api

import (
	"context"
	"time"

	"smart-router/internal/store"

	"go.uber.org/zap"
)

type CircuitBreakerManager struct {
	db     *store.DB
	logger *zap.Logger
	config CircuitBreakerConfig
}

type CircuitBreakerConfig struct {
	MinSamples               int
	OpenFailureRate          float64
	OpenMinFailures          int
	AuthFailureThreshold     int
	InitialCoolingSeconds    int
	MaxCoolingSeconds        int
	CoolingBackoff           []int
	HalfOpenProbeCount       int
	RecoverySuccessThreshold int
}

func NewCircuitBreakerManager(db *store.DB, logger *zap.Logger, config CircuitBreakerConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		db:     db,
		logger: logger,
		config: config,
	}
}

// UpdateCircuitState 更新熔断状态（每次请求完成后调用）
// groupID 非空时，分组级熔断参数覆盖全局配置
func (m *CircuitBreakerManager) UpdateCircuitState(ctx context.Context, channelID int, model string, groupID *int, success bool, errorClass string) error {
	// 读取当前状态
	var currentState string
	var failureCount, successCount int
	var coolingUntil time.Time

	err := m.db.Pool.QueryRow(ctx, `
		SELECT state, failure_count, success_count, COALESCE(cooling_until, '1970-01-01'::timestamp)
		FROM circuit_states
		WHERE channel_id = $1 AND model = $2 AND capability = ''
	`, channelID, model).Scan(&currentState, &failureCount, &successCount, &coolingUntil)

	if err != nil {
		// 没有记录，创建初始状态
		currentState = "closed"
		failureCount = 0
		successCount = 0
	}

	// 根据结果更新计数
	if success {
		successCount++
		failureCount = 0 // 成功后重置失败计数
	} else {
		failureCount++
		successCount = 0 // 失败后重置成功计数
	}

	// 生效配置：分组覆盖 > 全局
	cfg := m.effectiveConfig(ctx, groupID)

	// 应否开闸判定（仅 closed/degraded 需要样本统计）
	var shouldOpen bool
	if currentState == "closed" || currentState == "degraded" {
		shouldOpen = m.shouldOpen(ctx, channelID, model, cfg)
	}

	newState, newCoolingUntil := transitionCircuitState(currentState, coolingUntil, success, failureCount, successCount, shouldOpen, cfg)
	if newState != currentState {
		m.logger.Info("Circuit state transition",
			zap.Int("channel_id", channelID),
			zap.String("model", model),
			zap.String("from", currentState),
			zap.String("to", newState),
		)
	}

	// opened_at：开闸时记录（degraded/closed 时清空）
	var openedAt *time.Time
	if newState == "open" {
		t := time.Now()
		openedAt = &t
	}

	// 写入数据库（单一语句：指针为 nil 时写入 NULL）
	_, err = m.db.Pool.Exec(ctx, `
		INSERT INTO circuit_states (channel_id, model, capability, state, opened_at, cooling_until, failure_count, success_count, updated_at)
		VALUES ($1, $2, '', $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (channel_id, model, capability)
		DO UPDATE SET state = $3, opened_at = $4, cooling_until = $5, failure_count = $6, success_count = $7, updated_at = NOW()
	`, channelID, model, newState, openedAt, newCoolingUntil, failureCount, successCount)

	return err
}

// transitionCircuitState 计算熔断状态转换（纯函数，便于测试）。
// coolingUntil 为当前记录的冷却截止时间：open 且已到期 → 按 half_open 处理，
// 使探测成功/失败直接进入 half_open 分支（open → half_open 时间驱动，无需等待请求）。
func transitionCircuitState(currentState string, coolingUntil time.Time, success bool, failureCount, successCount int, shouldOpen bool, cfg CircuitBreakerConfig) (string, *time.Time) {
	now := time.Now()

	// 时间驱动转换：open 且冷却到期 → 视为 half_open
	effective := currentState
	if currentState == "open" && !coolingUntil.IsZero() && !now.Before(coolingUntil) {
		effective = "half_open"
	}

	switch effective {
	case "closed":
		if shouldOpen {
			t := now.Add(time.Duration(cfg.InitialCoolingSeconds) * time.Second)
			return "open", &t
		}
	case "half_open":
		if success {
			// 探测成功 → 降级状态（放行正常流量，持续观察）
			return "degraded", nil
		}
		// 探测失败 → 重新开闸，进入下一级指数退避冷却
		t := now.Add(nextCoolingDuration(failureCount, cfg))
		return "open", &t
	case "degraded":
		if successCount >= cfg.RecoverySuccessThreshold {
			return "closed", nil
		}
		if !success && shouldOpen {
			t := now.Add(time.Duration(cfg.InitialCoolingSeconds) * time.Second)
			return "open", &t
		}
	}
	return currentState, nil
}

// nextCoolingDuration 按失败次数计算指数退避冷却时长
func nextCoolingDuration(failureCount int, cfg CircuitBreakerConfig) time.Duration {
	backoffIndex := failureCount - 1
	if backoffIndex >= len(cfg.CoolingBackoff) {
		backoffIndex = len(cfg.CoolingBackoff) - 1
	}
	if backoffIndex < 0 {
		backoffIndex = 0
	}

	seconds := cfg.CoolingBackoff[backoffIndex]
	if seconds > cfg.MaxCoolingSeconds {
		seconds = cfg.MaxCoolingSeconds
	}

	return time.Duration(seconds) * time.Second
}

func (m *CircuitBreakerManager) shouldOpen(ctx context.Context, channelID int, model string, cfg CircuitBreakerConfig) bool {
	// 统计最近 10 分钟内最多 MinSamples 个样本的失败率
	// （LIMIT 必须作用于样本子查询，否则聚合结果只有一行、LIMIT 无效）
	var totalAttempts, failedAttempts int
	err := m.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END), 0) AS failed
		FROM (
			SELECT success
			FROM request_history
			WHERE channel_id = $1 AND model = $2 AND created_at >= NOW() - INTERVAL '10 minutes'
			ORDER BY created_at DESC
			LIMIT $3
		) recent
	`, channelID, model, cfg.MinSamples).Scan(&totalAttempts, &failedAttempts)

	if err != nil || totalAttempts < cfg.MinSamples {
		// 样本不足，不触发熔断
		return false
	}

	failureRate := float64(failedAttempts) / float64(totalAttempts)

	return failureRate > cfg.OpenFailureRate && failedAttempts >= cfg.OpenMinFailures
}

// effectiveConfig 计算生效配置：分组覆盖（非零字段）> 全局
func (m *CircuitBreakerManager) effectiveConfig(ctx context.Context, groupID *int) CircuitBreakerConfig {
	cfg := m.config
	if groupID == nil {
		return cfg
	}

	var minSamples, openMinFailures, initialCooling, maxCooling int
	var openFailureRate float64
	err := m.db.Pool.QueryRow(ctx, `
		SELECT cb_min_samples, cb_open_failure_rate, cb_open_min_failures,
		       cb_initial_cooling_seconds, cb_max_cooling_seconds
		FROM channel_groups WHERE id = $1
	`, *groupID).Scan(&minSamples, &openFailureRate, &openMinFailures, &initialCooling, &maxCooling)
	if err != nil {
		return cfg
	}

	if minSamples > 0 {
		cfg.MinSamples = minSamples
	}
	if openFailureRate > 0 {
		cfg.OpenFailureRate = openFailureRate
	}
	if openMinFailures > 0 {
		cfg.OpenMinFailures = openMinFailures
	}
	if initialCooling > 0 {
		cfg.InitialCoolingSeconds = initialCooling
	}
	if maxCooling > 0 {
		cfg.MaxCoolingSeconds = maxCooling
	}
	return cfg
}

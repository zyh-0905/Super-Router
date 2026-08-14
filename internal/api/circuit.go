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

	// 状态转换逻辑
	newState := currentState
	var newCoolingUntil *time.Time

	switch currentState {
	case "closed":
		// closed → open：失败率超阈值
		if m.shouldOpen(ctx, channelID, model, failureCount, cfg) {
			newState = "open"
			cooling := time.Now().Add(time.Duration(cfg.InitialCoolingSeconds) * time.Second)
			newCoolingUntil = &cooling
			m.logger.Info("Circuit opened",
				zap.Int("channel_id", channelID),
				zap.String("model", model),
				zap.Int("failure_count", failureCount),
			)
		}

	case "open":
		// open → half_open：冷却时间到期
		if time.Now().After(coolingUntil) {
			newState = "half_open"
			m.logger.Info("Circuit half-opened",
				zap.Int("channel_id", channelID),
				zap.String("model", model),
			)
		}

	case "half_open":
		// half_open → degraded：探测成功
		if success {
			newState = "degraded"
			m.logger.Info("Circuit degraded after successful probe",
				zap.Int("channel_id", channelID),
				zap.String("model", model),
			)
		} else {
			// half_open → open：探测失败，进入下一级冷却
			newState = "open"
			nextCooling := m.getNextCoolingDuration(failureCount, cfg)
			cooling := time.Now().Add(nextCooling)
			newCoolingUntil = &cooling
			m.logger.Info("Circuit re-opened after failed probe",
				zap.Int("channel_id", channelID),
				zap.String("model", model),
				zap.Duration("next_cooling", nextCooling),
			)
		}

	case "degraded":
		// degraded → closed：连续成功达到阈值
		if successCount >= cfg.RecoverySuccessThreshold {
			newState = "closed"
			m.logger.Info("Circuit closed after recovery",
				zap.Int("channel_id", channelID),
				zap.String("model", model),
				zap.Int("success_count", successCount),
			)
		} else if !success && m.shouldOpen(ctx, channelID, model, failureCount, cfg) {
			// degraded → open：再次触发熔断
			newState = "open"
			cooling := time.Now().Add(time.Duration(cfg.InitialCoolingSeconds) * time.Second)
			newCoolingUntil = &cooling
		}
	}

	// 更新数据库
	if newCoolingUntil != nil {
		_, err = m.db.Pool.Exec(ctx, `
			INSERT INTO circuit_states (channel_id, model, capability, state, opened_at, cooling_until, failure_count, success_count, updated_at)
			VALUES ($1, $2, '', $3, NOW(), $4, $5, $6, NOW())
			ON CONFLICT (channel_id, model, capability)
			DO UPDATE SET state = $3, cooling_until = $4, failure_count = $5, success_count = $6, updated_at = NOW()
		`, channelID, model, newState, newCoolingUntil, failureCount, successCount)
	} else {
		_, err = m.db.Pool.Exec(ctx, `
			INSERT INTO circuit_states (channel_id, model, capability, state, failure_count, success_count, updated_at)
			VALUES ($1, $2, '', $3, $4, $5, NOW())
			ON CONFLICT (channel_id, model, capability)
			DO UPDATE SET state = $3, failure_count = $4, success_count = $5, updated_at = NOW()
		`, channelID, model, newState, failureCount, successCount)
	}

	return err
}

func (m *CircuitBreakerManager) shouldOpen(ctx context.Context, channelID int, model string, recentFailures int, cfg CircuitBreakerConfig) bool {
	// 检查最近的失败率
	var totalAttempts, failedAttempts int
	err := m.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN NOT success THEN 1 ELSE 0 END) as failed
		FROM request_history
		WHERE channel_id = $1 AND model = $2 AND created_at >= NOW() - INTERVAL '10 minutes'
		ORDER BY created_at DESC
		LIMIT $3
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

func (m *CircuitBreakerManager) getNextCoolingDuration(failureCount int, cfg CircuitBreakerConfig) time.Duration {
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

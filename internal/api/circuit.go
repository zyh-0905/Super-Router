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

// UpdateCircuitState 更新熔断状态（每次请求完成后调用）。
// 使用事务 + SELECT ... FOR UPDATE 原子化「读-计数-状态转换-写」，
// 避免并发请求丢失计数（退避档位/恢复阈值失真）。
// P1-04：状态按分组桶隔离——groupID 非空时写入该分组专属桶，
// nil 时写入全局桶（group_id = 0）。groupID 同时决定分组级熔断参数覆盖。
func (m *CircuitBreakerManager) UpdateCircuitState(ctx context.Context, channelID int, model string, groupID *int, success bool, errorClass string) error {
	// 生效配置：分组覆盖 > 全局（只读，事务外计算）
	cfg := m.effectiveConfig(ctx, groupID)

	// 分组桶：nil → 全局桶 0
	bucket := 0
	if groupID != nil {
		bucket = *groupID
	}

	tx, err := m.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// H2：目标行不存在时 FOR UPDATE 无法锁住「虚空行」，两个并发事务
	// 都会读到 ErrNoRows 各自从 0 起算，后写者覆盖先写者 → 丢计数。
	// 先 INSERT ... ON CONFLICT DO NOTHING 预建行，使后续 FOR UPDATE
	// 有真实行可锁、可串行化。
	if _, err := tx.Exec(ctx, `
		INSERT INTO circuit_states (channel_id, model, capability, group_id, state)
		VALUES ($1, $2, '', $3, 'closed')
		ON CONFLICT (channel_id, model, capability, group_id) DO NOTHING
	`, channelID, model, bucket); err != nil {
		return err
	}

	// 锁定行并读取当前状态（串行化同一 (渠道, 模型, 分组桶) 的并发更新）
	var currentState string
	var failureCount, successCount int
	var coolingUntil time.Time

	err = tx.QueryRow(ctx, `
		SELECT state, failure_count, success_count, COALESCE(cooling_until, '1970-01-01'::timestamp)
		FROM circuit_states
		WHERE channel_id = $1 AND model = $2 AND capability = '' AND group_id = $3
		FOR UPDATE
	`, channelID, model, bucket).Scan(&currentState, &failureCount, &successCount, &coolingUntil)

	if err != nil {
		// DB 错误：不 fail-open，避免把已开闸状态误重置
		return err
	}

	// 根据结果更新计数
	if success {
		successCount++
		failureCount = 0 // 成功后重置失败计数
	} else {
		failureCount++
		successCount = 0 // 失败后重置成功计数
	}

	// H1：开闸判定必须包含当前请求结果，且成功请求绝不能触发开闸。
	// 调用方（proxy）保证顺序：先写 request_history（含当前结果），
	// 再调用本函数——shouldOpen 的 10 分钟窗口自然包含当前请求。
	// 这里再显式守卫 success：成功结果不参与开闸判定。
	var shouldOpen bool
	if !success && (currentState == "closed" || currentState == "degraded") {
		shouldOpen = m.shouldOpen(ctx, channelID, model, bucket, cfg)
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

	// 写入数据库（单一语句：指针为 nil 时写入 NULL；按分组桶 upsert）
	_, err = tx.Exec(ctx, `
		INSERT INTO circuit_states (channel_id, model, capability, group_id, state, opened_at, cooling_until, failure_count, success_count, updated_at)
		VALUES ($1, $2, '', $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (channel_id, model, capability, group_id)
		DO UPDATE SET state = $4, opened_at = $5, cooling_until = $6, failure_count = $7, success_count = $8, updated_at = NOW()
	`, channelID, model, bucket, newState, openedAt, newCoolingUntil, failureCount, successCount)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
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

// shouldOpen 统计最近 10 分钟内最多 MinSamples 个样本的失败率。
// P1-04：样本按分组桶过滤——bucket=0 匹配 request_history.group_id IS NULL 的全局流量，
// 否则只统计该分组的流量，实现组间熔断隔离。
// H5：客户端主动断开（error_class=client_canceled）不计入样本，
// 用户停止生成不应污染上游失败率。
func (m *CircuitBreakerManager) shouldOpen(ctx context.Context, channelID int, model string, bucket int, cfg CircuitBreakerConfig) bool {
	var totalAttempts, failedAttempts int
	err := m.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN NOT success THEN 1 ELSE 0 END), 0) AS failed
		FROM (
			SELECT success
			FROM request_history
			WHERE channel_id = $1 AND model = $2
			  AND created_at >= NOW() - INTERVAL '10 minutes'
			  AND (($3 = 0 AND group_id IS NULL) OR group_id = $3)
			  AND COALESCE(error_class, '') <> 'client_canceled'
			ORDER BY created_at DESC
			LIMIT $4
		) recent
	`, channelID, model, bucket, cfg.MinSamples).Scan(&totalAttempts, &failedAttempts)

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

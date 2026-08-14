package metrics

import (
	"context"
	"fmt"
	"log"
	"time"

	"smart-router/internal/store"

	"github.com/prometheus/client_golang/prometheus"
)

type channelSuccessRateSample struct {
	channelID   int
	model       string
	successRate float64
}

func replaceChannelSuccessRates(gauge *prometheus.GaugeVec, samples []channelSuccessRateSample) {
	gauge.Reset()
	for _, sample := range samples {
		gauge.WithLabelValues(
			fmt.Sprintf("%d", sample.channelID),
			sample.model,
		).Set(sample.successRate)
	}
}

// StartCollector 启动后台指标收集器
func StartCollector(db *store.DB, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[Metrics] Collector started with interval: %v", interval)

	// 首次立即执行一次
	ctx := context.Background()
	updateChannelSuccessRate(ctx, db)
	updateCircuitBreakerState(ctx, db)

	for range ticker.C {
		ctx := context.Background()

		// 更新渠道成功率
		if err := updateChannelSuccessRate(ctx, db); err != nil {
			log.Printf("[Metrics] Failed to update channel success rate: %v", err)
		}

		// 更新熔断状态
		if err := updateCircuitBreakerState(ctx, db); err != nil {
			log.Printf("[Metrics] Failed to update circuit breaker state: %v", err)
		}
	}
}

// updateChannelSuccessRate 更新渠道成功率指标
func updateChannelSuccessRate(ctx context.Context, db *store.DB) error {
	query := `
		SELECT channel_id, model,
		       SUM(CASE WHEN success THEN 1 ELSE 0 END)::float / NULLIF(COUNT(*), 0) as success_rate
		FROM request_history
		WHERE created_at >= NOW() - INTERVAL '10 minutes'
		GROUP BY channel_id, model
	`

	rows, err := db.Pool.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	samples := make([]channelSuccessRateSample, 0)
	for rows.Next() {
		var sample channelSuccessRateSample

		if err := rows.Scan(&sample.channelID, &sample.model, &sample.successRate); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}
		samples = append(samples, sample)
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}

	replaceChannelSuccessRates(ChannelSuccessRate, samples)
	if len(samples) > 0 {
		log.Printf("[Metrics] Updated %d channel success rate metrics", len(samples))
	}

	return nil
}

// updateCircuitBreakerState 更新熔断状态指标
func updateCircuitBreakerState(ctx context.Context, db *store.DB) error {
	query := `
		SELECT channel_id, model, state
		FROM circuit_states
		WHERE updated_at >= NOW() - INTERVAL '1 hour'
	`

	rows, err := db.Pool.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("query failed: %w", err)
	}
	defer rows.Close()

	// 状态映射：closed=0, open=1, half_open=2, degraded=3
	stateMap := map[string]float64{
		"closed":    0,
		"open":      1,
		"half_open": 2,
		"degraded":  3,
	}

	count := 0
	for rows.Next() {
		var channelID int
		var model string
		var state string

		if err := rows.Scan(&channelID, &model, &state); err != nil {
			log.Printf("[Metrics] Failed to scan row: %v", err)
			continue
		}

		// 映射状态到数值
		stateValue, ok := stateMap[state]
		if !ok {
			stateValue = -1 // 未知状态
		}

		// 更新 Prometheus Gauge
		CircuitBreakerState.WithLabelValues(
			fmt.Sprintf("%d", channelID),
			model,
		).Set(stateValue)

		count++
	}

	if count > 0 {
		log.Printf("[Metrics] Updated %d circuit breaker state metrics", count)
	}

	return rows.Err()
}

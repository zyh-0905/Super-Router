package quality

import (
	"context"
	"encoding/json"
	"fmt"

	"smart-router/internal/store"
)

// RunChannel Redis 频道名（固定格式：quality:run:<id>）。
func RunChannel(id int64) string { return fmt.Sprintf("quality:run:%d", id) }

// RedisPublisher 通过 Redis Pub/Sub 发布任务事件。
// Redis 不可用时 Publish 返回错误，但 Worker 继续依赖 PostgreSQL 完成任务（事件可丢失）。
type RedisPublisher struct {
	Redis *store.RedisClient
}

// NewRedisPublisher 创建 Redis 发布器（redis 为 nil 时 Publish 恒失败，调用方兜底）。
func NewRedisPublisher(redis *store.RedisClient) *RedisPublisher {
	return &RedisPublisher{Redis: redis}
}

// Publish 发布事件到任务频道（事件 JSON 必须包含 type、run_id、stage、progress）。
func (p *RedisPublisher) Publish(ctx context.Context, event Event) error {
	if p == nil || p.Redis == nil || p.Redis.Client == nil {
		return fmt.Errorf("redis publisher unavailable")
	}
	var runID int64
	if _, err := ParseRunID(event.RunID); err != nil {
		return fmt.Errorf("invalid run id in event: %w", err)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return p.Redis.Client.Publish(ctx, RunChannel(runID), payload).Err()
}

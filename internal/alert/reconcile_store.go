package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"smart-router/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// reconcileLockKey 告警 reconcile 专用 advisory lock 键
// （迁移 746213081，Telegram poller/report 746213082/746213083）。
const reconcileLockKey = 746213084

// SQLStore 是 EventStore 的 PostgreSQL 实现：
//   - WithReconcileLock 在专用连接上获取会话级 advisory lock；
//   - Reconcile 在单事务内 upsert 当前告警并恢复未出现告警。
type SQLStore struct {
	Pool *pgxpool.Pool
}

// NewSQLStore 创建 SQL 事件存储（DB 为空时返回 nil，便于测试注入）。
func NewSQLStore(db *store.DB) *SQLStore {
	if db == nil {
		return nil
	}
	return &SQLStore{Pool: db.Pool}
}

// WithReconcileLock 获取会话级 advisory lock 后执行 fn，锁随连接释放。
// 连接断开时 PostgreSQL 自动释放锁，其他 Checker 可立即接管。
func (s *SQLStore) WithReconcileLock(ctx context.Context, fn func(ctx context.Context) error) error {
	conn, err := s.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, reconcileLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		// 解锁使用独立超时上下文，避免 ctx 已取消时锁泄漏到连接归还
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, reconcileLockKey)
	}()

	return fn(ctx)
}

// Reconcile 单事务内同步告警生命周期：
//   - 当前评估中存在的 key：不存在 active 行则创建（新周期）；
//     已存在则更新 last_seen_at/当前值/occurrence_count，warning→critical 记录升级时间；
//   - active 行不在当前评估中：标记 recovered。
func (s *SQLStore) Reconcile(ctx context.Context, current []Alert, now time.Time) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. 更新/创建当前告警（部分唯一索引保证同 key 至多一条 active）
	for _, a := range current {
		if a.Key == "" {
			continue
		}
		metadata := a.Metadata
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		mdJSON, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata for %s: %w", a.Key, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO alert_events (
				alert_key, alert_type, severity, status, channel_id, group_id, model,
				title, message, current_value, threshold_value, unit, impact, recommendation,
				admin_path, metadata, first_seen_at, last_seen_at, occurrence_count
			) VALUES (
				$1, $2, $3, 'active', $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11,
				NULLIF($12, ''), NULLIF($13, ''), NULLIF($14, ''),
				$15, $16, $17, 1
			)
			ON CONFLICT (alert_key) WHERE status = 'active'
			DO UPDATE SET
				last_seen_at     = EXCLUDED.last_seen_at,
				current_value    = EXCLUDED.current_value,
				threshold_value  = EXCLUDED.threshold_value,
				occurrence_count = alert_events.occurrence_count + 1,
				severity = CASE
					WHEN EXCLUDED.severity = 'critical' AND alert_events.severity <> 'critical'
						THEN 'critical'
					ELSE alert_events.severity
				END,
				metadata = CASE
					WHEN EXCLUDED.severity = 'critical' AND alert_events.severity <> 'critical'
						THEN alert_events.metadata || jsonb_build_object('escalated_at', to_char($17, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))
					ELSE alert_events.metadata
				END
		`,
			a.Key, a.Type, string(a.Severity), a.ChannelID, a.GroupID, a.Model,
			a.Title, a.Message, a.CurrentValue, a.ThresholdValue, a.Unit,
			a.Impact, a.Recommendation, a.AdminPath, mdJSON, now, now,
		); err != nil {
			return fmt.Errorf("upsert %s: %w", a.Key, err)
		}
	}

	// 2. 恢复本轮未出现的 active 告警（current 为空时恢复全部）
	keys := make([]string, 0, len(current))
	for _, a := range current {
		if a.Key != "" {
			keys = append(keys, a.Key)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE alert_events
		SET status = 'recovered', recovered_at = $2
		WHERE status = 'active'
		  AND NOT (alert_key = ANY($1))
	`, keys, now); err != nil {
		return fmt.Errorf("recover stale alerts: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

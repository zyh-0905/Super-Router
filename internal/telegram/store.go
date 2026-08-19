package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"smart-router/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SQLConfigStore 是 ConfigStore 的 PostgreSQL 实现（telegram_config /
// telegram_subscribers / telegram_delivery_logs 三张表）。
type SQLConfigStore struct {
	Pool *pgxpool.Pool
}

// NewSQLConfigStore 创建 SQL 配置存储。
func NewSQLConfigStore(db *store.DB) *SQLConfigStore {
	if db == nil {
		return nil
	}
	return &SQLConfigStore{Pool: db.Pool}
}

// LoadConfig 读取单行 telegram_config（Bot Token 为 enc:v1: 密文，由调用方解密注入）。
func (s *SQLConfigStore) LoadConfig(ctx context.Context) (Config, error) {
	var c Config
	var lastReportAt *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT enabled, report_enabled, report_interval_minutes, report_minute,
		       timezone, include_recovered, include_ongoing, web_base_url,
		       last_update_id, last_report_at
		FROM telegram_config WHERE id = 1
	`).Scan(&c.Enabled, &c.ReportEnabled, &c.ReportIntervalMinutes, &c.ReportMinute,
		&c.Timezone, &c.IncludeRecovered, &c.IncludeOngoing, &c.WebBaseURL,
		&c.LastUpdateID, &lastReportAt)
	if err != nil {
		return c, fmt.Errorf("load telegram config: %w", err)
	}
	c.LastReportAt = lastReportAt
	return c, nil
}

// BotToken 读取并解密 Bot Token（enc:v1: 密文；明文直接透传）。
// decrypt 函数由调用方注入（crypto.Decrypt），避免 telegram 包依赖 crypto。
func (s *SQLConfigStore) BotToken(ctx context.Context, decrypt func(stored, key string) (string, error), key string) (string, error) {
	var stored string
	if err := s.Pool.QueryRow(ctx, `
		SELECT bot_token FROM telegram_config WHERE id = 1
	`).Scan(&stored); err != nil {
		return "", fmt.Errorf("load bot token: %w", err)
	}
	if stored == "" {
		return "", nil
	}
	return decrypt(stored, key)
}

// UpdateLastUpdateID 推进长轮询 offset（成功处理一条 update 后调用）。
func (s *SQLConfigStore) UpdateLastUpdateID(ctx context.Context, id int64) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_config SET last_update_id = $1, updated_at = NOW() WHERE id = 1
	`, id)
	return err
}

// UpdateLastPollAt 更新最近轮询时间。
func (s *SQLConfigStore) UpdateLastPollAt(ctx context.Context, t time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_config SET last_poll_at = $1, updated_at = NOW() WHERE id = 1
	`, t)
	return err
}

// UpdateLastReportAt 更新最近汇总时间。
func (s *SQLConfigStore) UpdateLastReportAt(ctx context.Context, t time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_config SET last_report_at = $1, updated_at = NOW() WHERE id = 1
	`, t)
	return err
}

// UpdateLastError 记录最近错误。
func (s *SQLConfigStore) UpdateLastError(ctx context.Context, msg string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_config SET last_error = $1, updated_at = NOW() WHERE id = 1
	`, msg)
	return err
}

// LoadSubscribers 读取启用订阅者（Worker 授权校验）。
func (s *SQLConfigStore) LoadSubscribers(ctx context.Context) ([]Subscriber, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, chat_id, enabled, alert_enabled, query_enabled, COALESCE(group_ids::text, '[]')
		FROM telegram_subscribers
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("load subscribers: %w", err)
	}
	defer rows.Close()

	var subs []Subscriber
	for rows.Next() {
		var sub Subscriber
		var groupIDsJSON string
		if err := rows.Scan(&sub.ID, &sub.ChatID, &sub.Enabled, &sub.AlertEnabled, &sub.QueryEnabled, &groupIDsJSON); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(groupIDsJSON), &sub.GroupIDs)
		subs = append(subs, sub)
	}
	return subs, rows.Err()
}

// HasDelivery 检查订阅者是否已有该窗口成功投递（幂等重试依据）。
func (s *SQLConfigStore) HasDelivery(ctx context.Context, subID int64, kind string, start, end time.Time) (bool, error) {
	var exists bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM telegram_delivery_logs
			WHERE subscriber_id = $1
			  AND message_kind = $2
			  AND success = true
			  AND window_start = $3
			  AND window_end = $4
		)
	`, subID, kind, start, end).Scan(&exists)
	return exists, err
}

// LogDelivery 写入投递审计。
func (s *SQLConfigStore) LogDelivery(ctx context.Context, l DeliveryLog) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO telegram_delivery_logs
			(subscriber_id, message_kind, window_start, window_end, success, telegram_message_id, error)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, ''))
	`, l.SubscriberID, l.MessageKind, l.WindowStart, l.WindowEnd, l.Success, l.TelegramMessageID, l.Error)
	return err
}

// MarkSubscriberFailure 记录订阅者发送失败（不自动删除订阅者）。
func (s *SQLConfigStore) MarkSubscriberFailure(ctx context.Context, subID int64, errMsg string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_subscribers
		SET failure_count = failure_count + 1, last_error = $2, updated_at = NOW()
		WHERE id = $1
	`, subID, errMsg)
	return err
}

// MarkSubscriberSuccess 清除失败计数并记录最近发送时间。
func (s *SQLConfigStore) MarkSubscriberSuccess(ctx context.Context, subID int64) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE telegram_subscribers
		SET failure_count = 0, last_error = '', last_sent_at = NOW(), updated_at = NOW()
		WHERE id = $1
	`, subID)
	return err
}

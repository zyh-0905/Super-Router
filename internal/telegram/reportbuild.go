package telegram

import (
	"context"
	"fmt"
	"time"

	"smart-router/internal/alert"
	"smart-router/internal/store"
)

// ReportBuilder 组装小时告警汇总（Checker Worker 定时发送与 Gateway 手动发送共用）。
type ReportBuilder struct {
	DB *store.DB
}

// NewReportBuilder 创建报告构建器。
func NewReportBuilder(db *store.DB) *ReportBuilder {
	return &ReportBuilder{DB: db}
}

// Build 组装完整汇总消息（HTML）。groupIDs 为空 = 全部。
func (b *ReportBuilder) Build(ctx context.Context, now time.Time, windowHours int, cfg Config, groupIDs []int) (string, error) {
	overview, err := b.overview(ctx)
	if err != nil {
		return "", fmt.Errorf("system overview: %w", err)
	}
	svc := alert.NewService(b.DB)
	since := now.Add(-time.Duration(windowHours) * time.Hour)
	changes, err := svc.ChangesSince(ctx, since, groupIDs)
	if err != nil {
		return "", fmt.Errorf("alert changes: %w", err)
	}
	return FormatReport(now, windowHours, overview, changes, cfg.IncludeRecovered, cfg.IncludeOngoing, cfg.WebBaseURL), nil
}

// overview 查询系统概况（站点/熔断/告警计数）。
func (b *ReportBuilder) overview(ctx context.Context) (SystemOverview, error) {
	var o SystemOverview
	err := b.DB.Pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled)
		FROM upstreams
	`).Scan(&o.TotalChannels, &o.ActiveChannels)
	if err != nil {
		return o, err
	}
	_ = b.DB.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM circuit_states WHERE state IN ('open', 'degraded')
	`).Scan(&o.OpenCircuits)
	_ = b.DB.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE severity = 'critical'),
		       COUNT(*) FILTER (WHERE severity = 'warning')
		FROM alert_events WHERE status = 'active'
	`).Scan(&o.ActiveAlerts, &o.CriticalAlerts, &o.WarningAlerts)
	return o, nil
}

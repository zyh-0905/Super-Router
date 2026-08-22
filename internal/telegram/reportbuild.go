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
// A2：groupIDs 同时作用于系统概况与告警变化查询——
// 分组受限订阅者的报告不包含其它分组的数据。
func (b *ReportBuilder) Build(ctx context.Context, now time.Time, windowHours int, cfg Config, groupIDs []int) (string, error) {
	overview, err := b.overview(ctx, groupIDs)
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

// BuildPush 组装事件驱动的告警变化推送消息（HTML）。
// since 为「上次推送时间」水位线：只查询其后的新出现/升级/恢复。
// 无变化时返回空字符串（调用方据此沉默，不发消息）。
func (b *ReportBuilder) BuildPush(ctx context.Context, since time.Time, cfg Config, groupIDs []int) (string, error) {
	svc := alert.NewService(b.DB)
	changes, err := svc.ChangesSince(ctx, since, groupIDs)
	if err != nil {
		return "", fmt.Errorf("alert changes: %w", err)
	}
	if changes.Total() == 0 {
		return "", nil
	}
	return FormatAlertPush(time.Now(), changes, cfg.WebBaseURL), nil
}

// groupMembership 分组过滤片段：$1::int[] 为空/NULL 时不限制；
// 否则目标列所在站点必须属于任一指定分组。
const groupMembership = `($1::int[] IS NULL OR cardinality($1::int[]) = 0 OR EXISTS (
	SELECT 1 FROM channel_group_members cgm
	WHERE cgm.channel_id = %s AND cgm.group_id = ANY($1)
))`

// groupedIDs nil/空 → SQL NULL（不限制）。
func groupedIDs(ids []int) interface{} {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// overview 查询系统概况（站点/熔断/告警计数，均应用分组过滤）。
func (b *ReportBuilder) overview(ctx context.Context, groupIDs []int) (SystemOverview, error) {
	var o SystemOverview
	gids := groupedIDs(groupIDs)

	err := b.DB.Pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*), COUNT(*) FILTER (WHERE enabled)
		FROM upstreams u
		WHERE `+groupMembership, "u.id"), gids).Scan(&o.TotalChannels, &o.ActiveChannels)
	if err != nil {
		return o, err
	}
	_ = b.DB.Pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*) FROM circuit_states cs
		WHERE cs.state IN ('open', 'degraded')
		  AND `+groupMembership, "cs.channel_id"), gids).Scan(&o.OpenCircuits)
	_ = b.DB.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE severity = 'critical'),
		       COUNT(*) FILTER (WHERE severity = 'warning')
		FROM alert_events ae
		WHERE ae.status = 'active'
		  AND (
		    $1::int[] IS NULL OR cardinality($1::int[]) = 0
		    OR ae.group_id = ANY($1)
		    OR (
		         ae.group_id IS NULL
		         AND ae.channel_id IS NOT NULL
		         AND EXISTS (
		             SELECT 1 FROM channel_group_members cgm
		             WHERE cgm.channel_id = ae.channel_id AND cgm.group_id = ANY($1)
		         )
		       )
		  )
	`, gids).Scan(&o.ActiveAlerts, &o.CriticalAlerts, &o.WarningAlerts)
	return o, nil
}

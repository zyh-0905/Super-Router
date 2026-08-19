package alert

import (
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// selectAlertColumns 全部查询共用的列序（与 scanAlert 的 Scan 顺序一一对应）。
// 可空文本列统一 COALESCE，避免 NULL 扫描到 string 失败。
// 列名均为代码常量，无注入风险。
const selectAlertColumns = `ae.id, ae.alert_key, ae.alert_type, ae.severity, ae.status, ae.channel_id, ae.group_id,
	COALESCE(ae.model, ''), COALESCE(u.name, ''), ae.title, ae.message,
	ae.current_value, ae.threshold_value, COALESCE(ae.unit, ''),
	COALESCE(ae.impact, ''), COALESCE(ae.recommendation, ''), COALESCE(ae.admin_path, ''),
	COALESCE(ae.metadata::text, '{}'), ae.first_seen_at, ae.last_seen_at, ae.recovered_at, ae.occurrence_count`

// alertJoin LEFT JOIN upstreams（渠道级告警富化站点名；channel_id 为 NULL 时自然为空）。
const alertJoin = `LEFT JOIN upstreams u ON u.id = ae.channel_id`

type alertScanner interface {
	Scan(dest ...interface{}) error
}

// scanAlert 扫描单行 alert_events。
func scanAlert(s alertScanner) (*Alert, error) {
	var a Alert
	var metadataJSON string
	if err := s.Scan(
		&a.ID, &a.Key, &a.Type, &a.Severity, &a.Status, &a.ChannelID, &a.GroupID, &a.Model, &a.ChannelName,
		&a.Title, &a.Message, &a.CurrentValue, &a.ThresholdValue, &a.Unit, &a.Impact, &a.Recommendation,
		&a.AdminPath, &metadataJSON, &a.FirstSeenAt, &a.LastSeenAt, &a.RecoveredAt, &a.OccurrenceCount,
	); err != nil {
		return nil, err
	}
	a.Metadata = map[string]interface{}{}
	_ = json.Unmarshal([]byte(metadataJSON), &a.Metadata)
	return &a, nil
}

// scanAlerts 扫描全部行并返回稳定排序的告警列表。
func scanAlerts(rows pgx.Rows) ([]Alert, error) {
	defer rows.Close()
	var alerts []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	SortAlerts(alerts)
	return alerts, nil
}

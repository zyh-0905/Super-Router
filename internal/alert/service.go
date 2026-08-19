package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"smart-router/internal/store"

	"github.com/jackc/pgx/v5"
)

// Service 只读取 alert_events 的持久化事件（不重新执行告警判断），
// 供 Web 告警页/统计、Telegram 汇总与 /alerts 命令共用同一数据口径。
// 若 Checker 暂停，读取的是最近一次 reconcile 后的状态。
type Service struct {
	DB *store.DB
}

// NewService 创建共享告警查询服务。
func NewService(db *store.DB) *Service {
	return &Service{DB: db}
}

// Active 返回当前 active 告警（单分组过滤；nil = 全部），稳定排序。
func (s *Service) Active(ctx context.Context, groupID *int) ([]Alert, error) {
	var gids []int
	if groupID != nil {
		gids = []int{*groupID}
	}
	return s.ActiveForGroups(ctx, gids)
}

// ActiveForGroups 返回当前 active 告警并按分组过滤：
//   - groupIDs 为空 = 全部；
//   - 分组专属告警（group_id ∈ 授权集合）可见；
//   - 渠道级告警（group_id IS NULL）只要所属渠道位于任一授权分组即可见，
//     不能因 alert_events.group_id 为 NULL 而被错误隐藏。
func (s *Service) ActiveForGroups(ctx context.Context, groupIDs []int) ([]Alert, error) {
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT `+selectAlertColumns+`
		FROM alert_events ae
		`+alertJoin+`
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
		ORDER BY ae.severity DESC, ae.first_seen_at DESC, ae.alert_key
	`, intArray(groupIDs))
	if err != nil {
		return nil, fmt.Errorf("query active alerts: %w", err)
	}
	defer rows.Close()
	return scanAlerts(rows)
}

// ChangesSince 返回 since 之后的告警变化（新出现 / 升级 / 持续中 / 恢复）。
// 升级时间记录在 metadata.escalated_at（由 Reconciler 写入）。
func (s *Service) ChangesSince(ctx context.Context, since time.Time, groupIDs []int) (AlertChanges, error) {
	var out AlertChanges

	// 新出现 / 升级 / 持续中（active）
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT `+selectAlertColumns+`
		FROM alert_events ae
		`+alertJoin+`
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
		ORDER BY ae.severity DESC, ae.first_seen_at DESC, ae.alert_key
	`, intArray(groupIDs))
	if err != nil {
		return out, fmt.Errorf("query active changes: %w", err)
	}
	active, err := scanAlerts(rows)
	if err != nil {
		return out, err
	}
	for _, a := range active {
		escalatedAt, escalated := metadataTime(a.Metadata, "escalated_at")
		switch {
		case !a.FirstSeenAt.Before(since):
			out.New = append(out.New, a)
		case escalated && !escalatedAt.Before(since):
			out.Escalated = append(out.Escalated, a)
		case a.Severity == SeverityCritical:
			out.Ongoing = append(out.Ongoing, a)
		}
	}

	// 已恢复（recovered_at >= since）
	rows, err = s.DB.Pool.Query(ctx, `
		SELECT `+selectAlertColumns+`
		FROM alert_events ae
		`+alertJoin+`
		WHERE ae.status = 'recovered'
		  AND ae.recovered_at IS NOT NULL
		  AND ae.recovered_at >= $2
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
		ORDER BY ae.recovered_at DESC
	`, intArray(groupIDs), since)
	if err != nil {
		return out, fmt.Errorf("query recovered changes: %w", err)
	}
	out.Recovered, err = scanAlerts(rows)
	return out, err
}

// GetByKey 返回指定 key 的最近一次事件周期（任意状态）。
func (s *Service) GetByKey(ctx context.Context, key string) (*Alert, error) {
	row := s.DB.Pool.QueryRow(ctx, `
		SELECT `+selectAlertColumns+`
		FROM alert_events ae
		`+alertJoin+`
		WHERE ae.alert_key = $1
		ORDER BY ae.id DESC
		LIMIT 1
	`, key)
	a, err := scanAlert(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// intArray nil/空切片 → SQL NULL（不筛选）；否则 pg 数组。
func intArray(ids []int) interface{} {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// metadataTime 从 metadata JSON 提取时间字段（缺失或非法返回零值 + false）。
func metadataTime(md map[string]interface{}, key string) (time.Time, bool) {
	if md == nil {
		return time.Time{}, false
	}
	v, ok := md[key]
	if !ok {
		return time.Time{}, false
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

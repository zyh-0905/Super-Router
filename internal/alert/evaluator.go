package alert

import (
	"context"
	"fmt"
	"time"

	"smart-router/internal/store"
)

// Evaluator 从数据库评估当前活跃告警（继承原 AdminHandler.buildAlerts 的全部逻辑）。
// 评估结果只用于 reconcile 输入，不直接写入 alert_events。
type Evaluator struct {
	DB *store.DB
}

// NewEvaluator 创建告警评估器。
func NewEvaluator(db *store.DB) *Evaluator {
	return &Evaluator{DB: db}
}

// LowBalanceThreshold 读取低余额告警阈值（默认 $1）。
func (e *Evaluator) LowBalanceThreshold(ctx context.Context) float64 {
	var v string
	if err := e.DB.Pool.QueryRow(ctx, `
		SELECT value FROM system_settings WHERE key = 'low_balance_threshold'
	`).Scan(&v); err == nil {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return 1.0
}

// groupFilter 分组过滤片段：nil = 不限制；否则站点需属于该分组。
const groupFilterSQL = `($1::int IS NULL OR EXISTS (
	SELECT 1 FROM channel_group_members cgm
	WHERE cgm.channel_id = %s AND cgm.group_id = $1
))`

// Evaluate 评估当前全部活跃告警（可选按分组过滤），返回稳定排序的列表。
func (e *Evaluator) Evaluate(ctx context.Context, groupID *int) ([]Alert, error) {
	var alerts []Alert
	now := time.Now()
	threshold := e.LowBalanceThreshold(ctx)

	// 1. 低余额告警（最新成功余额 <= 阈值）
	rows, err := e.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (b.channel_id) b.channel_id, u.name, b.balance
		FROM balance_checks b
		JOIN upstreams u ON u.id = b.channel_id
		WHERE b.source != ''
		  AND `+fmt.Sprintf(groupFilterSQL, "b.channel_id")+`
		ORDER BY b.channel_id, b.checked_at DESC
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query low balance: %w", err)
	}
	for rows.Next() {
		var cid int
		var name string
		var balance float64
		if err := rows.Scan(&cid, &name, &balance); err != nil {
			continue
		}
		if balance > threshold {
			continue
		}
		alerts = append(alerts, Alert{
			Key: StableKey(AlertInput{Type: TypeLowBalance, ChannelID: cid}),
			Type: TypeLowBalance, Severity: SeverityCritical,
			ChannelID: intPtr(cid),
			Title:     "余额不足",
			Message:   fmt.Sprintf("余额不足: %s 剩余 $%.2f（阈值 $%.2f）", name, balance, threshold),
			CurrentValue: fPtr(balance), ThresholdValue: fPtr(threshold), Unit: "USD",
			Impact:         "余额耗尽后该站点将无法响应请求",
			Recommendation: "为上游账户充值，或临时停用该站点",
			AdminPath:      "/channels",
			FirstSeenAt:    now, LastSeenAt: now,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan low balance: %w", err)
	}

	// 2. 倍率超限告警（最新实测倍率 > 站点上限）
	rows, err = e.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (p.upstream_id, p.model) p.upstream_id, u.name, p.model, p.real_ratio, u.ratio_limit
		FROM probe_results p
		JOIN upstreams u ON u.id = p.upstream_id
		WHERE p.success = true
		  AND u.ratio_limit > 0
		  AND `+fmt.Sprintf(groupFilterSQL, "p.upstream_id")+`
		ORDER BY p.upstream_id, p.model, p.checked_at DESC
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query ratio exceeded: %w", err)
	}
	for rows.Next() {
		var cid int
		var name, model string
		var ratio, limit float64
		if err := rows.Scan(&cid, &name, &model, &ratio, &limit); err != nil {
			continue
		}
		if ratio <= limit {
			continue
		}
		alerts = append(alerts, Alert{
			Key: StableKey(AlertInput{Type: TypeRatioExceeded, ChannelID: cid, Model: model}),
			Type: TypeRatioExceeded, Severity: SeverityCritical,
			ChannelID: intPtr(cid), Model: model,
			Title:     "倍率超限",
			Message:   fmt.Sprintf("倍率超标: %s %s 实测 %.4fx 超过上限 %.4fx", name, model, ratio, limit),
			CurrentValue: fPtr(ratio), ThresholdValue: fPtr(limit), Unit: "x",
			Impact:         "该模型可能参与成本路由，抬高转发成本",
			Recommendation: "检查价格同步或调整倍率上限",
			AdminPath:      "/channels",
			FirstSeenAt:    now, LastSeenAt: now,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan ratio exceeded: %w", err)
	}

	// 3. 熔断开闸/降级告警
	rows, err = e.DB.Pool.Query(ctx, `
		SELECT cs.channel_id, u.name, cs.model, cs.state, cs.group_id
		FROM circuit_states cs
		JOIN upstreams u ON u.id = cs.channel_id
		WHERE cs.state IN ('open', 'degraded')
		  AND `+fmt.Sprintf(groupFilterSQL, "cs.channel_id")+`
		ORDER BY cs.updated_at DESC LIMIT 20
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query circuit: %w", err)
	}
	for rows.Next() {
		var cid, stateGroupID int
		var name, model, state string
		if err := rows.Scan(&cid, &name, &model, &state, &stateGroupID); err != nil {
			continue
		}
		typ, sev := TypeCircuitDegraded, SeverityWarning
		label := "降级"
		if state == "open" {
			typ, sev, label = TypeCircuitOpen, SeverityCritical, "已开启"
		}
		a := Alert{
			Key: StableKey(AlertInput{Type: typ, ChannelID: cid, Model: model, GroupID: stateGroupID}),
			Type: typ, Severity: sev,
			ChannelID: intPtr(cid), Model: model,
			Title:     "熔断" + label,
			Message:   fmt.Sprintf("熔断%s: %s (%s)", label, name, model),
			Impact:         "该站点×模型组合当前不参与路由",
			Recommendation: "检查上游错误率，冷却结束后自动半开探测",
			AdminPath:      "/circuit",
			FirstSeenAt:    now, LastSeenAt: now,
		}
		if stateGroupID > 0 {
			a.GroupID = intPtr(stateGroupID)
		}
		alerts = append(alerts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan circuit: %w", err)
	}

	// 4. 站点禁用告警
	rows, err = e.DB.Pool.Query(ctx, `
		SELECT u.id, u.name
		FROM upstreams u
		WHERE u.enabled = false
		  AND `+fmt.Sprintf(groupFilterSQL, "u.id")+`
		ORDER BY u.id
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query disabled: %w", err)
	}
	for rows.Next() {
		var cid int
		var name string
		if err := rows.Scan(&cid, &name); err != nil {
			continue
		}
		alerts = append(alerts, Alert{
			Key: StableKey(AlertInput{Type: TypeChannelDisabled, ChannelID: cid}),
			Type: TypeChannelDisabled, Severity: SeverityWarning,
			ChannelID: intPtr(cid),
			Title:     "站点已禁用",
			Message:   fmt.Sprintf("站点已禁用: %s", name),
			Impact:         "该站点的全部模型不参与路由",
			Recommendation: "确认是有意停用，否则在站点页重新启用",
			AdminPath:      "/channels",
			FirstSeenAt:    now, LastSeenAt: now,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan disabled: %w", err)
	}

	// 5. 价格同步失败告警（最近 30 分钟内同步失败的站点）
	rows, err = e.DB.Pool.Query(ctx, `
		SELECT rh.channel_id, u.name
		FROM request_history rh
		JOIN upstreams u ON u.id = rh.channel_id
		WHERE rh.is_probe = true AND rh.capability = 'pricing'
		  AND rh.success = false
		  AND rh.created_at >= NOW() - INTERVAL '30 minutes'
		  AND `+fmt.Sprintf(groupFilterSQL, "rh.channel_id")+`
		GROUP BY rh.channel_id, u.name
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query pricing failed: %w", err)
	}
	for rows.Next() {
		var cid int
		var name string
		if err := rows.Scan(&cid, &name); err != nil {
			continue
		}
		alerts = append(alerts, Alert{
			Key: StableKey(AlertInput{Type: TypePricingSyncFailed, ChannelID: cid}),
			Type: TypePricingSyncFailed, Severity: SeverityWarning,
			ChannelID: intPtr(cid),
			Title:     "价格同步失败",
			Message:   fmt.Sprintf("价格同步失败: %s（最近一轮倍率查询失败）", name),
			Impact:         "成本估算可能使用过期价格",
			Recommendation: "检查上游 /api/pricing 接口与认证",
			AdminPath:      "/channels",
			FirstSeenAt:    now, LastSeenAt: now,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan pricing failed: %w", err)
	}

	SortAlerts(alerts)
	return alerts, nil
}

func intPtr(v int) *int { return &v }

func fPtr(v float64) *float64 { return &v }

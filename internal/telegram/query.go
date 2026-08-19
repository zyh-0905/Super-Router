package telegram

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"smart-router/internal/store"
)

// SQLQueryService 中转站只读查询的 PostgreSQL 实现。
// 只读取 upstreams、channel_groups、health_checks、balance_checks、probe_results、
// request_history、circuit_states 与 alert_events；绝不读取 API Key / Access Token /
// Balance Token 等凭据列。所有查询使用参数化 SQL，动态值只作为参数传入。
type SQLQueryService struct {
	DB *store.DB
}

// NewSQLQueryService 创建只读查询服务。
func NewSQLQueryService(db *store.DB) *SQLQueryService {
	return &SQLQueryService{DB: db}
}

// groupClause 分组过滤 SQL 片段（参数 $1 = int 数组，空 = 不限制）。
// channel_id 列名由调用方以常量传入（不存在用户输入拼接）。
const groupClause = `($1::int[] IS NULL OR cardinality($1::int[]) = 0 OR EXISTS (
	SELECT 1 FROM channel_group_members cgm
	WHERE cgm.channel_id = %s AND cgm.group_id = ANY($1)
))`

// grouped 将 groupIDs 映射为 SQL 参数（nil/空 → NULL）。
func grouped(groupIDs []int) interface{} {
	if len(groupIDs) == 0 {
		return nil
	}
	return groupIDs
}

// hostOf 只显示域名。
func hostOf(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// RelayList /relay 站点总览。
func (q *SQLQueryService) RelayList(ctx context.Context, groupIDs []int) (string, error) {
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT u.id, u.name, u.base_url, u.enabled,
		       COALESCE(b.balance, NULL), COALESCE(r.real_ratio, NULL),
		       COALESCE(cs.state, '')
		FROM upstreams u
		LEFT JOIN LATERAL (
			SELECT balance FROM balance_checks bc
			WHERE bc.channel_id = u.id AND bc.source != ''
			ORDER BY bc.checked_at DESC LIMIT 1
		) b ON true
		LEFT JOIN LATERAL (
			SELECT real_ratio FROM probe_results pr
			WHERE pr.upstream_id = u.id AND pr.success = true
			ORDER BY pr.checked_at DESC LIMIT 1
		) r ON true
		LEFT JOIN (
			SELECT DISTINCT ON (channel_id) channel_id, state
			FROM circuit_states
			ORDER BY channel_id, updated_at DESC
		) cs ON cs.channel_id = u.id
		WHERE `+fmt.Sprintf(groupClause, "u.id")+`
		ORDER BY u.id
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query relays: %w", err)
	}
	defer rows.Close()

	items := []RelaySummary{}
	for rows.Next() {
		var it RelaySummary
		if err := rows.Scan(&it.ID, &it.Name, &it.Host, &it.Healthy, &it.Balance, &it.Ratio, &it.CircuitState); err != nil {
			continue
		}
		it.Host = hostOf(it.Host)
		items = append(items, it)
	}
	return FormatRelayList(items), rows.Err()
}

// RelayDetail /relay <id> 详情。
func (q *SQLQueryService) RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	if !q.channelInGroups(ctx, id, groupIDs) {
		return "⛔ 无权查看该站点。", nil
	}
	var it RelayDetail
	err := q.DB.Pool.QueryRow(ctx, `
		SELECT u.id, u.name, u.base_url, u.enabled, u.protocol, COALESCE(u.relay_type, ''),
		       COALESCE(r.cnt, 0), COALESCE(r.sr, 0), COALESCE(r.avg_ms, 0), COALESCE(r.p95_ms, 0)
		FROM upstreams u
		LEFT JOIN (
			SELECT channel_id, COUNT(*) AS cnt,
			       ROUND(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 4) AS sr,
			       ROUND(AVG(total_duration_ms)) AS avg_ms,
			       ROUND(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY total_duration_ms)) AS p95_ms
			FROM request_history
			WHERE created_at >= NOW() - INTERVAL '24 hours'
			GROUP BY channel_id
		) r ON r.channel_id = u.id
		WHERE u.id = $1
	`, id).Scan(&it.ID, &it.Name, &it.Host, &it.Healthy, &it.Protocol, &it.RelayType,
		&it.Requests24h, &it.SuccessRate, &it.AverageMS, &it.P95MS)
	if err != nil {
		return "", fmt.Errorf("query relay detail: %w", err)
	}
	it.Host = hostOf(it.Host)

	// 最新余额 + 熔断状态
	_ = q.DB.Pool.QueryRow(ctx, `
		SELECT balance FROM balance_checks
		WHERE channel_id = $1 AND source != ''
		ORDER BY checked_at DESC LIMIT 1
	`, id).Scan(&it.Balance)
	_ = q.DB.Pool.QueryRow(ctx, `
		SELECT state FROM circuit_states
		WHERE channel_id = $1
		ORDER BY updated_at DESC LIMIT 1
	`, id).Scan(&it.CircuitState)

	// 分组名
	grows, err := q.DB.Pool.Query(ctx, `
		SELECT g.name FROM channel_group_members cgm
		JOIN channel_groups g ON g.id = cgm.group_id
		WHERE cgm.channel_id = $1 ORDER BY g.id
	`, id)
	if err == nil {
		for grows.Next() {
			var gn string
			if grows.Scan(&gn) == nil {
				it.Groups = append(it.Groups, gn)
			}
		}
		grows.Close()
	}

	return FormatRelayDetail(it), nil
}

// channelInGroups 校验站点是否在授权分组内（空 = 全部可见）。
func (q *SQLQueryService) channelInGroups(ctx context.Context, channelID int, groupIDs []int) bool {
	if len(groupIDs) == 0 {
		return true
	}
	var ok bool
	err := q.DB.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM channel_group_members
			WHERE channel_id = $1 AND group_id = ANY($2)
		)
	`, channelID, groupIDs).Scan(&ok)
	return err == nil && ok
}

// BalanceList /balance 全量列表。
func (q *SQLQueryService) BalanceList(ctx context.Context, groupIDs []int) (string, error) {
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (bc.channel_id) bc.channel_id, u.name, bc.balance, bc.currency, bc.source, bc.checked_at
		FROM balance_checks bc
		JOIN upstreams u ON u.id = bc.channel_id
		WHERE bc.source != ''
		  AND `+fmt.Sprintf(groupClause, "bc.channel_id")+`
		ORDER BY bc.channel_id, bc.checked_at DESC
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query balances: %w", err)
	}
	defer rows.Close()

	items := []BalanceSummary{}
	var latest *time.Time
	for rows.Next() {
		var it BalanceSummary
		if err := rows.Scan(&it.ChannelID, &it.Name, &it.Balance, &it.Currency, &it.Source, &it.CheckedAt); err != nil {
			continue
		}
		if latest == nil || (it.CheckedAt != nil && it.CheckedAt.After(*latest)) {
			latest = it.CheckedAt
		}
		items = append(items, it)
	}
	return FormatBalanceList(items, latest), rows.Err()
}

// BalanceDetail /balance <id> 最近历史。
func (q *SQLQueryService) BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	if !q.channelInGroups(ctx, id, groupIDs) {
		return "⛔ 无权查看该站点。", nil
	}
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT balance, currency, source, checked_at
		FROM balance_checks
		WHERE channel_id = $1
		ORDER BY checked_at DESC LIMIT 10
	`, id)
	if err != nil {
		return "", fmt.Errorf("query balance detail: %w", err)
	}
	defer rows.Close()

	var name string
	_ = q.DB.Pool.QueryRow(ctx, `SELECT name FROM upstreams WHERE id = $1`, id).Scan(&name)

	var b strings.Builder
	b.WriteString("💰 <b>" + EscapeHTML(name) + "</b> 最近余额\n")
	count := 0
	for rows.Next() {
		var balance float64
		var currency, source string
		var checkedAt time.Time
		if rows.Scan(&balance, &currency, &source, &checkedAt) != nil {
			continue
		}
		status := "✓"
		if source == "" {
			status = "✗"
		}
		b.WriteString(fmt.Sprintf("%s $%.2f %s · %s\n", status, balance, currency, checkedAt.Format("01-02 15:04")))
		count++
	}
	if count == 0 {
		b.WriteString("暂无有效检测结果。\n")
	}
	return b.String(), rows.Err()
}

// HealthList /health 全量列表。
func (q *SQLQueryService) HealthList(ctx context.Context, groupIDs []int) (string, error) {
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (hc.upstream_id) hc.upstream_id, u.name, hc.is_alive, hc.latency_ms, hc.checked_at
		FROM health_checks hc
		JOIN upstreams u ON u.id = hc.upstream_id
		WHERE `+fmt.Sprintf(groupClause, "hc.upstream_id")+`
		ORDER BY hc.upstream_id, hc.checked_at DESC
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query health: %w", err)
	}
	defer rows.Close()

	items := []HealthSummary{}
	for rows.Next() {
		var it HealthSummary
		if err := rows.Scan(&it.ChannelID, &it.Name, &it.Alive, &it.LatencyMS, &it.CheckedAt); err != nil {
			continue
		}
		items = append(items, it)
	}
	// 补充熔断状态
	states := map[int]string{}
	srows, err := q.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (channel_id) channel_id, state FROM circuit_states
		ORDER BY channel_id, updated_at DESC
	`)
	if err == nil {
		for srows.Next() {
			var cid int
			var st string
			if srows.Scan(&cid, &st) == nil {
				states[cid] = st
			}
		}
		srows.Close()
	}
	for i := range items {
		items[i].CircuitState = states[items[i].ChannelID]
	}
	return FormatHealthList(items), rows.Err()
}

// HealthDetail /health <id> 最近历史。
func (q *SQLQueryService) HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	if !q.channelInGroups(ctx, id, groupIDs) {
		return "⛔ 无权查看该站点。", nil
	}
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT is_alive, latency_ms, checked_at
		FROM health_checks
		WHERE upstream_id = $1
		ORDER BY checked_at DESC LIMIT 10
	`, id)
	if err != nil {
		return "", fmt.Errorf("query health detail: %w", err)
	}
	defer rows.Close()

	var name string
	_ = q.DB.Pool.QueryRow(ctx, `SELECT name FROM upstreams WHERE id = $1`, id).Scan(&name)

	var b strings.Builder
	b.WriteString("🩺 <b>" + EscapeHTML(name) + "</b> 最近健康\n")
	count := 0
	for rows.Next() {
		var alive bool
		var latency *int
		var checkedAt time.Time
		if rows.Scan(&alive, &latency, &checkedAt) != nil {
			continue
		}
		state := "✅ 存活"
		if !alive {
			state = "❌ 离线"
		}
		lat := "—"
		if latency != nil {
			lat = fmt.Sprintf("%dms", *latency)
		}
		b.WriteString(fmt.Sprintf("%s %s · %s\n", state, lat, checkedAt.Format("01-02 15:04")))
		count++
	}
	if count == 0 {
		b.WriteString("暂无有效检测结果。\n")
	}
	return b.String(), rows.Err()
}

// RatioList /ratio 全量列表（每站点最新实测模型）。
func (q *SQLQueryService) RatioList(ctx context.Context, groupIDs []int) (string, error) {
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (pr.upstream_id) pr.upstream_id, u.name, pr.model, pr.real_ratio,
		       COALESCE(u.ratio_limit, 0), pr.basis, pr.checked_at
		FROM probe_results pr
		JOIN upstreams u ON u.id = pr.upstream_id
		WHERE pr.success = true
		  AND `+fmt.Sprintf(groupClause, "pr.upstream_id")+`
		ORDER BY pr.upstream_id, pr.checked_at DESC
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query ratios: %w", err)
	}
	defer rows.Close()

	items := []RatioSummary{}
	for rows.Next() {
		var it RatioSummary
		if err := rows.Scan(&it.ChannelID, &it.Name, &it.Model, &it.Ratio, &it.Limit, &it.Basis, &it.CheckedAt); err != nil {
			continue
		}
		items = append(items, it)
	}
	return FormatRatioList(items), rows.Err()
}

// RatioDetail /ratio <id> 站点各模型最近实测。
func (q *SQLQueryService) RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	if !q.channelInGroups(ctx, id, groupIDs) {
		return "⛔ 无权查看该站点。", nil
	}
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT DISTINCT ON (model) model, real_ratio, basis, checked_at
		FROM probe_results
		WHERE upstream_id = $1 AND success = true
		ORDER BY model, checked_at DESC
	`, id)
	if err != nil {
		return "", fmt.Errorf("query ratio detail: %w", err)
	}
	defer rows.Close()

	var name string
	var limit float64
	_ = q.DB.Pool.QueryRow(ctx, `SELECT name, COALESCE(ratio_limit, 0) FROM upstreams WHERE id = $1`, id).Scan(&name, &limit)

	var b strings.Builder
	b.WriteString("📐 <b>" + EscapeHTML(name) + "</b> 实测倍率")
	if limit > 0 {
		b.WriteString(fmt.Sprintf("（上限 %.4fx）", limit))
	}
	b.WriteString("\n")
	count := 0
	for rows.Next() {
		var model, basis string
		var ratio float64
		var checkedAt time.Time
		if rows.Scan(&model, &ratio, &basis, &checkedAt) != nil {
			continue
		}
		b.WriteString(fmt.Sprintf("%s：%.4fx（%s）· %s\n",
			EscapeHTML(model), ratio, basisLabel(basis), checkedAt.Format("01-02 15:04")))
		count++
	}
	if count == 0 {
		b.WriteString("暂无有效检测结果。\n")
	}
	return b.String(), rows.Err()
}

func basisLabel(b string) string {
	if b == "official" {
		return "官网价基准"
	}
	return "基准估测"
}

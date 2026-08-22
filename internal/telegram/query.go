package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"smart-router/internal/station"
	"smart-router/internal/store"

	"github.com/jackc/pgx/v5"
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
	items, err := q.RelaySummaries(ctx, groupIDs)
	if err != nil {
		return "", err
	}
	return FormatRelayList(items), nil
}

// RelaySummaries 站点总览的结构化数据（供 /relay 文本与内联键盘共用，一次查询）。
func (q *SQLQueryService) RelaySummaries(ctx context.Context, groupIDs []int) ([]RelaySummary, error) {
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
		return nil, fmt.Errorf("query relays: %w", err)
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
	return items, rows.Err()
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
		if errors.Is(err, pgx.ErrNoRows) {
			return "🔍 站点不存在，请检查 ID（见 /relay 列表）。", nil
		}
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

// BalanceList /balance 全量列表：按中转站归并汇总（同 base_url 成员站点归为一行）。
// 订阅者分组过滤作用于成员口径：该站只要有成员在授权分组内即显示，
// 成员数只计范围内；账户余额取范围内成员最近一次成功检测（账户级共享）。
func (q *SQLQueryService) BalanceList(ctx context.Context, groupIDs []int) (string, error) {
	// 先做 lazy reconcile（与 Web 中转站视图同口径：共享 station 归一化）
	if err := q.reconcileStations(ctx); err != nil {
		return "", err
	}

	rows, err := q.DB.Pool.Query(ctx, `
		SELECT u.id, u.name, u.base_url, u.enabled, b.balance, b.checked_at
		FROM upstreams u
		LEFT JOIN LATERAL (
			SELECT balance, checked_at FROM balance_checks bc
			WHERE bc.channel_id = u.id AND bc.source != ''
			ORDER BY bc.checked_at DESC LIMIT 1
		) b ON true
		WHERE `+fmt.Sprintf(groupClause, "u.id")+`
		ORDER BY u.id
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query balances: %w", err)
	}
	defer rows.Close()

	type memberRow struct {
		id        int
		name      string
		key       string
		enabled   bool
		balance   *float64
		checkedAt *time.Time
	}
	var members []memberRow
	for rows.Next() {
		var r memberRow
		var rawBase string
		if err := rows.Scan(&r.id, &r.name, &rawBase, &r.enabled, &r.balance, &r.checkedAt); err != nil {
			continue
		}
		r.key = station.NormalizeBaseURL(rawBase)
		members = append(members, r)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan balances: %w", err)
	}

	// 中转站 ID 与名称（自定义名优先，空 = 自动命名）：
	// ID 用于 /balance <id> 明细，与 Web 中转站视图同口径。
	names := map[string]string{}
	ids := map[string]int{}
	srows, err := q.DB.Pool.Query(ctx, `SELECT id, base_url, display_name FROM relay_stations`)
	if err == nil {
		for srows.Next() {
			var sid int
			var key, dn string
			if srows.Scan(&sid, &key, &dn) == nil {
				names[key] = dn
				ids[key] = sid
			}
		}
		srows.Close()
	}

	// 分组聚合：key → {余额(最近), 成员数, 代表站点ID}
	type agg struct {
		balance   *float64
		checkedAt *time.Time
		count     int
		repID     int
	}
	byKey := map[string]*agg{}
	var order []string
	for _, r := range members {
		a := byKey[r.key]
		if a == nil {
			a = &agg{}
			byKey[r.key] = a
			order = append(order, r.key)
		}
		a.count++
		if a.repID == 0 {
			a.repID = r.id
		}
		if r.checkedAt != nil && (a.checkedAt == nil || r.checkedAt.After(*a.checkedAt)) {
			at := *r.checkedAt
			a.checkedAt = &at
			b := *r.balance
			a.balance = &b
		}
	}

	items := []BalanceSummary{}
	var latest *time.Time
	for _, key := range order {
		a := byKey[key]
		dn := names[key]
		if dn == "" {
			dn = station.AutoName(key)
		}
		items = append(items, BalanceSummary{
			StationID: ids[key], ChannelID: a.repID, Name: dn, Balance: a.balance,
			MemberCount: a.count, CheckedAt: a.checkedAt,
		})
		if a.checkedAt != nil && (latest == nil || a.checkedAt.After(*latest)) {
			latest = a.checkedAt
		}
	}
	return FormatBalanceList(items, latest), nil
}

// reconcileStations lazy reconcile：上游站点出现的新 base_url 自动建站（与 Web 同口径）。
func (q *SQLQueryService) reconcileStations(ctx context.Context) error {
	rows, err := q.DB.Pool.Query(ctx, `SELECT DISTINCT base_url FROM upstreams`)
	if err != nil {
		return fmt.Errorf("query base urls: %w", err)
	}
	var raw []string
	for rows.Next() {
		var b string
		if rows.Scan(&b) == nil && b != "" {
			raw = append(raw, b)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, b := range raw {
		if _, err := q.DB.Pool.Exec(ctx, `
			INSERT INTO relay_stations (base_url) VALUES ($1)
			ON CONFLICT (base_url) DO NOTHING
		`, station.NormalizeBaseURL(b)); err != nil {
			return err
		}
	}
	return nil
}

// BalanceDetail /balance <id> 中转站详情：账户余额 + 成员站点名列表
// （不展示成员各自余额——按用户要求，余额只在汇总口径展示）。
func (q *SQLQueryService) BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error) {
	var key, dn string
	if err := q.DB.Pool.QueryRow(ctx, `
		SELECT base_url, display_name FROM relay_stations WHERE id = $1
	`, id).Scan(&key, &dn); err != nil {
		return "中转站不存在（ID 见 /balance 列表）。", nil
	}
	if dn == "" {
		dn = station.AutoName(key)
	}

	// 成员（分组过滤：只列订阅者范围内的成员站点）
	rows, err := q.DB.Pool.Query(ctx, `
		SELECT u.id, u.name, u.enabled
		FROM upstreams u
		WHERE `+fmt.Sprintf(groupClause, "u.id")+`
		ORDER BY u.id
	`, grouped(groupIDs))
	if err != nil {
		return "", fmt.Errorf("query balance detail: %w", err)
	}
	defer rows.Close()

	keyMatch := map[int]bool{}
	allRows, err := q.DB.Pool.Query(ctx, `SELECT id, base_url FROM upstreams`)
	if err != nil {
		return "", fmt.Errorf("query upstream keys: %w", err)
	}
	for allRows.Next() {
		var cid int
		var b string
		if allRows.Scan(&cid, &b) == nil && station.NormalizeBaseURL(b) == key {
			keyMatch[cid] = true
		}
	}
	allRows.Close()

	var members []BalanceMember
	inRange := 0
	for rows.Next() {
		var m BalanceMember
		if err := rows.Scan(&m.ChannelID, &m.Name, &m.Enabled); err != nil {
			continue
		}
		if !keyMatch[m.ChannelID] {
			continue
		}
		members = append(members, m)
		inRange++
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("scan balance detail: %w", err)
	}

	// 账户余额：范围内成员最近一次成功检测
	var balance *float64
	var checkedAt *time.Time
	if inRange > 0 {
		brows, err := q.DB.Pool.Query(ctx, `
			SELECT bc.balance, bc.checked_at
			FROM balance_checks bc
			WHERE bc.source != ''
			  AND `+fmt.Sprintf(groupClause, "bc.channel_id")+`
			ORDER BY bc.checked_at DESC LIMIT 1
		`, grouped(groupIDs))
		if err == nil {
			if brows.Next() {
				var bal float64
				var at time.Time
				if brows.Scan(&bal, &at) == nil {
					balance = &bal
					checkedAt = &at
				}
			}
			brows.Close()
		}
	}

	return FormatBalanceDetail(dn, balance, checkedAt, members), nil
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

	items := []HealthHistoryItem{}
	for rows.Next() {
		var it HealthHistoryItem
		if rows.Scan(&it.Alive, &it.LatencyMS, &it.CheckedAt) != nil {
			continue
		}
		items = append(items, it)
	}
	return FormatHealthDetail(name, items), rows.Err()
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

	items := []RatioDetailItem{}
	for rows.Next() {
		var it RatioDetailItem
		if rows.Scan(&it.Model, &it.Ratio, &it.Basis, &it.CheckedAt) != nil {
			continue
		}
		items = append(items, it)
	}
	return FormatRatioDetail(name, limit, items), rows.Err()
}

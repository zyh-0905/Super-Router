package api

// 中转站归并：相同 base_url 的站点归纳为同一「中转站」。
//
// 设计约束（与用户确认）：
//   - 归并规则：规范化 base_url（scheme/host 小写、去尾斜杠、路径保留）
//     完全相同的站点归为同一中转站；不同路径视为不同站；
//   - 同一中转站的站点共用同一账户 → 余额口径相同，
//     卡片余额取成员站点中最近一次成功检测的余额；
//   - 归并完全自动：列表查询时按上游站点 lazy reconcile
//     （新 URL 自动建站），无手工挪站；
//   - 命名：display_name 由用户配置；空 = 按 URL 自动命名
//     （去 scheme 的 host[+路径]）。

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// normalizeBaseURL 规范化站点 base_url（归并键）：
// scheme 与 host 小写、去除尾部斜杠；路径与查询保留原样。
// 解析失败时退化为朴素去尾斜杠（仍可归并字面相同的 URL）。
func normalizeBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme == "" && u.Host == "") {
		return strings.TrimRight(s, "/")
	}
	out := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		out += p
	}
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// autoStationName 自动命名：去 scheme 的 host + 路径（如 api.247kan.com/v1）。
func autoStationName(baseURL string) string {
	s := strings.TrimPrefix(normalizeBaseURL(baseURL), "://")
	u, err := url.Parse(normalizeBaseURL(baseURL))
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	}
	out := u.Host
	if p := strings.Trim(u.Path, "/"); p != "" {
		out += "/" + p
	}
	return out
}

// stationMember 中转站成员（聚合视图中的单行）。
type stationMember struct {
	ChannelID    int        `json:"channel_id"`
	Name         string     `json:"name"`
	Enabled      bool       `json:"enabled"`
	Role         string     `json:"role"`
	Protocol     string     `json:"protocol"`
	RelayType    string     `json:"relay_type"`
	TestModel    string     `json:"test_model"`
	Healthy      *bool      `json:"healthy"`
	Balance      *float64   `json:"balance"`
	BalanceAt    *time.Time `json:"balance_checked_at"`
	CircuitState string     `json:"circuit_state"`
	Ratio        *float64   `json:"ratio"`
	RatioBasis   string     `json:"ratio_basis"`
	RatioModel   string     `json:"ratio_model"`
}

// relayStation 中转站聚合结果。
type relayStation struct {
	ID           int             `json:"id"`
	BaseURL      string          `json:"base_url"`
	DisplayName  string          `json:"display_name"`
	CustomName   bool            `json:"custom_name"`
	ChannelCount int             `json:"channel_count"`
	EnabledCount int             `json:"enabled_count"`
	Balance      *float64        `json:"balance"`
	BalanceAt    *time.Time      `json:"balance_checked_at"`
	Members      []stationMember `json:"members"`
}

// ListRelayStations GET /admin/relay-stations
// lazy reconcile（新 base_url 自动建站）→ 加载全部成员 → 按归并键分组。
func (h *AdminHandler) ListRelayStations(c *gin.Context) {
	ctx := c.Request.Context()

	// 1. lazy reconcile：上游站点中出现的新 base_url 自动建站。
	// 规范化在 Go 侧完成（url 解析），SQL 只做幂等插入。
	rows, err := h.db.Pool.Query(ctx, `SELECT DISTINCT base_url FROM upstreams`)
	if err != nil {
		h.logger.Warn("Load upstream base_urls failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "读取站点列表失败"})
		return
	}
	var rawURLs []string
	for rows.Next() {
		var b string
		if rows.Scan(&b) == nil && b != "" {
			rawURLs = append(rawURLs, b)
		}
	}
	rows.Close()
	for _, b := range rawURLs {
		if _, err := h.db.Pool.Exec(ctx, `
			INSERT INTO relay_stations (base_url) VALUES ($1)
			ON CONFLICT (base_url) DO NOTHING
		`, normalizeBaseURL(b)); err != nil {
			h.logger.Warn("Relay station upsert failed", zap.String("base_url", b), zap.Error(err))
		}
	}

	// 2. 加载站点（含最新余额/健康/熔断/倍率，一次批量查询）
	stations, err := h.loadRelayStations(ctx)
	if err != nil {
		h.logger.Warn("Load relay stations failed", zap.Error(err))
		c.JSON(500, gin.H{"error": "读取中转站失败"})
		return
	}

	c.JSON(200, gin.H{"stations": stations, "total": len(stations)})
}

// loadRelayStations 加载全部中转站与成员（站点与中转站两张表各一次查询，Go 侧分组）。
func (h *AdminHandler) loadRelayStations(ctx context.Context) ([]relayStation, error) {
	// 站点行
	srows, err := h.db.Pool.Query(ctx, `
		SELECT id, base_url, display_name FROM relay_stations ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	byKey := map[string]*relayStation{}
	var order []*relayStation
	for srows.Next() {
		var s relayStation
		if err := srows.Scan(&s.ID, &s.BaseURL, &s.DisplayName); err != nil {
			continue
		}
		s.Members = []stationMember{}
		if s.DisplayName == "" {
			s.DisplayName = autoStationName(s.BaseURL)
		} else {
			s.CustomName = true
		}
		byKey[s.BaseURL] = &s
		order = append(order, &s)
	}
	if err := srows.Err(); err != nil {
		return nil, err
	}

	// 成员行（批量加载：最新余额/健康/熔断/倍率）
	mrows, err := h.db.Pool.Query(ctx, `
		SELECT u.id, u.name, u.base_url, u.enabled, COALESCE(u.role, ''),
		       COALESCE(u.protocol, ''), COALESCE(u.relay_type, ''), COALESCE(u.test_model, ''),
		       b.balance, b.checked_at,
		       h.is_alive,
		       COALESCE(cs.state, ''),
		       pr.real_ratio, COALESCE(pr.basis, ''), COALESCE(pr.model, '')
		FROM upstreams u
		LEFT JOIN LATERAL (
			SELECT balance, checked_at FROM balance_checks bc
			WHERE bc.channel_id = u.id AND bc.source != ''
			ORDER BY bc.checked_at DESC LIMIT 1
		) b ON true
		LEFT JOIN LATERAL (
			SELECT is_alive FROM health_checks hc
			WHERE hc.upstream_id = u.id
			ORDER BY hc.checked_at DESC LIMIT 1
		) h ON true
		LEFT JOIN LATERAL (
			SELECT state FROM circuit_states cs2
			WHERE cs2.channel_id = u.id AND cs2.capability = ''
			ORDER BY cs2.updated_at DESC LIMIT 1
		) cs ON true
		LEFT JOIN LATERAL (
			SELECT real_ratio, basis, model FROM probe_results pr2
			WHERE pr2.upstream_id = u.id AND pr2.success = true
			ORDER BY pr2.checked_at DESC LIMIT 1
		) pr ON true
		ORDER BY u.id
	`)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()

	type memberRow struct {
		id        int
		key       string
		m         stationMember
		balanceAt *time.Time
	}
	var members []memberRow
	for mrows.Next() {
		var r memberRow
		var rawBase string
		var balance *float64
		var ratio *float64
		if err := mrows.Scan(&r.m.ChannelID, &r.m.Name, &rawBase, &r.m.Enabled, &r.m.Role,
			&r.m.Protocol, &r.m.RelayType, &r.m.TestModel,
			&balance, &r.balanceAt, &r.m.Healthy,
			&r.m.CircuitState, &ratio, &r.m.RatioBasis, &r.m.RatioModel); err != nil {
			continue
		}
		r.m.Balance = balance
		r.m.Ratio = ratio
		r.key = normalizeBaseURL(rawBase)
		members = append(members, r)
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}

	// 分组 + 站点级余额（成员中最近一次成功检测）
	for _, r := range members {
		st := byKey[r.key]
		if st == nil {
			continue // 未 reconcile 的孤立站点（理论不可达）
		}
		st.Members = append(st.Members, r.m)
		st.ChannelCount++
		if r.m.Enabled {
			st.EnabledCount++
		}
		if r.balanceAt != nil && (st.BalanceAt == nil || r.balanceAt.After(*st.BalanceAt)) {
			at := *r.balanceAt
			st.BalanceAt = &at
			b := *r.m.Balance
			st.Balance = &b
		}
	}

	// 按 display_name 排序（无成员的站点保留，前端展示空状态）
	sortStations(order)
	out := make([]relayStation, 0, len(order))
	for _, s := range order {
		out = append(out, *s)
	}
	return out, nil
}

func sortStations(stations []*relayStation) {
	for i := 1; i < len(stations); i++ {
		for j := i; j > 0 && stations[j].DisplayName < stations[j-1].DisplayName; j-- {
			stations[j], stations[j-1] = stations[j-1], stations[j]
		}
	}
}

// UpdateRelayStation PATCH /admin/relay-stations/:id
// {display_name}：非空 = 自定义命名；空 = 重置为自动命名。
func (h *AdminHandler) UpdateRelayStation(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(req.DisplayName)
	if len([]rune(name)) > 64 {
		c.JSON(400, gin.H{"error": "中转站名称不能超过 64 个字符"})
		return
	}

	ctx := c.Request.Context()
	ct, err := h.db.Pool.Exec(ctx, `
		UPDATE relay_stations SET display_name = $1, updated_at = NOW() WHERE id = $2
	`, name, id)
	if err != nil {
		h.logger.Warn("Update relay station failed", zap.String("id", id), zap.Error(err))
		c.JSON(500, gin.H{"error": "更新中转站失败"})
		return
	}
	if ct.RowsAffected() == 0 {
		c.JSON(404, gin.H{"error": "中转站不存在"})
		return
	}
	c.JSON(200, gin.H{"message": "中转站已更新"})
}

package telegram

// 中转站余额实时刷新：
// /balance（无参数）触发——后台并发探测各中转站真实余额，替换
// balance_checks 里 checker 周期检测留下的缓存（默认 10 分钟一次）。
//
// 与 /balance 的只读缓存查询（query.go BalanceList）不同：这里复用
// ProbeChecker 的多协议余额探测链（站点自定义接口 → 类型默认接口 →
// one-api/new-api → OpenAI 官方，含 Sub2API 自动登录、GET→POST 回退），
// 真实调用上游；探测成功后写回 balance_checks，Web 与后续 /balance 明细同步受益。
//
// 归并口径与 Web/只读查询一致（station.NormalizeBaseURL）：相同 base_url
// 的成员站点共用同一账户，取一个代表成员探测即得账户余额。

import (
	"context"
	"sync"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/station"
	"smart-router/internal/store"

	"go.uber.org/zap"
)

// balanceRefreshConcurrency 中转站余额并发探测上限（跨站并发，站内取代表成员）。
const balanceRefreshConcurrency = 4

// balanceRefreshBudget 单次余额刷新总闸（多站并发探测上界，防 goroutine 悬挂）。
const balanceRefreshBudget = 60 * time.Second

// BalanceRefresher 中转站余额实时刷新器。
type BalanceRefresher struct {
	db        *store.DB
	probe     *checker.ProbeChecker
	cryptoKey string
	logger    *zap.Logger
}

// NewBalanceRefresher 创建刷新器（probe 为 nil 时 Refresh 直接报错）。
func NewBalanceRefresher(db *store.DB, probe *checker.ProbeChecker, cryptoKey string, logger *zap.Logger) *BalanceRefresher {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &BalanceRefresher{db: db, probe: probe, cryptoKey: cryptoKey, logger: logger}
}

// stationMeta 中转站元信息（relay_stations 行）。
type stationMeta struct {
	id   int
	name string
}

// Refresh 实时探测全部授权中转站余额并返回格式化消息（HTML）。
// groupIDs 为空 = 全部；订阅者分组授权边界在此生效。
func (r *BalanceRefresher) Refresh(ctx context.Context, groupIDs []int) (string, error) {
	if r.probe == nil {
		return "", errBalanceProbeUnavailable
	}
	if err := r.reconcileStations(ctx); err != nil {
		return "", err
	}
	stations, err := r.loadStations(ctx)
	if err != nil {
		return "", err
	}
	channels, err := r.loadChannels(ctx, groupIDs)
	if err != nil {
		return "", err
	}
	if len(channels) == 0 {
		return FormatBalanceList(nil, nil), nil
	}

	// 按归并键分组（相同 base_url 的成员共用一个账户）
	type agg struct {
		key     string
		members []checker.Upstream
	}
	byKey := map[string]*agg{}
	var order []string
	for _, ch := range channels {
		key := station.NormalizeBaseURL(ch.BaseURL)
		a := byKey[key]
		if a == nil {
			a = &agg{key: key}
			byKey[key] = a
			order = append(order, key)
		}
		a.members = append(a.members, ch)
	}

	// 并发探测：每站一个 goroutine，跨站 semaphore 限流
	type probeResult struct {
		repID       int
		balance     *float64
		checkedAt   *time.Time
		memberCount int
	}
	results := make(map[string]probeResult, len(order))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, balanceRefreshConcurrency)
	for _, key := range order {
		wg.Add(1)
		go func(key string, members []checker.Upstream) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			repID, balance, checkedAt, memberCount := r.probeStation(ctx, members)
			mu.Lock()
			results[key] = probeResult{repID: repID, balance: balance, checkedAt: checkedAt, memberCount: memberCount}
			mu.Unlock()
		}(key, byKey[key].members)
	}
	wg.Wait()

	// 组装（保持首现顺序）
	var items []BalanceSummary
	var latest *time.Time
	for _, key := range order {
		pr := results[key]
		name := station.AutoName(key)
		stationID := 0
		if st, ok := stations[key]; ok {
			stationID = st.id
			if st.name != "" {
				name = st.name
			}
		}
		items = append(items, BalanceSummary{
			StationID:   stationID,
			ChannelID:   pr.repID,
			Name:        name,
			Balance:     pr.balance,
			MemberCount: pr.memberCount,
			CheckedAt:   pr.checkedAt,
		})
		if pr.checkedAt != nil && (latest == nil || pr.checkedAt.After(*latest)) {
			latest = pr.checkedAt
		}
	}
	return FormatBalanceList(items, latest), nil
}

// probeStation 探测一个中转站的账户余额：依次尝试成员站点，首个成功即返回，
// 并写回 balance_checks（让 Web 与后续查询同步受益）。全部失败返回 nil balance。
func (r *BalanceRefresher) probeStation(ctx context.Context, members []checker.Upstream) (repID int, balance *float64, checkedAt *time.Time, memberCount int) {
	memberCount = len(members)
	for _, m := range members {
		res, err := r.probe.ChannelBalance(ctx, m)
		if err != nil {
			r.logger.Debug("balance refresh probe failed",
				zap.Int("channel_id", m.ID), zap.Error(err))
			continue
		}
		b := res.Balance
		now := time.Now()
		r.writeBack(ctx, m.ID, res)
		return m.ID, &b, &now, memberCount
	}
	return 0, nil, nil, memberCount
}

// writeBack 把新鲜余额写回 balance_checks（与 BalanceChecker.CheckChannel 成功路径同口径）。
func (r *BalanceRefresher) writeBack(ctx context.Context, channelID int, res *checker.BalanceResult) {
	if r.db == nil || r.db.Pool == nil || res == nil {
		return
	}
	currency := res.Currency
	if currency == "" {
		currency = "USD"
	}
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO balance_checks (channel_id, balance, currency, source, checked_at)
		VALUES ($1, $2, $3, $4, NOW())
	`, channelID, res.Balance, currency, res.Source)
	if err != nil {
		r.logger.Warn("balance refresh write-back failed",
			zap.Int("channel_id", channelID), zap.Error(err))
	}
}

// loadChannels 加载授权范围内启用站点（含凭据解密）；groupIDs 为空 = 全部。
func (r *BalanceRefresher) loadChannels(ctx context.Context, groupIDs []int) ([]checker.Upstream, error) {
	rows, err := r.db.Pool.Query(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol, relay_type, test_model,
		       daily_probe_budget, balance_api_url, balance_api_token,
		       timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms,
		       balance_login_email, balance_login_password
		FROM upstreams u
		WHERE u.enabled = true
		  AND ($1::int[] IS NULL OR cardinality($1::int[]) = 0 OR EXISTS (
		      SELECT 1 FROM channel_group_members cgm
		      WHERE cgm.channel_id = u.id AND cgm.group_id = ANY($1)
		  ))
		ORDER BY u.id
	`, grouped(groupIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []checker.Upstream
	for rows.Next() {
		var u checker.Upstream
		if err := rows.Scan(
			&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol, &u.RelayType, &u.TestModel,
			&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken,
			&u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
			&u.BalanceLoginEmail, &u.BalanceLoginPassword,
		); err != nil {
			continue
		}
		if err := checker.DecryptCreds(&u, r.cryptoKey); err != nil {
			r.logger.Warn("balance refresh: decrypt creds failed, channel skipped",
				zap.Int("channel_id", u.ID), zap.Error(err))
			continue
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// loadStations 加载中转站元信息（归并键 → id/自定义名）。
func (r *BalanceRefresher) loadStations(ctx context.Context) (map[string]stationMeta, error) {
	rows, err := r.db.Pool.Query(ctx, `SELECT id, base_url, display_name FROM relay_stations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]stationMeta{}
	for rows.Next() {
		var id int
		var key, name string
		if rows.Scan(&id, &key, &name) == nil {
			m[key] = stationMeta{id: id, name: name}
		}
	}
	return m, rows.Err()
}

// reconcileStations lazy reconcile：上游站点出现的新 base_url 自动建站（与 Web/只读查询同口径）。
func (r *BalanceRefresher) reconcileStations(ctx context.Context) error {
	rows, err := r.db.Pool.Query(ctx, `SELECT DISTINCT base_url FROM upstreams`)
	if err != nil {
		return err
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
		if _, err := r.db.Pool.Exec(ctx, `
			INSERT INTO relay_stations (base_url) VALUES ($1)
			ON CONFLICT (base_url) DO NOTHING
		`, station.NormalizeBaseURL(b)); err != nil {
			return err
		}
	}
	return nil
}

var errBalanceProbeUnavailable = &balanceProbeErr{}

type balanceProbeErr struct{}

func (e *balanceProbeErr) Error() string {
	return "余额探测服务暂不可用，请稍后再试"
}

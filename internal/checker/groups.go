package checker

import (
	"context"
	"time"

	"smart-router/internal/crypto"
	"smart-router/internal/store"
)

// GroupSchedule 分组级调度配置（覆盖值，0 表示回退全局）
type GroupSchedule struct {
	ID               int
	Name             string
	Enabled          bool
	AliveInterval    time.Duration
	PricingInterval  time.Duration
	ProbeInterval    time.Duration
	BalanceInterval  time.Duration
	DailyProbeBudget float64
}

// ChannelSchedule 单个渠道的有效调度配置（取所属分组中最小间隔/最严格预算）
type ChannelSchedule struct {
	Upstream
	GroupIDs        []int
	AliveInterval   time.Duration
	PricingInterval time.Duration
	ProbeInterval   time.Duration
	BalanceInterval time.Duration
	EffectiveBudget float64 // 单站点有效探针预算：min(全局, 渠道自身, 所有启用分组)（P1-06）
}

// DecryptCreds 解密上游凭据（P1-07）：未配置密钥时明文透传；解密失败返回错误。
func DecryptCreds(u *Upstream, cryptoKey string) error {
	var err error
	if u.AccessToken, err = crypto.Decrypt(u.AccessToken, cryptoKey); err != nil {
		return err
	}
	if u.APIKey, err = crypto.Decrypt(u.APIKey, cryptoKey); err != nil {
		return err
	}
	if u.BalanceAPIToken, err = crypto.Decrypt(u.BalanceAPIToken, cryptoKey); err != nil {
		return err
	}
	return nil
}

// withUpstreamTimeout 将渠道 timeout_total_ms 映射到请求上下文（P2-07）：
// 渠道配置了总超时且小于固定上限时使用渠道值，否则使用 checker 客户端固定上限。
func withUpstreamTimeout(ctx context.Context, u Upstream, base time.Duration) (context.Context, context.CancelFunc) {
	if u.TimeoutTotalMS > 0 {
		t := time.Duration(u.TimeoutTotalMS) * time.Millisecond
		if t < base {
			return context.WithTimeout(ctx, t)
		}
	}
	return context.WithTimeout(ctx, base)
}

// LoadSchedules 加载所有启用渠道及其分组调度配置
// global* 为全局默认值（来自 config）；cryptoKey 用于解密渠道凭据。
func LoadSchedules(ctx context.Context, db *store.DB,
	globalAlive, globalPricing, globalProbe, globalBalance time.Duration, globalBudget float64, cryptoKey string) ([]ChannelSchedule, error) {

	// 1. 加载分组
	rows, err := db.Pool.Query(ctx, `
		SELECT id, name, enabled, alive_interval_seconds, pricing_interval_seconds,
		       probe_interval_seconds, balance_interval_seconds, daily_probe_budget
		FROM channel_groups
	`)
	if err != nil {
		return nil, err
	}

	groups := map[int]*GroupSchedule{}
	for rows.Next() {
		g := &GroupSchedule{}
		var aliveSec, pricingSec, probeSec, balanceSec int
		var budget float64
		if err := rows.Scan(&g.ID, &g.Name, &g.Enabled, &aliveSec, &pricingSec, &probeSec, &balanceSec, &budget); err != nil {
			continue
		}
		g.AliveInterval = time.Duration(aliveSec) * time.Second
		g.PricingInterval = time.Duration(pricingSec) * time.Second
		g.ProbeInterval = time.Duration(probeSec) * time.Second
		g.BalanceInterval = time.Duration(balanceSec) * time.Second
		g.DailyProbeBudget = budget
		groups[g.ID] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 2. 加载成员关系
	rows, err = db.Pool.Query(ctx, `SELECT channel_id, group_id FROM channel_group_members`)
	if err != nil {
		return nil, err
	}
	channelGroups := map[int][]int{}
	for rows.Next() {
		var chID, gID int
		if err := rows.Scan(&chID, &gID); err != nil {
			continue
		}
		channelGroups[chID] = append(channelGroups[chID], gID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 3. 加载启用渠道
	rows, err = db.Pool.Query(ctx, `
		SELECT id, name, base_url, access_token, api_key, enabled, role, protocol, relay_type,
		       daily_probe_budget, balance_api_url, balance_api_token, timeout_connect_ms, timeout_first_byte_ms, timeout_total_ms
		FROM upstreams
		WHERE enabled = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []ChannelSchedule
	for rows.Next() {
		var u Upstream
		if err := rows.Scan(
			&u.ID, &u.Name, &u.BaseURL, &u.AccessToken, &u.APIKey, &u.Enabled, &u.Role, &u.Protocol, &u.RelayType,
			&u.DailyProbeBudget, &u.BalanceAPIURL, &u.BalanceAPIToken, &u.TimeoutConnectMS, &u.TimeoutFirstByteMS, &u.TimeoutTotalMS,
		); err != nil {
			continue
		}

		// 凭据解密（P1-07）：解密失败时跳过该渠道，避免用错误凭据反复探测
		if err := DecryptCreds(&u, cryptoKey); err != nil {
			continue
		}

		s := ChannelSchedule{
			Upstream:        u,
			GroupIDs:        channelGroups[u.ID],
			AliveInterval:   globalAlive,
			PricingInterval: globalPricing,
			ProbeInterval:   globalProbe,
			BalanceInterval: globalBalance,
			EffectiveBudget: globalBudget,
		}

		// P1-06：预算取 min(全局, 渠道自身, 所有启用分组)，渠道默认 0.5 不再吞掉组预算
		if u.DailyProbeBudget > 0 && u.DailyProbeBudget < s.EffectiveBudget {
			s.EffectiveBudget = u.DailyProbeBudget
		}

		// 分组覆盖：间隔取最小（最频繁），预算取最小（最严格）
		for _, gid := range s.GroupIDs {
			g, ok := groups[gid]
			if !ok || !g.Enabled {
				continue
			}
			if g.AliveInterval > 0 && g.AliveInterval < s.AliveInterval {
				s.AliveInterval = g.AliveInterval
			}
			if g.PricingInterval > 0 && g.PricingInterval < s.PricingInterval {
				s.PricingInterval = g.PricingInterval
			}
			if g.ProbeInterval > 0 && g.ProbeInterval < s.ProbeInterval {
				s.ProbeInterval = g.ProbeInterval
			}
			if g.BalanceInterval > 0 && g.BalanceInterval < s.BalanceInterval {
				s.BalanceInterval = g.BalanceInterval
			}
			if g.DailyProbeBudget > 0 && g.DailyProbeBudget < s.EffectiveBudget {
				s.EffectiveBudget = g.DailyProbeBudget
			}
		}

		schedules = append(schedules, s)
	}

	return schedules, nil
}

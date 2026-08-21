package checker

import (
	"context"
	"testing"
	"time"

	"smart-router/internal/store"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestTracker 启动 miniredis 并构造 BudgetTracker。
func newTestTracker(t *testing.T) (*BudgetTracker, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis start: %v", err)
	}
	t.Cleanup(mr.Close)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewBudgetTracker(&store.RedisClient{Client: client}), mr
}

func TestReserveSeedsFromDBWhenKeysMissing(t *testing.T) {
	tracker, _ := newTestTracker(t)

	// C1 回归：键不存在时必须以 DB 已消费值播种（Lua 的 exists 显式 == 0）。
	// DB 侧已消费 3000 微美元，若播种分支失效会从 0 起算。
	ok, day, err := tracker.Reserve(context.Background(), 7,
		100,      // 本次预留
		5000,     // 渠道预算
		100000,   // 全局预算
		3000,     // DB 渠道已消费（播种）
		8000)     // DB 全局已消费（播种）
	if err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	if day == "" {
		t.Fatal("reserve must return the day bucket")
	}

	// 渠道计数器 = 播种 3000 + 预留 100 = 3100：再预留 2000（总计 5100 > 5000）应被拒。
	ok2, _, err := tracker.Reserve(context.Background(), 7, 2000, 5000, 100000, 3000, 8000)
	if err != nil {
		t.Fatalf("second reserve error: %v", err)
	}
	if ok2 {
		t.Fatal("seeding broken: budget accepted spend that exceeds the seeded ledger")
	}
	// 再预留 1900（总计 5000 == 预算）应成功。
	ok3, _, err := tracker.Reserve(context.Background(), 7, 1900, 5000, 100000, 3000, 8000)
	if err != nil || !ok3 {
		t.Fatalf("reserve at exact budget: ok=%v err=%v", ok3, err)
	}
}

func TestAdjustNoopWhenKeysMissing(t *testing.T) {
	tracker, _ := newTestTracker(t)

	// C2 回归：键缺失时 Adjust 必须 no-op，绝不能复活一个清零键。
	// 这里未做任何 Reserve，直接 Adjust——修复前会把当日账本写成 0。
	if err := tracker.Adjust(context.Background(), 7, "2026-08-21", -100); err != nil {
		t.Fatalf("adjust no-op: %v", err)
	}
	if tracker.redis.Client.Exists(context.Background(),
		"probe:budget:ch:7:2026-08-21").Val() != 0 {
		t.Fatal("Adjust resurrected a missing ledger key")
	}
}

func TestReserveAdjustSameDayBucket(t *testing.T) {
	tracker, _ := newTestTracker(t)

	ok, day, err := tracker.Reserve(context.Background(), 3, 500, 10000, 100000, 0, 0)
	if err != nil || !ok {
		t.Fatalf("reserve: ok=%v err=%v", ok, err)
	}
	// 结算 +100（补扣），键在同一日桶上。
	if err := tracker.Adjust(context.Background(), 3, day, 100); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	// 账本 = 500 + 100 = 600
	v := tracker.redis.Client.Get(context.Background(), "probe:budget:ch:3:"+day).Val()
	if v != "600" {
		t.Fatalf("ledger after adjust = %q, want 600", v)
	}
}

func TestToBudgetUnits(t *testing.T) {
	cases := []struct {
		usd  float64
		want int64
	}{
		{0, 0},
		{0.0000001, 1}, // 正金额不足 1 微美元保守取 1
		{0.00032, 320}, // 单次探测典型成本：32 tokens × $10/1M
		{0.5, 500000},
		{5.0, 5000000},
		{1.2345678, 1234568},
	}
	for _, c := range cases {
		if got := ToBudgetUnits(c.usd); got != c.want {
			t.Errorf("ToBudgetUnits(%v) = %d, want %d", c.usd, got, c.want)
		}
	}
}

func TestProbeResultBudgetSettlement(t *testing.T) {
	cases := []struct {
		name     string
		res      *ProbeResult
		keep     bool
		cost     float64
	}{
		{"nil result keeps reservation", nil, true, 0},
		{"balance_before failed: full refund", &ProbeResult{Stage: "balance_before"}, false, 0},
		{"chat failed: upstream may have billed", &ProbeResult{Stage: "chat"}, true, 0},
		{"balance_after failed (cost pending)", &ProbeResult{Stage: "balance_after", CostPending: true}, true, 0},
		{"success with positive cost", &ProbeResult{Stage: "ok", Cost: 0.0004}, false, 0.0004},
		{"success but cost unmeasurable (top-up/flux)", &ProbeResult{Stage: "ok", Cost: -0.001}, true, 0},
		{"success with zero cost", &ProbeResult{Stage: "ok", Cost: 0}, true, 0},
	}
	for _, c := range cases {
		keep, cost := c.res.BudgetSettlement()
		if keep != c.keep || cost != c.cost {
			t.Errorf("%s: settlement = (keep=%v, cost=%v), want (keep=%v, cost=%v)",
				c.name, keep, cost, c.keep, c.cost)
		}
	}
}

func TestBudgetDayUTC(t *testing.T) {
	// 固定 UTC 日期断言：BudgetDay 只做 UTC 归一化。
	if got := BudgetDay(mustTime(t, "2026-08-21T00:00:00Z")); got != "2026-08-21" {
		t.Fatalf("BudgetDay = %q", got)
	}
	// UTC+8 的 2026-08-21 08:00 仍是 UTC 的 2026-08-21 00:00。
	if got := BudgetDay(mustTime(t, "2026-08-21T08:00:00+08:00")); got != "2026-08-21" {
		t.Fatalf("BudgetDay(+08:00) = %q", got)
	}
}

func mustTime(t *testing.T, s string) (ts time.Time) {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return ts
}

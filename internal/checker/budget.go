package checker

import (
	"context"
	"fmt"
	"math"
	"time"

	"smart-router/internal/store"

	"github.com/redis/go-redis/v9"
)

// BudgetTracker 每日探针预算的 Redis 原子记账（P1-06）。
// 按 UTC 日期分桶，定时探针（checker）与手动探测（ratio 接口）共享同一组密钥，
// 通过 Lua 原子预留/结算，防止并发探测 check-then-act 导致预算超支。
// Redis 不可用时调用方应回退到数据库汇总检查（无原子性保证）。
type BudgetTracker struct {
	redis *store.RedisClient
}

func NewBudgetTracker(redis *store.RedisClient) *BudgetTracker {
	return &BudgetTracker{redis: redis}
}

// Available 返回该 tracker 是否可用（已配置 Redis）。
func (b *BudgetTracker) Available() bool {
	return b != nil && b.redis != nil && b.redis.Client != nil
}

const budgetKeyTTLSeconds = 172800 // 48h：跨时区自然日边界安全

// budgetReserveScript 原子预留：渠道与全局两个计数器同时不超过预算才成功。
var budgetReserveScript = redis.NewScript(`
local ch = tonumber(redis.call('get', KEYS[1]) or '0')
local gl = tonumber(redis.call('get', KEYS[2]) or '0')
local r = tonumber(ARGV[1])
if ch + r > tonumber(ARGV[2]) or gl + r > tonumber(ARGV[3]) then
	return 0
end
redis.call('set', KEYS[1], ch + r, 'EX', 172800)
redis.call('set', KEYS[2], gl + r, 'EX', 172800)
return 1
`)

// budgetAdjustScript 结算差值（可为负 = 退款），下限钳到 0。
var budgetAdjustScript = redis.NewScript(`
local ch = tonumber(redis.call('get', KEYS[1]) or '0')
local gl = tonumber(redis.call('get', KEYS[2]) or '0')
local d = tonumber(ARGV[1])
ch = ch + d
gl = gl + d
if ch < 0 then ch = 0 end
if gl < 0 then gl = 0 end
redis.call('set', KEYS[1], ch, 'EX', 172800)
redis.call('set', KEYS[2], gl, 'EX', 172800)
return 1
`)

// budgetKeys 渠道/全局预算键（UTC 自然日）。
func budgetKeys(channelID int) (string, string) {
	day := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("probe:budget:ch:%d:%s", channelID, day),
		fmt.Sprintf("probe:budget:global:%s", day)
}

// ToCentsForBudget 美元金额 → 整数分（四舍五入），金额均为小数值（单次探测约 0.0001 美元），
// 保守取整：不足 1 分按 1 分预留，避免零值导致预算完全失效。
func ToCentsForBudget(usd float64) int64 {
	c := int64(math.Round(usd * 100))
	if usd > 0 && c == 0 {
		return 1
	}
	return c
}

// Reserve 按估算成本预留预算（单位：美元金额由调用方转为分）。
// estimateCents/channelBudgetCents/globalBudgetCents 均为“分”。
// 返回 true 表示预留成功；false 表示预算不足或并发占用。
func (b *BudgetTracker) Reserve(ctx context.Context, channelID int, estimateCents, channelBudgetCents, globalBudgetCents int64) (bool, error) {
	if !b.Available() {
		return false, fmt.Errorf("redis unavailable")
	}
	chKey, glKey := budgetKeys(channelID)
	n, err := budgetReserveScript.Run(ctx, b.redis.Client, []string{chKey, glKey},
		estimateCents, channelBudgetCents, globalBudgetCents).Int()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// Adjust 结算实际成本差值（actualCents - reservedCents），可为负（退款）。
func (b *BudgetTracker) Adjust(ctx context.Context, channelID int, deltaCents int64) {
	if !b.Available() {
		return
	}
	chKey, glKey := budgetKeys(channelID)
	_, _ = budgetAdjustScript.Run(ctx, b.redis.Client, []string{chKey, glKey}, deltaCents).Result()
}

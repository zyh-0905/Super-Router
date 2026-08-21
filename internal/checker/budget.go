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
// Redis 不可用时调用方必须失败关闭付费探针（不再退化为无原子性的 DB 检查）。
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
// 键不存在时以数据库当日已消费金额播种（C3）：Redis 重启/清空后
// 计数不会从 0 起算，与 DB 对账保持一致，防止预算被绕过。
// 注意：Lua 中 0 是 truthy，exists 必须显式比较 == 0——
// 「not exists(...)」恒为 false，播种分支会变成死代码。
var budgetReserveScript = redis.NewScript(`
local ch = tonumber(redis.call('get', KEYS[1]) or '0')
local gl = tonumber(redis.call('get', KEYS[2]) or '0')
local r = tonumber(ARGV[1])
-- 键不存在：用 DB 已消费值播种（ARGV[4]/ARGV[5] 为微美元单位）
if redis.call('exists', KEYS[1]) == 0 then
	ch = tonumber(ARGV[4])
end
if redis.call('exists', KEYS[2]) == 0 then
	gl = tonumber(ARGV[5])
end
if ch + r > tonumber(ARGV[2]) or gl + r > tonumber(ARGV[3]) then
	return 0
end
redis.call('set', KEYS[1], ch + r, 'EX', 172800)
redis.call('set', KEYS[2], gl + r, 'EX', 172800)
return 1
`)

// budgetAdjustScript 结算差值（可为负 = 退款），下限钳到 0。
// 键缺失时 no-op：结算只调整仍存在的账本，绝不凭空造出一个
// 清零的当日计数器——探测期间键过期/被清时，get 回退 '0' 再
// set 会把当日累计消费整体抹掉。
var budgetAdjustScript = redis.NewScript(`
if redis.call('exists', KEYS[1]) == 0 or redis.call('exists', KEYS[2]) == 0 then
	return 0
end
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

// BudgetDay 返回指定时刻的 UTC 自然日标识（预算键分桶单位）。
func BudgetDay(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// budgetKeys 渠道/全局预算键（按 UTC 自然日分桶）。
// day 由 Reserve 计算并随结算传递：跨 UTC 午夜时预留与结算必须落在
// 同一个日桶——否则预留记在 day N、退款记到 day N+1，账目永久错位。
func budgetKeys(channelID int, day string) (string, string) {
	return fmt.Sprintf("probe:budget:ch:%d:%s", channelID, day),
		fmt.Sprintf("probe:budget:global:%s", day)
}

// ToBudgetUnits 美元金额 → 整数微美元（1e-6 USD，四舍五入）。
// 「分」作单位比单次探测的真实成本（约 $0.0002）粗两个数量级：
// 每探测固定记 1 分会把渠道预算上限压到设计值的约 3%，且与
// DB 的 SUM(cost) 闸门永久不一致；微美元可精确表示单次探测。
// 保守取整：正金额不足 1 微美元按 1 微美元预留，避免零值使预算失效。
func ToBudgetUnits(usd float64) int64 {
	u := int64(math.Round(usd * 1_000_000))
	if usd > 0 && u == 0 {
		return 1
	}
	return u
}

// Reserve 按估算成本预留预算（单位：微美元，见 ToBudgetUnits）。
// channelSpentUnits/globalSpentUnits 为 DB 汇总的当日已消费金额，
// 用于 Redis 键缺失时播种对账（C3）。
// 返回 (预留成功, 日桶标识, 错误)；日桶标识必须传给同一次探测的
// Adjust，保证跨日结算落在预留所在的桶。
func (b *BudgetTracker) Reserve(ctx context.Context, channelID int, estimateUnits, channelBudgetUnits, globalBudgetUnits, channelSpentUnits, globalSpentUnits int64) (bool, string, error) {
	if !b.Available() {
		return false, "", fmt.Errorf("redis unavailable")
	}
	day := BudgetDay(time.Now())
	chKey, glKey := budgetKeys(channelID, day)
	n, err := budgetReserveScript.Run(ctx, b.redis.Client, []string{chKey, glKey},
		estimateUnits, channelBudgetUnits, globalBudgetUnits, channelSpentUnits, globalSpentUnits).Int()
	if err != nil {
		return false, day, err
	}
	return n == 1, day, nil
}

// Adjust 结算实际成本差值（actualUnits - reservedUnits，可为负 = 退款）。
// day 为 Reserve 返回的日桶标识（同一探测必须用同一日桶结算）。
// 返回错误供调用方处理（H8）：补扣失败时调用方必须保守处置后续探针，
// 不允许静默吞掉导致 Redis 永久少记消费。
func (b *BudgetTracker) Adjust(ctx context.Context, channelID int, day string, deltaUnits int64) error {
	if !b.Available() {
		return fmt.Errorf("redis unavailable")
	}
	chKey, glKey := budgetKeys(channelID, day)
	_, err := budgetAdjustScript.Run(ctx, b.redis.Client, []string{chKey, glKey}, deltaUnits).Result()
	return err
}

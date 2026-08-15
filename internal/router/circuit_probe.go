package router

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 半开探测名额的 Redis 计数器：冷却到期后只放行 half_open_probe_count 个在途探测，
// 防止流量瞬间涌入刚恢复的渠道。TTL 兜底清理崩溃残留的名额。
const halfOpenProbeTTL = 5 * time.Minute

// reserveHalfOpenProbeScript 原子预约探测名额：
// INCR 后若超过限额立即回滚（DECR），避免被拒请求抬高计数器。
var reserveHalfOpenProbeScript = redis.NewScript(`
local n = redis.call('incr', KEYS[1])
if n == 1 then redis.call('expire', KEYS[1], ARGV[1]) end
if n <= tonumber(ARGV[2]) then
	return n
end
redis.call('decr', KEYS[1])
return 0
`)

// releaseHalfOpenProbeScript 原子释放探测名额（键不存在时为空操作，归零时删除键，幂等）
var releaseHalfOpenProbeScript = redis.NewScript(`
if redis.call('exists', KEYS[1]) == 1 then
	local n = redis.call('decr', KEYS[1])
	if n <= 0 then redis.call('del', KEYS[1]) end
	return n
end
return 0
`)

func halfOpenProbeKey(channelID int, model string) string {
	return fmt.Sprintf("circuit:probe:%d:%s", channelID, model)
}

// ReserveHalfOpenProbe 为半开渠道预约一个探测名额，返回是否预约成功。
// limit <= 0 表示不允许探测；Redis 完全不可用时返回错误（调用方应保守跳过该候选），
// 仅当本进程未配置 Redis（nil）时退化为放行，避免半开渠道永久无法恢复。
func (r *Router) ReserveHalfOpenProbe(ctx context.Context, channelID int, model string, limit int) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	if r.redis == nil || r.redis.Client == nil {
		return true, nil
	}
	n, err := reserveHalfOpenProbeScript.Run(ctx, r.redis.Client,
		[]string{halfOpenProbeKey(channelID, model)},
		int(halfOpenProbeTTL.Seconds()), limit).Int()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseHalfOpenProbe 释放半开探测名额（探测结束后调用，幂等）
func (r *Router) ReleaseHalfOpenProbe(ctx context.Context, channelID int, model string) {
	if r.redis == nil || r.redis.Client == nil {
		return
	}
	_, _ = releaseHalfOpenProbeScript.Run(ctx, r.redis.Client,
		[]string{halfOpenProbeKey(channelID, model)}).Result()
}

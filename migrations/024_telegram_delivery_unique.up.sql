-- 投递幂等数据库兜底（M3 整改）：
-- 「先 HasDelivery 查、再发、后 LogDelivery 写」是 check-then-act，
-- 锁失效/多实例降级时会重复投递。成功行加部分唯一索引，
-- LogDelivery 用 ON CONFLICT DO NOTHING 抢占，重复写入不产生第二行。
CREATE UNIQUE INDEX idx_telegram_delivery_logs_window
    ON telegram_delivery_logs(subscriber_id, message_kind, window_start, window_end)
    WHERE success = true;

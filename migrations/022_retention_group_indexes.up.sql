-- 022_retention_group_indexes: request_history 分组时间窗口热查询索引
-- 仪表盘统计、熔断样本与告警评估频繁按
--   (group_id, created_at) 与 (channel_id, model, group_id, created_at)
-- 筛选，单列 group_id 索引需要回表过滤大量历史行。
CREATE INDEX IF NOT EXISTS idx_request_history_group_time
    ON request_history(group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_history_channel_model_group_time
    ON request_history(channel_id, model, group_id, created_at DESC);

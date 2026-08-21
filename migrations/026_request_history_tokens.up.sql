-- 026_request_history_tokens: 真实流量用量与成本统计（B1）
-- 代理层捕获上游返回的 usage（非流式响应体 / 流式末尾 usage chunk），
-- 与实测倍率相乘得到真实成本估算；仅统计，不用于计费。
ALTER TABLE request_history
    ADD COLUMN IF NOT EXISTS prompt_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS completion_tokens INTEGER;

-- 成本聚合高频路径：按站点×模型×时间聚合 token 用量
CREATE INDEX IF NOT EXISTS idx_request_history_tokens
    ON request_history(channel_id, model, created_at DESC);

-- 011_retention_indexes: 快照/仪表盘高频查询索引（按时间取最新）
CREATE INDEX IF NOT EXISTS idx_health_checks_upstream_time ON health_checks(upstream_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_probe_results_upstream_model_time ON probe_results(upstream_id, model, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_balance_checks_channel_time2 ON balance_checks(channel_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS idx_declared_prices_upstream_model_time ON declared_prices(upstream_id, model, checked_at DESC);

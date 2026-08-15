-- 011_retention_indexes: 回滚
DROP INDEX IF EXISTS idx_declared_prices_upstream_model_time;
DROP INDEX IF EXISTS idx_balance_checks_channel_time2;
DROP INDEX IF EXISTS idx_probe_results_upstream_model_time;
DROP INDEX IF EXISTS idx_health_checks_upstream_time;

-- 003_balance 回滚
DROP TABLE IF EXISTS balance_checks;
ALTER TABLE channel_groups DROP COLUMN IF EXISTS balance_interval_seconds;
DROP TABLE IF EXISTS system_settings;

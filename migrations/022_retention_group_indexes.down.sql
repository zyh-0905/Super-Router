-- 022_retention_group_indexes down
DROP INDEX IF EXISTS idx_request_history_group_time;
DROP INDEX IF EXISTS idx_request_history_channel_model_group_time;

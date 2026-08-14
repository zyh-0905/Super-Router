-- 002_groups 回滚
ALTER TABLE decision_logs DROP COLUMN IF EXISTS group_id;
ALTER TABLE request_history DROP COLUMN IF EXISTS group_id;
DROP TABLE IF EXISTS api_key_groups;
DROP TABLE IF EXISTS channel_group_members;
DROP TABLE IF EXISTS channel_groups;

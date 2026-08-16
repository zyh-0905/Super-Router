-- 016_replay_context 回滚：删除重放上下文列与快照归档表
DROP TABLE IF EXISTS snapshot_archive;
ALTER TABLE decision_logs DROP COLUMN capabilities;
ALTER TABLE decision_logs DROP COLUMN estimated_input;
ALTER TABLE decision_logs DROP COLUMN max_output;
ALTER TABLE decision_logs DROP COLUMN timeout_ms;
ALTER TABLE decision_logs DROP COLUMN group_ids;
ALTER TABLE decision_logs DROP COLUMN effective_policy;

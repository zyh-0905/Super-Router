-- 016_replay_context: 决策日志保存完整重放上下文（P1-05）
-- 能力/预算/分组/生效策略在决策时持久化，重放不再依赖“当前环境”。
ALTER TABLE decision_logs ADD COLUMN capabilities JSONB NOT NULL DEFAULT '[]';
ALTER TABLE decision_logs ADD COLUMN estimated_input INT NOT NULL DEFAULT 0;
ALTER TABLE decision_logs ADD COLUMN max_output INT NOT NULL DEFAULT 0;
ALTER TABLE decision_logs ADD COLUMN timeout_ms INT NOT NULL DEFAULT 0;
ALTER TABLE decision_logs ADD COLUMN group_ids JSONB NOT NULL DEFAULT '[]';
ALTER TABLE decision_logs ADD COLUMN effective_policy JSONB NOT NULL DEFAULT '{}';

-- 历史健康快照归档：按内容哈希去重，供确定性重放加载不可变历史快照。
-- 清理策略：decision_logs 保留期清理后，孤儿归档行随之删除（checker 负责）。
CREATE TABLE snapshot_archive (
    checksum    VARCHAR(64) PRIMARY KEY,
    payload     JSONB NOT NULL,
    archived_at TIMESTAMP DEFAULT NOW()
);
CREATE INDEX idx_snapshot_archive_archived_at ON snapshot_archive(archived_at);

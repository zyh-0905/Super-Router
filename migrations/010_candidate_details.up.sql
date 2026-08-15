-- 010_candidate_details: 决策候选的六维评分（雷达图数据）
ALTER TABLE decision_logs ADD COLUMN candidate_details JSONB NOT NULL DEFAULT '[]';

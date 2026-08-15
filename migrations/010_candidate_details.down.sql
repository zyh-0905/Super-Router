-- 010_candidate_details: 回滚
ALTER TABLE decision_logs DROP COLUMN IF EXISTS candidate_details;

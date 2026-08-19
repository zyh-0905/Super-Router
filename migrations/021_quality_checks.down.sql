-- 021_quality_checks 回滚（仅人工执行）
-- 先删结果（外键指向 runs），再删任务
DROP TABLE IF EXISTS quality_check_results;
DROP TABLE IF EXISTS quality_check_runs;

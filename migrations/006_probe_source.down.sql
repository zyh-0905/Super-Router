-- 006_probe_source: 回滚
ALTER TABLE probe_results DROP COLUMN IF EXISTS source;

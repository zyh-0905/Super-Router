-- 006_probe_source: 区分手动/定时探测来源
ALTER TABLE probe_results ADD COLUMN source VARCHAR(20) DEFAULT 'scheduled';

-- 008_ratio_limit: 站点倍率上限（0 = 不限）
ALTER TABLE upstreams ADD COLUMN ratio_limit DECIMAL(10,4) NOT NULL DEFAULT 0;

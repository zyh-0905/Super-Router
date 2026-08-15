-- 014_test_model: 每个站点专属的测试台默认测试模型
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS test_model VARCHAR(100) NOT NULL DEFAULT '';

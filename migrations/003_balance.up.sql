-- 003_balance: 上游余额自动检测

-- 余额检测历史
CREATE TABLE balance_checks (
    id BIGSERIAL PRIMARY KEY,
    channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    balance DECIMAL(12,4) NOT NULL,
    currency VARCHAR(10) DEFAULT 'USD',
    source VARCHAR(50) DEFAULT '',      -- 检测成功的接口（oneapi / openai）
    error TEXT DEFAULT '',               -- 检测失败原因
    checked_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_balance_checks_channel_time ON balance_checks(channel_id, checked_at DESC);

-- 分组：余额检测间隔覆盖（0 = 跟随全局）
ALTER TABLE channel_groups ADD COLUMN balance_interval_seconds INT DEFAULT 0;

-- 系统设置（键值）
CREATE TABLE system_settings (
    key VARCHAR(64) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 默认设置：低余额告警阈值（美元）
INSERT INTO system_settings (key, value) VALUES ('low_balance_threshold', '1.00')
ON CONFLICT (key) DO NOTHING;

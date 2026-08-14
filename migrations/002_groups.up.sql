-- 002_groups: 中转站分组功能
-- 扁平多对多分组 + API Key 绑定 + 日志记录分组 + 默认分组迁移存量站点

-- 分组表
CREATE TABLE channel_groups (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    description TEXT DEFAULT '',
    enabled BOOLEAN DEFAULT true,

    -- 路由：分组默认策略（空 = 回退系统默认）
    default_strategy VARCHAR(50) DEFAULT '',
    -- 分组优先级（组内排序的辅助权重，暂预留）
    group_priority INT DEFAULT 0,

    -- 熔断参数覆盖（0/空 = 使用全局配置）
    cb_min_samples INT DEFAULT 0,
    cb_open_failure_rate DOUBLE PRECISION DEFAULT 0,
    cb_open_min_failures INT DEFAULT 0,
    cb_initial_cooling_seconds INT DEFAULT 0,
    cb_max_cooling_seconds INT DEFAULT 0,

    -- 健康检测覆盖（0 = 使用全局配置）
    alive_interval_seconds INT DEFAULT 0,
    pricing_interval_seconds INT DEFAULT 0,
    probe_interval_seconds INT DEFAULT 0,
    daily_probe_budget DECIMAL(10,2) DEFAULT 0,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 站点 ↔ 分组 多对多
CREATE TABLE channel_group_members (
    channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    group_id INT REFERENCES channel_groups(id) ON DELETE CASCADE,
    created_at TIMESTAMP DEFAULT NOW(),
    PRIMARY KEY (channel_id, group_id)
);
CREATE INDEX idx_cgm_group ON channel_group_members(group_id);

-- API Key ↔ 分组 绑定（无绑定行 = 不限制）
CREATE TABLE api_key_groups (
    api_key_id INT REFERENCES api_keys(id) ON DELETE CASCADE,
    group_id INT REFERENCES channel_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (api_key_id, group_id)
);
CREATE INDEX idx_akg_group ON api_key_groups(group_id);

-- 决策日志与请求历史记录分组
ALTER TABLE decision_logs ADD COLUMN group_id INT REFERENCES channel_groups(id) ON DELETE SET NULL;
ALTER TABLE request_history ADD COLUMN group_id INT REFERENCES channel_groups(id) ON DELETE SET NULL;
CREATE INDEX idx_decision_logs_group ON decision_logs(group_id);
CREATE INDEX idx_request_history_group ON request_history(group_id);

-- 种子：默认分组 + 存量站点迁移
INSERT INTO channel_groups (name, description)
VALUES ('默认分组', '系统自动创建的默认分组')
ON CONFLICT (name) DO NOTHING;

INSERT INTO channel_group_members (channel_id, group_id)
SELECT u.id, g.id
FROM upstreams u
JOIN channel_groups g ON g.name = '默认分组'
ON CONFLICT DO NOTHING;

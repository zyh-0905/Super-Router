-- 007_ratio_groups: 倍率检测分组（站点级自定义）
-- 每组指定默认检测模型，实测该组倍率时用默认模型代表整组
CREATE TABLE channel_ratio_groups (
    id SERIAL PRIMARY KEY,
    channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL,
    default_model VARCHAR(100) NOT NULL DEFAULT '',
    models JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (channel_id, name)
);
CREATE INDEX idx_ratio_groups_channel ON channel_ratio_groups(channel_id);

-- 020_alert_telegram: 告警生命周期持久化 + Telegram 告警通知
-- alert_events：可恢复、可审计的告警事件（Checker reconcile，Web/Telegram 共享读取）
-- telegram_config：单行 Bot 配置（bot_token 按 enc:v1: 密文存储）
-- telegram_subscribers：授权 Chat ID 订阅者（分组过滤）
-- telegram_delivery_logs：每条消息投递审计（幂等重试依据）

CREATE TABLE alert_events (
    id BIGSERIAL PRIMARY KEY,
    alert_key VARCHAR(255) NOT NULL,
    alert_type VARCHAR(64) NOT NULL,
    severity VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    channel_id INT REFERENCES upstreams(id) ON DELETE SET NULL,
    group_id INT REFERENCES channel_groups(id) ON DELETE SET NULL,
    model VARCHAR(100),
    title VARCHAR(200) NOT NULL,
    message TEXT NOT NULL,
    current_value DOUBLE PRECISION,
    threshold_value DOUBLE PRECISION,
    unit VARCHAR(32),
    impact TEXT,
    recommendation TEXT,
    admin_path VARCHAR(255),
    metadata JSONB NOT NULL DEFAULT '{}',
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    recovered_at TIMESTAMP,
    occurrence_count INT NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 同一问题同时最多一条 active（部分唯一索引）；恢复后再次出现插入新周期
CREATE UNIQUE INDEX idx_alert_events_active_key
    ON alert_events(alert_key) WHERE status = 'active';
CREATE INDEX idx_alert_events_channel_time
    ON alert_events(channel_id, created_at DESC);
CREATE INDEX idx_alert_events_status_time
    ON alert_events(status, last_seen_at DESC);

-- Telegram Bot 配置（单行表；bot_token 只存 enc:v1: 密文，API 只回显脱敏状态）
CREATE TABLE telegram_config (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT false,
    bot_token TEXT NOT NULL DEFAULT '',
    report_enabled BOOLEAN NOT NULL DEFAULT true,
    report_interval_minutes INT NOT NULL DEFAULT 60,
    report_minute INT NOT NULL DEFAULT 0,
    timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
    include_recovered BOOLEAN NOT NULL DEFAULT true,
    include_ongoing BOOLEAN NOT NULL DEFAULT true,
    web_base_url TEXT NOT NULL DEFAULT '',
    last_poll_at TIMESTAMP,
    last_update_id BIGINT NOT NULL DEFAULT 0,
    last_report_at TIMESTAMP,
    last_error TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
INSERT INTO telegram_config(id) VALUES (1) ON CONFLICT DO NOTHING;

-- 授权订阅者（仅管理员在 Web 后台手动录入，不开放 /start 自助绑定）
CREATE TABLE telegram_subscribers (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT UNIQUE NOT NULL,
    chat_type VARCHAR(16) NOT NULL DEFAULT 'private',
    display_name VARCHAR(200) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT true,
    alert_enabled BOOLEAN NOT NULL DEFAULT true,
    query_enabled BOOLEAN NOT NULL DEFAULT true,
    group_ids JSONB NOT NULL DEFAULT '[]',
    last_sent_at TIMESTAMP,
    last_error TEXT,
    failure_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_telegram_subscribers_enabled
    ON telegram_subscribers(enabled, alert_enabled);

-- 投递审计：同一窗口幂等重试的依据（崩溃后新 owner 只补发无成功记录的订阅者）
CREATE TABLE telegram_delivery_logs (
    id BIGSERIAL PRIMARY KEY,
    subscriber_id BIGINT REFERENCES telegram_subscribers(id) ON DELETE CASCADE,
    message_kind VARCHAR(32) NOT NULL,
    window_start TIMESTAMP,
    window_end TIMESTAMP,
    success BOOLEAN NOT NULL,
    telegram_message_id BIGINT,
    error TEXT,
    sent_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_telegram_delivery_logs_subscriber_time
    ON telegram_delivery_logs(subscriber_id, sent_at DESC);

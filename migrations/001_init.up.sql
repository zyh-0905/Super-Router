-- 上游中转站配置表
CREATE TABLE upstreams (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) UNIQUE NOT NULL,
    base_url TEXT NOT NULL,
    access_token TEXT NOT NULL,
    api_key TEXT NOT NULL,
    enabled BOOLEAN DEFAULT true,

    -- 路由相关字段
    role VARCHAR(20) DEFAULT 'primary',
    user_priority INT DEFAULT 50,
    model_mapping JSONB,
    capabilities JSONB,
    weight INT DEFAULT 1,

    -- 超时配置
    timeout_connect_ms INT DEFAULT 5000,
    timeout_first_byte_ms INT DEFAULT 10000,
    timeout_total_ms INT DEFAULT 60000,

    -- 预算控制
    daily_probe_budget DECIMAL(10,2) DEFAULT 0.50,

    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 健康检查记录（存活探测）
CREATE TABLE health_checks (
    id BIGSERIAL PRIMARY KEY,
    upstream_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL,
    is_alive BOOLEAN NOT NULL,
    latency_ms INT,
    checked_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_health_checks_upstream_epoch ON health_checks(upstream_id, epoch DESC);
CREATE INDEX idx_health_checks_epoch ON health_checks(epoch DESC);

-- 声明价格记录
CREATE TABLE declared_prices (
    id BIGSERIAL PRIMARY KEY,
    upstream_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL,
    model VARCHAR(100) NOT NULL,
    prompt_ratio DECIMAL(10,4),
    completion_ratio DECIMAL(10,4),
    checked_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_declared_prices_upstream_model_epoch ON declared_prices(upstream_id, model, epoch DESC);

-- 推理探针结果
CREATE TABLE probe_results (
    id BIGSERIAL PRIMARY KEY,
    upstream_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL,
    model VARCHAR(100) NOT NULL,
    success BOOLEAN NOT NULL,
    real_ratio DECIMAL(10,4),
    ttft_ms INT,
    cost DECIMAL(10,6),
    balance_before DECIMAL(10,2),
    balance_after DECIMAL(10,2),
    tokens_used INT,
    checked_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_probe_results_upstream_model_epoch ON probe_results(upstream_id, model, epoch DESC);

-- 熔断状态
CREATE TABLE circuit_states (
    id BIGSERIAL PRIMARY KEY,
    channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    model VARCHAR(100) NOT NULL,
    capability VARCHAR(50) DEFAULT '',
    state VARCHAR(20) NOT NULL,
    opened_at TIMESTAMP,
    cooling_until TIMESTAMP,
    failure_count INT DEFAULT 0,
    success_count INT DEFAULT 0,
    last_probe_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (channel_id, model, capability)
);
CREATE INDEX idx_circuit_states_state ON circuit_states(state);
CREATE INDEX idx_circuit_states_cooling ON circuit_states(cooling_until);

-- 请求历史（用于计算成功率和延迟）
CREATE TABLE request_history (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(64) NOT NULL,
    channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE,
    model VARCHAR(100) NOT NULL,
    capability VARCHAR(50) DEFAULT '',
    success BOOLEAN NOT NULL,
    first_byte_commit BOOLEAN NOT NULL,
    ttft_ms INT,
    total_duration_ms INT,
    status_code INT,
    error_class VARCHAR(50),
    is_probe BOOLEAN DEFAULT false,
    created_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_request_history_channel_model_time ON request_history(channel_id, model, created_at DESC);
CREATE INDEX idx_request_history_created_at ON request_history(created_at DESC);

-- 路由策略配置
CREATE TABLE routing_policies (
    id SERIAL PRIMARY KEY,
    token_id VARCHAR(64),
    model VARCHAR(100),
    policy_version VARCHAR(64) NOT NULL,
    strategy VARCHAR(50) NOT NULL,
    config JSONB NOT NULL,
    activated_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE (token_id, model)
);
CREATE INDEX idx_routing_policies_version ON routing_policies(policy_version);

-- 路由决策日志
CREATE TABLE decision_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(64) UNIQUE NOT NULL,
    token_id_hash VARCHAR(64) NOT NULL,
    model VARCHAR(100) NOT NULL,
    is_stream BOOLEAN,
    policy_version VARCHAR(64) NOT NULL,
    strategy VARCHAR(50) NOT NULL,
    epoch BIGINT NOT NULL,
    snapshot_checksum VARCHAR(64),

    candidate_order JSONB NOT NULL,
    excluded JSONB,
    all_scores JSONB,

    attempts JSONB NOT NULL,
    selected_channel INT REFERENCES upstreams(id) ON DELETE SET NULL,
    decision_reason TEXT,

    decided_at TIMESTAMP NOT NULL
);
CREATE INDEX idx_decision_logs_decided_at ON decision_logs(decided_at DESC);
CREATE INDEX idx_decision_logs_token_model ON decision_logs(token_id_hash, model);
CREATE INDEX idx_decision_logs_policy_epoch ON decision_logs(policy_version, epoch);

-- API Keys 管理
CREATE TABLE api_keys (
    id SERIAL PRIMARY KEY,
    key_hash VARCHAR(64) UNIQUE NOT NULL,
    key_prefix VARCHAR(16) NOT NULL,
    role VARCHAR(20) DEFAULT 'caller',
    enabled BOOLEAN DEFAULT true,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Epoch 计数器（用于生成单调递增的快照版本号）
CREATE TABLE epoch_counter (
    id INT PRIMARY KEY DEFAULT 1,
    current_epoch BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP DEFAULT NOW(),
    CHECK (id = 1)
);
INSERT INTO epoch_counter (id, current_epoch) VALUES (1, 0);

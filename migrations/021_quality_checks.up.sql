-- 021_quality_checks: API 接口质量检测任务队列与阶段结果
-- Gateway 创建任务并代理 SSE；Checker Quality Worker 领取执行；
-- 活跃任务部分唯一索引保证同一站点同时最多一个进行中任务。

CREATE TABLE quality_check_runs (
    id BIGSERIAL PRIMARY KEY,
    channel_id INT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    model VARCHAR(100) NOT NULL,
    depth VARCHAR(16) NOT NULL CHECK (depth IN ('basic', 'full')),
    status VARCHAR(24) NOT NULL CHECK (status IN (
        'queued', 'running', 'cancel_requested', 'completed',
        'failed', 'cancelled', 'expired'
    )),
    overall_status VARCHAR(16) CHECK (overall_status IS NULL OR overall_status IN (
        'good', 'attention', 'failed', 'unknown'
    )),
    current_stage VARCHAR(32) NOT NULL DEFAULT '',
    progress INT NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    attempt_count INT NOT NULL DEFAULT 0,
    worker_id VARCHAR(128) NOT NULL DEFAULT '',
    heartbeat_at TIMESTAMP,
    requested_by_key_hash VARCHAR(64) NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    started_at TIMESTAMP,
    finished_at TIMESTAMP
);

CREATE INDEX idx_quality_check_runs_channel_time
    ON quality_check_runs(channel_id, created_at DESC);
CREATE INDEX idx_quality_check_runs_queue
    ON quality_check_runs(status, created_at);
-- 同一站点同时最多一个 queued/running/cancel_requested 任务
CREATE UNIQUE INDEX idx_quality_check_runs_active_channel
    ON quality_check_runs(channel_id)
    WHERE status IN ('queued', 'running', 'cancel_requested');

CREATE TABLE quality_check_results (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES quality_check_runs(id) ON DELETE CASCADE,
    stage VARCHAR(32) NOT NULL,
    check_name VARCHAR(100) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN (
        'waiting', 'running', 'passed', 'attention', 'failed', 'unknown', 'skipped'
    )),
    http_status INT,
    latency_ms INT,
    ttfb_ms INT,
    actual_model VARCHAR(100) NOT NULL DEFAULT '',
    prompt_tokens INT,
    completion_tokens INT,
    total_tokens INT,
    details JSONB NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (run_id, stage, check_name)
);
CREATE INDEX idx_quality_check_results_run_time
    ON quality_check_results(run_id, created_at);

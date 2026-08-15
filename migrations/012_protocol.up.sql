-- 012_protocol: 站点接口协议（openai 兼容 / anthropic）
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS protocol VARCHAR(20) NOT NULL DEFAULT 'openai';

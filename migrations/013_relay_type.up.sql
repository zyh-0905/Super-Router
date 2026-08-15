-- 013_relay_type: 中转站类型（newapi / sub2api / custom）
ALTER TABLE upstreams ADD COLUMN IF NOT EXISTS relay_type VARCHAR(20) NOT NULL DEFAULT 'custom';

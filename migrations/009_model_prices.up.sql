-- 009_model_prices: 官方模型价格库 + 探测结果 token 拆分与计价基准
-- 价格库：官网输入/输出价与缓存价（$/1M），界面可维护
CREATE TABLE model_prices (
    model VARCHAR(100) PRIMARY KEY,
    input_price_per_m DECIMAL(12,4) NOT NULL,
    output_price_per_m DECIMAL(12,4) NOT NULL,
    cached_read_per_m DECIMAL(12,4),
    cached_write_per_m DECIMAL(12,4),
    note VARCHAR(200) DEFAULT '',
    updated_at TIMESTAMP DEFAULT NOW()
);

-- 探测结果：token 拆分（用于按官网输入/输出价分别计算）、计价基准、
-- 以及探测当时使用的官网价快照（价格库后续调整不影响历史倍率的一致性）
ALTER TABLE probe_results ADD COLUMN prompt_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE probe_results ADD COLUMN completion_tokens INT NOT NULL DEFAULT 0;
ALTER TABLE probe_results ADD COLUMN basis VARCHAR(20) NOT NULL DEFAULT 'baseline';
ALTER TABLE probe_results ADD COLUMN official_input_per_m DECIMAL(12,4);
ALTER TABLE probe_results ADD COLUMN official_output_per_m DECIMAL(12,4);

-- 种子：主流模型价格（2026-08 用户提供，以官网为准）
INSERT INTO model_prices (model, input_price_per_m, output_price_per_m, cached_read_per_m, cached_write_per_m, note) VALUES
('gpt-5.6-sol',           5.0000, 30.0000, 0.5000, 6.2500, '1052K · >272K 上下文价格上浮'),
('gpt-5.6-terra',         2.0000, 12.0000, 0.2000, 2.5000, '1052K · >272K 上下文价格上浮'),
('gpt-5.6-luna',          0.2000,  1.2000, 0.0200, 0.2500, '1052K · >272K 上下文价格上浮'),
('gpt-5.5',               5.0000, 30.0000, 0.5000, NULL,    '1052K · >272K 上下文价格上浮'),
('claude-opus-4.8',       5.0000, 25.0000, 0.5000, 6.2500, '1M · 支持 Fast Mode (×2.0)'),
('claude-opus-5',         5.0000, 25.0000, 0.5000, 6.2500, '1M · 支持 Fast Mode (×2.0)'),
('claude-sonnet-5',       2.0000, 10.0000, 0.2000, 2.5000, '1M · 促销价至 2026-08-31；9-1 后 3.00/15.00'),
('claude-fable-5',       10.0000, 50.0000, 1.0000, 12.5000, '1M · 不支持 Fast Mode'),
('grok-4.5',              2.0000, 12.0000, 0.5000, NULL,    '500K · 支持联网、函数调用'),
('grok-4.6',              2.2000, 13.2000, 0.5500, NULL,    '500K · 支持 Vision，可配置推理强度'),
('gemini-3.1-pro-preview',2.0000, 12.0000, 0.2000, NULL,    '1M · >200K 输入价格上浮 · Cache-Write 按小时计费'),
('gemini-3.6-flash',      1.5000,  7.5000, 0.1500, NULL,    '1M · 搜索接地超额单独收费 · Cache-Write 按小时计费')
ON CONFLICT (model) DO NOTHING;

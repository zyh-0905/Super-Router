-- 018_group_strategy_config: 分组级策略配置（策略中心的每分组权重）
-- 与 default_strategy 配套：default_strategy 为空时该分组跟随系统默认；
-- 非空时 strategy_config 生效（如 balanced_weights）。
ALTER TABLE channel_groups ADD COLUMN strategy_config JSONB NOT NULL DEFAULT '{}';

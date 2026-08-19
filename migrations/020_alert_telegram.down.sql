-- 020_alert_telegram 回滚（仅人工执行，应用启动不会自动跑 down 迁移）
-- 按依赖逆序删除：投递日志 → 订阅者 → 配置 → 告警事件
DROP TABLE IF EXISTS telegram_delivery_logs;
DROP TABLE IF EXISTS telegram_subscribers;
DROP TABLE IF EXISTS telegram_config;
DROP TABLE IF EXISTS alert_events;

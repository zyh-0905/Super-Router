-- 005_balance_token: 自定义余额接口的独立认证令牌
-- 部分中转站的余额接口需要网页控制台会话令牌（或系统访问令牌）
ALTER TABLE upstreams ADD COLUMN balance_api_token TEXT DEFAULT '';

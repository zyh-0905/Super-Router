-- 019_sub2api_auto_login: Sub2API 余额自动登录
-- 站点可配置余额登录邮箱/密码，checker 自动登录换取 JWT 查询余额，
-- 免去手动抓包令牌。密码与 api_key 一致，应用层信封加密后入库。
ALTER TABLE upstreams
    ADD COLUMN balance_login_email    TEXT NOT NULL DEFAULT '',
    ADD COLUMN balance_login_password TEXT NOT NULL DEFAULT '';

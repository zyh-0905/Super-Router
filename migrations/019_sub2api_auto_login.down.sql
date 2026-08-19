-- 019_sub2api_auto_login 回滚
ALTER TABLE upstreams
    DROP COLUMN balance_login_email,
    DROP COLUMN balance_login_password;

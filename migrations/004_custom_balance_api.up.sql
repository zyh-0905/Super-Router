-- 004_custom_balance_api: 站点级自定义余额接口地址
-- 有些中转站的管理 API 不在标准路径（或独立域名），允许逐站点指定

ALTER TABLE upstreams ADD COLUMN balance_api_url TEXT DEFAULT '';

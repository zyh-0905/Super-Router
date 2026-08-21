-- 027_relay_stations: 中转站归并（相同 base_url 的站点归纳为同一中转站）
-- base_url 为规范化后的唯一键（去尾斜杠、scheme/host 小写）；
-- display_name 空 = 自动命名（按 URL 域名/路径）。
-- 行在列表接口查询时按上游站点 lazy reconcile（INSERT ... ON CONFLICT DO NOTHING），
-- 无需在站点 CRUD 路径维护。
CREATE TABLE IF NOT EXISTS relay_stations (
    id           SERIAL PRIMARY KEY,
    base_url     TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

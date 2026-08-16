-- 017_policies_unique: routing_policies 唯一约束升级为 NULLS NOT DISTINCT（PG15+）
-- 目的：系统默认策略行 (token_id IS NULL, model IS NULL) 最多只能有一行，
-- 支持「策略中心」对系统默认策略的稳定 upsert。
-- 先清除历史重复行（保留最小 id），再重建约束。
DELETE FROM routing_policies a
USING routing_policies b
WHERE a.token_id IS NOT DISTINCT FROM b.token_id
  AND a.model IS NOT DISTINCT FROM b.model
  AND a.id > b.id;

ALTER TABLE routing_policies DROP CONSTRAINT routing_policies_token_id_model_key;
ALTER TABLE routing_policies ADD CONSTRAINT routing_policies_token_id_model_key
    UNIQUE NULLS NOT DISTINCT (token_id, model);

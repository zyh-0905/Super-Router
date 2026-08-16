-- 017_policies_unique 回滚：恢复普通 UNIQUE（NULL 视为不同值）
ALTER TABLE routing_policies DROP CONSTRAINT routing_policies_token_id_model_key;
ALTER TABLE routing_policies ADD CONSTRAINT routing_policies_token_id_model_key
    UNIQUE (token_id, model);

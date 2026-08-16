-- 015_circuit_groups 回滚：恢复渠道级熔断唯一键，丢弃分组隔离列
DROP INDEX IF EXISTS idx_circuit_states_channel_group;
ALTER TABLE circuit_states DROP CONSTRAINT circuit_states_channel_model_capability_group_key;
ALTER TABLE circuit_states ADD CONSTRAINT circuit_states_channel_id_model_capability_key
    UNIQUE (channel_id, model, capability);
ALTER TABLE circuit_states DROP COLUMN group_id;

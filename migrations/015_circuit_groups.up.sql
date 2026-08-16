-- 015_circuit_groups: 熔断状态按分组隔离（P1-04）
-- group_id = 0 表示“全局桶”：未限定分组的流量（admin、未绑定 Key、多组并集）共享；
-- group_id > 0 为分组专属桶。request_history.group_id 为 NULL 时归入全局桶。
ALTER TABLE circuit_states ADD COLUMN group_id INT NOT NULL DEFAULT 0;

-- 旧唯一键 (channel_id, model, capability) 不再适用，替换为含 group_id 的版本
ALTER TABLE circuit_states DROP CONSTRAINT circuit_states_channel_id_model_capability_key;
ALTER TABLE circuit_states ADD CONSTRAINT circuit_states_channel_model_capability_group_key
    UNIQUE (channel_id, model, capability, group_id);

CREATE INDEX idx_circuit_states_channel_group ON circuit_states(channel_id, group_id);

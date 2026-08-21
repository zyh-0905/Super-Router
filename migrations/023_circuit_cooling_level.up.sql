-- 熔断退避档位持久化（H4 整改）：
-- degraded 状态再次失败开闸时，需要延续此前的退避档位——
-- 进入 degraded 必经一次成功，failure_count 已清零，
-- 仅靠 failure_count 计算退避会永久退化到最短冷却档。
ALTER TABLE circuit_states ADD COLUMN cooling_level INT NOT NULL DEFAULT 0;

#!/bin/bash

set -e

echo "=== 阶段 1 健康检测子系统验证 ==="
echo ""

# 1. 编译 checker
echo "1. 编译 checker..."
go build -o bin/checker ./cmd/checker
echo "✓ 编译完成"
echo ""

# 2. 添加测试站点（需要替换为真实的中转站配置）
echo "2. 添加测试站点到数据库..."
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
-- 清理旧数据
TRUNCATE TABLE probe_results, declared_prices, health_checks, upstreams RESTART IDENTITY CASCADE;

-- 插入测试站点（请替换为你的真实中转站信息）
INSERT INTO upstreams (name, base_url, access_token, api_key, enabled, role, user_priority, model_mapping, capabilities)
VALUES
  ('test_relay_1', 'https://api.example1.com', 'sk-access-xxx', 'sk-api-xxx', true, 'primary', 100,
   '{"gpt-4o": "gpt-4o", "claude-3-7-sonnet": "claude-sonnet-3.5"}'::jsonb,
   '["tools", "vision"]'::jsonb),
  ('test_relay_2', 'https://api.example2.com', 'sk-access-yyy', 'sk-api-yyy', true, 'backup', 80,
   '{"gpt-4o": "gpt-4o"}'::jsonb,
   '["tools"]'::jsonb);

-- 查看插入的站点
SELECT id, name, base_url, enabled, role FROM upstreams;
EOF

echo "✓ 测试站点已添加"
echo ""

# 3. 运行 checker（后台运行 30 秒，使用本地开发配置连接 localhost）
echo "3. 启动 checker（运行 30 秒）..."
./bin/checker -config configs/config.local.yaml &
CHECKER_PID=$!

echo "✓ Checker 已启动 (PID: $CHECKER_PID)"
echo "等待 30 秒让各个探测任务执行..."
sleep 30

# 停止 checker
echo "停止 checker..."
kill $CHECKER_PID 2>/dev/null || true
wait $CHECKER_PID 2>/dev/null || true
echo "✓ Checker 已停止"
echo ""

# 4. 检查数据库表
echo "4. 检查数据库表..."
echo ""

echo "--- health_checks 表（存活探测记录）---"
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
SELECT
  u.name,
  hc.epoch,
  hc.is_alive,
  hc.latency_ms,
  hc.checked_at
FROM health_checks hc
JOIN upstreams u ON u.id = hc.upstream_id
ORDER BY hc.checked_at DESC
LIMIT 10;
EOF
echo ""

echo "--- declared_prices 表（价格同步记录）---"
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
SELECT
  u.name,
  dp.epoch,
  dp.model,
  dp.prompt_ratio,
  dp.completion_ratio,
  dp.checked_at
FROM declared_prices dp
JOIN upstreams u ON u.id = dp.upstream_id
ORDER BY dp.checked_at DESC
LIMIT 10;
EOF
echo ""

echo "--- probe_results 表（推理探针记录）---"
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
SELECT
  u.name,
  pr.epoch,
  pr.model,
  pr.success,
  pr.real_ratio,
  pr.ttft_ms,
  pr.cost,
  pr.tokens_used,
  pr.checked_at
FROM probe_results pr
JOIN upstreams u ON u.id = pr.upstream_id
ORDER BY pr.checked_at DESC
LIMIT 10;
EOF
echo ""

# 5. 检查预算
echo "--- 今日探针花费统计 ---"
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
SELECT
  u.name,
  COUNT(*) as probe_count,
  SUM(pr.cost) as total_cost,
  u.daily_probe_budget as budget,
  u.daily_probe_budget - COALESCE(SUM(pr.cost), 0) as remaining
FROM upstreams u
LEFT JOIN probe_results pr ON pr.upstream_id = u.id AND pr.checked_at >= CURRENT_DATE
GROUP BY u.id, u.name, u.daily_probe_budget;
EOF
echo ""

# 6. 检查 epoch
echo "--- Epoch 状态 ---"
docker exec -i smart-router-db psql -U gateway -d smart_router << EOF
SELECT current_epoch, updated_at FROM epoch_counter;
EOF
echo ""

echo "=== 验证完成 ==="
echo ""
echo "✓ 如果看到 health_checks 有数据：存活探测正常"
echo "✓ 如果看到 declared_prices 有数据：价格同步正常"
echo "✓ 如果看到 probe_results 有数据：推理探针正常"
echo "✓ 如果预算未超限：预算控制正常"
echo ""
echo "注意：如果站点 URL 是假的，探测会失败，这是正常的。"
echo "请替换 SQL 中的站点信息为真实的中转站配置后重新测试。"

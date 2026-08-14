#!/bin/bash

echo "🔐 测试 API Key 认证"
echo ""

# 测试健康检查（无需认证）
echo "1. 测试健康检查（无需认证）..."
curl -s http://localhost:8080/health | jq .
echo ""

# 测试错误的 API Key
echo "2. 测试错误的 API Key..."
curl -s -X GET http://localhost:8080/admin/channels \
  -H "Authorization: Bearer wrong-key" | jq .
echo ""

# 测试正确的 API Key
echo "3. 测试正确的 API Key..."
curl -s -X GET http://localhost:8080/admin/channels \
  -H "Authorization: Bearer test-admin-key" | jq .
echo ""

echo "✅ 如果第 3 步返回 {\"channels\": [], \"total\": 0}，说明认证正常"
echo "❌ 如果返回 {\"error\": \"invalid api key\"}，说明数据库中的 key_hash 不匹配"

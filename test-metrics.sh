#!/bin/bash
# Smart Router Metrics 测试脚本

set -e

echo "🧪 Testing Smart Router Metrics System..."
echo ""

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

GATEWAY_URL="http://localhost:8080"
METRICS_ENDPOINT="$GATEWAY_URL/metrics"
API_ENDPOINT="$GATEWAY_URL/v1/chat/completions"

# 测试计数器
PASSED=0
FAILED=0

# 测试函数
test_step() {
    echo -e "${BLUE}▶${NC} $1"
}

test_pass() {
    echo -e "${GREEN}✓${NC} $1"
    PASSED=$((PASSED + 1))
}

test_fail() {
    echo -e "${RED}✗${NC} $1"
    FAILED=$((FAILED + 1))
}

# 1. 检查 Gateway 是否运行
test_step "Checking if Gateway is running..."
if curl -s -f "$GATEWAY_URL/health" > /dev/null 2>&1; then
    test_pass "Gateway is running"
else
    test_fail "Gateway is not running on $GATEWAY_URL"
    echo "   Start it with: ./bin/gateway --config configs/config.yaml"
    exit 1
fi
echo ""

# 2. 检查 Metrics 端点
test_step "Checking /metrics endpoint..."
METRICS_OUTPUT=$(curl -s "$METRICS_ENDPOINT")
if [ $? -eq 0 ]; then
    test_pass "Metrics endpoint is accessible"
else
    test_fail "Cannot access metrics endpoint"
    exit 1
fi
echo ""

# 3. 验证核心指标存在
test_step "Verifying core metrics..."

METRICS=(
    "smart_router_requests_total"
    "smart_router_request_duration_seconds"
    "smart_router_routing_duration_seconds"
    "smart_router_channel_success_rate"
    "smart_router_circuit_breaker_state"
    "smart_router_snapshot_load_duration_seconds"
    "smart_router_proxy_requests_total"
    "smart_router_proxy_duration_seconds"
    "smart_router_failover_total"
)

for metric in "${METRICS[@]}"; do
    if echo "$METRICS_OUTPUT" | grep -q "$metric"; then
        test_pass "$metric exists"
    else
        test_fail "$metric not found"
    fi
done
echo ""

# 4. 发送测试请求
test_step "Sending test requests..."

# 使用首次初始化时创建的本地开发 caller Key；可通过环境变量覆盖
CALLER_API_KEY="${CALLER_API_KEY:-test-caller-key}"
echo "   Using caller API key prefix: ${CALLER_API_KEY:0:8}"
echo ""

REQUEST_PAYLOAD='{
  "model": "gpt-4",
  "messages": [{"role": "user", "content": "Hello, this is a test"}],
  "max_tokens": 10
}'

# 发送 5 个测试请求
SUCCESS_COUNT=0
for i in {1..5}; do
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$API_ENDPOINT" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer $CALLER_API_KEY" \
        -d "$REQUEST_PAYLOAD")

    HTTP_CODE=$(echo "$RESPONSE" | tail -n 1)

    if [ "$HTTP_CODE" -eq 200 ]; then
        SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
        echo -e "   ${GREEN}✓${NC} Request $i succeeded (HTTP $HTTP_CODE)"
    else
        echo -e "   ${YELLOW}⚠${NC} Request $i failed (HTTP $HTTP_CODE)"
    fi
done

if [ $SUCCESS_COUNT -gt 0 ]; then
    test_pass "Sent $SUCCESS_COUNT successful requests"
else
    test_fail "All test requests failed"
fi
echo ""

# 5. 验证 Metrics 更新
test_step "Verifying metrics were updated..."
sleep 2  # 等待 metrics 更新

NEW_METRICS_OUTPUT=$(curl -s "$METRICS_ENDPOINT")

# 检查请求计数器
if echo "$NEW_METRICS_OUTPUT" | grep -q "smart_router_requests_total.*[1-9]"; then
    test_pass "Request counter updated"
else
    test_fail "Request counter not updated"
fi

# 检查延迟直方图
if echo "$NEW_METRICS_OUTPUT" | grep -q "smart_router_request_duration_seconds_bucket"; then
    test_pass "Request duration histogram populated"
else
    test_fail "Request duration histogram not populated"
fi

# 检查路由决策耗时
if echo "$NEW_METRICS_OUTPUT" | grep -q "smart_router_routing_duration_seconds"; then
    test_pass "Routing duration recorded"
else
    test_fail "Routing duration not recorded"
fi
echo ""

# 6. 检查后台收集器
test_step "Checking background collector..."

# 检查渠道成功率是否有数据
if echo "$NEW_METRICS_OUTPUT" | grep -q "smart_router_channel_success_rate{"; then
    test_pass "Channel success rate is being collected"
else
    test_fail "Channel success rate not collected (might need to wait 30s)"
fi

# 检查熔断状态
if echo "$NEW_METRICS_OUTPUT" | grep -q "smart_router_circuit_breaker_state{"; then
    test_pass "Circuit breaker state is being collected"
else
    test_fail "Circuit breaker state not collected"
fi
echo ""

# 7. 生成指标样本
test_step "Generating sample metrics data..."
echo "$NEW_METRICS_OUTPUT" | grep "smart_router_" | head -20 > /tmp/smart-router-metrics-sample.txt
test_pass "Sample saved to /tmp/smart-router-metrics-sample.txt"
echo ""

# 8. Prometheus 查询示例
test_step "Example Prometheus queries:"
echo ""
echo -e "${YELLOW}# Real-time QPS${NC}"
echo "rate(smart_router_requests_total[1m])"
echo ""
echo -e "${YELLOW}# Success rate (last 5 minutes)${NC}"
echo "sum(rate(smart_router_requests_total{status=\"success\"}[5m])) / sum(rate(smart_router_requests_total[5m]))"
echo ""
echo -e "${YELLOW}# P95 latency by model${NC}"
echo "histogram_quantile(0.95, sum(rate(smart_router_request_duration_seconds_bucket[5m])) by (le, model))"
echo ""
echo -e "${YELLOW}# Channel success rate${NC}"
echo "smart_router_channel_success_rate"
echo ""

# 总结
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}✅ All tests passed! ($PASSED/$((PASSED+FAILED)))${NC}"
else
    echo -e "${YELLOW}⚠ Some tests failed: $PASSED passed, $FAILED failed${NC}"
fi
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📊 View metrics: $METRICS_ENDPOINT"
echo "📖 文档: MONITORING-QUICKREF.md"
echo ""

exit $FAILED

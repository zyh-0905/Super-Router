#!/bin/bash
# Smart Router 监控系统快速启动脚本

set -e

echo "🚀 Starting Smart Router Monitoring Stack..."
echo ""

# 颜色定义
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# 检查 Docker
if ! command -v docker &> /dev/null; then
    echo -e "${RED}❌ Docker is not installed${NC}"
    exit 1
fi

echo -e "${GREEN}✓${NC} Docker is available"

# 获取项目根目录
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

# 检查配置文件
if [ ! -f "prometheus.yml" ]; then
    echo -e "${RED}❌ prometheus.yml not found${NC}"
    exit 1
fi

if [ ! -f "grafana-dashboard.json" ]; then
    echo -e "${YELLOW}⚠ grafana-dashboard.json not found${NC}"
fi

echo -e "${GREEN}✓${NC} Configuration files found"
echo ""

# 停止已存在的容器
echo "🧹 Cleaning up existing containers..."
docker stop smart-router-prometheus 2>/dev/null || true
docker rm smart-router-prometheus 2>/dev/null || true
docker stop smart-router-grafana 2>/dev/null || true
docker rm smart-router-grafana 2>/dev/null || true
echo -e "${GREEN}✓${NC} Cleanup complete"
echo ""

# 启动 Prometheus
echo "📊 Starting Prometheus..."
docker run -d \
  --name smart-router-prometheus \
  -p 9090:9090 \
  --add-host=host.docker.internal:host-gateway \
  -v "$PROJECT_DIR/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  -v "$PROJECT_DIR/prometheus-alerts.yml:/etc/prometheus/alerts.yml:ro" \
  --restart unless-stopped \
  prom/prometheus \
  --config.file=/etc/prometheus/prometheus.yml \
  --web.console.libraries=/usr/share/prometheus/console_libraries \
  --web.console.templates=/usr/share/prometheus/consoles

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓${NC} Prometheus started on http://localhost:9090"
else
    echo -e "${RED}❌ Failed to start Prometheus${NC}"
    exit 1
fi
echo ""

# 等待 Prometheus 启动
echo "⏳ Waiting for Prometheus to be ready..."
sleep 3

# 启动 Grafana（3001 端口：避免与本机 3000 端口上其他服务冲突）
echo "📈 Starting Grafana..."
docker run -d \
  --name smart-router-grafana \
  -p 3001:3000 \
  -e "GF_SECURITY_ADMIN_PASSWORD=admin" \
  -e "GF_USERS_ALLOW_SIGN_UP=false" \
  --restart unless-stopped \
  grafana/grafana-oss

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓${NC} Grafana started on http://localhost:3001"
    echo -e "   Default credentials: ${YELLOW}admin / admin${NC}"
else
    echo -e "${RED}❌ Failed to start Grafana${NC}"
    exit 1
fi
echo ""

# 等待 Grafana 启动
echo "⏳ Waiting for Grafana to be ready..."
sleep 5

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${GREEN}✅ Monitoring stack started successfully!${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "📊 Access URLs:"
echo "   • Prometheus:     http://localhost:9090"
echo "   • Grafana:        http://localhost:3001"
echo "   • Gateway Metrics: http://localhost:8080/metrics"
echo ""
echo "🔧 Next steps:"
echo "   1. Ensure Smart Router Gateway is running on port 8080"
echo "   2. Check Prometheus targets: http://localhost:9090/targets"
echo "   3. Log in to Grafana (admin/admin) and add Prometheus data source:"
echo "      - URL: http://host.docker.internal:9090 (Mac/Win)"
echo "      - URL: http://172.17.0.1:9090 (Linux)"
echo "   4. Import dashboard from grafana-dashboard.json"
echo ""
echo "📖 文档: MONITORING-QUICKREF.md"
echo ""
echo "🛑 To stop monitoring stack:"
echo "   docker stop smart-router-prometheus smart-router-grafana"
echo ""

# 检查 Gateway 是否运行
if curl -s http://localhost:8080/metrics > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} Gateway is running and exposing metrics"
    echo ""
    echo "🎯 Quick test:"
    echo "   curl http://localhost:8080/metrics | grep smart_router"
else
    echo -e "${YELLOW}⚠ Warning: Gateway is not running on port 8080${NC}"
    echo "   Start it with: ./bin/gateway --config configs/config.yaml"
fi

echo ""

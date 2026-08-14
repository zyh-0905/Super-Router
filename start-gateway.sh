#!/bin/bash

echo "🚀 Smart Router Gateway - 启动脚本（本地开发模式）"
echo ""

# 检查数据库是否运行
echo "📡 检查 PostgreSQL..."
if ! docker ps | grep -q smart-router-db; then
    echo "⚠️  PostgreSQL 未运行，正在启动..."
    docker compose -p smart-router up -d postgres
    echo "⏳ 等待数据库启动（5秒）..."
    sleep 5
else
    echo "✅ PostgreSQL 已运行"
fi

# 检查 Redis 是否运行
echo "📡 检查 Redis..."
if ! docker ps | grep -q smart-router-redis; then
    echo "⚠️  Redis 未运行，正在启动..."
    docker compose -p smart-router up -d redis
    echo "⏳ 等待 Redis 启动（3秒）..."
    sleep 3
else
    echo "✅ Redis 已运行"
fi

# 停止 Docker 中的 Gateway（如果在运行）
echo "🛑 停止 Docker 中的 Gateway..."
if docker ps | grep -q smart-router-gateway; then
    docker compose -p smart-router stop gateway
    echo "✅ Docker Gateway 已停止"
else
    echo "✅ Docker Gateway 未运行"
fi

echo ""
echo "🔧 启动本地 Gateway..."
echo "   配置文件: configs/config.local.yaml"
echo "   监听地址: http://localhost:8080"
echo "   按 Ctrl+C 停止"
echo ""

# 启动 Gateway
./bin/gateway -config configs/config.local.yaml

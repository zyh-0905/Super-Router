#!/bin/bash
# Smart Router Web 控制台 - 快速启动
# 推荐：直接打开 Gateway 托管的页面（需先构建前端 dist）

cd "$(dirname "$0")/.."

if [ ! -f web/dist/index.html ]; then
    echo "📦 首次运行，正在构建前端…"
    (cd web && npm install --no-audit --no-fund && npm run build) || {
        echo "❌ 前端构建失败，请检查 Node.js (>=18) 与网络"
        exit 1
    }
fi

if ! curl -s http://localhost:8080/health > /dev/null 2>&1; then
    echo "⚠️  Gateway 未运行，请先启动：./start-gateway.sh"
fi

URL="http://localhost:8080/"

if command -v open > /dev/null 2>&1; then
    open "$URL"
elif command -v xdg-open > /dev/null 2>&1; then
    xdg-open "$URL"
else
    echo "请手动打开: $URL"
fi

echo "✨ 已打开 $URL"
echo "   默认管理员 Key: test-admin-key"
echo "   开发模式: cd web && npm run dev  (http://localhost:5173)"

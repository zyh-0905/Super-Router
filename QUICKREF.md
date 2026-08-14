# Smart Router Gateway - 快速参考卡片 📋

## 🎯 三个核心工具

| 工具 | 功能 | 端口 |
|-----|------|------|
| **Gateway** | API 网关 + 路由决策 + Web 托管 | 8080 |
| **Checker** | 健康检测（存活/价格/探针/余额，分组感知调度） | - |
| **Replay** | 决策重放 + 审计（CLI） | - |

## ⚡ 一分钟启动

```bash
# 1. 基础设施（PostgreSQL + Redis，首次自动迁移建表）
docker-compose up -d

# 2. 前端构建（首次）
cd web && npm install && npm run build && cd ..

# 3. 编译并启动（本地开发）
go build -o bin/gateway ./cmd/gateway
go build -o bin/checker ./cmd/checker
./bin/gateway -config configs/config.local.yaml -web-dir web
./bin/checker -config configs/config.local.yaml

# 4. 打开 Web 控制台
open http://localhost:8080/

# 前端开发模式（热更新）
cd web && npm run dev   # http://localhost:5173/
```

## 📍 访问地址

| 服务 | 地址 | 说明 |
|-----|------|------|
| Web 控制台 | http://localhost:8080/ | Gateway 同端口托管 |
| Gateway Health | http://localhost:8080/health | 健康检查（无认证） |
| Metrics | http://localhost:8080/metrics | Prometheus 指标 |
| Admin API | http://localhost:8080/admin/* | 管理接口（admin Key） |
| Prometheus | http://localhost:9090 | 监控（`./start-monitoring.sh`） |
| Grafana | http://localhost:3000 | 仪表板 |

## 🔑 默认凭证

| 项 | 值 |
|-----|-----|
| 管理员 API Key | `test-admin-key`（表为空时自动创建） |
| 调用方 API Key | `test-caller-key` |
| PostgreSQL | gateway / gateway_pass · db: smart_router |
| Grafana | admin / admin |

## 🧪 调用网关

```bash
# 基础调用
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <API Key>" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}]}'

# 分组限定路由（body group 字段或 X-Group 头）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <Key>" -H "X-Group: 高优组" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

响应头：`X-Selected-Channel` / `X-Selected-Channel-Id` / `X-Strategy` / `X-Group` / `X-Trace-ID` / `X-Request-ID`

## 🗂 分组速查

| 操作 | 方式 |
|-----|------|
| 创建/配置分组 | Web「站点 → 管理分组」（策略/熔断/检测间隔全覆盖，0=跟随全局） |
| 站点归属分组 | 站点编辑表单（多选）；新建默认归入「默认分组」 |
| 请求指定分组 | body `"group":"组名"` 或 `X-Group` 头（支持名称/ID） |
| Key 绑定分组 | Web「设置 → API Keys → 分组」；未指定组时自动限定绑定组并集 |
| 按组筛选 | `/admin/stats?group_id=` · `/admin/decisions?group_id=` · `/admin/circuit?group_id=` |

策略查找链：Token×模型 → Token → **分组默认** → 系统默认

## 💰 余额检测速查

| 项 | 值 |
|-----|-----|
| 默认频率 | 10 分钟（分组可覆盖 `balance_interval_seconds`） |
| 探测顺序 | 站点自定义接口（`balance_api_url` + `balance_api_token`）→ `/api/user/self` → OpenAI `credit_grants` |
| 低余额告警阈值 | 设置页配置，默认 $1（`GET/PATCH /admin/settings`） |
| 查看 | 站点卡片徽章 / 站点详情「余额」页签 / `GET /admin/channels/:id/balance` |
| 自定义接口抓包 | 网页控制台 F12 → Network → 余额请求的 URL 与 Authorization Bearer 令牌 |

## 🛠 常用运维命令

```bash
# 决策重放
go build -o bin/replay ./cmd/replay
./bin/replay --start "2026-08-13T00:00:00Z" --end "2026-08-13T23:59:59Z"

# 监控（Prometheus + Grafana）
./start-monitoring.sh

# 数据库直连
docker exec -it smart-router-db psql -U gateway -d smart_router

# 查看运行日志
tail -f /tmp/sr-gateway.log   # 若用 start-gateway.sh 则为前台输出
```

## 📚 文档

| 文档 | 说明 |
|------|------|
| `README.md` | 总览 + 快速开始 + 完整 API 表 |
| `web/README.md` | 前端开发（页面/组件/接口对照/设计要点） |
| `MONITORING-QUICKREF.md` | Prometheus/Grafana 速查 |
| `docs-archive/` | 历史开发文档归档（可删除） |

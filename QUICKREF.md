# Smart Router Gateway - 快速参考卡片 📋

## 🎯 三个核心工具

| 工具 | 功能 | 端口 |
|-----|------|------|
| **Gateway** | API 网关 + 路由决策 + Web 托管 | 8080 |
| **Checker** | 健康检测（存活/价格/探针/余额，分组感知调度） | - |
| **Replay** | 决策重放 + 审计（CLI） | - |

## ⚡ 一分钟启动

```bash
# 完整栈：PostgreSQL + Redis + Gateway + Checker
docker compose -p smart-router up -d --build
docker compose -p smart-router ps

# 查看 Gateway/Checker 日志
docker compose -p smart-router logs -f gateway checker
```

打开 <http://localhost:8080/>。Checker 没有宿主机端口，启动后会按配置自动执行存活、价格、推理探针和余额检测；失败的推理探针默认退避 6 小时后重试。

本地源码开发（可选）：安装 Go 1.26+ 和 Node.js 24+ 后，使用 `npm ci && npm run build`、`go build`，再按 README 的本地开发命令启动。

## 📍 访问地址

| 服务 | 地址 | 说明 |
|-----|------|------|
| Web 控制台 | http://localhost:8080/ | Gateway 同端口托管 |
| Gateway Health | http://localhost:8080/health | 健康检查（无认证） |
| Metrics | http://localhost:8080/metrics | Prometheus 指标 |
| Admin API | http://localhost:8080/admin/* | 管理接口（`Authorization: Bearer test-admin-key`） |
| Prometheus | http://localhost:9090 | 监控（`./start-monitoring.sh`） |
| Grafana | http://localhost:3001 | 仪表板 |

## 🔑 默认凭证

| 项 | 值 |
|-----|-----|
| 管理员 API Key | `test-admin-key`（本地开发配置，表为空时自动创建） |
| 调用方 API Key | `test-caller-key` |
| PostgreSQL | gateway / gateway_pass · db: smart_router |
| Grafana | admin / admin |

默认凭证只适用于本地开发；生产环境 `bootstrap_default_keys: false` 会在空库时生成随机 `sr-` 管理员 Key（仅日志打印一次），请及时替换并停用默认 Key。

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

## 🔌 站点配置速查（中转站类型 / 接口协议）

| 项 | 取值 | 作用 |
|-----|------|------|
| 中转站类型 | `newapi` / `sub2api` / `custom` | 自动配置余额接口：new-api → `/api/user/self`，sub2api → `/api/v1/auth/me`；留空按类型自动探测 |
| 接口协议 | `openai` / `anthropic` | anthropic 站点自动 OpenAI↔Anthropic 转换 + `x-api-key` 认证，对外仍是 OpenAI 接口 |
| 默认测试模型 | 每站点独立 | 测试台选择站点后自动预填该模型 |

## 💰 余额检测速查

| 项 | 值 |
|-----|-----|
| 默认频率 | 10 分钟（分组可覆盖 `balance_interval_seconds`） |
| 探测顺序 | 站点自定义接口（`balance_api_url` + `balance_api_token`）→ 类型默认接口 → one-api `/api/user/self` → OpenAI `credit_grants` |
| 响应格式 | 自动识别 `data.quota`（quota 自动换算美元）/ `data.user.quota` / `data.balance` / `total_available`；GET 失败自动回退 POST |
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

# Compose 日志
docker compose -p smart-router logs -f gateway checker

# 查看 Checker 最近余额结果
docker exec smart-router-db psql -U gateway -d smart_router -c "SELECT channel_id, balance, currency, source, error, checked_at FROM balance_checks ORDER BY checked_at DESC LIMIT 20;"

# 停止服务（保留数据卷）
docker compose -p smart-router down
# 不要随意使用 down -v，它会删除 PostgreSQL/Redis 数据卷
```

## 📚 文档

| 文档 | 说明 |
|------|------|
| `README.md` | 总览 + 快速开始 + 完整 API 表 |
| `web/README.md` | 前端开发（页面/组件/接口对照/设计要点） |
| `MONITORING-QUICKREF.md` | Prometheus/Grafana 速查 |

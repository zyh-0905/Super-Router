# Smart Router Gateway

智能路由网关 —— 统一管理多个 LLM 中转站（API Relay）的智能路由系统。

对外暴露一个 OpenAI 兼容接口，内部根据健康数据、价格、延迟、可靠性与**自定义分组**，把每个请求自动路由到最合适的上游站点，并在故障时自动切换。

## 核心特性

### 🧭 智能路由引擎
- 不可变健康快照（Redis 缓存 + SHA256 校验）+ 确定性决策（相同输入 = 相同输出）
- **5 种路由策略**：`custom_priority`（自定义优先级，默认）、`price_first`（低价）、`latency_first`（低延迟）、`reliability_first`（高成功率）、`balanced`（加权综合）
- 策略查找链：Token×模型 → Token → **分组默认** → 系统默认
- 8 类硬过滤：禁用、模型不支持、能力缺失、熔断中、超价格上限等
- 首字节前故障切换（最多 3 次候选）；四态熔断器 closed → open → half_open → degraded，指数退避冷却

### 🗂 中转站分组
- 扁平**多对多分组**：一个站点可属于多个分组；站点/分组/Key 归属全部界面化管理
- 请求通过 body `group` 字段或 `X-Group` 头指定分组，**网关只在组内站点中路由**
- 分组级配置：默认策略、熔断参数、健康检测间隔、探针预算（0 = 跟随全局）
- **API Key 分组绑定**：caller Key 可绑定分组——未指定组时自动限定绑定组并集内路由，越组请求 403；admin 不受限
- 决策日志与请求历史记录 group_id，统计/决策/熔断接口支持按组筛选

### 🩺 健康检测（checker 进程）
- 分组感知调度器（5 秒 tick，每站点按有效间隔独立调度）
- **存活探测**（默认 30s）：`GET /v1/models`
- **价格同步**（默认 10m）：`GET /api/pricing`（one-api 系）
- **推理探针**（默认 1h）：真实推理，**余额差值反推真实倍率**，全局/分组/站点三重预算保护
- **余额检测**（默认 10m）：见下节

### 💰 余额自动检测
- **多协议自动探测**：站点自定义接口 → one-api/new-api（`/api/user/self`）→ OpenAI 官方（`credit_grants`）
- **站点级自定义接口**：余额接口地址 + 独立令牌均可逐站点配置（适配不开放标准管理 API 的中转站，如网页控制台 JWT 会话令牌）
- 站点卡片余额徽章、详情页余额历史折线；**低余额告警**（阈值可在设置页配置，默认 $1）

### 📊 可观测性
- 全量决策日志（策略、分组、候选排序与得分、排除原因、快照校验和）
- Prometheus 指标（9 个核心指标）+ Grafana 仪表板 + 14 条告警规则
- 结构化日志（Zap + trace_id 全链路）
- 决策重放 CLI（`./bin/replay`）：历史决策回放与策略对比

### 🖥 Web 控制台（Vue 3 + Vite）
苹果风格、明暗双主题（跟随系统）、全真实数据：

| 页面 | 功能 |
|---|---|
| 总览 | 24h 请求/成功率/延迟（同比）、趋势图、模型分布、告警、分组切换器 |
| 站点 | 分组筛选、站点增删改、健康/统计/余额详情、上游模型列表一键映射 |
| 测试台 | 真流式请求、分组限定路由、路由决策信息（渠道/策略/分组/Trace ID） |
| 决策 | 决策审计表格（分组列）、筛选、详情抽屉（候选得分/排除原因） |
| 熔断 | 四态熔断器、分组切换、手动重置 |
| 设置 | 连接配置、API Keys（分组绑定）、默认测试模型、低余额阈值 |

## 快速开始

### 1. 启动基础服务

```bash
docker-compose up -d        # PostgreSQL 16 + Redis 7
```

首次启动自动执行迁移脚本（含分组/余额表）并创建默认 API Key：
- 管理员：`test-admin-key`（全部权限）
- 调用方：`test-caller-key`（仅调用 API）

### 2. 构建并启动服务

```bash
# 前端（首次）
cd web && npm install && npm run build && cd ..

# 编译
go build -o bin/gateway ./cmd/gateway
go build -o bin/checker ./cmd/checker

# 启动（本地开发用 config.local.yaml，连 localhost 数据库）
./bin/gateway -config configs/config.local.yaml -web-dir web
./bin/checker -config configs/config.local.yaml
```

### 3. 打开 Web 控制台

浏览器访问 **http://localhost:8080/**（Gateway 同端口静态托管，无跨域问题）。

开发模式：`cd web && npm run dev` → http://localhost:5173/（API 自动代理到 :8080）。

## 使用网关（调用方视角）

```bash
# 全站点路由
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <你的 API Key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}]}'

# 限定在指定分组内路由（body 字段或 X-Group 头二选一）
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <绑定了该分组的 Key>" \
  -H "X-Group: 高优组" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

响应头携带真实路由信息：`X-Selected-Channel`、`X-Strategy`、`X-Group`、`X-Trace-ID`。

## 管理 API 一览

| 功能 | 接口 |
|---|---|
| 站点 CRUD | `GET/POST/PATCH/DELETE /admin/channels[/:id]` |
| 站点健康 / 余额 | `GET /admin/health/:id` · `GET /admin/channels/:id/balance` |
| 上游模型列表 | `GET /admin/channels/:id/models` · `POST /admin/upstream/models` |
| 分组 CRUD | `GET/POST /admin/groups` · `PATCH/DELETE /admin/groups/:id` |
| 统计聚合 | `GET /admin/stats[?group_id=]` |
| 决策日志 | `GET /admin/decisions?limit=[&group_id=]` |
| 熔断 | `GET /admin/circuit[?group_id=]` · `POST /admin/circuit/:id/reset` |
| API Keys | `GET/POST /admin/keys` · `PATCH/DELETE /admin/keys/:id`（支持分组绑定） |
| 系统设置 | `GET/PATCH /admin/settings`（低余额阈值） |
| 运行配置 | `GET /admin/config`（只读） |
| 健康检查 | `GET /health`（无认证）· `GET /metrics`（Prometheus） |

## 配置说明

配置文件 `configs/config.yaml`（本地开发用 `configs/config.local.yaml`）：

```yaml
server:
  port: 8080
checker:
  alive_interval: 30s      # 存活探测
  pricing_interval: 10m    # 价格同步
  probe_interval: 1h       # 推理探针
  balance_interval: 10m    # 余额检测
  daily_probe_budget: 5.00 # 全局每日探针预算（美元）
routing:
  default_strategy: custom_priority
  max_attempts: 3
  total_budget_ms: 15000
  circuit_breaker: {...}   # 熔断参数（分组可覆盖）
```

环境变量覆盖：`DATABASE_HOST/PORT/USER/PASSWORD/NAME`、`REDIS_HOST/PORT`。

## 决策重放（CLI）

```bash
go build -o bin/replay ./cmd/replay
./bin/replay --start "2026-08-13T00:00:00Z" --end "2026-08-13T23:59:59Z" --table
# 完整参数见 ./bin/replay --help
```

## 项目结构

```
smart-router/
├── cmd/
│   ├── gateway/          # 主服务：API 网关 + 路由 + 静态托管 Web
│   ├── checker/          # 健康检测：分组感知调度器（存活/价格/探针/余额）
│   └── replay/           # 决策重放 CLI
├── internal/
│   ├── api/              # HTTP handlers（认证/代理/管理/熔断）
│   ├── router/           # 路由决策引擎（快照/策略/过滤/排序）
│   ├── checker/          # 检测器实现
│   ├── store/            # PostgreSQL / Redis 访问
│   ├── config/           # 配置加载
│   ├── metrics/          # Prometheus 指标
│   └── logger/           # Zap 日志
├── migrations/           # SQL 迁移（001 基础 / 002 分组 / 003 余额 / 004-005 余额接口扩展）
├── configs/              # 配置文件
├── web/                  # 前端（Vue 3 + Vite，构建产物 dist/ 由 Gateway 托管）
├── web-legacy/           # 旧版单文件前端（历史备份）
├── docs-archive/         # 历史开发文档归档（可安全删除）
├── prometheus.yml / prometheus-alerts.yml / grafana-dashboard.json
├── docker-compose.yml
└── Dockerfile            # 三阶段构建（前端 → Go → 运行时）
```

## 技术栈

- **后端**：Go 1.26 · Gin · pgxpool · go-redis · Viper · Zap
- **前端**：Vue 3 · Vite · vue-router · ECharts 5（苹果风设计系统，明暗双主题）
- **数据**：PostgreSQL 16 · Redis 7
- **监控**：Prometheus · Grafana

## 文档索引

| 文档 | 说明 |
|---|---|
| `README.md` | 本文档（总览 + 快速开始 + API） |
| `web/README.md` | 前端开发文档（页面/组件/接口对照） |
| `QUICKREF.md` | 快速参考卡（常用命令/分组/余额速查） |
| `MONITORING-QUICKREF.md` | Prometheus/Grafana 监控速查 |
| `docs-archive/` | 历史阶段文档归档，可安全删除 |

## License

MIT

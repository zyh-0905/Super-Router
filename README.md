# Smart Router Gateway

智能路由网关 —— 统一管理多个 LLM 中转站（API Relay）的智能路由系统。

对外暴露一个 OpenAI 兼容接口，内部根据健康数据、价格、延迟、可靠性与**自定义分组**，把每个请求自动路由到最合适的上游站点，并在故障时自动切换。

## 核心特性

### 🧭 智能路由引擎
- 不可变健康快照（Redis 缓存 + SHA256 校验）+ 确定性决策（相同输入 = 相同输出）
- **5 种路由策略**：`custom_priority`（自定义优先级，默认）、`price_first`（低价）、`latency_first`（低延迟）、`reliability_first`（高成功率）、`balanced`（加权综合）
- 策略查找链：Token×模型 → Token → **分组默认** → 系统默认
- 硬过滤：禁用、模型不支持、能力缺失、超价格上限、熔断开闸/冷却中、超延迟上限等
- 首字节前故障切换（最多 3 次候选）；四态熔断器 closed → open → half_open → degraded，指数退避冷却，冷却到期自动半开并按 `half_open_probe_count` 放行探测流量自愈
- **决策可解释**：每个候选按成本/可靠性/延迟/负载/优先级/综合六维打分（0-100）写入决策日志，Web 决策页以雷达图呈现

### 🗂 中转站分组
- 扁平**多对多分组**：一个站点可属于多个分组；站点/分组/Key 归属全部界面化管理
- 请求通过 body `group` 字段或 `X-Group` 头指定分组，**网关只在组内站点中路由**
- 分组级配置：默认策略、熔断参数、健康检测间隔、探针预算（0 = 跟随全局）
- **API Key 分组绑定**：caller Key 可绑定分组——未指定组时自动限定绑定组并集内路由，越组请求 403；admin 不受限
- 决策日志与请求历史记录 group_id，统计/决策/熔断接口支持按组筛选

### 🔌 接口协议与中转站类型
- **站点级接口协议**：`openai`（OpenAI 兼容，默认）/ `anthropic`（Claude 原生）。网关对外始终是 OpenAI 接口，anthropic 站点自动完成请求/响应/SSE 流式双向转换与 `x-api-key` 认证，健康检查/推理探针/模型列表同步适配
- **中转站类型**：`newapi`（new-api/one-api 系）/ `sub2api`（Sub2API）/ `custom`（自定义）。选择类型后自动配置余额接口：new-api → `/api/user/self`，sub2api → `/api/v1/auth/me`；余额接口地址留空时按类型自动探测
- 余额响应自动识别多格式：one-api `data.quota`（quota 单位自动换算美元）、new-api 会话嵌套 `data.user.quota`、`data.balance`、OpenAI `total_available`；仅支持 POST 的余额接口自动 GET→POST 回退

### 🩺 健康检测（checker 进程）
- 分组感知调度器（5 秒 tick，每站点按有效间隔独立调度）
- **存活探测**（默认 30s）：`GET /v1/models`（按站点协议自动选择认证头）
- **价格同步**（默认 10m）：同步上游声明的 prompt/completion 倍率
- **推理探针**（默认 1h）：真实推理，**余额差值反推真实倍率**，全局/分组/站点三重预算保护；失败的探针退避重试（默认 6h）
- **余额检测**（默认 10m）：见下节

### ✨ 实时倍率（实测真实价格）
- 站点「倍率」页签**按需实测**指定模型的真实倍率（推理前后余额差 ÷ token × 官网价），支持流式展示、并发锁（409）、每日预算门禁（429）
- **倍率检测分组**：每站点可建多个检测组，各组自定义默认检测模型（如 4o 系、Claude 系），支持整组一键实测；卡片展示代表倍率与成员
- 实测倍率（`official` 基准优先于 `baseline`）立即失效快照并参与路由成本估算；可设站点**倍率上限**，超限产生告警
- **官方模型价格库**：内置常用模型官网输入/输出价，实测时快照官网价并换算真实单价

### 💰 余额自动检测
- **多协议自动探测**：站点自定义接口 → 中转站类型默认接口 → one-api/new-api（`/api/user/self`）→ OpenAI 官方（`credit_grants`）
- **站点级自定义接口**：余额接口地址 + 独立令牌均可逐站点配置（适配不开放标准管理 API 的中转站，如网页控制台 JWT 会话令牌）；401/403 时解析响应错误码并提示令牌过期
- 站点卡片余额徽章、详情页余额历史折线；**低余额告警**（阈值可在设置页配置，默认 $1）

### 📊 可观测性
- 全量决策日志（策略、分组、候选排序与得分、排除原因、快照校验和）
- Prometheus 指标（9 个核心指标）+ Grafana 仪表板 + 13 条告警规则
- 结构化日志（Zap + trace_id 全链路）
- 决策重放 CLI（`./bin/replay`）：历史决策回放与策略对比

### 🖥 Web 控制台（Vue 3 + Vite）
苹果风格、明暗双主题（跟随系统）、全真实数据：

| 页面 | 功能 |
|---|---|
| 总览 | 24h 请求/成功率/延迟（同比）、趋势图、模型分布、告警、分组切换器、站点综合信息抽屉（倍率/余额/健康/成功率/延迟五图） |
| 站点 | 分组筛选、站点增删改（接口协议/中转站类型/默认测试模型）、健康/统计/余额/倍率详情、倍率检测分组、上游模型列表一键映射 |
| 测试台 | 真流式请求、站点选择自动预填该站点默认测试模型、分组限定路由、路由决策信息（渠道/策略/分组/Trace ID） |
| 决策 | 决策审计表格（分组列）、筛选、详情抽屉（候选六维评分雷达图/排除原因） |
| 熔断 | 四态熔断器、分组切换、手动重置 |
| 设置 | 连接配置、API Keys（分组绑定）、每站点默认测试模型、官方模型价格库、低余额阈值 |

## 快速开始

### 1. 准备环境

完整 Compose 启动需要：

- Docker Desktop（Windows/macOS）或 Docker Engine（Linux）
- Docker Compose v2（`docker compose` 子命令）
- 可访问 Docker Hub 的网络；首次构建会下载 Go、Node、PostgreSQL、Redis 镜像和依赖

建议先确认环境：

```bash
docker version
docker compose version
```

### 2. 一键启动完整栈（推荐）

```bash
docker compose -p smart-router up -d --build
docker compose -p smart-router ps
```

该命令会启动四个服务：PostgreSQL 16、Redis 7、Gateway 和 Checker。Gateway 对外提供 API/Web，Checker 在后台执行存活、价格、推理探针和余额检测，不占用宿主机端口。首次初始化数据库时会执行 `migrations/` 中的脚本。API Key 引导行为由 `configs/config.yaml` 的 `server.bootstrap_default_keys` 控制：

- **本地开发**（`config.local.yaml`，`bootstrap_default_keys: true`）：表为空时自动创建 `test-admin-key`（管理员）与 `test-caller-key`（调用方）；
- **生产**（默认 `false`）：空库时生成一次性随机管理员 Key（`sr-` 前缀），只在启动日志打印一次，请立即妥善保存。

查看状态和日志：

```bash
docker compose -p smart-router ps
docker compose -p smart-router logs -f gateway checker
docker compose -p smart-router restart checker
```

访问地址：

- Web 控制台：<http://localhost:8080/>
- Gateway 健康检查：<http://localhost:8080/health>
- Prometheus 指标：<http://localhost:8080/metrics>

Checker 默认周期为存活 30 秒、价格 10 分钟、余额 10 分钟、推理探针 1 小时；失败的推理探针默认退避 6 小时后重试。推理探针会调用真实上游并可能产生费用，全局每日预算默认为 `$5`。如不希望产生探针费用，可在 `configs/config.yaml` 或分组设置中调整探针周期/预算。历史数据默认保留 30 天（`checker.retention_days`）。

PostgreSQL 和 Redis 使用命名数据卷持久化。不要使用 `docker compose down -v`，除非确认要删除全部本地数据。`/docker-entrypoint-initdb.d` 只会在 PostgreSQL 数据卷首次初始化时自动执行；已有数据卷不会因为新增迁移文件而自动补跑，需要按迁移顺序手工执行未应用的 SQL。

### 3. 管理 API 认证

`/health` 和 `/metrics` 无需认证；所有 `/admin/*` 接口必须携带管理员 Bearer Key：

```bash
curl -H "Authorization: Bearer test-admin-key" http://localhost:8080/admin/groups
```

默认 Key 仅适用于本地开发。部署到共享或生产环境前，请在 Web 控制台创建新 Key，并停用默认 Key，同时通过环境变量或安全配置替换数据库凭据。

### 4. 本地 Go/Vite 开发（可选）

适用于需要调试源码的场景；日常运行优先使用上面的 Compose 栈。先启动 PostgreSQL/Redis，再在本机安装 Go 1.26+ 和 Node.js 24+：

```bash
# 前端（首次）
cd web && npm ci && npm run build && cd ..

# 编译
go build -o bin/gateway ./cmd/gateway
go build -o bin/checker ./cmd/checker
go build -o bin/replay ./cmd/replay

# 启动（本地开发配置连接 localhost 上的数据库）
./bin/gateway -config configs/config.local.yaml -web-dir web
./bin/checker -config configs/config.local.yaml
```

开发模式可另开终端运行 `cd web && npm run dev`，访问 <http://localhost:5173/>；Vite 会把 API 代理到 Gateway 的 8080 端口。

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

响应头携带真实路由信息：`X-Selected-Channel`、`X-Selected-Channel-Id`、`X-Strategy`、`X-Group`、`X-Trace-ID`、`X-Request-ID`。

## 管理 API 一览

| 功能 | 接口 |
|---|---|
| 站点 CRUD | `GET/POST/PATCH/DELETE /admin/channels[/:id]`（含协议/中转站类型/默认测试模型/倍率上限等字段） |
| 站点健康 / 余额 | `GET /admin/health/:id` · `GET /admin/channels/:id/balance` |
| 上游模型列表 | `GET /admin/channels/:id/models` · `POST /admin/upstream/models`（按站点协议发送认证头） |
| 实时倍率 | `GET /admin/channels/:id/ratio` · `POST /admin/channels/:id/probe-ratio`（按需实测，计入每日探测预算） |
| 倍率检测分组 | `POST/PATCH/DELETE /admin/channels/:id/ratio-groups[/:gid]` · `POST /admin/channels/:id/ratio-groups/:gid/probe`（每组自定义默认检测模型） |
| 官方模型价格库 | `GET/POST /admin/model-prices` · `DELETE /admin/model-prices/:model` |
| 分组 CRUD | `GET/POST /admin/groups` · `PATCH/DELETE /admin/groups/:id` |
| 统计聚合 | `GET /admin/stats[?group_id=]` · `GET /admin/channel-metrics`（站点综合指标） |
| 决策日志 | `GET /admin/decisions?limit=[&group_id=]`（含候选六维评分与故障切换明细） |
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
  bootstrap_default_keys: false  # 生产：空库生成随机管理员 Key（仅日志打印一次）
checker:
  alive_interval: 30s      # 存活探测
  pricing_interval: 10m    # 价格同步
  probe_interval: 1h       # 推理探针
  balance_interval: 10m    # 余额检测
  daily_probe_budget: 5.00 # 全局每日探针预算（美元）
  probe_failed_backoff: 6h # 探针失败退避重试
  probe_model: "gpt-4o"    # 推理探针使用的模型
  retention_days: 30       # 历史数据保留天数
routing:
  default_strategy: custom_priority
  max_attempts: 3
  total_budget_ms: 15000
  filter: { max_price_cap: 100.0, max_ttft_ms: 5000 }   # 硬过滤上限
  circuit_breaker: {...}   # 熔断参数（分组可覆盖）
  balanced_weights: {...}  # balanced 策略权重（cost/reliability/latency/load）
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
│   ├── api/              # HTTP handlers（认证/代理/管理/熔断/倍率）
│   ├── router/           # 路由决策引擎（快照/策略/过滤/排序/六维评分）
│   ├── checker/          # 检测器实现（存活/定价/探针/余额/熔断）
│   ├── protocol/         # OpenAI ↔ Anthropic 协议转换与中转站类型定义
│   ├── store/            # PostgreSQL / Redis 访问
│   ├── config/           # 配置加载
│   ├── metrics/          # Prometheus 指标
│   └── logger/           # Zap 日志
├── migrations/           # SQL 迁移（001 基础 ~ 014 站点测试模型）
├── configs/              # 配置文件
├── web/                  # 前端（Vue 3 + Vite，构建产物 dist/ 由 Gateway 托管）
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

## License

MIT

# Smart Router Gateway

智能路由网关 —— 统一管理多个 LLM 中转站（API Relay）的智能路由系统。

对外暴露一个 OpenAI 兼容接口，内部根据健康数据、价格、延迟、可靠性与**自定义分组**，把每个请求自动路由到最合适的上游站点，并在故障时自动切换。

## 核心特性

### 🧭 智能路由引擎
- 不可变健康快照（Redis 缓存 + SHA256 校验）+ 确定性决策（相同输入 = 相同输出）
- **快照批量加载**：健康/价格/探针/历史/熔断按表各一次查询装配，往返次数与站点数无关（缓存到期瞬间不会被 N+1 放大成 DB 尖峰）
- **请求体透明转发**：网关只解析路由所需字段，其余字段（`top_p`/`stop`/`response_format`/`tool_choice`/多模态内容等）原样转发上游，仅移除网关扩展字段 `group`，OpenAI 兼容语义不被破坏
- **5 种路由策略**：`custom_priority`（自定义优先级，默认）、`price_first`（低价）、`latency_first`（低延迟）、`reliability_first`（高成功率）、`balanced`（加权综合）
- 策略查找链：Token×模型 → Token → **分组默认** → 系统默认
- 硬过滤：禁用、模型不支持、能力缺失、超价格上限、熔断开闸/冷却中、超延迟上限等
- 首字节前故障切换（最多 3 次候选）；**TTFB 尝试级超时使用独立 sentinel 错误**，与客户端断开严格区分，超时自动切换下一个候选（P1-02 整改）；四态熔断器 closed → open → half_open → degraded，指数退避冷却，冷却到期自动半开并按 `half_open_probe_count` 放行探测流量自愈
- **熔断状态按分组隔离**（P1-04 整改）：`circuit_states` 以分组为独立桶，一个分组的失败不会打开另一个分组的熔断
- **决策可解释**：每个候选按成本/可靠性/延迟/负载/优先级/综合六维打分（0-100）写入决策日志，Web 决策页以雷达图呈现

### 🗂 中转站分组
- 扁平**多对多分组**：一个站点可属于多个分组；站点/分组/Key 归属全部界面化管理
- 请求通过 body `group` 字段或 `X-Group` 头指定分组，**网关只在组内站点中路由**
- 分组级配置：默认策略、熔断参数、健康检测间隔、探针预算（0 = 跟随全局）
- **API Key 分组绑定**：caller Key 可绑定分组——未指定组时自动限定绑定组并集内路由，越组请求 403；**绑定恰好一个组时自动采用该组的默认策略/组级熔断并写入审计**；绑定多个组时为并集路由（策略按全局、group_ids 记入决策日志）；admin 不受限
- 决策日志与请求历史记录 group_id，统计/决策/熔断接口支持按组筛选

### 🔌 接口协议与中转站类型
- **站点级接口协议**：`openai`（OpenAI 兼容，默认）/ `anthropic`（Claude 原生）。网关对外始终是 OpenAI 接口，anthropic 站点自动完成请求/响应/SSE 流式双向转换与 `x-api-key` 认证，健康检查/推理探针/模型列表同步适配
- **协议转换覆盖范围**：文本消息、**多模态内容块**（`image_url` 的 data URL 转 base64 源、外链转 url 源）、system 提示、**工具调用双向转换**（请求侧 `tools`/`tool_calls`/`tool` 角色 → `input_schema`/`tool_use`/`tool_result`；响应侧 `tool_use` → `tool_calls`，非流式与流式 `input_json_delta` 均已覆盖）
- **中转站类型**：`newapi`（new-api/one-api 系）/ `sub2api`（Sub2API）/ `custom`（自定义）。选择类型后自动按「Base URL + 类型默认路径」补全余额接口完整地址：new-api → `{base_url}/api/user/self`，sub2api → `{base_url}/api/v1/auth/me?timezone=Asia%2FShanghai`；修改 Base URL 时若余额接口未手动改过会跟随更新；余额接口地址留空时按类型自动探测
- 余额响应自动识别多格式：one-api `data.quota`（quota 单位自动换算美元）、new-api 会话嵌套 `data.user.quota`、`data.balance`、OpenAI `total_available`；仅支持 POST 的余额接口自动 GET→POST 回退

### 🩺 健康检测（checker 进程）
- 分组感知调度器（5 秒 tick，每站点按有效间隔独立调度）
- **存活探测**（默认 30s）：`GET /v1/models`（按站点协议自动选择认证头）
- **价格同步**（默认 10m）：同步上游声明的 prompt/completion 倍率（仅 `newapi` 类型——`/api/pricing` 为 new-api/one-api 系接口，sub2api/custom 自动跳过）。空模型名或非正倍率的条目**入库前丢弃并告警**（零价格会被成本估算视为"免费"而扭曲低价策略排序）；**每轮查询计入该站点 request_history**（`is_probe` 标记），失败按错误类别归类（auth_error/rate_limited/timeout/upstream_error/decode_error 等），最近 30 分钟同步失败的站点产生「价格同步失败」告警
- **推理探针**（默认 1h）：真实推理，**余额差值反推真实倍率**，全局/分组/站点三重预算保护；失败的探针退避重试（默认 6h）；**模型使用站点默认测试模型（test_model）**，未配置时回退全局 `checker.probe_model`
- **余额检测**（默认 10m）：见下节

### ✨ 实时倍率（实测真实价格）
- 站点「倍率」页签**按需实测**指定模型的真实倍率（推理前后余额差 ÷ token × 官网价），支持流式展示、并发锁（409）、每日预算门禁（429）
- **倍率检测分组**：每站点可建多个检测组，各组自定义默认检测模型（如 4o 系、Claude 系），支持整组一键实测；卡片展示代表倍率与成员
- 实测倍率（`official` 基准优先于 `baseline`）立即失效快照并参与路由成本估算；可设站点**倍率上限**，超限产生告警
- **官方模型价格库**：内置常用模型官网输入/输出价，实测时快照官网价并换算真实单价

### 💰 余额自动检测
- **多协议自动探测**：站点自定义接口 → 中转站类型默认接口 → one-api/new-api（`/api/user/self`）→ OpenAI 官方（`credit_grants`）
- **Sub2API 余额自动登录**：站点配置余额登录邮箱/密码后，checker 自动调用登录接口换取会话 JWT 查询余额，免去手动抓包令牌；令牌缓存于 Redis（TTL 跟随 expires_in），余额接口 401 时自动重登重试一次；登录失败回退静态令牌链。密码与其它凭据一致应用层信封加密入库，接口只回显邮箱
- **站点级自定义接口**：余额接口地址 + 独立令牌均可逐站点配置（适配不开放标准管理 API 的中转站，如网页控制台 JWT 会话令牌）；401/403 时解析响应错误码并提示令牌过期
- 站点卡片余额徽章、详情页余额历史折线；**低余额告警**（阈值可在设置页配置，默认 $1）

### 📊 可观测性
- 全量决策日志（策略、分组、候选排序与得分、排除原因、快照校验和）
- Prometheus 指标（11 个核心指标）+ Grafana 仪表板 + 13 条告警规则
- 结构化日志（Zap + trace_id 全链路）
- 决策重放 CLI（`./bin/replay`）：历史决策回放与策略对比
- **告警生命周期持久化**：checker 每 30s 评估并 reconcile 到 `alert_events`（新出现/持续计数/严重度升级/恢复），Web 与 Telegram 共用同一数据口径；Checker 暂停时 Web 显示最近一次持久化状态与数据时间
- **告警页**：`/alerts` 汇总全部活跃告警（低余额/倍率超标/熔断开闸降级/站点禁用/价格同步失败），按严重度排序、跟随分组筛选、30s 自动刷新，一键跳转对应处理页
- **预警弹窗**：Web 控制台每 30 秒轮询系统告警，**仅 critical 级别**（低余额/倍率超标/熔断开闸）新出现或严重度升级时从**右下角弹出预警卡片**（弹跳动效 + 倒计时进度条，自动消失），可一键跳转告警页

### 📨 Telegram 告警（默认关闭）
- **每小时整点汇总**：checker 按小时整点（时区/间隔/分钟可配置）向全部授权订阅者发送告警汇总：系统概况、新出现、严重度升级、持续中、已恢复与查询命令提示；窗口内无变化时发送心跳摘要
- **主动查询命令**：`/alerts` `/alert <key>` `/status` `/relay [id]` `/balance [id]` `/health [id]` `/ratio [id]`——只读数据库，不触发上游调用；多实例部署由 PostgreSQL advisory lock 保证只有一个 poller/report owner
- **订阅者与分组过滤**：Chat ID 由管理员在「设置 → Telegram 告警」手动录入，可分别控制告警推送/查询权限并限定分组范围；一个订阅者发送失败不影响其他订阅者，投递审计幂等重试
- **安全边界**：Bot Token 加密存储（enc:v1: 信封），API 只回显「已配置 + 尾号」；Token 与上游凭据不进入前端 localStorage、Telegram 消息、质量结果 details 或日志

### 🖥 Web 控制台（Vue 3 + Vite）
苹果风格、明暗双主题（跟随系统）、全真实数据：

| 页面 | 功能 |
|---|---|
| 总览 | 24h 请求/成功率/延迟（同比）、趋势图、模型分布、告警、分组切换器、站点综合信息抽屉（倍率/余额/健康/成功率/延迟五图） |
| 站点 | 分组筛选、站点增删改（接口协议/中转站类型/默认测试模型/余额自动登录）、健康/统计/余额/倍率详情、倍率检测分组、上游模型列表一键映射 |
| 测试台 | 真流式请求、站点选择自动预填该站点默认测试模型、分组限定路由、路由决策信息（渠道/策略/分组/Trace ID）、请求中等待动画 |
| 决策 | 决策审计表格（分组列）、筛选、详情抽屉（候选六维评分雷达图/排除原因）、**编辑模式多选/全选删除与导出** |
| 策略中心 | 路由策略按「系统默认」与「每个分组」分别配置；5 种内置策略做成可视化卡片（卡片内展示各因素权重），「加权均衡」支持成本/可靠性/延迟/负载四维权重滑块，未配置的分组一键恢复跟随系统默认，保存立即生效 |
| 熔断 | 四态熔断器、分组切换、手动重置 |
| 告警 | 全部活跃告警统一视图（低余额/倍率超标/熔断开闸降级/站点禁用/价格同步失败），按严重度排序、跟随分组筛选、30s 自动刷新、按类型跳转处理页、**Telegram 状态摘要与立即发送** |
| 设置 | 连接配置、API Keys（分组绑定）、每站点默认测试模型、官方模型价格库、低余额阈值、**Telegram 告警**（Bot 配置/订阅者管理/发送状态） |

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

### 2. 一键启动完整栈（本地开发）

```bash
docker compose -p smart-router up -d --build
docker compose -p smart-router ps
```

该命令会启动四个服务：PostgreSQL 16、Redis 7、Gateway 和 Checker。Gateway 对外提供 API/Web，Checker 在后台执行存活、价格、推理探针和余额检测，不占用宿主机端口。**数据库迁移由应用启动时自动执行**（版本化迁移器，带 advisory lock 与 `schema_migrations` 记录；存量数据卷会自动识别已应用的迁移并补齐缺失版本，不再依赖 `/docker-entrypoint-initdb.d` 首次初始化）。

> ⚠️ **当前 compose 栈是开发配置，不能直接对外部署。**
> `docker-compose.yml` 显式加载 `configs/config.local.yaml`，与 `configs/config.yaml` 的生产默认值有两处实质差异：
>
> | 配置项 | 开发（compose 当前） | 生产（`config.yaml`） |
> |---|---|---|
> | `bootstrap_default_keys` | `true` —— 空库写入 `test-admin-key` / `test-caller-key` | `false` —— 生成一次性随机管理员 Key（`sr-` 前缀），仅启动日志打印一次 |
> | `allow_private_upstream` / `allow_http_upstream` | `true` —— **SSRF 防护关闭**，允许 http 与私网上游 | `false` —— 仅允许 https 公网上游 |
>
> 对外部署时请把 compose 的 `command` 改回 `configs/config.yaml`，或将这两处差异挪到 `docker-compose.override.yml`，使主文件保持生产安全默认。

查看状态和日志：

```bash
docker compose -p smart-router ps
docker compose -p smart-router logs -f gateway checker
docker compose -p smart-router restart checker
```

访问地址：

- Web 控制台：<http://localhost:8080/>
- Gateway 健康检查（liveness）：<http://localhost:8080/health>
- Gateway 就绪检查（readiness，检查 PostgreSQL/Redis）：<http://localhost:8080/ready>
- Prometheus 指标：<http://localhost:8080/metrics>

Checker 默认周期为存活 30 秒、价格 10 分钟、余额 10 分钟、推理探针 1 小时；失败的推理探针默认退避 6 小时后重试。推理探针会调用真实上游并可能产生费用，全局每日预算默认为 `$5`，按「全局/渠道/分组三重取最小值 + Redis 原子预留」执行（定时与手动探测共享同一预算记账）。定时探针的模型为各站点默认测试模型（`test_model`，测试台自动预填的同一字段），未配置的站点回退 `checker.probe_model`。如不希望产生探针费用，可在 `configs/config.yaml` 或分组设置中调整探针周期/预算。历史数据默认保留 30 天（`checker.retention_days`），快照归档随决策日志保留期同步回收。

PostgreSQL 和 Redis 使用命名数据卷持久化。不要使用 `docker compose down -v`，除非确认要删除全部本地数据。

### 可选：监控栈（Prometheus + Alertmanager）

```bash
docker compose --profile monitoring -p smart-router up -d
```

或使用脚本 `./start-monitoring.sh`（额外包含 Grafana）。Alertmanager 默认使用占位接收器，接入真实通知渠道请编辑 `alertmanager.yml`。

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
| 告警列表 | `GET /admin/alerts[?group_id=]`（全部活跃告警，告警页专用） |
| 倍率检测分组 | `POST/PATCH/DELETE /admin/channels/:id/ratio-groups[/:gid]` · `POST /admin/channels/:id/ratio-groups/:gid/probe`（每组自定义默认检测模型） |
| 官方模型价格库 | `GET/POST /admin/model-prices` · `DELETE /admin/model-prices/:model` |
| 分组 CRUD | `GET/POST /admin/groups` · `PATCH/DELETE /admin/groups/:id` |
| 系统默认策略 | `GET/PUT /admin/policies`（策略中心：5 种内置策略选择 + balanced 四维权重） |
| 分组策略 | `GET/PUT /admin/groups/:id/strategy`（每分组独立策略与权重；空 = 跟随系统默认） |
| 统计聚合 | `GET /admin/stats[?group_id=]` · `GET /admin/channel-metrics`（站点综合指标） |
| 决策日志 | `GET /admin/decisions?limit=[&group_id=]`（含候选六维评分与故障切换明细） · `DELETE /admin/decisions`（批量删除，body `{"request_ids":[...]}`） |
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
  read_timeout: 30s
  write_timeout: 0             # SSE 长流式不限写超时（首字节由 TTFB 计时器控制）
  read_header_timeout: 10s     # 慢速请求防护
  idle_timeout: 120s
  max_header_bytes: 1048576
  bootstrap_default_keys: false  # 生产：空库生成随机管理员 Key（仅日志打印一次）
  allowed_origins: []            # CORS 白名单（空 = 允许任意来源）；生产建议填写可信前端域名
  metrics_token: ""              # 非空时 /metrics 要求 Bearer 认证
  allow_private_upstream: false  # SSRF 防护：生产禁止私网/环回上游（开发配置可放宽）
  allow_http_upstream: false     # SSRF 防护：生产仅允许 https 上游
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
  total_budget_ms: 30000     # 总预算：容纳多候选各等首字节（每尝试上限 = max_ttft_ms + 连接超时）
  filter: { max_price_cap: 100.0, max_ttft_ms: 5000 }   # 硬过滤上限
  circuit_breaker: {...}   # 熔断参数（分组可覆盖；状态按分组隔离）
  balanced_weights: {...}  # balanced 策略权重（cost/reliability/latency/load）
security:
  encryption_key: ""       # 上游凭据信封加密密钥（base64 32 字节，生产用环境变量 SR_ENC_KEY 注入）
```

环境变量覆盖：`DATABASE_HOST/PORT/USER/PASSWORD/NAME`、`REDIS_HOST/PORT`、`SR_ENC_KEY`。

启动时会对配置做 schema/范围校验（端口、间隔、预算、权重、熔断参数等），非法配置直接 fail fast。

### 路由数据口径

策略排序依赖的三类指标各有明确来源与精度边界，理解它们才能解释决策结果：

**延迟（TTFT）** —— 取每个「站点 × 模型」最近 **20 次成功探测**的 `percentile_cont` 真实 P50/P95。
单次探测抖动可达数倍（同一站点同一模型实测区间可从 2.2s 到 15.4s），因此不使用单点采样。
无探测数据的站点按一个大值参与排序，即"未知延迟排在已知低延迟之后"。

**成本** —— 按以下优先级估算，越靠前越可信：

| 优先级 | 来源 | 说明 |
|---|---|---|
| 1 | 实测倍率 `official` | 倍率 × **探测当时快照的官网价**，价格库后续调整不影响历史一致性 |
| 2 | 实测倍率 `baseline` | 倍率 × $10/1M 混合基准（价格库未收录该模型时的旧口径） |
| 3 | 声明价格 `declared_prices` | 上游 `/api/pricing` 倍率 × 基准单价；仅取正值条目 |
| 4 | 保守兜底 | 输入 $10/1M、输出 $30/1M |

兜底刻意取偏高值：把未知价格当作免费会让该站点在 `price_first`/`balanced` 下永远排第一。
声明价格的基准单价（`declaredRatioBasePerM`）是未经上游核实的假设值，仅作为无实测数据时的兜底，
精确化应通过实测倍率而非调整该常量。

**输入 token** —— 无分词器，按字符类别启发式估算（ASCII 约 4 字符/token，CJK 约 1.7 字符/token），
计入工具定义与多模态块。**仅用于成本估算与 `max_price_cap` 硬过滤，不用于计费**；
真实用量以上游返回的 `usage` 为准。

### 上游凭据加密（P1-07 整改）

生产环境请注入 AES-256 密钥后**轮换所有已存凭据**（在 Web 控制台重新保存一次即可，保存时自动加密入库）：

```bash
export SR_ENC_KEY=$(openssl rand -base64 32)   # 生成并保存到 Secret 管理方案
docker compose up -d --build
```

未配置密钥时凭据按明文透传（仅限本地开发，启动日志会输出安全告警）。管理端详情接口只返回余额令牌的配置状态与脱敏尾号，不再回显明文。数据库层面按「KMS 集成」的过渡方案是应用层信封加密，迁移到云 KMS 时替换 `internal/crypto` 即可。

### SSRF 防护（P2-04 整改）

站点创建/更新与上游模型探测接口会校验 URL：生产仅允许 `https://` 且阻断环回、RFC1918 私网、链路本地、IPv6 ULA 与云元数据地址，重定向目标同样校验。本地开发可在 `config.local.yaml` 中设置 `allow_private_upstream: true` / `allow_http_upstream: true` 放宽。

## 决策重放（CLI）

```bash
go build -o bin/replay ./cmd/replay
./bin/replay --start "2026-08-13T00:00:00Z" --end "2026-08-13T23:59:59Z" --table
# 完整参数见 ./bin/replay --help
```

重放为**确定性**：决策日志保存了快照哈希、生效策略快照、能力、预算与分组上下文，历史健康快照按内容哈希归档于 `snapshot_archive`；重放时加载不可变归档数据重跑路由。若归档/策略快照缺失（如保留期已清理的旧数据），报告会将其标记为「环境模拟」并明确非确定性，不能作为审计证据。

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

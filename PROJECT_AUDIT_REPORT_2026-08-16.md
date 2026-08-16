# Super Router 项目专业审核报告

**审核日期：** 2026-08-16  
**项目：** `smart-router` / Super Router  
**审核结论：** 当前项目具备可构建、可运行的完整雏形，但在生产安全、协议兼容性、故障切换、分组隔离、数据迁移和可观测性闭环方面仍存在高风险问题，暂不建议未经整改直接承载生产流量。

## 1. 执行摘要

本次审核从代码、数据库迁移、容器编排、前端、监控、测试和文档七个层面检查了项目。项目的主链路清晰：Gateway 接收 OpenAI 风格请求，认证 API Key，读取 PostgreSQL/Redis 快照，依据分组、能力、价格、延迟、可靠性和熔断状态选择上游；Checker 负责存活、价格、推理、余额探针；Replay CLI 读取决策日志并重新执行路由；Vue 3/Vite 前端提供管理员控制台。

### 风险统计

| 等级 | 数量 | 结论 |
|---|---:|---|
| P0 | 0 | 未发现明确的立即接管或数据破坏级问题，但不代表已完成渗透测试 |
| P1 | 8 | 会导致核心承诺失效、错误路由/审计、凭据暴露或数据状态不一致，应优先整改 |
| P2 | 13 | 生产加固、可靠性、运维和安全边界问题，发布前应完成 |
| P3 | 4 | 测试、性能和可维护性缺口，建议纳入持续改进 |

### 总体判断

项目“能编译、能启动、基本功能可演示”，但还没有达到“可审计、可预测、可安全运营”的生产标准。最需要先处理的是：

1. 修复 OpenAI 请求字段丢失和 TTFB 超时不切换问题。
2. 明确定义并实现分组策略、熔断和审计隔离。
3. 对上游凭据做加密、脱敏和最小权限控制。
4. 将迁移、配置校验、健康检查、告警通知和容器硬化补齐。

## 2. 项目理解

### 2.1 组件与职责

- **Gateway (`cmd/gateway`)**：HTTP 服务、CORS、认证、`/v1/chat/completions`、管理员 API、静态前端、Prometheus `/metrics`。
- **Router (`internal/router`)**：加载策略和健康快照，进行分组限制、能力/预算/价格过滤，再按策略排序候选。快照包含 epoch、渠道健康状态、模型价格和 SHA-256 内容哈希。
- **Proxy (`internal/api/proxy.go`)**：调用上游、支持非流式和 SSE 流式、尝试级预算、失败记录和有限重试。
- **Circuit breaker (`internal/api/circuit.go`)**：基于请求历史统计失败率，使用 PostgreSQL 行锁/事务以及 Redis Lua 预约半开探针。
- **Checker (`cmd/checker`, `internal/checker`)**：周期性执行 alive、pricing、probe、balance，并写入健康/价格/余额/请求历史。
- **Replay (`cmd/replay`, `internal/replay`)**：读取历史决策日志，重新调用当前 Router 对比候选和策略结果。
- **Storage**：PostgreSQL 保存渠道、分组、Key、策略、历史和熔断状态；Redis 保存快照缓存和部分限流/预算状态。
- **Web (`web`)**：Vue 3/Vite 管理界面，图表使用 ECharts。
- **Monitoring**：Prometheus 规则、Grafana dashboard 和可选的 Docker 启动脚本。

### 2.2 主要请求链路

```text
客户端
  -> Gateway CORS/日志/Prometheus middleware
  -> API Key 哈希认证与 Key 分组加载
  -> 解析 body.group / X-Group
  -> Router.LoadPolicy + LoadSnapshot
  -> 分组/能力/价格/预算/熔断硬过滤
  -> 策略排序
  -> Proxy 按候选调用上游
  -> 首字节后流式转发或读完整响应
  -> 记录 decision_logs / request_history / metrics
```

## 3. 审核范围与方法

检查范围包括：Go 代码与单元测试、前端源码与构建配置、14 组 SQL 迁移、Dockerfile、`docker-compose.yml`、Prometheus/Grafana 配置、启动脚本、README/快速参考和敏感文件风险。重点关注正确性、认证与凭据、SSRF、数据一致性、超时与重试、分组/熔断语义、迁移、可观测性、部署硬化和测试覆盖。

本报告中的位置均以仓库 `Super-Router` 目录为基准；行号以审核时源码为准，后续修改后应重新定位。

## 4. 高风险问题（P1）

### P1-01 OpenAI 兼容接口会静默丢弃合法请求字段

- **证据：** `internal/api/proxy.go:68-81` 的请求结构将消息 `content` 定义为 `string`，未建模多模态数组；该结构只保留少量字段。
- **触发条件：** 客户端发送图片/音频内容数组，或使用 `top_p`、`stop`、`response_format`、`tool_choice`、`tool_calls`、`stream_options`、`max_completion_tokens`、推理参数等未声明字段。
- **影响：** 多模态请求会绑定失败或返回 400；其他字段在反序列化后被静默丢弃，再序列化转发给上游，导致调用语义改变。对外“OpenAI 兼容”承诺不成立，且客户端很难定位问题。
- **修复建议：** 请求体采用 `json.RawMessage`/保留未知字段的结构，明确网关扩展字段与上游字段分离；仅删除 `group` 等网关字段，原样保留其余 JSON；为文本、多模态、工具调用、结构化输出和流式选项添加契约测试。对不支持的字段应显式返回可诊断错误，而不是静默丢弃。
- **建议测试：** 使用官方 OpenAI SDK 发送多模态、工具调用、JSON schema、`stop` 和 `max_completion_tokens`，在 mock upstream 断言收到的 JSON 与输入等价。

### P1-02 首字节超时不会切换到下一个候选渠道

- **证据：** `internal/api/proxy.go:280-312` 的 TTFB 计时器调用 `cancelAttempt()`；`internal/api/proxy.go:721-740` 将 `context.Canceled` 判定为不可重试。注释声称尝试级超时应表现为 `DeadlineExceeded`，实现却产生 `Canceled`。
- **触发条件：** 第一个候选在总预算内没有返回首字节，计时器触发取消。
- **影响：** Gateway 直接返回 502，剩余候选不会尝试；在上游偶发卡顿时可用性显著下降，且熔断统计把网关超时与客户端主动断开混淆。
- **修复建议：** 为每次尝试创建带 deadline 的子上下文，或定义专用 sentinel error（例如 `errTTFBTimeout`）并在取消后优先识别；只有调用方上下文已取消时才判定为 client canceled。补充非流式和流式首字节超时的故障切换测试。

### P1-03 Key 分组场景的策略、审计和统计不完整

- **证据：** `internal/api/proxy.go:131-191` 在未显式指定分组时只把 Key 绑定的多个组放入 `GroupIDs`；`internal/router/router.go:111-124` 只有单个 `GroupID` 才加载组默认策略，`151-175` 的 `GroupIDs` 仅用于渠道过滤；`router.go:193-213,271-289` 将单个 `GroupID` 写入结果/日志。
- **触发条件：** API Key 绑定一个或多个组，客户端不发送 `group`/`X-Group`。
- **影响：** 即使只绑定一个组，也不会自动采用该组策略；决策日志、请求历史和响应分组头可能为空；多个组的策略覆盖优先级、并集/交集语义未定义，分组统计可能漏数或混入。
- **修复建议：** 明确三种语义：显式组、单组 Key 默认组、多组 Key 组集合。若多组允许并集，应定义策略合并规则或要求显式选择；统一将“生效组”写入路由结果、日志、历史、熔断和指标；对无权限组返回 403/400，而不是静默回退。
- **建议测试：** Key 绑定 0/1/2 组，分别验证策略、候选集合、`group_id`、`X-Group`、审计记录和统计查询。

### P1-04 熔断状态和失败率没有按组隔离

- **证据：** `migrations/001_init.up.sql:71-84` 的 `circuit_states` 唯一键只有 `(channel_id, model, capability)`；`internal/api/circuit.go:178-203` 统计 `request_history` 时只按 channel/model，不筛 `group_id`。
- **触发条件：** 同一渠道/模型同时属于多个组，且不同组使用不同熔断阈值或流量质量。
- **影响：** 一个组的失败会打开另一个组的熔断；所谓组级覆盖无法实现真正隔离，可能造成健康流量被错误摘除，或故障流量继续进入。
- **修复建议：** 若产品语义要求组级熔断，将 `group_id` 纳入状态主键、查询、快照和管理端 reset；失败率查询必须按生效组过滤。若决定保持渠道级熔断，应删除组级配置/文档承诺并明确这是共享状态。

### P1-05 Replay 不是历史状态的确定性重放

- **证据：** `internal/replay/replayer.go:176-190` 只使用历史模型/流式标志和新的策略名；注释明确指出能力、预算、分组未记录，Router 重新读取当前策略和当前快照。
- **触发条件：** 健康状态、分组绑定、模型价格、策略、渠道权重或熔断状态在历史请求后发生变化。
- **影响：** Replay 结果不能证明“当时为什么选了该渠道”，也不能作为可靠的策略回归/审计证据；结果差异可能来自当前环境，而非策略变更。
- **修复建议：** 在 decision log 保存 snapshot epoch/hash、已生效策略快照、能力、预算、组、候选及排序分数；重放时加载不可变历史快照和策略版本。若只支持“当前环境模拟”，应更名为 simulation，并在输出中明确非确定性。

### P1-06 Checker 的分组探针预算在常用配置下被忽略

- **证据：** `internal/checker/groups.go:118-143` 先以渠道预算覆盖全局预算；只有 `u.DailyProbeBudget <= 0` 时才应用组预算。配置默认渠道预算为 `0.5`（`configs/config.yaml` 的渠道创建默认值由 `admin.go:133-135` 设置）。代码注释却写明“渠道自身与分组取最小”。
- **触发条件：** 渠道有非零预算并属于配置了更小预算的组。
- **影响：** 组级成本上限通常不会生效；定时探针还采用“检查后消费”，在剩余预算不足一个完整探针时可能超支；多个 Checker 实例之间缺少原子预留，水平扩展会放大超支。
- **修复建议：** 按文档实现 `min(global, channel, all-enabled-groups)`；用 Redis Lua/数据库原子扣减或预留“探针成本”，按 UTC 日期分桶并设置幂等 request id；记录拒绝原因和实际消耗。

### P1-07 上游凭据明文存储且管理端返回余额 Token

- **证据：** `migrations/001_init.up.sql:5-7` 将 `access_token`、`api_key` 定义为明文文本；`migrations/005_balance_token.up.sql:3` 增加明文 `balance_api_token`；`internal/api/admin.go:323-358` 在详情响应返回完整 `balance_api_token`，列表查询也读取该字段（`admin.go:240-295`）。
- **触发条件：** 数据库备份、日志/调试导出、管理员浏览器或低权限管理端被读取。
- **影响：** 数据库或管理端任一泄露即可直接调用上游和余额接口，影响范围通常跨多个供应商；前端/代理层日志误配置也可能造成二次泄露。
- **修复建议：** 使用 KMS/Vault/云 Secret Manager 或应用层 envelope encryption；数据库只保存密文和 key version；列表/详情只返回是否配置及脱敏尾缀，更新采用“未提供则保持、显式 rotate”；禁止把密钥写入日志、备份导出和前端状态。对现有库和历史 dump 做轮换与泄露排查。

### P1-08 渠道创建/更新与分组同步不是原子事务

- **证据：** `internal/api/admin.go:137-164` 先插入渠道，再独立调用 `syncChannelGroups`；同步失败只记录警告仍返回 201。更新 `admin.go:470-493` 先提交渠道字段，再独立同步分组，失败返回 500 但已留下部分更新。
- **触发条件：** 组 ID 不存在、数据库短暂故障、并发更新或同步中途失败。
- **影响：** API 响应与实际状态不一致；新渠道可能没有默认组，更新重试可能造成难以解释的部分成功。
- **修复建议：** 在同一 PostgreSQL transaction 中校验渠道/组、更新渠道和成员关系，失败整体回滚；事务提交后再失效快照。对 PATCH 使用版本号/`updated_at` 乐观锁，避免覆盖并发修改。

## 5. 中高风险问题（P2）

### P2-01 生产数据库凭据固定且 Redis 无认证

- **证据：** `configs/config.yaml:8-17` 和 `docker-compose.yml:8-11,48-78` 使用固定 `gateway_pass`；Redis 配置为空密码且只依赖网络隔离。
- **影响：** 配置、镜像层、日志或误暴露网络一旦泄露即可访问数据库/Redis；Redis 可被篡改快照、预算和锁状态。
- **建议：** 使用 Docker secrets/外部 Secret Manager，启动时强制校验生产环境不得使用默认密码；Redis 启用 ACL/TLS 或至少设置随机密码和网络策略。

### P2-02 前端把管理员 Key 长期保存到 localStorage，并默认填充开发 Key

- **证据：** `web/src/store.js:6-15,63-66` 从/向 `localStorage` 保存 `sr_apikey`，默认值为 `test-admin-key`；`SettingsView.vue:270-271` 也展示该默认值。
- **影响：** 同源 XSS、恶意扩展、共享电脑和浏览器备份可读取管理员凭据；误把开发配置部署到生产会导致已知 Key 被尝试。
- **建议：** 默认不填充任何 Key；优先使用短时会话、HttpOnly/SameSite cookie 或内存存储；提供显式登出/撤销，生产构建移除开发提示并强制后端禁用 bootstrap Key。

### P2-03 CORS 允许任意 Origin，同时遗漏分组头

- **证据：** `cmd/gateway/main.go:158-168` 设置 `Access-Control-Allow-Origin: *`，允许 Authorization，但 `Allow-Headers` 没有 `X-Group`，`Expose-Headers` 也没有 `X-Group`。
- **影响：** 任意网站可发起跨域请求（浏览器仍受凭据策略限制，但 Bearer Key 一旦被前端保存就会被滥用）；合法前端不能稳定发送/读取分组头，分组路由功能在跨域场景失效。
- **建议：** 使用可信 Origin 白名单并按环境配置；补全 `X-Group`、`X-Request-ID` 等实际需要的头；设置 `Vary: Origin`，并对预检和凭据模式做集成测试。

### P2-04 管理员可控制任意 URL，存在 SSRF 风险

- **证据：** `internal/api/admin.go:36-51,86-100` 接收任意 `base_url`/`balance_api_url`；`admin.go:743-748` 的 `/admin/upstream/models` 还直接接受任意 URL，Checker/Proxy 会访问这些地址。
- **影响：** 被盗管理员 Key 或被诱导操作后，Gateway/Checker 可访问环回、私网、链路本地和云元数据地址，读取内部服务响应或进行端口探测。
- **建议：** 解析并规范化 URL，仅允许 HTTPS（开发环境显式放宽）；解析后阻止 loopback、RFC1918、link-local、IPv6 ULA、Unix socket、重定向到私网和云元数据 IP；更稳妥的是维护供应商域名/网段 allowlist，并在每次重定向后再次校验。

### P2-05 请求上下文被大量替换为 context.Background

- **证据：** `internal/api/auth.go:45,90,147`、`internal/api/admin.go:137,237,310,373` 等多处使用 `context.Background()`；`proxy.go:378` 也使用独立 background timeout。
- **影响：** 客户端断开后认证、数据库查询、管理操作或后续后台工作仍可能继续，浪费连接池和资源；请求取消无法传播，优雅停机也更难收敛。
- **建议：** handler 使用 `c.Request.Context()`，后台任务显式建立带超时/取消的生命周期上下文；区分“请求级”与“服务级”任务，补充客户端断开和停机测试。

### P2-06 HTTP Server 和上游 Transport 的资源边界不完整

- **证据：** `cmd/gateway/main.go:253-258` 只设置 Read/WriteTimeout，WriteTimeout 配置为 0；未设置 `ReadHeaderTimeout`、`IdleTimeout`、`MaxHeaderBytes`。`internal/api/proxy.go:42-45` 仅配置固定 10 秒 Dial timeout。
- **影响：** 慢速请求、异常大 Header、空闲连接和长时间无响应可能占用 goroutine/连接；WriteTimeout 无限会让异常流式连接长期占用资源。
- **建议：** 设置合理的 header/idle/max-header 边界；流式请求使用专门的连接策略和最大总时长/空闲心跳，而非全局无限写超时；配置连接池上限、TLS handshake、response-header 和 expect-continue timeout。

### P2-07 数据库中的渠道 timeout 字段没有真正驱动 Gateway/Checker

- **证据：** `migrations/001_init.up.sql:18-20` 保存 `timeout_connect_ms`、`timeout_first_byte_ms`、`timeout_total_ms`；多个 Checker 查询这些字段（如 `internal/checker/groups.go:88-104`），但各实现使用固定 10 秒/30 秒客户端 timeout，Gateway 使用固定 Dial 和全局总预算。
- **影响：** 管理端显示/保存的超时配置与实际行为不一致，无法按供应商处理慢连接或长推理；运维调参没有效果。
- **建议：** 在调用前将字段映射到 per-request context、Transport 和 response-header timeout，并校验最小/最大值；删除未实现字段或明确标记为未来能力。

### P2-08 监控中间件把健康、指标和管理请求混入推理指标

- **证据：** `cmd/gateway/main.go:154` 在所有路由前注册 `metrics.PrometheusMiddleware()`；`internal/metrics/middleware.go:41-69` 对没有 handler 设置上下文的请求使用 `model=unknown`、`channel=unknown`，并按 HTTP 状态计成功/失败。
- **影响：** `/health`、`/metrics`、所有 `/admin` 请求污染 QPS、成功率和延迟，告警阈值与仪表盘不再代表推理流量；流式响应头已写 200 后中途失败也可能被记为成功。
- **建议：** 按路由拆分业务指标与 HTTP 访问指标；只在代理 handler 明确记录推理结果，记录“首字节成功/流中断/上游失败”独立状态；为管理和探针请求使用不同指标名或排除。

### P2-09 `/metrics` 与健康检查暴露过多运行信息，健康端点不是 readiness

- **证据：** `cmd/gateway/main.go:172-180` 对 `/health`、`/metrics` 不认证；Prometheus 指标包含模型、渠道 ID、熔断和运行状态。健康端点只返回 `status=ok`，不检查 PostgreSQL/Redis。
- **影响：** 未授权访问者可枚举运行拓扑和模型/渠道标签；编排系统可能在数据库或 Redis 不可用时仍把实例判为 ready，导致流量黑洞。
- **建议：** 将 metrics 放到内网/独立监听器或加 mTLS/网络 ACL；对标签做进一步脱敏；拆分 liveness/readiness，readiness 检查数据库、Redis、快照可用性并设置超时。

### P2-10 Alertmanager 通知链路被注释，Prometheus 配置依赖外部挂载

- **证据：** `prometheus.yml:12-18` 的 `alerting` 配置被注释；`prometheus.yml:42-43` 引用 `/etc/prometheus/alerts.yml`，只有 `start-monitoring.sh` 才额外挂载该文件。独立 `promtool check config prometheus.yml` 在未挂载规则文件时失败。
- **影响：** 规则即使触发也没有通知出口；直接运行 Prometheus 或遗漏挂载时启动失败，部署方式与配置文件不自洽。
- **建议：** 在受控环境启用 Alertmanager、通知路由、抑制和演练；把规则与配置作为同一部署单元进行校验，CI 中同时挂载/检查，避免依赖脚本隐式补文件。

### P2-11 Grafana dashboard 使用旧 schema 和旧 panel 类型

- **证据：** `grafana-dashboard.json:8,15,111` 使用 `schemaVersion:16`、`graph`、`piechart`。
- **影响：** 在现代 Grafana 版本导入时可能出现迁移、面板缺失或查询行为变化，监控可视化不可重复。
- **建议：** 在目标 Grafana 版本导出并锁定 dashboard schema；使用当前 time series、pie chart 等官方 panel；将数据源 UID、变量和导入测试纳入 CI/运维演练。

### P2-12 迁移机制只依赖 PostgreSQL 首次初始化

- **证据：** `docker-compose.yml:16` 将整个 `migrations` 目录挂载到 `/docker-entrypoint-initdb.d`；README 明确说明已有数据卷不会自动补跑新迁移（`README.md:89-110`）。仓库没有 `schema_migrations`、启动时迁移器或幂等升级流程。
- **影响：** 已有数据卷升级时容易漏迁移，应用启动后才在运行期暴露缺列/缺表；上下行 SQL 同时放入 init 目录也使 fresh DB 行为依赖命名顺序和脚本维护纪律。
- **建议：** 引入版本化迁移工具（如 `goose`/`migrate`），启动前执行带锁的 up migration；CI 验证 fresh DB 和从每个历史版本升级；回滚必须经过演练，不要把 down migration 当 init 脚本。

### P2-13 配置缺少启动时 schema/范围校验，部分值会运行期崩溃或失效

- **证据：** `internal/config/config.go:96-123` 只读取并反序列化，不验证端口、预算、权重、阈值或 `cooling_backoff`；`internal/api/circuit.go:160-175` 对空 `CoolingBackoff` 可能索引 `[-1]`；`auth_failure_threshold` 只在配置结构/装配中出现，未参与熔断决策。
- **影响：** 错误配置可能启动成功后在高失败路径 panic；负预算、非法权重/阈值可能产生不可预测路由；管理员以为设置了认证失败阈值但实际没有防护。
- **建议：** 使用显式 validation（范围、枚举、非空数组、权重总和）；启动时 fail fast；删除未实现配置或接入明确的认证失败计数/锁定机制，并为配置加载添加表驱动测试。

## 6. 中低风险问题（P3）

### P3-01 测试体系缺少跨边界和集成验证

- **现状：** Go 测试约 47 个，集中在纯路由/策略函数；Gateway、Checker、Replay、Admin handler 没有完整集成测试。缺少认证中间件、PostgreSQL/Redis、迁移、完整故障切换、流式中断、Replay 确定性、Admin CRUD 事务和 SSRF 防护测试。
- **风险：** 当前“全绿”主要证明纯函数正确，无法证明真实依赖、并发和故障路径。
- **建议：** 使用 Testcontainers 或 Compose CI 启动 PostgreSQL/Redis；增加 mock upstream 场景矩阵、HTTP 契约测试、迁移升级测试、race/负载测试和前端 E2E。

### P3-02 前端缺少标准 test/lint/typecheck 和 CI

- **证据：** `web/package.json` 没有 `test`、`lint`、`typecheck` 脚本；仓库未发现 CI 工作流；Dockerfile 只 build，不执行测试。
- **建议：** 增加 ESLint、Vue type checking、组件测试、Playwright E2E 和依赖审计；在 PR/镜像构建中执行并阻断失败。

### P3-03 ECharts 包体过大且存在已知中危 XSS 公告

- **证据：** `web/package.json:12` 使用 `echarts ^5.6.0`，`npm audit --audit-level=high` 报告 ECharts `<6.1.0` 的 GHSA-fgmj-fm8m-jvvx（moderate XSS）；`npm run build` 生成约 1.036 MB 的 ECharts chunk（gzip 约 343 KB）并产生大包警告。
- **影响：** 管理界面首屏加载成本高；若图表配置或数据被不可信输入影响，旧版本存在 XSS 风险。
- **建议：** 评估升级到修复版本（注意 6.x breaking change），或锁定已修复的兼容版本并做回归；按需引入图表、路由级懒加载和 chunk 分割。

### P3-04 手动倍率探测沿用普通 15 秒前端超时

- **证据：** `web/src/api.js:14-28` 默认请求超时 15 秒；`web/src/api.js:97-104` 的 `probe-ratio` 没有覆盖该 timeout。
- **影响：** 真实上游探测或排队超过 15 秒时，后台可能仍在执行而界面误报失败，重复点击会制造重复探针和成本。
- **建议：** 为探测接口使用独立较长超时、轮询任务状态或服务端异步 job；按钮增加幂等/禁用和取消状态。

## 7. 已验证的优点

- API Key 使用 SHA-256 哈希存储，生产空库可生成随机管理员 Key，并提示只显示一次（`internal/api/auth.go`、`cmd/gateway/main.go:81-82`）。
- 代理请求体有 8 MiB 上限，上游错误体读取有长度限制，降低了内存和日志放大风险。
- 半开探针预约使用 Redis Lua 原子操作；熔断状态更新使用事务和行锁，基础并发思路正确。
- 快照带 epoch 和 SHA-256 内容哈希；模型指标有标签基数上限。
- Compose 服务依赖 PostgreSQL/Redis healthcheck；数据库和 Redis 端口默认绑定回环。
- 前端构建、后端构建、Compose 配置和告警规则均可通过基础验证。
- README 对本地/生产 Key、首次初始化迁移和监控启动方式有明确说明，便于继续完善。

## 8. 验证记录

| 检查项 | 结果 | 说明 |
|---|---|---|
| `npm run build` | 通过 | Vite 构建成功；有 ECharts 大 chunk 警告 |
| `docker compose config --quiet` | 通过 | Compose 语法和插值有效 |
| `docker build --progress=plain -t smart-router-audit .` | 通过 | Gateway、Checker、前端均成功构建 |
| `go test ./...` | 通过 | 在 `golang:1.26-alpine` 容器执行；全部 Go 包通过 |
| `go vet ./...` | 通过 | 在同一容器执行 |
| `go test -race ./...` | 通过 | 在 `golang:1.26-bookworm` 容器执行 |
| `npm audit --audit-level=high` | 命令成功但有报告 | 1 个 moderate：ECharts `<6.1.0` XSS 公告 |
| `promtool check rules` | 通过 | 发现 13 条规则 |
| `promtool test rules` | 通过 | 告警规则测试通过 |
| `promtool check config` | 单独检查失败 | 配置引用 `/etc/prometheus/alerts.yml`；启动脚本会额外挂载该文件，CI 应按真实部署方式检查 |

主机未安装 Go，因此没有把主机命令缺失计为项目失败；后端检查均使用官方 Go 容器完成。

## 9. 整改路线图

### 0-7 天：阻断高风险

1. 修复请求体保真、TTFB sentinel timeout 和候选切换。
2. 冻结/轮换所有已有上游和余额凭据；从 API 响应、日志、前端状态和备份中移除明文。
3. 为 `base_url`、余额 URL 和模型探测 URL 加 SSRF allowlist/私网阻断。
4. 把渠道与分组同步改为单事务，拒绝不存在的组 ID。
5. 决定分组熔断的真实语义，并补齐 group_id 主键/查询/日志。

### 1-4 周：恢复可预测性和运维闭环

1. 设计可确定 Replay：保存 epoch/hash、策略版本、能力、预算、分组和候选分数。
2. 实现全局/渠道/分组预算最小值与原子预留，支持多 Checker 实例。
3. 引入版本化迁移器、启动锁和升级 CI。
4. 增加配置 schema 校验、readiness、HTTP/Transport 超时和请求上下文传播。
5. 将业务指标与管理/健康指标分离，保护 metrics，接通 Alertmanager 通知。

### 1-3 个月：工程化和长期质量

1. 建立 PostgreSQL/Redis/上游 mock 集成测试、契约测试、E2E 和负载/故障演练。
2. 使用非 root、固定 digest 基础镜像、只读文件系统、cap drop、资源限制和 healthcheck 强化容器。
3. 升级 ECharts 并做按需加载；补齐前端 lint/typecheck/test/CI。
4. 对 Grafana dashboard、备份加密、密钥轮换、审计保留和 incident runbook 做正式运维验收。

## 10. 发布前验收清单

- [ ] P1-01 至 P1-08 均有代码修复、回归测试和负责人。
- [ ] 生产 secret 不在仓库、镜像、日志、前端 localStorage 或未加密备份中。
- [ ] 任意 URL 访问经过 SSRF 防护和重定向复核。
- [ ] 迁移可从空库和每个历史版本升级，且应用启动会阻止未迁移数据库接流量。
- [ ] readiness 能识别 PostgreSQL/Redis/快照不可用；metrics 只能被监控网络访问。
- [ ] TTFB、流中断、上游 4xx/5xx、客户端取消、熔断和重试均有集成测试。
- [ ] 分组策略、组并集、组统计和组熔断语义在文档/API/实现中一致。
- [ ] Prometheus + Alertmanager + Grafana 在目标版本完成一次告警演练。
- [ ] 容器以非 root 运行，镜像 tag 固定或 digest 锁定，资源和网络边界已配置。

**最终结论：** 项目适合作为继续迭代的开发/预发布版本；完成 P1 项并通过上述验收前，不建议直接接入真实用户流量或把当前审计日志作为合规级审计证据。

# Smart Router Telegram 告警与 API 质量检测设计

- **日期**：2026-08-19
- **状态**：已完成需求确认，待用户审阅规格后进入实施计划
- **适用分支**：`main`

## 1. 背景与目标

本次优化包含两个相互关联、但职责独立的能力：

1. 将现有告警系统接入 Telegram：
   - 每小时整点主动发送一次告警汇总；
   - 支持多个管理员手动配置的 Chat ID；
   - 支持授权用户主动查询告警和中转站信息。
2. 将 Hvoy 风格的 API 接口质量检测集成到现有站点详情中：
   - 复用数据库中已保存的站点凭据；
   - 默认使用站点 `test_model`，允许当前任务临时切换已映射模型；
   - 同时提供基础连通性检测和深度质量检测；
   - 检测过程在本网站内以动画和实时进度呈现；
   - 检测结果持久化，并可被 Web 与 Telegram 查询。

本设计不把 API Key、Access Token、Balance Token 或 Telegram Bot Token 下发到浏览器，也不依赖 Hvoy 的外部检测接口。

## 2. 设计原则

- **数据库是事实源**：任务、结果、告警生命周期和 Telegram 发送状态均可恢复、可审计。
- **Gateway 负责编排，Checker 负责后台工作**：HTTP Handler 不执行长时间上游检测。
- **Web 与 Telegram 使用同一数据口径**：共享告警、站点信息和质量结果查询服务。
- **凭据最小暴露**：上游凭据和 Bot Token 继续使用现有 `internal/crypto` 的 AES-256-GCM 应用层加密；响应和日志只返回脱敏信息。
- **质量判断是启发式结论**：检测结果使用“通过 / 需要关注 / 异常 / 无法判断”，不把行为探测包装成绝对真实性证明。
- **默认安全和低打扰**：Telegram 默认关闭；质量检测不自动周期执行；只对明确失败的质量检测生成告警，不因“需要关注”直接制造 Critical 告警。

## 3. 总体架构

```text
Web 站点详情
  └─ Gateway Admin API
       ├─ 创建质量检测任务
       ├─ 查询任务/历史结果
       └─ SSE 推送任务状态
              │
              ├─ PostgreSQL：任务队列、结果、告警事件
              └─ Redis Pub/Sub：实时事件加速
                         │
                    Checker
       ├─ Quality Worker：领取并执行质量检测
       ├─ Alert Reconciler：计算并维护告警生命周期
       └─ Telegram Bot Worker：轮询命令、每小时发送汇总

Telegram 用户
  └─ Bot API
       ├─ /alerts /alert
       ├─ /relay /balance /health /ratio
       ├─ /quality
       └─ 授权 Chat ID 校验
```

新增或扩展的后端边界：

```text
internal/quality/   质量检测领域模型、执行器、结果归纳
internal/alert/     告警评估、生命周期 reconcile、消息数据模型
internal/telegram/  Bot API 客户端、长轮询、权限、命令和消息格式化
```

`internal/api` 只保留 HTTP Handler 和请求参数校验；`cmd/checker` 负责启动 Quality Worker、Alert Reconciler 和 Telegram Worker。

## 4. API 质量检测

### 4.1 用户流程

```text
站点列表 → 打开站点详情 → 查看余额/健康/倍率
  → 点击“一键质量检测”
  → 默认选中 test_model
  → 可临时选择已映射模型
  → 创建异步任务
  → 页面通过 SSE 展示阶段动画
  → 结果写入历史并展示总体结论
```

临时模型选择只影响当前任务，不修改 `upstreams.test_model` 或模型映射。

模型解析顺序：

1. 站点 `test_model`；
2. 全局 `checker.probe_model`（且必须存在于该站点的有效模型映射中）；
3. 站点第一个有效模型映射；
4. 无可用模型时拒绝创建任务并提示配置模型映射。

### 4.2 检测深度

一次“一键质量检测”默认执行 `full`；后端保留 `basic` 参数供后续轻量检测使用。

#### Basic 阶段

1. **connectivity**
   - 先调用当前协议对应的 `/v1/models` 做连接、认证和基础可达性检查；
   - 如果上游不支持模型列表但聊天端点可用，则记录 `models_endpoint_unavailable`，继续后续聊天检测；
   - 检查 Base URL/DNS/连接/TLS、HTTP 状态、连接耗时、TTFB 和总耗时。
2. **protocol**
   - OpenAI/Anthropic 请求路径和认证头；
   - 请求/响应协议转换；
   - 非流式 OpenAI 兼容响应结构。
3. **stream**
   - SSE 建立；
   - 事件解析；
   - 首字节和事件数量；
   - 正常结束和 `[DONE]`；
   - Anthropic SSE 转 OpenAI SSE 的完整性。

#### Full 阶段

在 Basic 通过或可继续的基础上增加。一次 `full` 任务最多执行两次小型聊天请求：一次非流式、一次流式；`behavior` 优先复用非流式响应，不额外发起第三次聊天请求。每次请求默认使用较小的 `max_tokens`，并在界面明确提示可能产生少量费用。

4. **usage**
   - `prompt_tokens`、`completion_tokens`、`total_tokens`；
   - usage 字段之间的一致性；
   - 如果余额接口可用，执行前后余额差值记录；
   - 使用小 `max_tokens`，明确标注可能产生少量费用。
   - 余额读取本身不产生费用；只有实际聊天探测可能产生上游费用；
5. **behavior**
   - 返回模型名是否与请求模型/映射模型一致；
   - 响应是否为空、过短、结构异常；
   - 标准测试问题是否获得可解析回答；
   - 不为模型行为阶段额外发起聊天请求，优先复用同一轮非流式响应；
   - 模型身份、知识截止时间等信号只作为启发式检查；
   - 工具调用/多模态能力仅在站点能力标记或模型映射允许时检查，否则标记为 skipped。

行为检测的结果只能是：

```text
passed / attention / failed / unknown / skipped
```

不得直接输出“该站点一定是假模型”等确定性结论。

### 4.3 总体结论

质量检测总体结果：

```text
good       所有必需阶段通过，行为阶段无明显异常
attention  基础链路可用，但存在行为、usage 或性能信号需要关注
failed     连接、协议或流式等关键阶段失败
unknown    数据不足或上游无法提供可判断信号
```

关键阶段失败时创建 `quality_check_failed` 告警事件；仅 `attention` 默认不创建 Critical 告警。

### 4.4 任务状态与并发

任务状态：

```text
queued → running → completed
                    ├─ failed
                    ├─ cancelled
                    └─ expired
```

辅助状态：

```text
cancel_requested
```

并发规则：

- 同一站点同时最多一个 `queued/running/cancel_requested` 任务；
- 全局默认最多三个 Quality Worker 任务并行；
- 同站点重复创建返回 HTTP `409`，并带当前 `run_id`；
- Checker 崩溃后，心跳超过 2 分钟的 `running` 任务重新排队；
- 单任务最多重试两次，超过后标记 `failed`；
- 每个阶段之间检查取消标志；正在进行的 HTTP 请求使用 context 取消。

### 4.5 数据表

新增 `quality_check_runs`：

```text
id BIGSERIAL PRIMARY KEY
channel_id INT REFERENCES upstreams(id) ON DELETE CASCADE
model VARCHAR(100) NOT NULL
depth VARCHAR(16) NOT NULL
status VARCHAR(24) NOT NULL
overall_status VARCHAR(16)
current_stage VARCHAR(32)
progress INT NOT NULL DEFAULT 0
attempt_count INT NOT NULL DEFAULT 0
worker_id VARCHAR(128)
heartbeat_at TIMESTAMP
requested_by_key_hash VARCHAR(64)
error TEXT
created_at TIMESTAMP NOT NULL DEFAULT NOW()
started_at TIMESTAMP
finished_at TIMESTAMP
```

新增 `quality_check_results`：

```text
id BIGSERIAL PRIMARY KEY
run_id BIGINT REFERENCES quality_check_runs(id) ON DELETE CASCADE
stage VARCHAR(32) NOT NULL
check_name VARCHAR(100) NOT NULL
status VARCHAR(16) NOT NULL
http_status INT
action_latency_ms INT
ttfb_ms INT
actual_model VARCHAR(100)
prompt_tokens INT
completion_tokens INT
total_tokens INT
details JSONB NOT NULL DEFAULT '{}'
error TEXT
created_at TIMESTAMP NOT NULL DEFAULT NOW()
```

索引和约束：

- `quality_check_runs(channel_id, created_at DESC)`；
- `quality_check_results(run_id, created_at)`；
- 活跃任务部分唯一索引：同一 `channel_id` 的 `queued/running/cancel_requested` 最多一条。

### 4.6 HTTP API

```text
POST /admin/channels/:id/quality-checks
GET  /admin/channels/:id/quality-checks
GET  /admin/quality-checks/:run_id
GET  /admin/quality-checks/:run_id/events
POST /admin/quality-checks/:run_id/cancel
```

创建请求：

```json
{
  "model": "claude-sonnet-5",
  "depth": "full"
}
```

创建响应：

```json
{
  "run_id": "qc_123",
  "channel_id": 5,
  "model": "claude-sonnet-5",
  "depth": "full",
  "status": "queued"
}
```

任务详情响应包含：

```text
run 摘要
当前进度
当前阶段
各阶段结果
开始/结束时间
总体结论
```

SSE 事件：

```text
task_started
stage_started
stage_progress
stage_result
task_warning
task_failed
task_completed
task_cancelled
```

事件示例：

```text
event: stage_result
data: {"stage":"stream","status":"passed","ttfb_ms":612,"events_received":12}
```

浏览器端使用带 `Authorization` 的 `fetch` 流式读取 SSE，而不是原生 `EventSource`，因为原生 `EventSource` 无法携带现有 Bearer Header。

### 4.7 Web 集成与动画

质量检测嵌入 `web/src/views/ChannelsView.vue` 的站点详情区域，不新增主导航。

页面顺序：

```text
站点信息
→ 余额/健康/倍率/24h 指标
→ API 接口质量检测卡片
→ 最近 5 次质量检测历史
```

检测卡片阶段：

```text
连接性 → 协议一致性 → 流式响应 → Usage/计费 → 模型行为
```

状态表现：

- waiting：灰色圆点；
- running：蓝色呼吸圆点、进度条和计时器；
- passed：绿色勾；
- attention：橙色感叹号；
- failed：红色叉；
- skipped：灰色短横线。

动画要求：

- 阶段切换使用平滑过渡，不整页闪烁；
- SSE 数据到达时显示轻量波形/事件计数动画；
- 完成时只播放一次轻量完成动画；
- 取消显示“正在停止”，随后进入 cancelled；
- 页面刷新时读取当前任务，再重连 SSE；
- 任务已完成时直接展示持久化结果；
- 窄屏下指标两列、结果单列、阶段列表可横向滚动。

## 5. 告警生命周期

### 5.1 共享告警服务

将当前 `AdminHandler.buildAlerts()` 的动态告警判断逐步抽取到 `internal/alert`，由同一套评估结果供：

- Web `/admin/alerts`；
- Web `/admin/stats`；
- Telegram 汇总；
- Telegram `/alerts` 和 `/alert`；
- 告警事件 reconcile。

Gateway 读取持久化的 active 事件；Checker 定期 reconcile。若 Checker 暂停，Web 仍返回最近一次持久化状态，并显示数据更新时间。

### 5.2 告警类型

```text
low_balance
ratio_exceeded
circuit_open
circuit_degraded
channel_disabled
pricing_sync_failed
quality_check_failed
```

稳定 `alert_key` 示例：

```text
low_balance:channel-3
ratio_exceeded:channel-5:model-claude-sonnet-5
circuit_open:channel-3:model-gpt-5.5:group-2
pricing_sync_failed:channel-7
quality_check_failed:channel-5:model-claude-sonnet-5:stream
```

### 5.3 数据表

新增 `alert_events`：

```text
id BIGSERIAL PRIMARY KEY
alert_key VARCHAR(255) NOT NULL
alert_type VARCHAR(64) NOT NULL
severity VARCHAR(16) NOT NULL
status VARCHAR(16) NOT NULL DEFAULT 'active'
channel_id INT REFERENCES upstreams(id) ON DELETE SET NULL
group_id INT REFERENCES channel_groups(id) ON DELETE SET NULL
model VARCHAR(100)
title VARCHAR(200) NOT NULL
message TEXT NOT NULL
current_value DOUBLE PRECISION
threshold_value DOUBLE PRECISION
unit VARCHAR(32)
impact TEXT
recommendation TEXT
admin_path VARCHAR(255)
metadata JSONB NOT NULL DEFAULT '{}'
first_seen_at TIMESTAMP NOT NULL
last_seen_at TIMESTAMP NOT NULL
recovered_at TIMESTAMP
occurrence_count INT NOT NULL DEFAULT 1
created_at TIMESTAMP NOT NULL DEFAULT NOW()
```

约束：

- 对 `status = 'active'` 的 `alert_key` 建部分唯一索引；
- 同一个问题恢复后再次出现，创建新的事件周期；
- 不在告警消息、metadata 或日志中保存凭据。

### 5.4 Reconcile 规则

每轮评估结果与 active 事件比较：

- 不存在相同 `alert_key`：创建 active，`occurrence_count = 1`；
- 已存在：更新 `last_seen_at`、当前值和 occurrence；
- warning → critical：更新 severity，记录升级时间；
- active 但本轮不存在：标记 recovered，写入 `recovered_at`；
- reconcile 使用 PostgreSQL advisory lock，避免多个 Checker 重复改变生命周期。

## 6. Telegram 集成

### 6.1 Bot 运行方式

使用 Telegram Bot API 的 HTTPS 客户端和 `getUpdates` 长轮询，不要求公网 Webhook 地址。

- 仅一个 Checker 实例持有 Telegram poller；
- 通过 PostgreSQL advisory lock 选主；
- poller 失效后其他 Checker 可接管；
- `sendMessage` 使用 HTML 格式并统一转义动态字段；
- 单条消息超过 Telegram 限制时自动拆分并加 `(1/N)`；
- 一个订阅者发送失败不影响其他订阅者。

### 6.2 配置表

新增单行 `telegram_config`：

```text
id SMALLINT PRIMARY KEY CHECK (id = 1)
enabled BOOLEAN NOT NULL DEFAULT false
bot_token TEXT NOT NULL DEFAULT ''
report_enabled BOOLEAN NOT NULL DEFAULT true
report_interval_minutes INT NOT NULL DEFAULT 60
report_minute INT NOT NULL DEFAULT 0
timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai'
include_recovered BOOLEAN NOT NULL DEFAULT true
include_ongoing BOOLEAN NOT NULL DEFAULT true
web_base_url TEXT NOT NULL DEFAULT ''
last_poll_at TIMESTAMP
last_report_at TIMESTAMP
last_error TEXT
updated_at TIMESTAMP NOT NULL DEFAULT NOW()
```

`bot_token` 存储 `enc:v1:` 密文；API 只返回 `configured`、脱敏尾缀和运行状态，不返回完整 Token。

### 6.3 订阅者表

新增 `telegram_subscribers`：

```text
id BIGSERIAL PRIMARY KEY
chat_id BIGINT UNIQUE NOT NULL
chat_type VARCHAR(16) NOT NULL DEFAULT 'private'
display_name VARCHAR(200) NOT NULL DEFAULT ''
enabled BOOLEAN NOT NULL DEFAULT true
alert_enabled BOOLEAN NOT NULL DEFAULT true
query_enabled BOOLEAN NOT NULL DEFAULT true
group_ids JSONB NOT NULL DEFAULT '[]'
last_sent_at TIMESTAMP
last_error TEXT
failure_count INT NOT NULL DEFAULT 0
created_at TIMESTAMP NOT NULL DEFAULT NOW()
updated_at TIMESTAMP NOT NULL DEFAULT NOW()
```

`group_ids = []` 表示可查询全部分组；非空时，告警和查询均只返回绑定分组范围内的信息。

新增 `telegram_delivery_logs`：

```text
id BIGSERIAL PRIMARY KEY
subscriber_id BIGINT REFERENCES telegram_subscribers(id) ON DELETE CASCADE
message_kind VARCHAR(32) NOT NULL
window_start TIMESTAMP
window_end TIMESTAMP
success BOOLEAN NOT NULL
telegram_message_id BIGINT
error TEXT
sent_at TIMESTAMP NOT NULL DEFAULT NOW()
```

### 6.4 管理端 API

```text
GET   /admin/telegram/config
PATCH /admin/telegram/config
POST  /admin/telegram/test
POST  /admin/telegram/report

GET    /admin/telegram/subscribers
POST   /admin/telegram/subscribers
PATCH  /admin/telegram/subscribers/:id
DELETE /admin/telegram/subscribers/:id
POST   /admin/telegram/subscribers/:id/test
GET    /admin/telegram/delivery-logs
```

所有接口要求 admin；订阅者只能通过网站后台手动录入，不开放 Telegram `/start` 自助绑定。

### 6.5 每小时告警汇总

每小时整点执行，默认时区 `Asia/Shanghai`，默认间隔 60 分钟。每条消息包含：

1. 标题、时间和统计窗口；
2. 当前活跃告警总数及 Critical/Warning 数量；
3. 当前可用站点、熔断站点等系统概况；
4. 过去一小时新出现告警；
5. 严重度升级；
6. 持续中的 Critical 和重要 Warning；
7. 已恢复告警及持续时长；
8. 查询命令提示。

消息模板示例：

```text
🛰 Smart Router 告警汇总
━━━━━━━━━━━━━━━━
时间：2026-08-19 14:00
统计窗口：过去 1 小时

📊 系统概况
可用中转站：7 / 9
当前熔断：2
活跃告警：5 条
🔴 Critical：2  🟠 Warning：3

🆕 新出现
🔴 Claude Relay B
   类型：倍率超限
   模型：claude-sonnet-5
   当前：2.84x / 上限：2.00x
   持续：19 分钟
   影响：该模型可能参与成本路由
   建议：检查价格同步或调整倍率上限

⬆️ 严重度升级
🟠 → 🔴 OpenAI Relay D
   类型：上游错误率过高
   当前错误率：64.2%

🔁 持续中
🔴 Sub2API C / gpt-5.5
   熔断开启，持续 2 小时 18 分钟

✅ 已恢复
Relay E · 价格同步失败
故障持续：42 分钟

查询：/alerts · /relay 3 · /balance 3 · /health 3 · /ratio 3
```

当窗口内没有新增、升级或恢复时，发送心跳式摘要，不发送空消息：

```text
过去 1 小时没有新的告警变化。
当前活跃告警：2 条（Critical 1 / Warning 1）
```

### 6.6 Telegram 主动查询命令

```text
/start
/help
/alerts
/alerts critical
/alert <alert_id>
/status
/relay
/relay <channel_id>
/balance
/balance <channel_id>
/health
/health <channel_id>
/ratio
/ratio <channel_id>
/quality <channel_id>
```

查询只读数据库中的最新检测结果，不因查询临时调用上游接口，避免 Telegram 查询触发额外费用或限流。

`/quality <channel_id>` 返回最近一次质量检测摘要和各阶段状态，不启动新的检测任务。

未授权 Chat ID、停用订阅者或无查询权限时统一返回：

```text
⛔ 当前 Chat ID 未授权，请联系管理员。
```

### 6.7 中转站查询内容

`/relay`：站点总览，显示名称、健康、最新余额、代表倍率和异常标记。

`/relay <id>`：显示：

- 协议、relay type、分组；
- 存活、最近延迟、24h 请求/成功率；
- 熔断状态和冷却信息；
- 最新余额、来源和检测时间；
- 代表模型倍率、基准、上限和检测时间。

`/balance` / `/health` / `/ratio`：分别显示全量紧凑列表；带 ID 时显示单站点最近历史。

所有查询：

- 不显示 API Key、Access Token、Balance Token；
- Base URL 默认只显示域名；
- 使用授权订阅者的分组过滤；
- 没有数据时明确显示“暂无有效检测结果”和数据时间。

## 7. Web Telegram 管理台

集成到 `SettingsView.vue` 的“Telegram 告警”区域：

### Bot 配置卡片

- 启用开关；
- Bot Token 密码输入和脱敏状态；
- 时区；
- 每小时整点汇总开关；
- 是否包含恢复告警；
- 是否包含持续告警；
- 保存、测试连接、立即发送测试报告。

### 订阅者表格

字段：

```text
名称、Chat ID、类型、告警推送、查询权限、分组范围、最近发送、状态、操作
```

操作：

```text
新增、编辑、启用/停用、删除、发送测试消息
```

### 发送状态

展示：

```text
Bot 状态
最近轮询
最近汇总
下次汇总
成功发送数
失败数
```

告警页增加 Telegram 状态摘要和“立即发送当前告警汇总”按钮；该按钮只触发一次发送，不改变正常调度。

## 8. 错误处理与安全边界

- 质量检测默认仅 admin 可发起；
- SSE 也必须经过 Bearer admin 认证；
- 质量检测请求和 Telegram 动态内容不得记录凭据；
- Telegram HTML 消息中的站点名、模型名、错误信息必须转义；
- Telegram API 网络错误采用指数退避，不在失败时高速重试；
- 发送失败记录到订阅者和 delivery log；连续失败只标记异常，不自动删除订阅者；
- 质量任务过期、取消、重试和实际消耗要写入结果摘要；
- 完整质量检测明确提示可能产生少量上游费用；
- `web_base_url` 为空时只发送命令，不生成不可用链接；
- Telegram Bot Token、上游凭据、余额令牌和管理 Key 不进入前端 localStorage、Telegram 消息、质量结果 `details` 或日志。

## 9. 数据库迁移与部署

新增一组版本化迁移，建议版本号为 `020_quality_telegram`，包含：

- `quality_check_runs`；
- `quality_check_results`；
- `alert_events`；
- `telegram_config`；
- `telegram_subscribers`；
- `telegram_delivery_logs`；
- 相关索引、部分唯一索引、外键和 down migration。

迁移继续使用现有 `migrations/embed.go` 和启动时迁移机制。Telegram 默认为关闭，因此升级后不会主动访问 Telegram。

Docker Compose 不新增服务；Gateway 和 Checker 继续使用现有镜像。Checker 启动后按配置决定是否启用质量 Worker、Alert Reconciler 和 Telegram Worker。

## 10. 测试与验收

### 后端单元测试

- 质量任务状态转换、重试、过期回收、取消；
- OpenAI/Anthropic 基础检测和 SSE 解析；
- usage/余额差值归纳；
- 行为检测的通过、关注、异常和未知分支；
- 告警 key 稳定性和 reconcile 生命周期；
- 告警升级、恢复和持续时长；
- Telegram 命令解析、Chat ID 权限和分组过滤；
- Telegram HTML 转义和超长消息拆分；
- Telegram API 错误和退避。

### 集成测试

使用 `httptest` 上游 Mock 覆盖：

- OpenAI 非流式/流式；
- Anthropic 非流式/流式转换；
- HTTP 401/429/5xx、超时、无 `[DONE]`、非法 JSON；
- usage 缺失；
- 质量任务创建、领取、恢复和完成；
- Fake Telegram Bot API 的 `getMe`、`getUpdates`、`sendMessage`。

PostgreSQL 集成测试覆盖：

- 迁移从空库执行；
- 活跃任务部分唯一索引；
- `SKIP LOCKED` 多 Worker 不重复领取；
- active 告警唯一性和恢复后重新出现；
- 订阅者分组过滤。

### 前端验收

- npm run lint；
- npm run build；
- 站点详情创建、SSE 进度、取消、刷新恢复、历史查看；
- Telegram 设置保存、脱敏、测试发送、订阅者 CRUD；
- 窄屏布局和动画 `prefers-reduced-motion` 降级。

### 验收场景

1. 保存一个 OpenAI 站点，点击一键检测，完整结果在站点详情动画显示；
2. 保存一个 Anthropic 站点，验证请求/响应/SSE 转换结果；
3. 同一站点重复点击，返回 409 而不是创建重复任务；
4. Checker 重启后，未完成任务可重新排队；
5. 告警从新出现、持续、升级到恢复，Telegram 每个状态只按规则发送；
6. 未授权 Chat ID 无法查询；
7. 绑定分组的 Chat ID 看不到其他分组站点；
8. Telegram 发送失败不会阻塞 Checker 或其他订阅者；
9. `/relay`、`/balance`、`/health`、`/ratio` 与 Web 使用相同最新数据；
10. 所有日志、API 响应和 Telegram 消息均无凭据泄露。

## 11. 分阶段实施边界

### 第一阶段：基础可用

- 数据库迁移；
- 质量任务队列和 full/basic 执行器；
- 站点详情一键检测、SSE、结果历史；
- alert_events reconcile；
- Telegram 配置、订阅者管理、每小时告警汇总；
- `/alerts`、`/status`、`/relay`、`/relay <id>`。

### 第二阶段：查询完善

- `/balance`、`/health`、`/ratio` 全量和单站点详情；
- `/quality <id>`；
- 发送日志和告警页联动；
- 分组过滤细化。

### 后续可选能力

- Telegram `/quality-check <id>` 主动触发质量检测；
- Critical 告警即时推送；
- 可配置告警类型和分组订阅；
- 质量趋势图和模型行为基线；
- Web 管理台直达链接和内联键盘。

以上后续能力不纳入第一阶段，避免 Telegram 命令触发上游费用和扩大首期范围。

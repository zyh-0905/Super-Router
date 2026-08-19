# Telegram 告警与中转站查询 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Smart Router 增加可恢复的告警生命周期、Telegram 每小时告警汇总、多 Chat ID 授权和中转站只读查询能力。

**Architecture:** 将现有 AdminHandler 中动态告警计算抽取为共享 internal/alert 服务，由 Checker 周期性 reconcile 到 PostgreSQL alert_events；Gateway 和 Telegram 都读取持久化事件与共享站点查询 DTO。Telegram 使用 Checker 内的 Bot Worker，通过 Bot API 长轮询接收命令、按小时整点发送汇总，并用 PostgreSQL advisory lock 保证多实例只有一个 poller/report owner。

**Tech Stack:** Go 1.26、Gin、pgx/v5、Redis、PostgreSQL、Zap、Vue 3/Vite、Telegram Bot HTTP API。

---

## 文件地图与依赖顺序

### 新建文件

- **migrations/020_alert_telegram.up.sql**：告警事件、Telegram 配置、订阅者、投递日志表及索引。
- **migrations/020_alert_telegram.down.sql**：上述表和索引的人工回滚脚本。
- **internal/alert/types.go**：告警 DTO、稳定 key、严重度和生命周期类型。
- **internal/alert/evaluator.go**：从数据库评估当前活跃告警。
- **internal/alert/reconciler.go**：将当前评估结果同步到 alert_events。
- **internal/alert/service.go**：Web 和 Telegram 共用的 active/history 查询服务。
- **internal/alert/formatter.go**：告警详情和小时汇总所需的结构化数据组装。
- **internal/telegram/types.go**：Telegram 配置、订阅者、命令和发送结果类型。
- **internal/telegram/client.go**：Bot API getMe、getUpdates、sendMessage HTTP 客户端。
- **internal/telegram/format.go**：HTML 安全转义、告警/中转站消息格式化和超长拆分。
- **internal/telegram/query.go**：Telegram 只读查询所需的中转站信息查询。
- **internal/telegram/worker.go**：长轮询、授权、命令分发、小时报告和投递记录。
- **internal/api/telegram.go**：Telegram 配置、订阅者和手动发送管理接口。
- **internal/api/telegram_test.go**：管理接口的请求校验、脱敏和授权测试。
- **internal/alert/*_test.go**：告警 key、评估、reconcile、恢复和升级测试。
- **internal/telegram/*_test.go**：Bot API、命令权限、格式化、拆分和失败重试测试。

### 修改文件

- **internal/api/admin.go**：将 GetAlerts/GetStats 使用的告警来源切换到共享 alert service；保留响应字段兼容现有前端。
- **cmd/gateway/main.go**：注册 Telegram 管理 API，注入 alert service。
- **cmd/checker/main.go**：启动 Alert Reconciler 和 Telegram Worker，并在退出时停止。
- **web/src/api.js**：增加 Telegram 管理 API 方法。
- **web/src/views/SettingsView.vue**：增加 Telegram 配置和订阅者管理卡片。
- **web/src/views/AlertsView.vue**：展示 Telegram 状态和立即发送入口。
- **web/src/store.js**：增加 Telegram 状态缓存字段（只保存非敏感状态，不保存 Bot Token）。
- **internal/migrate/migrate.go**：增加 020_alert_telegram 的存量基线探测。
- **README.md、QUICKREF.md、web/README.md**：补充 Telegram 配置、命令和安全运行说明。

### 执行顺序

1. 迁移与领域类型；
2. 告警评估/reconcile 和 Web 读取切换；
3. Telegram 客户端、格式化、查询和 Worker；
4. Gateway/Checker 装配；
5. Web 管理台；
6. 集成验证、文档和提交。

---

## Task 1: 告警与 Telegram 数据库迁移

**Files:**
- Create: migrations/020_alert_telegram.up.sql
- Create: migrations/020_alert_telegram.down.sql
- Modify: internal/migrate/migrate.go
- Test: migrations/020_alert_telegram.up.sql via PostgreSQL integration test

- [ ] **Step 1: 写迁移验收测试或 SQL 检查清单**

验证空库执行后存在以下对象：

```sql
SELECT to_regclass('public.alert_events');
SELECT to_regclass('public.telegram_config');
SELECT to_regclass('public.telegram_subscribers');
SELECT to_regclass('public.telegram_delivery_logs');
```

同时检查：

```sql
SELECT indexname FROM pg_indexes
WHERE indexname IN (
  'idx_alert_events_active_key',
  'idx_alert_events_channel_time',
  'idx_telegram_subscribers_enabled',
  'idx_telegram_delivery_logs_subscriber_time'
);
```

- [ ] **Step 2: 创建告警事件表**

在 migrations/020_alert_telegram.up.sql 中创建 alert_events，至少包含：

```sql
CREATE TABLE alert_events (
    id BIGSERIAL PRIMARY KEY,
    alert_key VARCHAR(255) NOT NULL,
    alert_type VARCHAR(64) NOT NULL,
    severity VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    channel_id INT REFERENCES upstreams(id) ON DELETE SET NULL,
    group_id INT REFERENCES channel_groups(id) ON DELETE SET NULL,
    model VARCHAR(100),
    title VARCHAR(200) NOT NULL,
    message TEXT NOT NULL,
    current_value DOUBLE PRECISION,
    threshold_value DOUBLE PRECISION,
    unit VARCHAR(32),
    impact TEXT,
    recommendation TEXT,
    admin_path VARCHAR(255),
    metadata JSONB NOT NULL DEFAULT '{}',
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at TIMESTAMP NOT NULL,
    recovered_at TIMESTAMP,
    occurrence_count INT NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_alert_events_active_key
    ON alert_events(alert_key) WHERE status = 'active';
CREATE INDEX idx_alert_events_channel_time
    ON alert_events(channel_id, created_at DESC);
CREATE INDEX idx_alert_events_status_time
    ON alert_events(status, last_seen_at DESC);
```

- [ ] **Step 3: 创建 Telegram 配置、订阅者和投递日志表**

使用单行配置表，Bot Token 按 enc:v1: 密文存储：

```sql
CREATE TABLE telegram_config (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    enabled BOOLEAN NOT NULL DEFAULT false,
    bot_token TEXT NOT NULL DEFAULT '',
    report_enabled BOOLEAN NOT NULL DEFAULT true,
    report_interval_minutes INT NOT NULL DEFAULT 60,
    report_minute INT NOT NULL DEFAULT 0,
    timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
    include_recovered BOOLEAN NOT NULL DEFAULT true,
    include_ongoing BOOLEAN NOT NULL DEFAULT true,
    web_base_url TEXT NOT NULL DEFAULT '',
    last_poll_at TIMESTAMP,
    last_update_id BIGINT NOT NULL DEFAULT 0,
    last_report_at TIMESTAMP,
    last_error TEXT,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
INSERT INTO telegram_config(id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE telegram_subscribers (
    id BIGSERIAL PRIMARY KEY,
    chat_id BIGINT UNIQUE NOT NULL,
    chat_type VARCHAR(16) NOT NULL DEFAULT 'private',
    display_name VARCHAR(200) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT true,
    alert_enabled BOOLEAN NOT NULL DEFAULT true,
    query_enabled BOOLEAN NOT NULL DEFAULT true,
    group_ids JSONB NOT NULL DEFAULT '[]',
    last_sent_at TIMESTAMP,
    last_error TEXT,
    failure_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_telegram_subscribers_enabled
    ON telegram_subscribers(enabled, alert_enabled);

CREATE TABLE telegram_delivery_logs (
    id BIGSERIAL PRIMARY KEY,
    subscriber_id BIGINT REFERENCES telegram_subscribers(id) ON DELETE CASCADE,
    message_kind VARCHAR(32) NOT NULL,
    window_start TIMESTAMP,
    window_end TIMESTAMP,
    success BOOLEAN NOT NULL,
    telegram_message_id BIGINT,
    error TEXT,
    sent_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_telegram_delivery_logs_subscriber_time
    ON telegram_delivery_logs(subscriber_id, sent_at DESC);
```

- [ ] **Step 4: 增加迁移基线探测**

在 internal/migrate/migrate.go 的 canary map 中增加：

```go
"020_alert_telegram": "SELECT to_regclass('public.alert_events') IS NOT NULL AND to_regclass('public.telegram_config') IS NOT NULL",
```

- [ ] **Step 5: 添加 down migration**

按依赖逆序删除：delivery logs、subscribers、telegram_config、alert_events 和索引；down migration 不由应用启动自动执行。

- [ ] **Step 6: 运行迁移验证**

Run:

```bash
docker compose config --quiet
go test ./internal/migrate ./internal/config
```

Expected: Compose 校验成功，相关 Go 测试通过。若宿主机没有 Go，使用项目已有 Go 1.26 容器执行同样的 test 命令。

- [ ] **Step 7: Commit**

```bash
git add migrations/020_alert_telegram.up.sql migrations/020_alert_telegram.down.sql internal/migrate/migrate.go
git commit -m "feat: add alert and Telegram persistence schema"
```

---

## Task 2: 抽取告警领域模型与共享评估器

**Files:**
- Create: internal/alert/types.go
- Create: internal/alert/evaluator.go
- Create: internal/alert/service.go
- Test: internal/alert/types_test.go
- Test: internal/alert/evaluator_test.go
- Modify: internal/api/admin.go

- [ ] **Step 1: 写稳定 key 和严重度测试**

测试以下输入输出：

```go
func TestAlertKeyIsStable(t *testing.T) {
    got := StableKey(AlertInput{Type: "low_balance", ChannelID: 3})
    if got != "low_balance:channel-3" { t.Fatal(got) }
}

func TestAlertKeyIncludesModelAndGroup(t *testing.T) {
    got := StableKey(AlertInput{Type: "circuit_open", ChannelID: 3, Model: "gpt-5.5", GroupID: 2})
    if got != "circuit_open:channel-3:model-gpt-5.5:group-2" { t.Fatal(got) }
}

func TestSeverityRank(t *testing.T) {
    if SeverityRank("critical") <= SeverityRank("warning") { t.Fatal("rank order invalid") }
}
```

- [ ] **Step 2: 定义 alert DTO 和接口**

internal/alert/types.go 使用明确 DTO，避免 API map[string]interface{} 在核心逻辑中继续扩散：

```go
type Severity string
const (
    SeverityCritical Severity = "critical"
    SeverityWarning  Severity = "warning"
)

type AlertStatus string
const (
    StatusActive    AlertStatus = "active"
    StatusRecovered AlertStatus = "recovered"
)

type Alert struct {
    ID              int64
    Key             string
    Type            string
    Severity        Severity
    Status          AlertStatus
    ChannelID       *int
    GroupID         *int
    Model           string
    Title           string
    Message         string
    CurrentValue    *float64
    ThresholdValue  *float64
    Unit            string
    Impact          string
    Recommendation  string
    AdminPath       string
    Metadata        map[string]interface{}
    FirstSeenAt     time.Time
    LastSeenAt      time.Time
    RecoveredAt     *time.Time
    OccurrenceCount int
}

type AlertInput struct {
    Type      string
    Severity  Severity
    ChannelID int
    GroupID   int
    Model     string
}

type AlertChanges struct {
    New       []Alert
    Escalated []Alert
    Ongoing   []Alert
    Recovered []Alert
}

type EventStore interface {
    Reconcile(ctx context.Context, current []Alert, now time.Time) error
}

type Evaluator struct { DB *store.DB }
func (e *Evaluator) Evaluate(ctx context.Context, groupID *int) ([]Alert, error)

type Service struct { DB *store.DB }
func (s *Service) Active(ctx context.Context, groupID *int) ([]Alert, error)
func (s *Service) ChangesSince(ctx context.Context, since time.Time, groupIDs []int) (AlertChanges, error)
func (s *Service) GetByKey(ctx context.Context, key string) (*Alert, error)
```

`Evaluate` 必须覆盖现有 buildAlerts 的余额、倍率、熔断、禁用站点和价格同步失败逻辑，并为每条结果生成稳定 key。质量检测失败的输入通过单独的 UpsertQualityFailure 方法在质量计划中接入。

- [ ] **Step 3: 运行测试确认缺少实现**

Run:

```bash
go test ./internal/alert -run 'TestAlertKey|TestSeverityRank' -v
```

Expected: FAIL，因为新包和函数尚未实现。

- [ ] **Step 4: 实现纯函数和数据库查询 DTO**

实现 StableKey、SeverityRank、持续时长和告警输入转换；数据库查询只在 evaluator.go 中完成，返回稳定排序的 Alert 列表：critical 优先、first_seen_at 倒序、key 作为最终 tie-breaker。

- [ ] **Step 5: 实现 active/history service**

service.go 只读取 alert_events，不重新执行告警判断。分组过滤必须同时覆盖“分组专属告警”和“渠道级告警”：

```sql
WHERE $1::int IS NULL
   OR ae.group_id = $1
   OR (
        ae.group_id IS NULL
        AND ae.channel_id IS NOT NULL
        AND EXISTS (
            SELECT 1 FROM channel_group_members cgm
            WHERE cgm.channel_id = ae.channel_id AND cgm.group_id = $1
        )
   )
```

对于绑定多个分组的 Telegram 用户，使用参数化数组和 ANY；渠道级告警只要所属渠道位于任一授权分组即可返回，不能因 alert_events.group_id 为 NULL 而被错误隐藏，也不能拼接用户输入。

- [ ] **Step 6: 实现 SQL reconcile 测试**

使用 fake Evaluator 和 fake EventStore 对 reconcile 事务边界做确定性测试；真实 SQL 在 Task 9 的 PostgreSQL 验收中覆盖：

1. 新 key 创建 active；
2. 相同 key 更新 last_seen_at 和 occurrence_count；
3. warning 升级 critical；
4. active 本轮消失后写 recovered_at；
5. 恢复后再次出现可以插入新的 active 行；
6. advisory lock 失败时不修改状态。

- [ ] **Step 7: 将 AdminHandler 切换到共享 service**

修改 GetAlerts 和 GetStats：

- 由 alert.Service.Active 读取 active events；
- 保持现有 JSON 字段 alerts、total、group_id、generated_at；
- 保持前端已有 id、name、channel、model、sev、ago 字段兼容；
- 将新的 impact、recommendation、admin_path、first_seen_at、last_seen_at 作为附加字段返回；
- 如果没有 reconcile 数据，响应中返回 data_freshness 或 generated_at，不能静默伪造实时状态。

- [ ] **Step 8: 运行告警包和 API 回归测试**

```bash
go test ./internal/alert ./internal/api -run 'Alert|Stats|Group' -v
```

Expected: 新告警测试和已有 API 单元测试通过。

- [ ] **Step 9: Commit**

```bash
git add internal/alert internal/api/admin.go
git commit -m "feat: persist and reconcile alert lifecycle"
```

---

## Task 3: Checker 告警 Reconciler

**Files:**
- Create: internal/alert/reconciler.go
- Test: internal/alert/reconciler_test.go
- Modify: cmd/checker/main.go

- [ ] **Step 1: 写 reconcile 调度测试**

测试：

- 启动时执行一次；
- 每个 tick 至多执行一次；
- 两个实例只有 advisory lock owner 执行；
- Evaluate 失败时保留已有 active 告警，不把全部告警误恢复；
- 成功后才执行 active → recovered。

- [ ] **Step 2: 实现 Reconciler**

```go
type Reconciler struct {
    Evaluator *Evaluator
    Store     EventStore
    Logger    *zap.Logger
}

func (r *Reconciler) Reconcile(ctx context.Context) error
```

Reconcile 必须在同一个数据库锁保护下：Evaluator 先完整计算当前告警；只有评估成功才调用 EventStore.Reconcile 在单事务内 upsert 当前告警并恢复未出现告警。任何评估 SQL 失败都返回错误并跳过 Store。

- [ ] **Step 3: 接入 Checker 生命周期**

cmd/checker/main.go 中：

- 创建 reconciler；
- 启动时立即执行一次；
- 5 秒 tick 中以 30 秒最小间隔执行一次；
- 使用 checker context；
- shutdown 时等待当前 reconcile 完成。

- [ ] **Step 4: 运行 checker 测试**

```bash
go test ./cmd/checker ./internal/alert -v
```

Expected: 原有 checker 调度测试和新增 reconcile 测试通过。

- [ ] **Step 5: Commit**

```bash
git add cmd/checker/main.go internal/alert/reconciler.go internal/alert/reconciler_test.go
git commit -m "feat: reconcile alerts from checker"
```

---

## Task 4: Telegram Bot API 客户端与消息格式化

**Files:**
- Create: internal/telegram/types.go
- Create: internal/telegram/client.go
- Create: internal/telegram/format.go
- Create: internal/telegram/format_test.go
- Test: internal/telegram/client_test.go

- [ ] **Step 1: 写格式化和拆分测试**

```go
func TestEscapeHTML(t *testing.T) {
    got := EscapeHTML(`<Relay & "A">`)
    if got != "&lt;Relay &amp; &quot;A&quot;&gt;" { t.Fatal(got) }
}

func TestSplitTelegramMessage(t *testing.T) {
    parts := SplitMessage(strings.Repeat("x", 9000), 4096)
    if len(parts) != 3 { t.Fatalf("got %d parts", len(parts)) }
    if !strings.Contains(parts[0], "(1/3)") { t.Fatal(parts[0]) }
}
```

- [ ] **Step 2: 定义客户端接口**

```go
type Config struct {
    Enabled               bool
    ReportEnabled         bool
    ReportIntervalMinutes int
    ReportMinute          int
    Timezone              string
    IncludeRecovered      bool
    IncludeOngoing        bool
    LastUpdateID          int64
    LastReportAt          *time.Time
}

type Subscriber struct {
    ID           int64
    ChatID       int64
    Enabled      bool
    AlertEnabled bool
    QueryEnabled bool
    GroupIDs     []int
}

type Update struct {
    UpdateID int64
    ChatID   int64
    Text     string
}

type RelaySummary struct {
    ID           int
    Name         string
    Host         string
    Healthy      bool
    Balance      *float64
    Ratio        *float64
    CircuitState string
}

type RelayDetail struct {
    RelaySummary
    Protocol     string
    RelayType    string
    Groups       []string
    Requests24h  int
    SuccessRate  float64
    AverageMS    int
    P95MS        int
}

type BalanceSummary struct {
    ChannelID int
    Name      string
    Balance   *float64
    Currency  string
    Source    string
    CheckedAt *time.Time
}

type HealthSummary struct {
    ChannelID    int
    Name         string
    Alive        bool
    LatencyMS    *int
    SuccessRate  float64
    CircuitState string
    CheckedAt    *time.Time
}

type RatioSummary struct {
    ChannelID int
    Name      string
    Model     string
    Ratio     *float64
    Limit     float64
    Basis     string
    CheckedAt *time.Time
}


type BotClient interface {
    GetMe(ctx context.Context) error
    GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error)
    SendMessage(ctx context.Context, chatID int64, html string) (int64, error)
}
```

HTTP 实现只允许 Telegram API base URL 为配置常量，Bot Token 通过 URL path 或请求认证传入，不写日志。请求 context 必须包含超时。

- [ ] **Step 3: 实现 getMe/getUpdates/sendMessage**

覆盖：

- HTTP 非 2xx；
- Telegram response ok=false；
- network timeout；
- 429 Retry-After；
- 响应 JSON 解码失败；
- sendMessage 的 HTML parse_mode；
- 记录 Telegram message_id。

- [ ] **Step 4: 实现告警汇总 formatter**

消息固定顺序：标题、窗口、系统概况、当前 active、new、escalated、ongoing、recovered、查询提示。持续告警显示摘要；新告警显示当前值、阈值、持续时间、影响和建议。空变化窗口发送心跳摘要。

- [ ] **Step 5: 实现中转站查询 formatter**

提供：

```go
func FormatRelayList(items []RelaySummary) string
func FormatRelayDetail(item RelayDetail) string
func FormatBalanceList(items []BalanceSummary) string
func FormatHealthList(items []HealthSummary) string
func FormatRatioList(items []RatioSummary) string
```

所有动态字段经过 EscapeHTML；Base URL 只显示域名；没有结果时输出“暂无有效检测结果”和检测时间。

- [ ] **Step 6: 运行 Telegram 包测试**

```bash
go test ./internal/telegram -v
```

Expected: formatter 和 fake HTTP client 测试通过。

- [ ] **Step 7: Commit**

```bash
git add internal/telegram
git commit -m "feat: add Telegram Bot client and message formatting"
```

---

## Task 5: Telegram 中转站查询服务与命令权限

**Files:**
- Create: internal/telegram/query.go
- Create: internal/telegram/commands.go
- Create: internal/telegram/commands_test.go

- [ ] **Step 1: 写授权测试**

覆盖：

- 未知 chat_id 被拒绝；
- disabled subscriber 被拒绝；
- query_enabled=false 只能拒绝查询；
- group_ids=[] 可看全部；
- group_ids=[2] 看不到 group_id=3；
- `/relay@botname 3` 能正确解析为 relay id 3；
- 空参数命令返回帮助文本。

- [ ] **Step 2: 定义命令和查询接口**

```go
type QueryService interface {
    RelayList(ctx context.Context, groupIDs []int) (string, error)
    RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error)
    BalanceList(ctx context.Context, groupIDs []int) (string, error)
    BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error)
    HealthList(ctx context.Context, groupIDs []int) (string, error)
    HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error)
    RatioList(ctx context.Context, groupIDs []int) (string, error)
    RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error)
}
```

数据只读取 upstreams、channel_groups、health_checks、balance_checks、probe_results、declared_prices、request_history、circuit_states 和 alert_events。

- [ ] **Step 3: 实现只读查询 SQL**

每个查询都必须：

- 使用参数化 SQL；
- 对绑定分组加 EXISTS/ANY 过滤；
- 只取最新或限定窗口数据；
- 不读取 API Key、Access Token、Balance Token；
- 查询异常返回可读错误，不泄露 SQL。

- [ ] **Step 4: 实现命令分发**

第一期命令：

```text
/start /help /alerts /alerts critical /alert <alert_key>
/status /relay /relay <id> /balance /balance <id>
/health /health <id> /ratio /ratio <id>
```

`/quality <id>` 在 API 质量计划完成后接入；本计划先为未知命令返回帮助，不伪造质量结果。

- [ ] **Step 5: 运行命令测试**

```bash
go test ./internal/telegram -run 'Command|Query|Permission' -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/telegram
git commit -m "feat: add authorized Telegram relay queries"
```

---

## Task 6: Telegram Worker 与小时汇总调度

**Files:**
- Create: internal/telegram/worker.go
- Create: internal/telegram/worker_test.go
- Modify: cmd/checker/main.go

- [ ] **Step 1: 写 Worker 调度测试**

覆盖：

- disabled 配置不轮询、不发送；
- getUpdates offset 单调递增并持久化 last_update_id；
- 未授权命令不执行查询；
- 只发送 enabled + alert_enabled 订阅者；
- 每小时整点只发送一次；
- report_interval_minutes=60；
- 一个订阅者失败后继续其他订阅者；
- 发送结果写入 delivery log；
- 429 使用 Retry-After，不忙循环。

- [ ] **Step 2: 实现 advisory lock owner**

定义两个固定 advisory lock key：

```go
const telegramPollerLock int64 = 746213082
const telegramReportLock int64 = 746213083
```

poller 和 report 任务分别获取锁；失去数据库连接时释放 ownership，其他 Checker 可接管。

- [ ] **Step 3: 实现长轮询**

每次调用 GetUpdates 使用 50 秒以内 HTTP timeout；启动时读取 telegram_config.last_update_id，成功处理一条 update 后事务性更新 last_update_id，再以 update_id+1 继续；解析 message text 时去掉 `/command@botname` 后缀；查询命令在同一 worker context 中执行并发送响应。

- [ ] **Step 4: 实现整点报告**

使用配置 timezone 和 report_minute：

```go
func ShouldSendReport(now time.Time, cfg Config, lastReport time.Time) bool
```

要求同一时间窗口幂等：先在锁内计算 window_start/window_end；发送前查询 telegram_delivery_logs，已有该订阅者该窗口 success=true 的记录则跳过。进程在中途崩溃后，新 owner 只补发没有成功记录的订阅者；失败记录允许下一轮短间隔重试，成功订阅者不会重复收到同一窗口。完成全部订阅者尝试后更新 last_report_at。

- [ ] **Step 5: 接入 Checker**

cmd/checker/main.go 启动：

```go
go alertReconciler.Run(ctx)
go telegramWorker.Run(ctx)
```

Worker 内部使用独立 goroutine，但共享 checker shutdown context。Checker 无法连接 Telegram 时只记录错误，不影响 alive/pricing/probe/balance。

- [ ] **Step 6: 运行测试**

```bash
go test ./cmd/checker ./internal/telegram ./internal/alert -v
```

- [ ] **Step 7: Commit**

```bash
git add cmd/checker/main.go internal/telegram/worker.go internal/telegram/worker_test.go
git commit -m "feat: run Telegram polling and hourly alert reports"
```

---

## Task 7: Gateway Telegram 管理 API

**Files:**
- Create: internal/api/telegram.go
- Create: internal/api/telegram_test.go
- Modify: cmd/gateway/main.go

- [ ] **Step 1: 写管理接口测试**

覆盖：

- 非 admin 返回 403；
- GET config 不返回完整 bot_token；
- PATCH config 加密新 Token；
- 空 Token 不覆盖已有 Token；
- report_interval_minutes 只能为正数；
- report_minute 在 0 到 59；
- timezone 非法时返回 400；
- chat_id 必须为非零整数；
- subscriber group_ids 中存在禁用/不存在分组时返回 400；
- 删除不存在 subscriber 返回 404。

- [ ] **Step 2: 实现 TelegramHandler**

```go
type TelegramHandler struct {
    DB        *store.DB
    CryptoKey string
    Logger    *zap.Logger
}
```

实现：

```text
GET/PATCH /admin/telegram/config
POST       /admin/telegram/test
POST       /admin/telegram/report
GET/POST   /admin/telegram/subscribers
PATCH/DELETE /admin/telegram/subscribers/:id
POST       /admin/telegram/subscribers/:id/test
GET        /admin/telegram/delivery-logs
```

PATCH Config 返回：

```json
{
  "enabled": false,
  "bot_configured": true,
  "bot_token_suffix": "abcd",
  "report_interval_minutes": 60,
  "timezone": "Asia/Shanghai",
  "last_poll_at": null,
  "last_report_at": null,
  "last_error": ""
}
```

- [ ] **Step 3: 注册路由**

cmd/gateway/main.go 将所有 Telegram 管理接口放入现有 adminGroup，继续使用 AuthMiddleware + RequireRole("admin")。

- [ ] **Step 4: 运行 API 测试**

```bash
go test ./internal/api -run 'Telegram' -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/api/telegram.go internal/api/telegram_test.go cmd/gateway/main.go
git commit -m "feat: add Telegram admin APIs"
```

---

## Task 8: Web Telegram 管理台和告警页联动

**Files:**
- Modify: web/src/api.js
- Modify: web/src/store.js
- Modify: web/src/views/SettingsView.vue
- Modify: web/src/views/AlertsView.vue

- [ ] **Step 1: 增加 api.js 方法**

新增方法：

```js
getTelegramConfig()
updateTelegramConfig(payload)
testTelegramConnection()
sendTelegramTestReport()
listTelegramSubscribers()
createTelegramSubscriber(payload)
updateTelegramSubscriber(id, payload)
deleteTelegramSubscriber(id)
sendTelegramSubscriberTest(id)
sendTelegramAlertSummary()
getTelegramDeliveryLogs()
```

所有方法复用已有 request() 和 admin Bearer Header；Token 仅通过 PATCH 发送，不写入 store。

- [ ] **Step 2: 增加 SettingsView Telegram 区域**

实现：

- 启用开关；
- Token 密码输入；
- 脱敏配置状态；
- 时区和整点汇总选项；
- 恢复/持续告警开关；
- 保存、测试连接、立即发送报告；
- 订阅者新增/编辑/启用/停用/删除/测试发送；
- 分组范围多选；
- 最后发送错误和连续失败次数展示。

保存成功后重新 GET config，确保表单不保留完整 Token。

- [ ] **Step 3: 增加 AlertsView 状态**

在告警页顶部展示：

```text
Telegram：已启用/未启用
最近汇总：时间
发送对象：数量
```

增加“立即发送当前告警汇总”按钮，成功/失败使用已有 toast。

- [ ] **Step 4: 运行前端质量检查**

```bash
cd web
npm run lint
npm run build
```

Expected: 无新增 error；已有 warning 不得增加。

- [ ] **Step 5: Commit**

```bash
git add web/src/api.js web/src/store.js web/src/views/SettingsView.vue web/src/views/AlertsView.vue
git commit -m "feat: add Telegram settings and alert status UI"
```

---

## Task 9: 文档、端到端验收和发布检查

**Files:**
- Modify: README.md
- Modify: QUICKREF.md
- Modify: web/README.md
- Test: Go, frontend and Compose validation commands

- [ ] **Step 1: 更新运行配置说明**

文档必须说明：

- Telegram 默认关闭；
- Bot Token 通过 Web 管理台保存并加密；
- 订阅者必须手动录入 Chat ID；
- 默认每小时整点发送；
- 查询命令和分组过滤；
- 查询不直接调用上游；
- Telegram 运行依赖 Checker；
- `docker compose down -v` 会删除数据库数据。

- [ ] **Step 2: 执行后端检查**

```bash
go test ./...
go vet ./...
go test -race ./...
```

如果宿主机没有 Go，使用 Go 1.26 容器执行同样命令。

- [ ] **Step 3: 执行前端和 Compose 检查**

```bash
cd web
npm run lint
npm run build
cd ..
docker compose config --quiet
```

- [ ] **Step 4: 验证敏感信息边界**

检查：

```bash
git grep -n "bot_token" -- '*.go' '*.vue' '*.md'
git grep -n "Authorization" -- internal/telegram internal/api/telegram.go
```

结果必须满足：完整 Bot Token 不出现在响应日志、Telegram 消息和前端状态；Authorization 不写入质量/告警 details。

- [ ] **Step 5: Commit 文档和最终验收**

```bash
git add README.md QUICKREF.md web/README.md MONITORING-QUICKREF.md
git commit -m "docs: document Telegram alert integration"
```

最终验收：

1. 一个新告警能进入 active；
2. 同一告警持续时 occurrence_count 增加而不是创建重复 active；
3. 告警恢复后 Telegram 汇总展示恢复；
4. 每个授权 Chat ID 独立投递，单个失败不阻塞其他；
5. 未授权 Chat ID 无法执行查询；
6. 分组绑定 Chat ID 看不到其他分组；
7. /relay、/balance、/health、/ratio 与 Web 数据口径一致；
8. Gateway/Checker 重启后告警状态和发送状态可恢复；
9. Go 测试、race、vet、前端 lint/build 和 Compose 检查通过。

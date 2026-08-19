# API 质量检测 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有站点详情中增加复用已保存凭据的一键 API 质量检测，后台异步执行 Basic/Full 阶段，通过带认证的 SSE 实时渲染并保存历史结果。

**Architecture:** Gateway 只创建、查询、取消任务和代理实时事件；Checker Quality Worker 使用 PostgreSQL SKIP LOCKED 领取任务，调用 OpenAI/Anthropic 上游执行最多两次小型聊天探测，并把结果写入 quality_check_runs/results。Redis Pub/Sub 加速 SSE，数据库轮询兜底；关键阶段失败写入共享 alert_events，Telegram 的 /quality 命令读取最近一次结果。

**Tech Stack:** Go 1.26、Gin、pgx/v5、Redis Pub/Sub、PostgreSQL、现有 protocol/checker/crypto 包、Vue 3/Vite、Fetch streaming SSE、Node 24 built-in test runner。

---

## 文件地图与依赖顺序

### 新建文件

- **migrations/021_quality_checks.up.sql**：质量任务和阶段结果表、索引、活跃任务唯一约束。
- **migrations/021_quality_checks.down.sql**：人工回滚。
- **internal/quality/types.go**：任务、阶段、结果、总体结论和事件 DTO。
- **internal/quality/repository.go**：创建、领取、更新、取消、恢复任务的 PostgreSQL Repository。
- **internal/quality/executor.go**：检测阶段编排和总体结果归纳。
- **internal/quality/connectivity.go**：模型列表、连接和认证检查。
- **internal/quality/chat.go**：OpenAI/Anthropic 非流式/流式探测与协议复用。
- **internal/quality/behavior.go**：usage、模型名、响应结构和启发式行为判定。
- **internal/quality/publisher.go**：Redis run channel 发布和订阅。
- **internal/quality/worker.go**：队列领取、并发、心跳、取消和过期回收。
- **internal/quality/*_test.go**：任务、阶段、协议、结果、Worker 和 publisher 测试。
- **internal/api/quality.go**：质量任务管理 API 和 SSE。
- **internal/api/quality_test.go**：admin 权限、模型校验、409、取消和 SSE 测试。
- **web/src/components/QualityCheckPanel.vue**：站点详情内嵌的质量检测卡片。
- **web/src/components/QualityStageTimeline.vue**：阶段状态和动画。
- **web/src/quality.js**：SSE 解析、阶段状态合并和前端显示映射纯函数。
- **web/tests/quality.test.js**：Node 内置 test runner 测试。

### 修改文件

- **internal/migrate/migrate.go**：增加 021_quality_checks 基线探测。
- **cmd/checker/main.go**：启动 Quality Worker。
- **cmd/gateway/main.go**：注册质量 API。
- **internal/alert/types.go 或新建 internal/alert/external.go**：为质量硬失败提供稳定告警写入接口。
- **internal/telegram/query.go、commands.go、format.go**：增加 /quality <channel_id> 最近结果查询。
- **web/src/api.js**：任务 CRUD、历史和 fetch SSE。
- **web/src/views/ChannelsView.vue**：在站点详情指标下嵌入 QualityCheckPanel。
- **web/package.json**：增加 test:quality 脚本，不引入新依赖。
- **README.md、web/README.md、QUICKREF.md**：补充费用、检测层级、结果语义和接口。

### 依赖

本计划依赖 Telegram/告警计划已提供的 internal/alert 共享服务和 alert_events 表。质量检测本身可以先实现；最后接入 quality_check_failed 告警和 Telegram /quality。

---

## Task 1: 质量任务数据库迁移

**Files:**
- Create: migrations/021_quality_checks.up.sql
- Create: migrations/021_quality_checks.down.sql
- Modify: internal/migrate/migrate.go

- [ ] **Step 1: 写迁移对象检查**

```sql
SELECT to_regclass('public.quality_check_runs');
SELECT to_regclass('public.quality_check_results');
SELECT indexname FROM pg_indexes
WHERE indexname = 'idx_quality_check_runs_active_channel';
```

- [ ] **Step 2: 创建 quality_check_runs**

```sql
CREATE TABLE quality_check_runs (
    id BIGSERIAL PRIMARY KEY,
    channel_id INT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    model VARCHAR(100) NOT NULL,
    depth VARCHAR(16) NOT NULL CHECK (depth IN ('basic', 'full')),
    status VARCHAR(24) NOT NULL CHECK (status IN (
        'queued', 'running', 'cancel_requested', 'completed',
        'failed', 'cancelled', 'expired'
    )),
    overall_status VARCHAR(16) CHECK (overall_status IS NULL OR overall_status IN (
        'good', 'attention', 'failed', 'unknown'
    )),
    current_stage VARCHAR(32) NOT NULL DEFAULT '',
    progress INT NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    attempt_count INT NOT NULL DEFAULT 0,
    worker_id VARCHAR(128) NOT NULL DEFAULT '',
    heartbeat_at TIMESTAMP,
    requested_by_key_hash VARCHAR(64) NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    started_at TIMESTAMP,
    finished_at TIMESTAMP
);

CREATE INDEX idx_quality_check_runs_channel_time
    ON quality_check_runs(channel_id, created_at DESC);
CREATE INDEX idx_quality_check_runs_queue
    ON quality_check_runs(status, created_at);
CREATE UNIQUE INDEX idx_quality_check_runs_active_channel
    ON quality_check_runs(channel_id)
    WHERE status IN ('queued', 'running', 'cancel_requested');
```

- [ ] **Step 3: 创建 quality_check_results**

```sql
CREATE TABLE quality_check_results (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES quality_check_runs(id) ON DELETE CASCADE,
    stage VARCHAR(32) NOT NULL,
    check_name VARCHAR(100) NOT NULL,
    status VARCHAR(16) NOT NULL CHECK (status IN (
        'waiting', 'running', 'passed', 'attention', 'failed', 'unknown', 'skipped'
    )),
    http_status INT,
    latency_ms INT,
    ttfb_ms INT,
    actual_model VARCHAR(100) NOT NULL DEFAULT '',
    prompt_tokens INT,
    completion_tokens INT,
    total_tokens INT,
    details JSONB NOT NULL DEFAULT '{}',
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (run_id, stage, check_name)
);
CREATE INDEX idx_quality_check_results_run_time
    ON quality_check_results(run_id, created_at);
```

- [ ] **Step 4: 增加 migration canary 和 down migration**

migrate canary：

```go
"021_quality_checks": "SELECT to_regclass('public.quality_check_runs') IS NOT NULL AND to_regclass('public.quality_check_results') IS NOT NULL",
```

Down migration 先删除 results，再删除 runs。

- [ ] **Step 5: 运行迁移相关检查**

```bash
go test ./internal/migrate ./internal/config
docker compose config --quiet
```

- [ ] **Step 6: Commit**

```bash
git add migrations/021_quality_checks.up.sql migrations/021_quality_checks.down.sql internal/migrate/migrate.go
git commit -m "feat: add API quality check schema"
```

---

## Task 2: 质量领域类型和 Repository

**Files:**
- Create: internal/quality/types.go
- Create: internal/quality/repository.go
- Create: internal/quality/repository_test.go

- [ ] **Step 1: 写任务 ID、状态和模型解析测试**

```go
func TestPublicRunIDRoundTrip(t *testing.T) {
    if got := PublicRunID(123); got != "qc_123" { t.Fatal(got) }
    id, err := ParseRunID("qc_123")
    if err != nil || id != 123 { t.Fatalf("id=%d err=%v", id, err) }
}

func TestOverallStatusPrecedence(t *testing.T) {
    got := Summarize([]StageResult{{Status: StatusPassed}, {Status: StatusAttention}})
    if got != OverallAttention { t.Fatal(got) }
}
```

- [ ] **Step 2: 定义核心类型**

```go
type RunStatus string
type ResultStatus string
type OverallStatus string

type Run struct {
    ID                 int64
    ChannelID          int
    Model              string
    Depth              string
    Status             RunStatus
    OverallStatus      OverallStatus
    CurrentStage       string
    Progress           int
    AttemptCount       int
    WorkerID           string
    HeartbeatAt        *time.Time
    RequestedByKeyHash string
    Error              string
    CreatedAt          time.Time
    StartedAt          *time.Time
    FinishedAt         *time.Time
}

type StageResult struct {
    Stage            string
    CheckName        string
    Status           ResultStatus
    HTTPStatus       *int
    LatencyMS        *int
    TTFBMS           *int
    ActualModel      string
    PromptTokens     *int
    CompletionTokens *int
    TotalTokens      *int
    Details          map[string]interface{}
    Error            string
}

type Channel struct {
    ID                 int
    Name               string
    BaseURL            string
    Protocol           string
    RelayType          string
    TestModel          string
    APIKey             string
    AccessToken        string
    ModelMapping       map[string]string
    Capabilities       []string
    TimeoutConnectMS   int
    TimeoutFirstByteMS int
    TimeoutTotalMS     int
}

type TokenUsage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
    Present          bool
}

type ChatEvidence struct {
    RequestedModel string
    ActualModel    string
    Text           string
    Usage          TokenUsage
    HTTPStatus     int
    TTFBMS         int
    TotalMS        int
    StreamEvents   int
    DoneReceived   bool
}

type Event struct {
    Type     string      `json:"type"`
    RunID    string      `json:"run_id"`
    Stage    string      `json:"stage,omitempty"`
    Progress int         `json:"progress,omitempty"`
    Result   interface{} `json:"result,omitempty"`
}

type Publisher interface {
    Publish(ctx context.Context, event Event) error
}

type AlertSink interface {
    QualityFailure(ctx context.Context, channelID int, model, stage, message string, metadata map[string]interface{}) error
    ResolveQualityFailures(ctx context.Context, channelID int, model string, passedStages []string) error
}

type StageContext struct {
    Run     *Run
    Channel *Channel
    NonStream *ChatEvidence
    Stream    *ChatEvidence
}

type Stage interface {
    Name() string
    Run(ctx context.Context, input *StageContext) StageResult
}

type NoopAlertSink struct{}
func (NoopAlertSink) QualityFailure(context.Context, int, string, string, string, map[string]interface{}) error { return nil }
func (NoopAlertSink) ResolveQualityFailures(context.Context, int, string, []string) error { return nil }
```

- [ ] **Step 3: 定义 Repository 接口**

```go
type Repository interface {
    Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error)
    Get(ctx context.Context, id int64) (*Run, []StageResult, error)
    ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error)
    FindActiveByChannel(ctx context.Context, channelID int) (*Run, error)
    ClaimNext(ctx context.Context, workerID string) (*Run, error)
    UpsertResult(ctx context.Context, runID int64, result StageResult) error
    SetProgress(ctx context.Context, runID int64, stage string, progress int) error
    Heartbeat(ctx context.Context, runID int64, workerID string) error
    RequestCancel(ctx context.Context, runID int64) error
    IsCancelRequested(ctx context.Context, runID int64) (bool, error)
    Complete(ctx context.Context, runID int64, overall OverallStatus) error
    Fail(ctx context.Context, runID int64, message string) error
    Cancel(ctx context.Context, runID int64) error
    RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error)
}
```

- [ ] **Step 4: 写 ClaimNext 和 stale recovery 测试**

测试 SQL 语义：

```sql
SELECT id FROM quality_check_runs
WHERE status = 'queued'
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT 1;
```

断言：领取后 status=running、attempt_count+1、started_at/heartbeat_at 设置；超时且未超重试次数回 queued，超重试次数标 expired。

- [ ] **Step 5: 实现 PostgresRepository**

所有动态字段使用参数；Create 捕获活跃任务唯一索引冲突，转换为带 ExistingRunID 的 ErrChannelBusy；Get 返回 run 和按阶段顺序排列的结果。

- [ ] **Step 6: 运行 Repository 测试**

```bash
go test ./internal/quality -run 'RunID|Overall|Repository|Claim|Stale' -v
```

- [ ] **Step 7: Commit**

```bash
git add internal/quality/types.go internal/quality/repository.go internal/quality/repository_test.go
git commit -m "feat: add quality check task repository"
```

---

## Task 3: 上游加载、模型解析和连通性检测

**Files:**
- Create: internal/quality/connectivity.go
- Create: internal/quality/connectivity_test.go
- Create: internal/quality/executor.go
- Test: internal/quality/executor_test.go

- [ ] **Step 1: 写模型解析测试**

覆盖：

```text
显式模型且存在映射 → 使用显式模型
test_model 存在映射 → 使用 test_model
全局 probe_model 存在映射 → 使用全局模型
三者均不可用 → 使用按名称排序的第一个有效模型
没有有效映射 → ErrNoMappedModel
```

- [ ] **Step 2: 定义 Executor 和站点加载器**

```go
type Executor struct {
    DB          *store.DB
    Repo        Repository
    Publisher   Publisher
    HTTPClient  *http.Client
    CryptoKey   string
    ProbeModel  string
    AlertSink   AlertSink
    Logger      *zap.Logger
}

func (e *Executor) Execute(ctx context.Context, run *Run) error
```

LoadChannel 从 upstreams 读取单站点配置并使用 crypto.Decrypt；解密失败不写入 details，只返回通用 credential_decrypt_failed。
Executor 构造时先注入 NoopAlertSink，Task 6 再替换为 internal/alert 实现，确保前置任务编译通过。

- [ ] **Step 3: 写 models endpoint 检测测试**

使用 httptest.Server 覆盖：

- OpenAI Authorization Bearer；
- Anthropic x-api-key + anthropic-version；
- 200 合法 JSON；
- 401；
- timeout；
- 404 模型列表但聊天端点后续可用时 status=attention、code=models_endpoint_unavailable；
- 绝不把 API Key 写进 result.Error 或 details。

- [ ] **Step 4: 实现 connectivity stage**

使用 protocol.ModelsEndpoint 和协议认证头；应用站点 timeout_total_ms 与 10 秒检测上限中的较小值；记录 HTTP 状态、耗时和错误类别。

- [ ] **Step 5: 实现阶段编排骨架**

Executor.Execute 的固定阶段顺序：

```go
stages := []Stage{
    ConnectivityStage{},
    ProtocolStage{},
    StreamStage{},
    UsageStage{},
    BehaviorStage{},
}
```

basic 在 StreamStage 后完成；full 继续 Usage/Behavior。每个阶段开始/结束都更新 Repository 和 Publisher；阶段之间检查取消。

- [ ] **Step 6: 运行连通性测试**

```bash
go test ./internal/quality -run 'ResolveModel|Connectivity|Credential' -v
```

- [ ] **Step 7: Commit**

```bash
git add internal/quality/connectivity.go internal/quality/connectivity_test.go internal/quality/executor.go internal/quality/executor_test.go
git commit -m "feat: add quality connectivity checks"
```

---

## Task 4: 非流式、流式、Usage 与行为检测

**Files:**
- Create: internal/quality/chat.go
- Create: internal/quality/chat_test.go
- Create: internal/quality/behavior.go
- Create: internal/quality/behavior_test.go
- Modify: internal/quality/executor.go

- [ ] **Step 1: 写非流式协议测试**

OpenAI mock 返回：

```json
{
  "id":"chatcmpl-test",
  "model":"gpt-5.5",
  "choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],
  "usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
}
```

Anthropic mock 返回 message response，断言经过 protocol.AnthropicToOpenAI 后 choices/message/usage 正常。

- [ ] **Step 2: 写流式协议测试**

覆盖：

- OpenAI data chunks + [DONE]；
- Anthropic message_start/text_delta/message_delta/message_stop；
- 首字节耗时记录；
- 无 [DONE]；
- 首字节超时；
- 中途非法 JSON；
- 事件数和总文本长度记录。

- [ ] **Step 3: 实现最多两次聊天请求**

增加 BuildProbeScenario(capabilities) 纯函数并测试：

```text
text-only：non-stream 普通文本，stream 普通文本
vision：non-stream 附带固定、体积极小的 data URL 图片内容块
tools：stream 附带固定函数定义和明确 tool_choice
tools + vision：分别放入两次既有请求，不新增第三次聊天请求
```


Executor 持有一次 non-stream result 和一次 stream result：

Executor 使用 Task 2 已定义的 ChatEvidence，分别保存 non-stream 和 stream 证据。

Protocol、Usage、Behavior 复用 non-stream evidence；Stream 复用 stream evidence，不额外发起行为请求。

- [ ] **Step 4: 实现 Usage 判定**

规则：

- prompt/completion/total 都存在且 total 等于两者之和 → passed；
- usage 缺失但响应可用 → attention；
- 负数或明显不一致 → failed；
- 余额前后差可读取时写入 details，不把余额令牌写入；
- 余额 API 失败不让基础质量检测整体失败，标 attention。
- StageResult.details 由后端 allowlist 组装，只允许指标、事件数量、判定证据和脱敏错误类别；禁止存入请求头、凭据和完整上游请求体；

- [ ] **Step 5: 实现 Behavior 判定**

规则：

- actual_model 与映射后的上游模型一致 → passed；
- actual_model 为空 → attention；
- 返回模型明显与映射不一致 → attention；
- 响应空、choices 缺失或无法解析 → failed；
- 身份/知识截止信号写入 details.evidence，只能产生 attention，不直接判“假模型”；
- 未声明 tools/vision 能力时相关检查为 skipped。

- [ ] **Step 6: 实现总体结果归纳**

关键阶段 connectivity/protocol/stream 任一 failed → OverallFailed；无关键失败但存在 attention → OverallAttention；全部通过/跳过 → OverallGood；数据不足 → OverallUnknown。

- [ ] **Step 7: 运行完整 executor 测试**

```bash
go test ./internal/quality -run 'Chat|Stream|Usage|Behavior|Summarize' -v
```

- [ ] **Step 8: Commit**

```bash
git add internal/quality/chat.go internal/quality/chat_test.go internal/quality/behavior.go internal/quality/behavior_test.go internal/quality/executor.go
git commit -m "feat: add deep API quality checks"
```

---

## Task 5: Redis 事件发布和 Quality Worker

**Files:**
- Create: internal/quality/publisher.go
- Create: internal/quality/publisher_test.go
- Create: internal/quality/worker.go
- Create: internal/quality/worker_test.go
- Modify: cmd/checker/main.go

- [ ] **Step 1: 写 Publisher 测试**

频道格式固定：

```go
func RunChannel(id int64) string { return fmt.Sprintf("quality:run:%d", id) }
```

事件 JSON 必须包含 type、run_id、stage、progress；Redis 不可用时 Publish 返回错误，但 Worker 继续依赖 PostgreSQL 完成任务。

- [ ] **Step 2: 写 Worker 测试**

覆盖：

- 全局并发上限 3；
- ClaimNext 返回空时等待；
- 每 10 秒 heartbeat；
- cancel_requested 取消执行；
- Execute 失败标 failed；
- Worker 启动时 RecoverStale；
- Redis 发布失败不丢数据库状态；
- shutdown context 取消所有任务并等待 goroutine。
- Checker 每日 retention cleanup 删除超过 checker.retention_days 的 quality_check_runs，quality_check_results 通过外键级联删除；

- [ ] **Step 3: 实现 Worker**

```go
type Worker struct {
    Repo          Repository
    Executor      *Executor
    WorkerID      string
    MaxConcurrent int
    PollInterval  time.Duration
    Logger        *zap.Logger
}

func (w *Worker) Run(ctx context.Context)
```

使用 semaphore 控制并发；ClaimNext 使用 SKIP LOCKED；任务 goroutine 完成前必须写最终状态。

- [ ] **Step 4: 接入 Checker**

cmd/checker/main.go：

- 创建 PostgresRepository、RedisPublisher、Executor、Worker；
- 使用配置中的 encryption_key 和 probe_model；
- 启动 `go qualityWorker.Run(ctx)`；
- Telegram/告警 Worker 失败不能停止 Quality Worker，反之亦然。

- [ ] **Step 5: 运行 Worker/Checker 测试**

```bash
go test ./internal/quality ./cmd/checker -run 'Worker|Scheduler|Probe' -v
```

- [ ] **Step 6: Commit**

```bash
git add internal/quality/publisher.go internal/quality/publisher_test.go internal/quality/worker.go internal/quality/worker_test.go cmd/checker/main.go
git commit -m "feat: run asynchronous API quality workers"
```

---

## Task 6: 质量失败告警接入

**Files:**
- Create or Modify: internal/alert/external.go
- Test: internal/alert/external_test.go
- Modify: internal/quality/executor.go

- [ ] **Step 1: 写质量失败告警测试**

断言稳定 key：

```go
quality_check_failed:channel-5:model-claude-sonnet-5:stream
```

关键阶段失败创建 warning active；同 key 再失败更新 occurrence；后续同模型 full 检测关键阶段通过则恢复该 key。Behavior attention 不创建 Critical。

- [ ] **Step 2: 定义 AlertSink**

```go
type AlertSink interface {
    QualityFailure(ctx context.Context, channelID int, model, stage, message string, metadata map[string]interface{}) error
    ResolveQualityFailures(ctx context.Context, channelID int, model string, passedStages []string) error
}
```

internal/alert 实现该接口，quality 只依赖接口。

- [ ] **Step 3: 接入 Executor**

任务最终写库成功后再写告警；告警写入失败记录 warning 日志，不反向把已完成检测改成 failed。

- [ ] **Step 4: 运行告警联动测试**

```bash
go test ./internal/quality ./internal/alert -run 'QualityFailure' -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/alert/external.go internal/alert/external_test.go internal/quality/executor.go
git commit -m "feat: alert on hard API quality failures"
```

---

## Task 7: Gateway 质量任务 API 与 SSE

**Files:**
- Create: internal/api/quality.go
- Create: internal/api/quality_test.go
- Modify: cmd/gateway/main.go

- [ ] **Step 1: 写 API 测试**

覆盖：

- 非 admin 拒绝；
- channel 不存在/禁用；
- 模型不在 model_mapping；
- depth 非 basic/full；
- 创建返回 qc_<id>；
- 同站点活跃任务返回 409 和 existing_run_id；
- 历史 limit 最大 100；
- cancel completed 返回 409；
- SSE Bearer 认证；
- Redis 不可用时 SSE 每秒轮询 DB；
- 客户端断开后停止订阅和 ticker。

- [ ] **Step 2: 实现 QualityHandler**

```go
type QualityHandler struct {
    Repo      quality.Repository
    Publisher *quality.RedisPublisher
    DB        *store.DB
    Logger    *zap.Logger
}
```

接口：

```text
POST /admin/channels/:id/quality-checks
GET  /admin/channels/:id/quality-checks
GET  /admin/quality-checks/:run_id
GET  /admin/quality-checks/:run_id/events
POST /admin/quality-checks/:run_id/cancel
```

创建时使用 auth middleware 已写入的 key_hash 作为 requested_by_key_hash，不保存明文 admin Key。

- [ ] **Step 3: 实现 SSE**

SSE 响应头：

```go
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive
```

流程：先发送当前 DB snapshot，再订阅 Redis；每 1 秒 poll DB 检查状态变化；terminal 状态发送最终事件并结束；所有 JSON 通过 json.Marshal。

- [ ] **Step 4: 注册路由并测试**

```bash
go test ./internal/api -run 'Quality' -v
go test ./cmd/gateway
```

- [ ] **Step 5: Commit**

```bash
git add internal/api/quality.go internal/api/quality_test.go cmd/gateway/main.go
git commit -m "feat: expose API quality task endpoints"
```

---

## Task 8: 前端 SSE 解析和 API 客户端

**Files:**
- Create: web/src/quality.js
- Create: web/tests/quality.test.js
- Modify: web/src/api.js
- Modify: web/package.json

- [ ] **Step 1: 写 SSE 解析失败测试**

web/tests/quality.test.js：

```js
import test from 'node:test'
import assert from 'node:assert/strict'
import { parseSSEChunk, mergeQualityEvent, sanitizeQualityDetails } from '../src/quality.js'

test('parses split SSE frames', () => {
  const first = parseSSEChunk('', 'event: stage_result\ndata: {"stage":"stream"')
  assert.equal(first.events.length, 0)
  const second = parseSSEChunk(first.buffer, '}\n\n')
  assert.equal(second.events[0].event, 'stage_result')
})

test('merges stage result without losing earlier stages', () => {
  const state = mergeQualityEvent({ stages: { connectivity: { status: 'passed' } } }, {
    event: 'stage_result', data: { stage: 'stream', status: 'passed' }
  })
  assert.equal(state.stages.connectivity.status, 'passed')
  assert.equal(state.stages.stream.status, 'passed')
})

test('removes credential-shaped fields from details', () => {
  const safe = sanitizeQualityDetails({ latency_ms: 12, authorization: 'Bearer secret', api_key: 'secret' })
  assert.deepEqual(safe, { latency_ms: 12 })
})
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd web
node --test tests/quality.test.js
```

Expected: FAIL，因为 quality.js 尚不存在。

- [ ] **Step 3: 实现纯函数**

quality.js 提供：

```js
export function parseSSEChunk(buffer, chunk)
export function mergeQualityEvent(state, event)
export function qualityLabel(status)
export function stageLabel(stage)
export function isTerminalStatus(status)
export function sanitizeQualityDetails(details)
```

解析器必须保留不完整尾块，支持 event/data 多行，不对非 JSON 行抛出未捕获异常。

- [ ] **Step 4: 增加 api.js 方法**

```js
createQualityCheck(channelId, payload)
listQualityChecks(channelId, limit = 5)
getQualityCheck(runId)
cancelQualityCheck(runId)
streamQualityEvents(runId, { signal, onEvent, onDisconnect })
```

streamQualityEvents 使用 fetch + authHeaders + ReadableStream reader，而不是 EventSource。

- [ ] **Step 5: 增加 package script 并运行测试**

package.json：

```json
"test:quality": "node --test tests/quality.test.js"
```

Run:

```bash
npm run test:quality
npm run lint
npm run build
```

- [ ] **Step 6: Commit**

```bash
git add web/src/quality.js web/tests/quality.test.js web/src/api.js web/package.json
git commit -m "feat: add quality SSE client state helpers"
```

---

## Task 9: 站点详情 integrated 质量检测 UI

**Files:**
- Create: web/src/components/QualityStageTimeline.vue
- Create: web/src/components/QualityCheckPanel.vue
- Modify: web/src/views/ChannelsView.vue

- [ ] **Step 1: 实现 QualityStageTimeline**

Props：

```js
stages: Object
currentStage: String
progress: Number
reducedMotion: Boolean
```

固定阶段：connectivity、protocol、stream、usage、behavior。每阶段显示 waiting/running/passed/attention/failed/unknown/skipped；running 使用呼吸圆点，prefers-reduced-motion 下禁用循环动画。

- [ ] **Step 2: 实现 QualityCheckPanel 状态机**

Panel 接收：

```js
channel
modelOptions
probeModelFallback
```

内部状态：

```text
idle / loading / queued / running / cancel_requested / completed / failed / cancelled
```

行为：

- 默认模型 test_model；
- 临时模型不保存到 channel；
- 创建任务后打开流；
- SSE 断开后调用 getQualityCheck 并退化为 1 秒 polling；
- 页面卸载时 AbortController.abort；
- cancel 后显示“正在停止”；
- terminal 后加载最近 5 条历史。

- [ ] **Step 3: 实现完成结果和历史**

显示：总体结论、模型、深度、总耗时、通过/关注/失败计数、各阶段指标。阶段详情折叠展示 details；使用过滤函数去除 authorization、api_key、access_token、balance_token 等敏感键。

历史项操作：查看详情、复制摘要、重新检测；不提供批量删除。

- [ ] **Step 4: 集成 ChannelsView**

将 QualityCheckPanel 放在余额/健康/倍率/24h 指标之后；打开不同站点时重置选中任务并加载对应历史；没有模型映射时禁用按钮并给出配置提示。

- [ ] **Step 5: 运行前端检查**

```bash
cd web
npm run test:quality
npm run lint
npm run build
```

Expected: 新测试通过，lint 无 error，build 成功；ECharts 大 chunk 警告不作为本功能失败。

- [ ] **Step 6: Commit**

```bash
git add web/src/components/QualityStageTimeline.vue web/src/components/QualityCheckPanel.vue web/src/views/ChannelsView.vue web/src/styles/base.css
git commit -m "feat: embed API quality checks in channel details"
```

---

## Task 10: Telegram /quality 查询

**Files:**
- Modify: internal/telegram/query.go
- Modify: internal/telegram/commands.go
- Modify: internal/telegram/format.go
- Modify: internal/telegram/commands_test.go

- [ ] **Step 1: 写 /quality 权限和格式测试**

覆盖：

- `/quality 5` 返回最近一次任务；
- 没有历史返回“暂无质量检测结果”；
- 站点不在授权分组返回拒绝；
- running 结果显示当前阶段和进度；
- completed 显示 overall、五阶段状态、耗时和时间；
- 不触发 Create 或真实上游调用。

- [ ] **Step 2: 扩展 QueryService**

```go
QualityLatest(ctx context.Context, channelID int, groupIDs []int) (string, error)
```

SQL 查询 quality_check_runs 最新记录及 results；group filtering 继续通过 channel_group_members 校验。

- [ ] **Step 3: 注册命令并格式化**

消息示例：

```text
🧪 最近一次接口质量检测
站点：Claude Relay B
模型：claude-sonnet-5
结果：🟢 良好
连接：通过
协议：通过
流式：通过
Usage：通过
模型行为：需要关注
总耗时：2.84 秒
```

- [ ] **Step 4: 运行 Telegram 测试**

```bash
go test ./internal/telegram -run 'Quality' -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/telegram/query.go internal/telegram/commands.go internal/telegram/format.go internal/telegram/commands_test.go
git commit -m "feat: query API quality results from Telegram"
```

---

## Task 11: 文档、完整验证与验收

**Files:**
- Modify: README.md
- Modify: QUICKREF.md
- Modify: web/README.md
- Test: full backend/frontend/compose checks

- [ ] **Step 1: 更新文档**

写明：

- 一键检测复用已保存凭据；
- 默认模型和临时模型规则；
- basic/full 阶段；
- full 最多两次小型聊天请求；
- 可能产生少量费用；
- 结果是启发式质量信号，不是绝对模型真实性证明；
- 任务由 Checker 执行；
- 页面通过带认证 fetch SSE 展示；
- Telegram /quality 只查询历史，不触发检测。

- [ ] **Step 2: 运行后端完整检查**

```bash
go test ./...
go vet ./...
go test -race ./...
```

- [ ] **Step 3: 运行前端完整检查**

```bash
cd web
npm run test:quality
npm run lint
npm run build
cd ..
docker compose config --quiet
```

- [ ] **Step 4: 运行敏感信息扫描**

```bash
git grep -n -E 'Authorization|api_key|access_token|balance_api_token' -- internal/quality internal/api/quality.go web/src/components/QualityCheckPanel.vue
```

人工确认只存在请求构造、字段名过滤或脱敏代码，不存在凭据日志和 API 返回。

- [ ] **Step 5: 验收关键场景**

1. OpenAI full 任务完成且最多两次聊天请求；
2. Anthropic 非流式/流式转换通过；
3. 同站点并发创建返回 409；
4. 任务取消和 Checker 重启恢复正确；
5. Redis 断开时 Web 仍通过 DB polling 得到最终状态；
6. 页面刷新后恢复任务和动画状态；
7. 硬失败创建 quality_check_failed 告警，后续通过可恢复；
8. Behavior attention 不被描述为“假模型”；
9. Telegram /quality 使用授权分组过滤；
10. Go test/vet/race、前端 test/lint/build 和 Compose 检查全部通过。

- [ ] **Step 6: Commit**

```bash
git add README.md QUICKREF.md web/README.md
git commit -m "docs: document API quality checks"
```

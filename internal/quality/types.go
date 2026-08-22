// Package quality 提供 API 接口质量检测领域模型：
//   - Gateway 创建任务、查询历史、代理 SSE；
//   - Checker Quality Worker 领取并执行 basic/full 阶段检测；
//   - 结果持久化到 quality_check_runs/results，供 Web 与 Telegram 查询。
//
// 结论是启发式质量信号（good/attention/failed/unknown），
// 不把行为探测包装成绝对真实性证明。
package quality

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RunStatus 任务状态。
type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunCancelRequested RunStatus = "cancel_requested"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
	RunExpired         RunStatus = "expired"
)

// IsTerminal 终态判断。
func IsTerminal(s RunStatus) bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled, RunExpired:
		return true
	}
	return false
}

// ResultStatus 阶段/检查结果状态。
type ResultStatus string

const (
	StatusWaiting   ResultStatus = "waiting"
	StatusRunning   ResultStatus = "running"
	StatusPassed    ResultStatus = "passed"
	StatusAttention ResultStatus = "attention"
	StatusFailed    ResultStatus = "failed"
	StatusUnknown   ResultStatus = "unknown"
	StatusSkipped   ResultStatus = "skipped"
)

// OverallStatus 总体结论。
type OverallStatus string

const (
	OverallGood      OverallStatus = "good"
	OverallAttention OverallStatus = "attention"
	OverallFailed    OverallStatus = "failed"
	OverallUnknown   OverallStatus = "unknown"
)

// 阶段常量（执行顺序：basic = 前三阶段，full = 全部五阶段，
// authenticity = 连接性 + 模型鉴定）。
const (
	StageConnectivity  = "connectivity"
	StageProtocol      = "protocol"
	StageStream        = "stream"
	StageUsage         = "usage"
	StageBehavior      = "behavior"
	StageAuthenticity  = "authenticity"
)

// BasicStages basic 深度执行的阶段。
var BasicStages = []string{StageConnectivity, StageProtocol, StageStream}

// FullStages full 深度执行的阶段（含模型鉴定）。
var FullStages = []string{StageConnectivity, StageProtocol, StageStream, StageUsage, StageBehavior, StageAuthenticity}

// 关键阶段（任一 failed → 总体 failed）。
var criticalStages = map[string]bool{
	StageConnectivity: true, StageProtocol: true, StageStream: true,
}

// Run 任务 DTO。
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

// StageResult 单阶段/单检查结果。
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

// Channel 质量检测所需的最小站点视图（凭据仅存在于 Checker 进程内存）。
type Channel struct {
	ID                 int
	Name               string
	BaseURL            string
	Protocol           string
	RelayType          string
	TestModel          string
	APIKey             string
	AccessToken        string
	BalanceAPIURL      string
	BalanceAPIToken    string
	ModelMapping       map[string]string
	Capabilities       []string
	TimeoutConnectMS   int
	TimeoutFirstByteMS int
	TimeoutTotalMS     int
}

// TokenUsage OpenAI 兼容用量。
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Present          bool
}

// ChatEvidence 单次聊天探测的证据（非流式/流式各一份）。
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

// Event SSE 事件 DTO。
type Event struct {
	Type     string      `json:"type"`
	RunID    string      `json:"run_id"`
	Stage    string      `json:"stage,omitempty"`
	Progress int         `json:"progress,omitempty"`
	Result   interface{} `json:"result,omitempty"`
}

// Publisher 实时事件发布接口（Redis 实现 + 测试 fake）。
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

// AlertSink 质量硬失败告警写入接口（internal/alert 实现该接口）。
type AlertSink interface {
	QualityFailure(ctx context.Context, channelID int, model, stage, message string, metadata map[string]interface{}) error
	ResolveQualityFailures(ctx context.Context, channelID int, model string, passedStages []string) error
}

// NoopAlertSink 无告警实现的兜底（前置 Task 编译通过用）。
type NoopAlertSink struct{}

func (NoopAlertSink) QualityFailure(context.Context, int, string, string, string, map[string]interface{}) error {
	return nil
}
func (NoopAlertSink) ResolveQualityFailures(context.Context, int, string, []string) error {
	return nil
}

// StageContext 单阶段执行输入（NonStream/Stream 由前序阶段产出，后续阶段复用）。
type StageContext struct {
	Run       *Run
	Channel   *Channel
	NonStream *ChatEvidence
	Stream    *ChatEvidence
}

// Stage 阶段执行接口。
type Stage interface {
	Name() string
	Run(ctx context.Context, input *StageContext) StageResult
}

// PublicRunID int64 内部 ID → 公开 run_id（qc_<id>）。
func PublicRunID(id int64) string {
	return fmt.Sprintf("qc_%d", id)
}

// ParseRunID 公开 run_id → 内部 ID。
func ParseRunID(s string) (int64, error) {
	if !strings.HasPrefix(s, "qc_") {
		return 0, fmt.Errorf("invalid run id %q", s)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "qc_"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid run id %q", s)
	}
	return id, nil
}

// statusRank 归纳优先级：failed > unknown > attention > passed > skipped > waiting/running。
var statusRank = map[ResultStatus]int{
	StatusWaiting:   0,
	StatusRunning:   0,
	StatusSkipped:   1,
	StatusPassed:    2,
	StatusAttention: 3,
	StatusUnknown:   4,
	StatusFailed:    5,
}

// Summarize 阶段结果 → 总体结论：
//   - 关键阶段（connectivity/protocol/stream）任一 failed → failed；
//   - 无关键失败但存在 attention → attention；
//   - 全部通过/跳过 → good；
//   - 数据不足 → unknown。
func Summarize(results []StageResult) OverallStatus {
	if len(results) == 0 {
		return OverallUnknown
	}
	criticalFailed := false
	worst := StatusPassed
	for _, r := range results {
		if criticalStages[r.Stage] && r.Status == StatusFailed {
			criticalFailed = true
		}
		if statusRank[r.Status] > statusRank[worst] {
			worst = r.Status
		}
	}
	if criticalFailed {
		return OverallFailed
	}
	switch worst {
	case StatusFailed, StatusUnknown:
		return OverallFailed // 非关键阶段 failed 也归 failed（行为判定除外——见 behavior 的 attention 语义）
	case StatusAttention:
		return OverallAttention
	}
	return OverallGood
}

package quality

import (
	"context"
	"strings"
	"time"
)

// chatTimeout 单次聊天探测的兜底超时（渠道未配置 timeout_total_ms 时使用）。
//
// 注意：不能比渠道配置更严。实测慢速上游非流式 TTFB 可达 30s 以上
// （聚合生成：等全部内容生成完才返回首字节），若按 min(渠道值, 30s) 取值，
// 渠道明明配置了 60s 总超时却会被 30s 掐断，protocol 阶段必然超时失败。
const chatTimeout = 60 * time.Second

// maxChatTimeout 渠道配置值的安全上限（防配置异常导致任务永久挂起）。
const maxChatTimeout = 2 * time.Minute

// effectiveTimeout 渠道超时优先：渠道配置了 timeout_total_ms 时尊重它
// （上限 2 分钟），否则使用固定兜底。
func effectiveTimeout(ch *Channel, base time.Duration) time.Duration {
	if ch.TimeoutTotalMS > 0 {
		t := time.Duration(ch.TimeoutTotalMS) * time.Millisecond
		if t > maxChatTimeout {
			t = maxChatTimeout
		}
		return t
	}
	return base
}

// chatErrClass 聊天探测失败 → 稳定错误类别（写入 result.Details.code）。
// RunChat 的错误不含凭据与上游响应体（见 chat.go 注释），可以安全分类。
func chatErrClass(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "first byte timeout"):
		return "ttfb_timeout"
	case strings.Contains(msg, "upstream 429"):
		return "rate_limited"
	case strings.Contains(msg, "upstream 401"), strings.Contains(msg, "upstream 403"):
		return "auth_error"
	case strings.Contains(msg, "upstream 5"):
		return "upstream_error"
	case strings.Contains(msg, "upstream 4"):
		return "bad_request"
	case strings.Contains(msg, "decode"):
		return "decode_error"
	default:
		return "chat_request_error"
	}
}

// evidenceForStages 为 usage/behavior 阶段选择证据：
// 优先取「HTTP 200 且有响应文本」的证据（非流式或流式皆可）；
// 没有可用证据时返回 nil → 判定为 unknown，绝不虚构。
// 同时把所选证据的 metrics 记入阶段结果（detail 展示所选来源）。
func evidenceForStages(nonStream, stream *ChatEvidence) (*ChatEvidence, string) {
	usable := func(ev *ChatEvidence) bool {
		return ev != nil && ev.HTTPStatus == 200 && strings.TrimSpace(ev.Text) != ""
	}
	if usable(nonStream) {
		return nonStream, "non_stream"
	}
	if usable(stream) {
		return stream, "stream"
	}
	// 无可用证据：优先保留 nonStream 供 Judge* 输出 unknown/attention
	if nonStream != nil {
		return nonStream, "none"
	}
	return stream, "none"
}

// applyEvidenceMetrics 把所选证据的 HTTP/耗时/模型/token 指标复制进阶段结果，
// 并记录证据来源（non_stream/stream/none），让详情可解释。
func applyEvidenceMetrics(res *StageResult, ev *ChatEvidence, source string) {
	res.Details["evidence_source"] = source
	if ev == nil {
		return
	}
	res.HTTPStatus = intPtr(ev.HTTPStatus)
	res.TTFBMS = intPtr(ev.TTFBMS)
	res.LatencyMS = intPtr(ev.TotalMS)
	res.ActualModel = ev.ActualModel
	if ev.Usage.Present {
		res.PromptTokens = intPtr(ev.Usage.PromptTokens)
		res.CompletionTokens = intPtr(ev.Usage.CompletionTokens)
		res.TotalTokens = intPtr(ev.Usage.TotalTokens)
	}
}

// chatFailedResult 聊天请求失败时的阶段结果（保留原始错误便于定位，
// 长度截断防异常信息撑爆字段）。
// 注意：不覆盖 Judge* 已经做出的判定——JudgeProtocol/JudgeStream 在
// 首字节超时时会返回 attention（非流式超时是启发式信号而非功能故障），
// 只有 unknown/空状态才提升为 failed。
func chatFailedResult(res StageResult, err error) StageResult {
	if res.Status == "" || res.Status == StatusUnknown {
		res.Status = StatusFailed
	}
	res.Error = truncateRunErr(err.Error(), 200)
	if res.Details["code"] == nil {
		res.Details["code"] = chatErrClass(err)
	}
	return res
}

func truncateRunErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// protocolStage 复用非流式证据：协议转换 + 非流式响应结构。
type protocolStage struct {
	executor *Executor
}

func (s protocolStage) Name() string { return StageProtocol }

func (s protocolStage) Run(ctx context.Context, input *StageContext) StageResult {
	// 首次进入时发起非流式探测（整个任务至多一次）
	if input.NonStream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		ev, err := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout), s.executor.httpClient())
		input.NonStream = ev
		if err != nil {
			// 保留原始错误（如 first byte timeout / upstream 401），
			// 而不是统一写成 chat_request_failed 掩盖真实原因
			return chatFailedResult(
				JudgeProtocol(ev, chatErrClass(err) == "ttfb_timeout"), err)
		}
	}
	return JudgeProtocol(input.NonStream, false)
}

// streamStage 发起流式探测（整个任务至多一次）。
type streamStage struct {
	executor *Executor
}

func (s streamStage) Name() string { return StageStream }

func (s streamStage) Run(ctx context.Context, input *StageContext) StageResult {
	if input.Stream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		sc.Stream = true
		ev, err := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout), s.executor.httpClient())
		input.Stream = ev
		if err != nil {
			// 保留原始错误（如 first byte timeout），不掩盖真实原因
			return chatFailedResult(
				JudgeStream(ev, chatErrClass(err) == "ttfb_timeout"), err)
		}
	}
	return JudgeStream(input.Stream, false)
}

// usageStage 复用非流式证据（若协议阶段未成功发起，尝试发起）。
type usageStage struct {
	executor *Executor
}

func (s usageStage) Name() string { return StageUsage }

func (s usageStage) Run(ctx context.Context, input *StageContext) StageResult {
	if input.NonStream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		ev, _ := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout), s.executor.httpClient())
		input.NonStream = ev
	}
	ev, source := evidenceForStages(input.NonStream, input.Stream)
	res := JudgeUsage(ev)
	applyEvidenceMetrics(&res, ev, source)
	return res
}

// behaviorStage 复用非流式证据，不额外发起聊天请求。
type behaviorStage struct {
	executor *Executor
}

func (s behaviorStage) Name() string { return StageBehavior }

func (s behaviorStage) Run(ctx context.Context, input *StageContext) StageResult {
	if input.NonStream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		ev, _ := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout), s.executor.httpClient())
		input.NonStream = ev
	}
	// 映射后的上游模型名
	mapped := ""
	if input.Channel.ModelMapping != nil {
		mapped = input.Channel.ModelMapping[input.Run.Model]
	}
	ev, source := evidenceForStages(input.NonStream, input.Stream)
	res := JudgeBehavior(ev, input.Run.Model, mapped)
	applyEvidenceMetrics(&res, ev, source)
	return res
}

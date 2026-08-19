package quality

import (
	"context"
	"time"
)

// chatTimeout 单次聊天探测的请求超时上限（渠道 timeout_total_ms 更小时取渠道值）。
const chatTimeout = 30 * time.Second

// effectiveTimeout 渠道超时与固定上限取较小值。
func effectiveTimeout(ch *Channel, base time.Duration) time.Duration {
	if ch.TimeoutTotalMS > 0 {
		t := time.Duration(ch.TimeoutTotalMS) * time.Millisecond
		if t < base {
			return t
		}
	}
	return base
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
		ev, err := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout))
		input.NonStream = ev
		if err != nil {
			res := JudgeProtocol(ev)
			res.Error = "chat_request_failed"
			res.Details["code"] = "chat_request_failed"
			return res
		}
	}
	res := JudgeProtocol(input.NonStream)
	return res
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
		ev, err := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout))
		input.Stream = ev
		if err != nil {
			res := JudgeStream(ev)
			res.Error = "stream_request_failed"
			res.Details["code"] = "stream_request_failed"
			return res
		}
	}
	return JudgeStream(input.Stream)
}

// usageStage 复用非流式证据（若协议阶段未成功发起，尝试发起）。
type usageStage struct {
	executor *Executor
}

func (s usageStage) Name() string { return StageUsage }

func (s usageStage) Run(ctx context.Context, input *StageContext) StageResult {
	if input.NonStream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		ev, _ := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout))
		input.NonStream = ev
	}
	return JudgeUsage(input.NonStream)
}

// behaviorStage 复用非流式证据，不额外发起聊天请求。
type behaviorStage struct {
	executor *Executor
}

func (s behaviorStage) Name() string { return StageBehavior }

func (s behaviorStage) Run(ctx context.Context, input *StageContext) StageResult {
	if input.NonStream == nil {
		sc := BuildProbeScenario(input.Channel.Capabilities, input.Run.Model, 32)
		ev, _ := RunChat(ctx, input.Channel, sc, effectiveTimeout(input.Channel, chatTimeout))
		input.NonStream = ev
	}
	// 映射后的上游模型名
	mapped := ""
	if input.Channel.ModelMapping != nil {
		mapped = input.Channel.ModelMapping[input.Run.Model]
	}
	return JudgeBehavior(input.NonStream, input.Run.Model, mapped)
}

package quality

import (
	"context"
	"strings"
)

// JudgeUsage usage 阶段判定：
//   - prompt/completion/total 都存在且 total == 两者之和 → passed；
//   - usage 缺失但响应可用 → attention；
//   - 负数或明显不一致 → attention（部分中转站 total 含缓存 token，常见现象）。
// 证据不可用（HTTPStatus=0）→ unknown，不虚构判定。
func JudgeUsage(ev *ChatEvidence) StageResult {
	res := StageResult{Stage: StageUsage, CheckName: "usage_consistency", Details: map[string]interface{}{}}
	if ev == nil || ev.HTTPStatus == 0 {
		res.Status = StatusUnknown
		res.Details["reason"] = "no_chat_evidence"
		return res
	}
	u := ev.Usage
	res.Details["usage_present"] = u.Present
	if u.Present {
		res.PromptTokens = intPtr(u.PromptTokens)
		res.CompletionTokens = intPtr(u.CompletionTokens)
		res.TotalTokens = intPtr(u.TotalTokens)
	}
	switch {
	case !u.Present:
		res.Status = StatusAttention
		res.Details["code"] = "usage_missing"
		res.Error = "usage_missing"
	case u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0:
		res.Status = StatusFailed
		res.Error = "negative_tokens"
	case u.TotalTokens != u.PromptTokens+u.CompletionTokens:
		// 部分中转站 total 含缓存命中/推理 token，与 prompt+completion 不一致
		// 属于常见现象，是启发式信号而非功能故障 → attention 而非 failed
		res.Status = StatusAttention
		res.Error = "usage_inconsistent"
		res.Details["code"] = "usage_inconsistent"
		res.Details["expected_total"] = u.PromptTokens + u.CompletionTokens
	default:
		res.Status = StatusPassed
	}
	return res
}

// JudgeBehavior behavior 阶段判定：
//   - actual_model 与映射后的上游模型一致 → passed；
//   - actual_model 为空 → attention；
//   - 返回模型明显与映射不一致 → attention；
//   - 响应空、choices 缺失或无法解析 → failed；
//   - 身份/知识截止等信号只写入 details.evidence，只能产生 attention。
//
// 行为检测的结果只能是 passed / attention / failed / unknown / skipped，
// 不得直接输出"该站点一定是假模型"等确定性结论。
func JudgeBehavior(ev *ChatEvidence, requestedModel, mappedModel string) StageResult {
	res := StageResult{Stage: StageBehavior, CheckName: "model_behavior", Details: map[string]interface{}{}}
	if ev == nil {
		res.Status = StatusUnknown
		res.Details["reason"] = "no_chat_evidence"
		return res
	}
	res.ActualModel = ev.ActualModel
	res.Details["requested_model"] = requestedModel
	if mappedModel != "" && mappedModel != requestedModel {
		res.Details["mapped_model"] = mappedModel
	}

	// 请求根本没得到响应（超时/连接失败）且无任何文本 → 无法判断，
	// 不能归因为「空响应」（empty_response 暗示上游已响应但内容为空）
	if ev.HTTPStatus == 0 && strings.TrimSpace(ev.Text) == "" {
		res.Status = StatusUnknown
		res.Error = "no_chat_response"
		res.Details["code"] = "no_chat_response"
		return res
	}

	// 结构判定：响应空/不可解析 → failed
	// 注：HTTPStatus==0（请求未获响应）已在上方归为 unknown，此处是
	// HTTP 200 但内容为空 —— 上游确实响应了空内容，是结构异常。
	if strings.TrimSpace(ev.Text) == "" {
		res.Status = StatusFailed
		res.Error = "empty_response"
		res.Details["code"] = "empty_response"
		return res
	}

	// 模型一致性判定
	switch {
	case ev.ActualModel == "":
		res.Status = StatusAttention
		res.Error = "actual_model_missing"
		res.Details["code"] = "actual_model_missing"
	case ev.ActualModel == requestedModel || (mappedModel != "" && ev.ActualModel == mappedModel):
		res.Status = StatusPassed
		res.Details["evidence"] = map[string]interface{}{"matched": true}
	default:
		res.Status = StatusAttention
		res.Error = "model_mismatch"
		res.Details["code"] = "model_mismatch"
		res.Details["evidence"] = map[string]interface{}{
			"actual_model":    ev.ActualModel,
			"requested_model": requestedModel,
		}
	}
	return res
}

// JudgeProtocol protocol 阶段判定：请求/响应协议转换与非流式响应结构。
// 复用非流式证据；请求已按协议构造，响应已成功解析 → passed；
// HTTP 非 200 → failed；解析失败由 RunChat 错误路径体现。
// ttfbTimedOut：请求已发出但首字节超时 → attention 而非 failed——
// 实测存在「非流式挂起、流式完全正常」的上游（如聚合生成型中转站），
// 对这类站点非流式超时是启发式信号，不是功能故障。
func JudgeProtocol(ev *ChatEvidence, ttfbTimedOut bool) StageResult {
	res := StageResult{Stage: StageProtocol, CheckName: "protocol_conversion", Details: map[string]interface{}{}}
	if ev == nil {
		res.Status = StatusUnknown
		res.Details["reason"] = "no_chat_evidence"
		return res
	}
	res.HTTPStatus = intPtr(ev.HTTPStatus)
	res.TTFBMS = intPtr(ev.TTFBMS)
	res.LatencyMS = intPtr(ev.TotalMS)
	switch {
	case ev.HTTPStatus == 0 && ttfbTimedOut:
		res.Status = StatusAttention
		res.Error = "non_stream_first_byte_timeout"
		res.Details["code"] = "non_stream_first_byte_timeout"
	case ev.HTTPStatus == 0:
		// 请求已发出但上游无响应（超时/连接中断）→ 失败而非 unknown；
		// unknown 仅保留给「无证据」（ev == nil）的情况
		res.Status = StatusFailed
		res.Error = "no_http_response"
		res.Details["code"] = "no_http_response"
	case ev.HTTPStatus >= 400:
		res.Status = StatusFailed
		res.Error = classifyHTTPStatus(ev.HTTPStatus)
	case strings.TrimSpace(ev.Text) == "":
		// HTTP 200 但响应结构异常（choices 缺失/内容为空）→ 协议结构检查失败
		res.Status = StatusFailed
		res.Error = "empty_content"
		res.Details["code"] = "empty_content"
	default:
		res.Status = StatusPassed
		res.Details["responded"] = true
	}
	return res
}

// JudgeStream stream 阶段判定：SSE 建立、事件解析、首字节与 [DONE] 完整性。
func JudgeStream(ev *ChatEvidence, ttfbTimedOut bool) StageResult {
	res := StageResult{Stage: StageStream, CheckName: "stream_integrity", Details: map[string]interface{}{}}
	if ev == nil {
		res.Status = StatusUnknown
		res.Details["reason"] = "no_stream_evidence"
		return res
	}
	res.HTTPStatus = intPtr(ev.HTTPStatus)
	res.TTFBMS = intPtr(ev.TTFBMS)
	res.LatencyMS = intPtr(ev.TotalMS)
	res.Details["events_received"] = ev.StreamEvents
	res.Details["done_received"] = ev.DoneReceived
	res.Details["text_length"] = len([]rune(ev.Text))

	switch {
	case ev.HTTPStatus == 0 && ttfbTimedOut:
		res.Status = StatusAttention
		res.Error = "stream_first_byte_timeout"
		res.Details["code"] = "stream_first_byte_timeout"
	case ev.HTTPStatus == 0:
		// 请求已发出但上游无响应（超时/连接中断）→ 失败而非 unknown
		res.Status = StatusFailed
		res.Error = "no_http_response"
		res.Details["code"] = "no_http_response"
	case ev.HTTPStatus >= 400:
		res.Status = StatusFailed
		res.Error = classifyHTTPStatus(ev.HTTPStatus)
	case ev.StreamEvents == 0:
		res.Status = StatusFailed
		res.Error = "no_sse_events"
	case ev.Text == "":
		res.Status = StatusAttention
		res.Error = "empty_stream_content"
		res.Details["code"] = "empty_stream_content"
	case !ev.DoneReceived:
		res.Status = StatusAttention
		res.Error = "done_marker_missing"
		res.Details["code"] = "done_marker_missing"
	default:
		res.Status = StatusPassed
	}
	return res
}

// streamContextKey 标记 RunChat 用于流式证据（阶段注册表用）。
var _ = context.Background

package quality

import (
	"context"
	"strings"
)

// JudgeUsage usage 阶段判定：
//   - prompt/completion/total 都存在且 total == 两者之和 → passed；
//   - usage 缺失但响应可用 → attention；
//   - 负数或明显不一致 → failed。
func JudgeUsage(ev *ChatEvidence) StageResult {
	res := StageResult{Stage: StageUsage, CheckName: "usage_consistency", Details: map[string]interface{}{}}
	if ev == nil {
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
		res.Status = StatusFailed
		res.Error = "usage_inconsistent"
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

	// 结构判定：响应空/不可解析 → failed
	if strings.TrimSpace(ev.Text) == "" && !strings.HasPrefix(ev.Text, "pong") {
		// 探测问题要求精确回答 "pong"；空响应 = 结构异常
		if strings.TrimSpace(ev.Text) == "" {
			res.Status = StatusFailed
			res.Error = "empty_response"
			res.Details["code"] = "empty_response"
			return res
		}
	}
	// 标准测试问题是否获得可解析回答（启发式：包含 pong 或非空）
	if strings.TrimSpace(ev.Text) == "" {
		res.Status = StatusFailed
		res.Error = "empty_response"
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
func JudgeProtocol(ev *ChatEvidence) StageResult {
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
	case ev.HTTPStatus == 0:
		res.Status = StatusUnknown
		res.Error = "no_response"
	case ev.HTTPStatus >= 400:
		res.Status = StatusFailed
		res.Error = classifyHTTPStatus(ev.HTTPStatus)
	case ev.Text == "":
		res.Status = StatusAttention
		res.Error = "empty_content"
		res.Details["code"] = "empty_content"
	default:
		res.Status = StatusPassed
		res.Details["responded"] = true
	}
	return res
}

// JudgeStream stream 阶段判定：SSE 建立、事件解析、首字节与 [DONE] 完整性。
func JudgeStream(ev *ChatEvidence) StageResult {
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
	case ev.HTTPStatus == 0:
		res.Status = StatusUnknown
		res.Error = "no_response"
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

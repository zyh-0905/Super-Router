package quality

import (
	"testing"
)

func TestJudgeUsageConsistentPassed(t *testing.T) {
	ev := &ChatEvidence{HTTPStatus: 200, Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, Present: true}}
	res := JudgeUsage(ev)
	if res.Status != StatusPassed {
		t.Fatalf("status = %s", res.Status)
	}
	if res.PromptTokens == nil || *res.PromptTokens != 10 {
		t.Fatalf("tokens = %+v", res)
	}
}

func TestJudgeUsageMissingAttention(t *testing.T) {
	ev := &ChatEvidence{HTTPStatus: 200, Usage: TokenUsage{Present: false}, Text: "pong"}
	res := JudgeUsage(ev)
	if res.Status != StatusAttention {
		t.Fatalf("status = %s, want attention", res.Status)
	}
	if res.Details["code"] != "usage_missing" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestJudgeUsageNegativeFailed(t *testing.T) {
	ev := &ChatEvidence{HTTPStatus: 200, Usage: TokenUsage{PromptTokens: -1, CompletionTokens: 2, TotalTokens: 1, Present: true}}
	if res := JudgeUsage(ev); res.Status != StatusFailed {
		t.Fatalf("negative: %s", res.Status)
	}
}

func TestJudgeUsageInconsistentAttention(t *testing.T) {
	// 部分中转站 total 含缓存命中/推理 token，与 prompt+completion 不一致
	// 属于常见现象，是启发式信号而非功能故障 → attention 而非 failed
	ev := &ChatEvidence{HTTPStatus: 200, Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 12, Present: true}}
	res := JudgeUsage(ev)
	if res.Status != StatusAttention {
		t.Fatalf("inconsistent: %s", res.Status)
	}
	if res.Details["code"] != "usage_inconsistent" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestJudgeUsageNoEvidenceUnknown(t *testing.T) {
	if res := JudgeUsage(nil); res.Status != StatusUnknown {
		t.Fatalf("nil evidence: %s", res.Status)
	}
}

func TestJudgeBehaviorModelMatchPassed(t *testing.T) {
	ev := &ChatEvidence{ActualModel: "gpt-4o", Text: "pong"}
	res := JudgeBehavior(ev, "gpt-4o", "gpt-4o-up")
	if res.Status != StatusPassed {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestJudgeBehaviorMappedModelAccepted(t *testing.T) {
	// 上游返回映射后的模型名也算一致
	ev := &ChatEvidence{ActualModel: "gpt-4o-up", Text: "pong"}
	res := JudgeBehavior(ev, "gpt-4o", "gpt-4o-up")
	if res.Status != StatusPassed {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestJudgeBehaviorEmptyResponseFailed(t *testing.T) {
	// HTTP 200 但内容为空 → 结构异常，failed
	ev := &ChatEvidence{HTTPStatus: 200, ActualModel: "gpt-4o", Text: ""}
	res := JudgeBehavior(ev, "gpt-4o", "")
	if res.Status != StatusFailed {
		t.Fatalf("empty: %s", res.Status)
	}
	if res.Details["code"] != "empty_response" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestJudgeBehaviorNoHTTPResponseUnknown(t *testing.T) {
	// 请求没得到响应（超时/连接失败）→ 无法判断，不误报为「空响应」
	ev := &ChatEvidence{HTTPStatus: 0, Text: ""}
	res := JudgeBehavior(ev, "gpt-4o", "")
	if res.Status != StatusUnknown {
		t.Fatalf("no response: %s, want unknown", res.Status)
	}
	if res.Details["code"] != "no_chat_response" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestJudgeBehaviorActualModelMissingAttention(t *testing.T) {
	ev := &ChatEvidence{ActualModel: "", Text: "pong"}
	res := JudgeBehavior(ev, "gpt-4o", "")
	if res.Status != StatusAttention {
		t.Fatalf("missing model: %s", res.Status)
	}
}

func TestJudgeBehaviorModelMismatchAttention(t *testing.T) {
	ev := &ChatEvidence{ActualModel: "some-other-model", Text: "pong"}
	res := JudgeBehavior(ev, "gpt-4o", "")
	if res.Status != StatusAttention {
		t.Fatalf("mismatch: %s, want attention (启发式，不判假模型)", res.Status)
	}
	if res.Details["code"] != "model_mismatch" {
		t.Fatalf("details = %+v", res.Details)
	}
	// 身份信号只作为 evidence，不产生确定性结论
	if res.Details["evidence"] == nil {
		t.Fatalf("evidence missing: %+v", res.Details)
	}
}

func TestJudgeBehaviorNoEvidenceUnknown(t *testing.T) {
	if res := JudgeBehavior(nil, "gpt-4o", ""); res.Status != StatusUnknown {
		t.Fatalf("nil evidence: %s", res.Status)
	}
}

func TestJudgeProtocolHTTPStatusMapping(t *testing.T) {
	ok := &ChatEvidence{HTTPStatus: 200, Text: "pong", TTFBMS: 10, TotalMS: 20}
	if res := JudgeProtocol(ok, false); res.Status != StatusPassed {
		t.Fatalf("200: %s", res.Status)
	}
	auth := &ChatEvidence{HTTPStatus: 401}
	res := JudgeProtocol(auth, false)
	if res.Status != StatusFailed || res.Error != "auth_error" {
		t.Fatalf("401: %+v", res)
	}
	rate := &ChatEvidence{HTTPStatus: 429}
	if res := JudgeProtocol(rate, false); res.Status != StatusFailed || res.Error != "rate_limited" {
		t.Fatalf("429: %+v", res)
	}
}

func TestJudgeProtocolTTFBTimeoutAttention(t *testing.T) {
	// 非流式首字节超时 → attention（非功能故障，流式可能正常）
	ev := &ChatEvidence{HTTPStatus: 0, TTFBMS: 60000}
	res := JudgeProtocol(ev, true)
	if res.Status != StatusAttention || res.Details["code"] != "non_stream_first_byte_timeout" {
		t.Fatalf("ttfb timeout: %+v", res)
	}
	// 非超时原因的无响应 → 仍为 failed
	res = JudgeProtocol(ev, false)
	if res.Status != StatusFailed || res.Error != "no_http_response" {
		t.Fatalf("no response: %+v", res)
	}
}

func TestJudgeStreamIntegrity(t *testing.T) {
	good := &ChatEvidence{HTTPStatus: 200, Text: "pong", StreamEvents: 3, DoneReceived: true, TTFBMS: 5}
	res := JudgeStream(good, false)
	if res.Status != StatusPassed {
		t.Fatalf("good stream: %s", res.Status)
	}
	if res.Details["events_received"] != 3 || res.Details["done_received"] != true {
		t.Fatalf("details = %+v", res.Details)
	}

	noDone := &ChatEvidence{HTTPStatus: 200, Text: "pong", StreamEvents: 2, DoneReceived: false}
	res = JudgeStream(noDone, false)
	if res.Status != StatusAttention || res.Details["code"] != "done_marker_missing" {
		t.Fatalf("no done: %+v", res)
	}

	noEvents := &ChatEvidence{HTTPStatus: 200, StreamEvents: 0}
	if res := JudgeStream(noEvents, false); res.Status != StatusFailed {
		t.Fatalf("no events: %s", res.Status)
	}
}

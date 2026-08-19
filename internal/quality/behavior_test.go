package quality

import (
	"testing"
)

func TestJudgeUsageConsistentPassed(t *testing.T) {
	ev := &ChatEvidence{Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, Present: true}}
	res := JudgeUsage(ev)
	if res.Status != StatusPassed {
		t.Fatalf("status = %s", res.Status)
	}
	if res.PromptTokens == nil || *res.PromptTokens != 10 {
		t.Fatalf("tokens = %+v", res)
	}
}

func TestJudgeUsageMissingAttention(t *testing.T) {
	ev := &ChatEvidence{Usage: TokenUsage{Present: false}, Text: "pong"}
	res := JudgeUsage(ev)
	if res.Status != StatusAttention {
		t.Fatalf("status = %s, want attention", res.Status)
	}
	if res.Details["code"] != "usage_missing" {
		t.Fatalf("details = %+v", res.Details)
	}
}

func TestJudgeUsageNegativeFailed(t *testing.T) {
	ev := &ChatEvidence{Usage: TokenUsage{PromptTokens: -1, CompletionTokens: 2, TotalTokens: 1, Present: true}}
	if res := JudgeUsage(ev); res.Status != StatusFailed {
		t.Fatalf("negative: %s", res.Status)
	}
}

func TestJudgeUsageInconsistentFailed(t *testing.T) {
	ev := &ChatEvidence{Usage: TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 12, Present: true}}
	if res := JudgeUsage(ev); res.Status != StatusFailed {
		t.Fatalf("inconsistent: %s", res.Status)
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
	ev := &ChatEvidence{ActualModel: "gpt-4o", Text: ""}
	res := JudgeBehavior(ev, "gpt-4o", "")
	if res.Status != StatusFailed {
		t.Fatalf("empty: %s", res.Status)
	}
	if res.Details["code"] != "empty_response" {
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
	if res := JudgeProtocol(ok); res.Status != StatusPassed {
		t.Fatalf("200: %s", res.Status)
	}
	auth := &ChatEvidence{HTTPStatus: 401}
	res := JudgeProtocol(auth)
	if res.Status != StatusFailed || res.Error != "auth_error" {
		t.Fatalf("401: %+v", res)
	}
	rate := &ChatEvidence{HTTPStatus: 429}
	if res := JudgeProtocol(rate); res.Status != StatusFailed || res.Error != "rate_limited" {
		t.Fatalf("429: %+v", res)
	}
}

func TestJudgeStreamIntegrity(t *testing.T) {
	good := &ChatEvidence{HTTPStatus: 200, Text: "pong", StreamEvents: 3, DoneReceived: true, TTFBMS: 5}
	res := JudgeStream(good)
	if res.Status != StatusPassed {
		t.Fatalf("good stream: %s", res.Status)
	}
	if res.Details["events_received"] != 3 || res.Details["done_received"] != true {
		t.Fatalf("details = %+v", res.Details)
	}

	noDone := &ChatEvidence{HTTPStatus: 200, Text: "pong", StreamEvents: 2, DoneReceived: false}
	res = JudgeStream(noDone)
	if res.Status != StatusAttention || res.Details["code"] != "done_marker_missing" {
		t.Fatalf("no done: %+v", res)
	}

	noEvents := &ChatEvidence{HTTPStatus: 200, StreamEvents: 0}
	if res := JudgeStream(noEvents); res.Status != StatusFailed {
		t.Fatalf("no events: %s", res.Status)
	}
}

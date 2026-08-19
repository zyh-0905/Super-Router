package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestClassifyErrorUpstreamStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusUnauthorized, "auth_error"},
		{http.StatusForbidden, "auth_error"},
		{http.StatusInternalServerError, "upstream_error"},
		{http.StatusBadGateway, "upstream_error"},
		{http.StatusBadRequest, "bad_request"},
	}
	for _, tc := range cases {
		err := &upstreamError{StatusCode: tc.status}
		if got := classifyError(err); got != tc.want {
			t.Errorf("classifyError(status=%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestClassifyErrorContext(t *testing.T) {
	if got := classifyError(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("classifyError(deadline) = %q, want timeout", got)
	}
	if got := classifyError(context.Canceled); got != "client_canceled" {
		t.Errorf("classifyError(canceled) = %q, want client_canceled", got)
	}
	if got := classifyError(fmt.Errorf("connection refused")); got != "network_error" {
		t.Errorf("classifyError(network) = %q, want network_error", got)
	}
}

func TestIsRetryable(t *testing.T) {
	ctx := context.Background()
	retryable := []error{
		&upstreamError{StatusCode: http.StatusTooManyRequests},
		&upstreamError{StatusCode: http.StatusRequestTimeout},
		&upstreamError{StatusCode: http.StatusInternalServerError},
		&upstreamError{StatusCode: http.StatusBadGateway},
		&upstreamError{Err: fmt.Errorf("dial tcp: connection refused")}, // 无 HTTP 响应：按网络错误处理
		fmt.Errorf("dial tcp: connection refused"),
		context.DeadlineExceeded, // 调用方仍存活：尝试级超时可换渠道
	}
	for _, err := range retryable {
		if !isRetryable(err, ctx) {
			t.Errorf("isRetryable(%v) = false, want true", err)
		}
	}

	nonRetryable := []error{
		&upstreamError{StatusCode: http.StatusUnauthorized},
		&upstreamError{StatusCode: http.StatusBadRequest},
		&upstreamError{StatusCode: http.StatusNotFound},
		context.Canceled,
	}
	for _, err := range nonRetryable {
		if isRetryable(err, ctx) {
			t.Errorf("isRetryable(%v) = true, want false", err)
		}
	}

	// 调用方已超时/断开：deadline 错误不可重试
	deadCtx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	<-deadCtx.Done()
	if isRetryable(context.DeadlineExceeded, deadCtx) {
		t.Error("deadline exceeded with dead caller context must not be retryable")
	}
}

func TestClassifyErrorNetworkWrappedUpstream(t *testing.T) {
	// 上游连接被拒：StatusCode 为 0，应归类为网络错误
	err := &upstreamError{Err: fmt.Errorf("http request: %w", fmt.Errorf("connection refused"))}
	if got := classifyError(err); got != "network_error" {
		t.Errorf("classifyError(wrapped network) = %q, want network_error", got)
	}
	if !isRetryable(err, context.Background()) {
		t.Error("isRetryable(wrapped network) = false, want true")
	}
}

func TestUpstreamErrorWrapsUnderlyingCause(t *testing.T) {
	cause := fmt.Errorf("dial timeout")
	err := &upstreamError{Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("upstreamError must wrap its underlying error")
	}
	if err.Error() != "dial timeout" {
		t.Fatalf("unexpected error string: %q", err.Error())
	}
}

// P1-02：TTFB sentinel 必须归类为 timeout 且允许切换候选
func TestTTFBTimeoutClassifyAndRetryable(t *testing.T) {
	live := context.Background()
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	ttfbErr := fmt.Errorf("%w: no first byte within 1000ms", errTTFBTimeout)
	if got := classifyError(ttfbErr); got != "timeout" {
		t.Fatalf("classifyError(ttfbTimeout) = %q, want timeout", got)
	}
	if !isRetryable(ttfbErr, live) {
		t.Fatal("TTFB timeout with live caller must be retryable (switch to next candidate)")
	}
	if isRetryable(ttfbErr, dead) {
		t.Fatal("TTFB timeout with dead caller must not be retryable")
	}
}

// attemptTTFBBudget：单次尝试不得耗尽剩余总预算，为后续候选保留切换份额
func TestAttemptTTFBBudget(t *testing.T) {
	// 剩余充足：maxTTFT(5000)+connect(5000)=10000 < 15000 → 10000
	if got := attemptTTFBBudget(15000, 5000, 5000); got != 10000 {
		t.Fatalf("budget = %d, want 10000", got)
	}
	// 剩余较少：封顶于剩余总预算
	if got := attemptTTFBBudget(8000, 5000, 5000); got != 8000 {
		t.Fatalf("budget = %d, want 8000", got)
	}
	// 剩余不足 1s：保底 1000ms
	if got := attemptTTFBBudget(500, 5000, 5000); got != 1000 {
		t.Fatalf("budget = %d, want 1000", got)
	}
	// 渠道/策略未配置：使用默认 5000
	if got := attemptTTFBBudget(15000, 0, 0); got != 10000 {
		t.Fatalf("budget = %d, want 10000 (defaults)", got)
	}
	// 策略放宽 max_ttft_ms=20000：等于剩余总预算
	if got := attemptTTFBBudget(15000, 20000, 0); got != 15000 {
		t.Fatalf("budget = %d, want 15000", got)
	}
	// 策略收紧 max_ttft_ms=2000：5000+2000... connect 默认 5000 → 7000
	if got := attemptTTFBBudget(15000, 2000, 0); got != 7000 {
		t.Fatalf("budget = %d, want 7000", got)
	}
}

// headerSafe：中文等非 ASCII 头值必须 URI 编码（浏览器按 Latin-1 读头会乱码），ASCII 原样
func TestHeaderSafe(t *testing.T) {
	if got := headerSafe("mock-site"); got != "mock-site" {
		t.Fatalf("ascii passthrough: %q", got)
	}
	if got := headerSafe(""); got != "" {
		t.Fatalf("empty passthrough: %q", got)
	}
	enc := headerSafe("默认分组")
	if enc == "默认分组" {
		t.Fatal("non-ascii header must be percent-encoded")
	}
	for _, r := range enc {
		if r > 126 {
			t.Fatalf("encoded value must be pure ascii, got %q", enc)
		}
	}
	if _, err := url.PathUnescape(enc); err != nil {
		t.Fatalf("encoded value must be unescape-able: %v", err)
	}
}

// P1-02：计时器触发的取消错误被替换为 sentinel 的条件
func TestTTFBSentinelDistinctFromClientCancel(t *testing.T) {
	plain := fmt.Errorf("http request: %w", context.Canceled)
	if !errors.Is(plain, context.Canceled) {
		t.Fatal("wrapped canceled must be detectable")
	}
	if classifyError(plain) != "client_canceled" {
		t.Fatalf("plain cancel should classify as client_canceled, got %q", classifyError(plain))
	}
	if isRetryable(plain, context.Background()) {
		t.Fatal("plain cancel must not be retryable")
	}
}

// P1-01：多模态 content 数组与未知字段必须能解析且原样保留
func TestRequestFidelityPreservesUnknownFields(t *testing.T) {
	body := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}, {"type": "image_url", "image_url": {"url": "data:image/png;base64,AA=="}}]}],
		"stream": true,
		"top_p": 0.9,
		"stop": ["END"],
		"response_format": {"type": "json_object"},
		"tool_choice": "auto",
		"stream_options": {"include_usage": true},
		"max_completion_tokens": 2048,
		"group": "高优组"
	}`)

	var req ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("multimodal/unknown fields must parse: %v", err)
	}
	if req.Model != "gpt-4o" || !req.Stream || req.Group != "高优组" {
		t.Fatalf("minimal view parse mismatch: %+v", req)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages = %d", len(req.Messages))
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	upstream := cloneMap(raw)
	delete(upstream, "group")
	if _, ok := upstream["top_p"]; !ok {
		t.Fatal("top_p must be preserved for upstream forwarding")
	}
	if _, ok := upstream["stop"]; !ok {
		t.Fatal("stop must be preserved")
	}
	if _, ok := upstream["response_format"]; !ok {
		t.Fatal("response_format must be preserved")
	}
	if _, ok := upstream["group"]; ok {
		t.Fatal("gateway extension field group must be removed")
	}
	out, err := json.Marshal(upstream)
	if err != nil {
		t.Fatal(err)
	}
	var check map[string]interface{}
	if err := json.Unmarshal(out, &check); err != nil {
		t.Fatal(err)
	}
	if _, ok := check["max_completion_tokens"]; !ok {
		t.Fatal("max_completion_tokens must survive marshal roundtrip")
	}
}

// 输入 token 估算：此前固定为 len(messages)*100，与内容长度无关，
// 使成本估算与 max_price_cap 过滤形同虚设。
func TestEstimateInputTokensScalesWithContent(t *testing.T) {
	parse := func(body string) *ChatCompletionRequest {
		t.Helper()
		var req ChatCompletionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		return &req
	}

	short := parse(`{"messages":[{"role":"user","content":"hi"}]}`)
	long := parse(`{"messages":[{"role":"user","content":"` + strings.Repeat("word ", 400) + `"}]}`)

	if estimateInputTokens(long) <= estimateInputTokens(short) {
		t.Fatalf("long prompt must estimate higher: short=%d long=%d",
			estimateInputTokens(short), estimateInputTokens(long))
	}

	// 2000 个 ASCII 字符 ≈ 500 token，允许量级范围内的偏差
	if got := estimateInputTokens(long); got < 300 || got > 900 {
		t.Fatalf("2000 ASCII chars estimated %d tokens, want roughly 500", got)
	}

	// 非 ASCII 密度更高：同字符数的中文应比英文估得多
	cn := parse(`{"messages":[{"role":"user","content":"` + strings.Repeat("中", 100) + `"}]}`)
	en := parse(`{"messages":[{"role":"user","content":"` + strings.Repeat("a", 100) + `"}]}`)
	if estimateInputTokens(cn) <= estimateInputTokens(en) {
		t.Fatalf("CJK must estimate higher than ASCII: cn=%d en=%d",
			estimateInputTokens(cn), estimateInputTokens(en))
	}

	// 空消息也至少记 1 token，避免成本恒为 0
	if got := estimateInputTokens(parse(`{"messages":[]}`)); got < 1 {
		t.Fatalf("empty request estimated %d, want >= 1", got)
	}
}

// 多模态内容块必须计入估算（文本块按文本、图片块按等效常量）
func TestEstimateInputTokensMultimodal(t *testing.T) {
	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"describe"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}
	]}]}`), &req); err != nil {
		t.Fatal(err)
	}
	// 图片块应显著抬高估算，不能像纯文本一样只有几个 token
	if got := estimateInputTokens(&req); got < mediaPartTokens {
		t.Fatalf("multimodal estimate = %d, want >= %d", got, mediaPartTokens)
	}
}


package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// openaiNonStreamResponse 标准 OpenAI 非流式响应。
const openaiNonStreamResponse = `{
  "id":"chatcmpl-test",
  "object":"chat.completion",
  "model":"gpt-4o",
  "choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],
  "usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
}`

// anthropicNonStreamResponse Anthropic 原生响应（经 AnthropicToOpenAI 转换）。
const anthropicNonStreamResponse = `{
  "id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-5",
  "content":[{"type":"text","text":"pong"}],
  "stop_reason":"end_turn",
  "usage":{"input_tokens":10,"output_tokens":2}
}`

func TestNonStreamOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, openaiNonStreamResponse)
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	ev, err := RunChat(ctx(), ch, BuildProbeScenario(nil, "gpt-4o", 32), 10*time.Second)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if ev.ActualModel != "gpt-4o" || ev.Text != "pong" {
		t.Fatalf("evidence = %+v", ev)
	}
	if !ev.Usage.Present || ev.Usage.PromptTokens != 10 || ev.Usage.CompletionTokens != 2 || ev.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", ev.Usage)
	}
	if ev.HTTPStatus != 200 {
		t.Fatalf("http status = %d", ev.HTTPStatus)
	}
}

func TestNonStreamAnthropicConversion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "sk-ant-test" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		// 校验请求已转换为 Anthropic 格式
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["messages"]; !ok {
			t.Fatalf("anthropic body missing messages: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, anthropicNonStreamResponse)
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "anthropic", APIKey: "sk-ant-test"}
	ev, err := RunChat(ctx(), ch, BuildProbeScenario(nil, "claude-sonnet-5", 32), 10*time.Second)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if ev.ActualModel != "claude-sonnet-5" || ev.Text != "pong" {
		t.Fatalf("evidence = %+v", ev)
	}
	if !ev.Usage.Present || ev.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", ev.Usage)
	}
}

func TestStreamOpenAIFullSequence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":"po"}}]}` + "\n\n",
			`data: {"id":"c2","model":"gpt-4o","choices":[{"delta":{"content":"ng"}}]}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, c := range chunks {
			fmt.Fprint(w, c)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	sc := BuildProbeScenario(nil, "gpt-4o", 32)
	sc.Stream = true
	ev, err := RunChat(ctx(), ch, sc, 10*time.Second)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ev.Text != "pong" {
		t.Fatalf("text = %q", ev.Text)
	}
	if !ev.DoneReceived || ev.StreamEvents != 3 {
		t.Fatalf("done=%v events=%d", ev.DoneReceived, ev.StreamEvents)
	}
}

func TestStreamAnthropicConversion(t *testing.T) {
	events := []string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"id":"m1","model":"claude-sonnet-5","usage":{"input_tokens":10}}}` + "\n\n",
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n",
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}` + "\n\n",
		`event: content_block_stop` + "\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n",
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n",
		`event: message_stop` + "\n" + `data: {"type":"message_stop"}` + "\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprint(w, e)
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "anthropic", APIKey: "sk-ant-test"}
	sc := BuildProbeScenario(nil, "claude-sonnet-5", 32)
	sc.Stream = true
	ev, err := RunChat(ctx(), ch, sc, 10*time.Second)
	if err != nil {
		t.Fatalf("anthropic stream: %v", err)
	}
	if ev.Text != "pong" || ev.StreamEvents < 3 {
		t.Fatalf("evidence = %+v", ev)
	}
}

func TestStreamMissingDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":"x"}}]}`+"\n\n")
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	sc := BuildProbeScenario(nil, "gpt-4o", 32)
	sc.Stream = true
	ev, err := RunChat(ctx(), ch, sc, 10*time.Second)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ev.DoneReceived {
		t.Fatal("done must be false when [DONE] missing")
	}
	if ev.Text != "x" {
		t.Fatalf("text = %q", ev.Text)
	}
}

func TestStreamInvalidJSONMidway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {invalid json}\n\n")
		fmt.Fprint(w, `data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	sc := BuildProbeScenario(nil, "gpt-4o", 32)
	sc.Stream = true
	ev, err := RunChat(ctx(), ch, sc, 10*time.Second)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if ev.Text != "ok" || !ev.DoneReceived {
		t.Fatalf("invalid JSON must be skipped: %+v", ev)
	}
}

func TestChatFirstByteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := RunChat(ctx, ch, BuildProbeScenario(nil, "gpt-4o", 32), 10*time.Second); err == nil {
		t.Fatal("expected first byte timeout error")
	}
}

func TestChatUsageMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c","model":"gpt-4o","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	ev, err := RunChat(ctx(), ch, BuildProbeScenario(nil, "gpt-4o", 32), 10*time.Second)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if ev.Usage.Present {
		t.Fatal("usage must be absent when upstream omits it")
	}
}

func TestChatCredentialNotInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"invalid api key sk-test"}}`)
	}))
	t.Cleanup(srv.Close)

	ch := &Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	_, err := RunChat(ctx(), ch, BuildProbeScenario(nil, "gpt-4o", 32), 10*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Fatalf("credential leaked into error: %v", err)
	}
}

func ctx() context.Context {
	return context.Background()
}

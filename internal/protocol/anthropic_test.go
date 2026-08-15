package protocol

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestOpenAIToAnthropicBasic(t *testing.T) {
	in := map[string]interface{}{
		"model": "claude-sonnet-5",
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": "你是助手"},
			map[string]interface{}{"role": "user", "content": "你好"},
		},
		"max_tokens": 256.0,
	}
	out := OpenAIToAnthropic(in)
	if out["model"] != "claude-sonnet-5" {
		t.Fatalf("model = %v", out["model"])
	}
	if out["system"] != "你是助手" {
		t.Fatalf("system = %v", out["system"])
	}
	if out["max_tokens"] != 256 {
		t.Fatalf("max_tokens = %v", out["max_tokens"])
	}
	msgs, _ := out["messages"].([]map[string]interface{})
	if len(msgs) != 1 || msgs[0]["content"] != "你好" {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestOpenAIToAnthropicDefaultMaxTokens(t *testing.T) {
	out := OpenAIToAnthropic(map[string]interface{}{"model": "m"})
	if out["max_tokens"] != 1024 {
		t.Fatalf("default max_tokens = %v, want 1024", out["max_tokens"])
	}
}

func TestAnthropicToOpenAIResponse(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5",
		"content":[{"type":"text","text":"你好"},{"type":"text","text":"世界"}],
		"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":4}}`)
	out, err := AnthropicToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	_ = json.Unmarshal(out, &obj)
	if obj["object"] != "chat.completion" {
		t.Fatalf("object = %v", obj["object"])
	}
	choices := obj["choices"].([]interface{})
	c0 := choices[0].(map[string]interface{})
	msg := c0["message"].(map[string]interface{})
	if msg["content"] != "你好世界" {
		t.Fatalf("content = %v", msg["content"])
	}
	if c0["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", c0["finish_reason"])
	}
	usage := obj["usage"].(map[string]interface{})
	if usage["total_tokens"].(float64) != 14 {
		t.Fatalf("total_tokens = %v", usage["total_tokens"])
	}
}

func TestAnthropicErrorParse(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	status, msg, ok := AnthropicError(body)
	if !ok || status != 500 || msg != "Overloaded" {
		t.Fatalf("got status=%d msg=%q ok=%v", status, msg, ok)
	}
	if _, _, ok := AnthropicError([]byte(`{"type":"message"}`)); ok {
		t.Fatal("non-error body must not be parsed as error")
	}
}

func TestStreamTransformer(t *testing.T) {
	// anthropic SSE 流
	stream := "" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_9\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"你\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"好\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	tr := NewAnthropicStreamTransformer(io.NopCloser(strings.NewReader(stream)), "claude")
	out, err := io.ReadAll(tr)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"role":"assistant"`) {
		t.Fatalf("missing role chunk: %s", s)
	}
	if !strings.Contains(s, "你") || !strings.Contains(s, "好") {
		t.Fatalf("missing text deltas: %s", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("missing DONE: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"stop"`) {
		t.Fatalf("missing finish_reason: %s", s)
	}
}

func TestEndpoints(t *testing.T) {
	if ChatEndpoint("https://x.com/", ProtocolAnthropic) != "https://x.com/v1/messages" {
		t.Fatal("anthropic chat endpoint wrong")
	}
	if ChatEndpoint("https://x.com", ProtocolOpenAI) != "https://x.com/v1/chat/completions" {
		t.Fatal("openai chat endpoint wrong")
	}
	if ModelsEndpoint("https://x.com/") != "https://x.com/v1/models" {
		t.Fatal("models endpoint wrong")
	}
}

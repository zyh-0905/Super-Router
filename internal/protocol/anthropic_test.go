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

// 多模态内容块此前被断言为 string 而静默丢成空字符串，这里覆盖文本 + 图片两种块。
func TestOpenAIToAnthropicMultimodalContent(t *testing.T) {
	in := map[string]interface{}{
		"model": "claude-sonnet-5",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "这是什么"},
					map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{
						"url": "data:image/png;base64,iVBORw0KGgo=",
					}},
					map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{
						"url": "https://example.com/a.jpg",
					}},
				},
			},
		},
	}
	out := OpenAIToAnthropic(in)
	msgs, ok := out["messages"].([]map[string]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %#v", out["messages"])
	}
	blocks, ok := msgs[0]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 3 {
		t.Fatalf("content blocks = %#v", msgs[0]["content"])
	}

	if blocks[0]["type"] != "text" || blocks[0]["text"] != "这是什么" {
		t.Fatalf("text block = %#v", blocks[0])
	}

	src0 := blocks[1]["source"].(map[string]interface{})
	if src0["type"] != "base64" || src0["media_type"] != "image/png" || src0["data"] != "iVBORw0KGgo=" {
		t.Fatalf("base64 image block = %#v", src0)
	}

	src1 := blocks[2]["source"].(map[string]interface{})
	if src1["type"] != "url" || src1["url"] != "https://example.com/a.jpg" {
		t.Fatalf("url image block = %#v", src1)
	}
}

// system 为内容块数组时应压平为纯文本（Anthropic 的 system 是顶层字符串字段）
func TestOpenAIToAnthropicSystemBlocks(t *testing.T) {
	out := OpenAIToAnthropic(map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "system", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "你是"},
				map[string]interface{}{"type": "text", "text": "助手"},
			}},
			map[string]interface{}{"role": "user", "content": "hi"},
		},
	})
	if out["system"] != "你是助手" {
		t.Fatalf("system = %v", out["system"])
	}
}

// assistant.tool_calls → tool_use 块；tool 角色 → user + tool_result 块
func TestOpenAIToAnthropicToolRoundTrip(t *testing.T) {
	out := OpenAIToAnthropic(map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "北京天气"},
			map[string]interface{}{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []interface{}{
					map[string]interface{}{
						"id": "call_1", "type": "function",
						"function": map[string]interface{}{
							"name": "get_weather", "arguments": `{"city":"beijing"}`,
						},
					},
				},
			},
			map[string]interface{}{"role": "tool", "tool_call_id": "call_1", "content": "晴 25C"},
		},
	})
	msgs := out["messages"].([]map[string]interface{})
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %#v", len(msgs), msgs)
	}

	useBlocks := msgs[1]["content"].([]map[string]interface{})
	if len(useBlocks) != 1 || useBlocks[0]["type"] != "tool_use" || useBlocks[0]["name"] != "get_weather" {
		t.Fatalf("tool_use block = %#v", useBlocks)
	}
	input := useBlocks[0]["input"].(map[string]interface{})
	if input["city"] != "beijing" {
		t.Fatalf("tool_use input = %#v", input)
	}

	if msgs[2]["role"] != "user" {
		t.Fatalf("tool result role = %v, want user", msgs[2]["role"])
	}
	resBlocks := msgs[2]["content"].([]map[string]interface{})
	if resBlocks[0]["type"] != "tool_result" || resBlocks[0]["tool_use_id"] != "call_1" {
		t.Fatalf("tool_result block = %#v", resBlocks[0])
	}
}

// 响应方向：tool_use 块必须还原为 OpenAI tool_calls，而不是被丢弃
func TestAnthropicToOpenAIToolUse(t *testing.T) {
	body := []byte(`{"id":"msg_2","model":"claude-sonnet-5",
		"content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"beijing"}}],
		"stop_reason":"tool_use","usage":{"input_tokens":5,"output_tokens":9}}`)
	out, err := AnthropicToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	_ = json.Unmarshal(out, &obj)
	c0 := obj["choices"].([]interface{})[0].(map[string]interface{})
	if c0["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]interface{})
	if msg["content"] != nil {
		t.Fatalf("content should be null when only tool_use, got %#v", msg["content"])
	}
	calls := msg["tool_calls"].([]interface{})
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %#v", calls)
	}
	fn := calls[0].(map[string]interface{})["function"].(map[string]interface{})
	if fn["name"] != "get_weather" || !strings.Contains(fn["arguments"].(string), "beijing") {
		t.Fatalf("function = %#v", fn)
	}
}

// 流式 tool_use：首帧带函数名，input_json_delta 增量拼接 arguments
func TestStreamTransformerToolUse(t *testing.T) {
	stream := "" +
		"event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_9\",\"model\":\"claude\"}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"beijing\\\"}\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n"

	out, err := io.ReadAll(NewAnthropicStreamTransformer(io.NopCloser(strings.NewReader(stream)), "claude"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("missing tool name frame: %s", s)
	}
	// 首个工具调用的下标必须是 0
	if !strings.Contains(s, `"index":0`) {
		t.Fatalf("first tool_call index must be 0: %s", s)
	}
	if !strings.Contains(s, `beijing`) {
		t.Fatalf("missing argument delta: %s", s)
	}
	if !strings.Contains(s, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing tool_calls finish_reason: %s", s)
	}
}

// 纯文本路径不受多模态改动影响（回归保护）
func TestOpenAIToAnthropicPlainTextUnchanged(t *testing.T) {
	out := OpenAIToAnthropic(map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "你好"},
		},
	})
	msgs := out["messages"].([]map[string]interface{})
	if len(msgs) != 1 || msgs[0]["content"] != "你好" {
		t.Fatalf("plain text content = %#v", msgs[0]["content"])
	}
}


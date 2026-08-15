// Package protocol 提供 OpenAI ↔ Anthropic 的协议转换，使网关对外保持统一的
// OpenAI 兼容接口，而对 Anthropic 协议站点透明地做请求/响应/流式格式转换。
package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

// IsAnthropic 判断协议是否为 anthropic
func IsAnthropic(p string) bool { return p == ProtocolAnthropic }

// ValidProtocol 校验协议取值
func ValidProtocol(p string) bool {
	return p == "" || p == ProtocolOpenAI || p == ProtocolAnthropic
}

// ChatEndpoint 聊天补全端点
func ChatEndpoint(baseURL, proto string) string {
	base := strings.TrimRight(baseURL, "/")
	if IsAnthropic(proto) {
		return base + "/v1/messages"
	}
	return base + "/v1/chat/completions"
}

// ModelsEndpoint 模型列表端点（两种协议相同）
func ModelsEndpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/v1/models"
}

// AnthropicHeaders anthropic 认证头
func AnthropicHeaders(apiKey string) map[string]string {
	return map[string]string{
		"content-type":      "application/json",
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	}
}

// OpenAIToAnthropic OpenAI 请求 → Anthropic 请求
func OpenAIToAnthropic(openaiReq map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	if v, ok := openaiReq["model"]; ok {
		out["model"] = v
	}
	maxTokens := 1024
	if v, ok := openaiReq["max_tokens"].(float64); ok && v > 0 {
		maxTokens = int(v)
	}
	out["max_tokens"] = maxTokens
	if v, ok := openaiReq["temperature"].(float64); ok {
		out["temperature"] = v
	}

	messages := []map[string]interface{}{}
	if msgs, ok := openaiReq["messages"].([]interface{}); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			content, _ := mm["content"].(string)
			if role == "system" {
				out["system"] = content
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			messages = append(messages, map[string]interface{}{"role": role, "content": content})
		}
	}
	out["messages"] = messages

	if tools, ok := openaiReq["tools"].([]interface{}); ok {
		at := []map[string]interface{}{}
		for _, t := range tools {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			f, ok := tm["function"].(map[string]interface{})
			if !ok {
				continue
			}
			tool := map[string]interface{}{}
			if n, ok := f["name"]; ok {
				tool["name"] = n
			}
			if d, ok := f["description"]; ok {
				tool["description"] = d
			}
			if p, ok := f["parameters"]; ok {
				tool["input_schema"] = p
			}
			at = append(at, tool)
		}
		if len(at) > 0 {
			out["tools"] = at
		}
	}
	if v, ok := openaiReq["stream"].(bool); ok && v {
		out["stream"] = true
	}
	return out
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicResponse struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Model   string                  `json:"model"`
	Content []anthropicContentBlock `json:"content"`
	StopReason string               `json:"stop_reason"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// AnthropicError 解析 anthropic 错误响应，返回可映射的状态码与消息
func AnthropicError(body []byte) (int, string, bool) {
	var raw struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, "", false
	}
	if raw.Type != "error" {
		return 0, "", false
	}
	msg := raw.Error.Message
	if msg == "" {
		msg = raw.Error.Type
	}
	status := 500
	switch raw.Error.Type {
	case "invalid_request_error":
		status = 400
	case "authentication_error", "permission_error":
		status = 401
	case "not_found_error":
		status = 404
	case "rate_limit_error":
		status = 429
	case "api_error", "overloaded_error":
		status = 500
	}
	return status, msg, true
}

// AnthropicToOpenAI anthropic 非流式响应 → OpenAI ChatCompletion JSON
func AnthropicToOpenAI(body []byte) ([]byte, error) {
	var ar anthropicResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return nil, fmt.Errorf("解析 anthropic 响应失败: %w", err)
	}
	var sb strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	finish := map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length"}[ar.StopReason]
	if finish == "" {
		finish = "stop"
	}
	out := map[string]interface{}{
		"id":      ar.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   ar.Model,
		"choices": []map[string]interface{}{{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": sb.String(),
			},
			"finish_reason": finish,
		}},
		"usage": map[string]interface{}{
			"prompt_tokens":     ar.Usage.InputTokens,
			"completion_tokens": ar.Usage.OutputTokens,
			"total_tokens":      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}
	return json.Marshal(out)
}

// ===== 流式转换：anthropic SSE → OpenAI chat.completion.chunk SSE =====

// NewAnthropicStreamTransformer 包装 anthropic 流式响应体，
// 输出 OpenAI 格式的 SSE 字节流。
func NewAnthropicStreamTransformer(src io.ReadCloser, model string) io.ReadCloser {
	return &streamTransformer{
		src:     src,
		reader:  bufio.NewReader(src),
		model:   model,
		id:      fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		created: time.Now().Unix(),
	}
}

type streamTransformer struct {
	src     io.ReadCloser
	reader  *bufio.Reader
	model   string
	id      string
	created int64

	out     []byte
	started bool
	stopped bool
	err     error
}

func (t *streamTransformer) Read(p []byte) (int, error) {
	for len(t.out) == 0 {
		if t.stopped || t.err != nil {
			err := t.err
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		if err := t.nextEvent(); err != nil {
			if err == io.EOF {
				t.stopped = true
				continue
			}
			t.err = err
			return 0, err
		}
	}
	n := copy(p, t.out)
	t.out = t.out[n:]
	return n, nil
}

func (t *streamTransformer) Close() error { return t.src.Close() }

// nextEvent 读取一个 anthropic SSE 事件并生成对应的 OpenAI chunk（写入 t.out）
func (t *streamTransformer) nextEvent() error {
	event := ""
	var dataLines []string
	for {
		line, err := t.reader.ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			// 事件结束
			if len(dataLines) == 0 {
				return nil
			}
			return t.handleEvent(event, strings.Join(dataLines, "\n"))
		}
		if err != nil {
			if len(dataLines) > 0 {
				return t.handleEvent(event, strings.Join(dataLines, "\n"))
			}
			return err
		}
	}
}

func (t *streamTransformer) handleEvent(event, data string) error {
	if event == "error" || strings.HasPrefix(data, `{"type":"error"`) {
		status, msg, _ := AnthropicError([]byte(data))
		t.err = fmt.Errorf("anthropic stream error: %s (status %d)", msg, status)
		return t.err
	}

	chunk := func(delta map[string]interface{}, finish *string) error {
		if !t.started {
			t.started = true
		}
		obj := map[string]interface{}{
			"id":      t.id,
			"object":  "chat.completion.chunk",
			"created": t.created,
			"model":   t.model,
			"choices": []map[string]interface{}{{
				"index":         0,
				"delta":         delta,
				"finish_reason": finish,
			}},
		}
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		t.out = append(t.out, []byte("data: "+string(b)+"\n\n")...)
		return nil
	}

	switch event {
	case "message_start":
		var d struct {
			Message struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		if d.Message.ID != "" {
			t.id = d.Message.ID
		}
		return chunk(map[string]interface{}{"role": "assistant", "content": ""}, nil)
	case "content_block_delta":
		var d struct {
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil
		}
		if d.Delta.Type != "text_delta" || d.Delta.Text == "" {
			return nil
		}
		return chunk(map[string]interface{}{"content": d.Delta.Text}, nil)
	case "message_delta":
		var d struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		finish := map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length"}[d.Delta.StopReason]
		if finish == "" {
			finish = "stop"
		}
		if err := chunk(map[string]interface{}{}, &finish); err != nil {
			return err
		}
		t.out = append(t.out, []byte("data: [DONE]\n\n")...)
		t.stopped = true
		return nil
	case "message_stop":
		if !t.stopped {
			t.out = append(t.out, []byte("data: [DONE]\n\n")...)
			t.stopped = true
		}
		return nil
	default:
		return nil
	}
}

// IsProbablySSE 判断响应体是否为 SSE 流（兜底用）
func IsProbablySSE(header string) bool {
	return strings.Contains(header, "text/event-stream")
}

var _ io.ReadCloser = (*streamTransformer)(nil)

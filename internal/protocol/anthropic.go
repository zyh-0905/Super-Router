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

			if role == "system" {
				// Anthropic 的 system 是顶层字段，只接受文本
				out["system"] = contentToText(mm["content"])
				continue
			}

			// OpenAI 的 tool 角色 → Anthropic 的 user + tool_result 块
			if role == "tool" {
				id, _ := mm["tool_call_id"].(string)
				messages = append(messages, map[string]interface{}{
					"role": "user",
					"content": []map[string]interface{}{{
						"type":        "tool_result",
						"tool_use_id": id,
						"content":     contentToText(mm["content"]),
					}},
				})
				continue
			}

			if role != "user" && role != "assistant" {
				continue
			}

			// 内容块：文本与图片原样转换，不再退化为空字符串
			content := convertContentToAnthropic(mm["content"])

			// assistant 的 tool_calls → tool_use 块，与文本块并列
			if calls, ok := mm["tool_calls"].([]interface{}); ok && len(calls) > 0 {
				blocks := toBlockSlice(content)
				for _, c := range calls {
					if b := toolCallToAnthropic(c); b != nil {
						blocks = append(blocks, b)
					}
				}
				content = blocks
			}

			if isEmptyContent(content) {
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

// convertContentToAnthropic 转换 OpenAI 的 content 字段。
// 字符串原样返回；内容块数组（多模态）逐块转换为 Anthropic 块。
// 此前该字段被断言为 string，数组内容会静默变成空字符串，导致多模态请求丢失全部内容。
func convertContentToAnthropic(v interface{}) interface{} {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		blocks := []map[string]interface{}{}
		for _, p := range c {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			if b := partToAnthropicBlock(pm); b != nil {
				blocks = append(blocks, b)
			}
		}
		return blocks
	}
	return ""
}

// partToAnthropicBlock 单个 OpenAI 内容块 → Anthropic 内容块（无法识别时返回 nil）
func partToAnthropicBlock(part map[string]interface{}) map[string]interface{} {
	switch part["type"] {
	case "text":
		text, _ := part["text"].(string)
		if text == "" {
			return nil
		}
		return map[string]interface{}{"type": "text", "text": text}

	case "image_url":
		iu, ok := part["image_url"].(map[string]interface{})
		if !ok {
			return nil
		}
		url, _ := iu["url"].(string)
		if url == "" {
			return nil
		}
		// data:image/jpeg;base64,XXXX → base64 源；http(s) → url 源
		if mediaType, data, ok := parseDataURL(url); ok {
			return map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": mediaType,
					"data":       data,
				},
			}
		}
		return map[string]interface{}{
			"type":   "image",
			"source": map[string]interface{}{"type": "url", "url": url},
		}
	}
	return nil
}

// parseDataURL 解析 data URL，返回 media type 与 base64 负载
func parseDataURL(u string) (mediaType, data string, ok bool) {
	if !strings.HasPrefix(u, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(u, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", false
	}
	meta, payload := rest[:comma], rest[comma+1:]
	if !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(meta, ";base64")
	if mediaType == "" || payload == "" {
		return "", "", false
	}
	return mediaType, payload, true
}

// toolCallToAnthropic OpenAI tool_call → Anthropic tool_use 块
func toolCallToAnthropic(c interface{}) map[string]interface{} {
	cm, ok := c.(map[string]interface{})
	if !ok {
		return nil
	}
	f, ok := cm["function"].(map[string]interface{})
	if !ok {
		return nil
	}
	name, _ := f["name"].(string)
	if name == "" {
		return nil
	}
	id, _ := cm["id"].(string)
	// OpenAI 的 arguments 是 JSON 字符串，Anthropic 的 input 是对象
	input := map[string]interface{}{}
	if args, _ := f["arguments"].(string); args != "" {
		_ = json.Unmarshal([]byte(args), &input)
	}
	return map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": input,
	}
}

// contentToText 把任意 content 形态压平成纯文本（system / tool_result 用）
func contentToText(v interface{}) string {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		var sb strings.Builder
		for _, p := range c {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			if pm["type"] == "text" {
				t, _ := pm["text"].(string)
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return ""
}

// toBlockSlice 将 content 归一化为块数组（字符串包装成单个 text 块）
func toBlockSlice(content interface{}) []map[string]interface{} {
	switch c := content.(type) {
	case []map[string]interface{}:
		return c
	case string:
		if c == "" {
			return []map[string]interface{}{}
		}
		return []map[string]interface{}{{"type": "text", "text": c}}
	}
	return []map[string]interface{}{}
}

// isEmptyContent 判断转换后的 content 是否为空（空消息会被 Anthropic 拒绝）
func isEmptyContent(content interface{}) bool {
	switch c := content.(type) {
	case string:
		return c == ""
	case []map[string]interface{}:
		return len(c) == 0
	}
	return true
}


type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
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
	toolCalls := []map[string]interface{}{}
	for _, c := range ar.Content {
		switch c.Type {
		case "text":
			sb.WriteString(c.Text)
		case "tool_use":
			// 请求方向已经转换了 tools，响应方向必须把 tool_use 还原成 OpenAI tool_calls，
			// 否则模型发起的工具调用会被静默丢弃，客户端只看到空回复。
			args := "{}"
			if len(c.Input) > 0 {
				args = string(c.Input)
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   c.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      c.Name,
					"arguments": args,
				},
			})
		}
	}

	finish := map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls"}[ar.StopReason]
	if finish == "" {
		finish = "stop"
	}

	message := map[string]interface{}{
		"role":    "assistant",
		"content": sb.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		// OpenAI 语义：有 tool_calls 且无文本时 content 为 null
		if sb.Len() == 0 {
			message["content"] = nil
		}
		finish = "tool_calls"
	}

	out := map[string]interface{}{
		"id":      ar.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   ar.Model,
		"choices": []map[string]interface{}{{
			"index":         0,
			"message":       message,
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
		src:       src,
		reader:    bufio.NewReader(src),
		model:     model,
		id:        fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		created:   time.Now().Unix(),
		toolIndex: -1,
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

	// toolIndex 为当前 tool_use 块在 OpenAI tool_calls 数组中的下标。
	// 从 -1 起，content_block_start 时自增，使首个工具调用得到下标 0。
	toolIndex int
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
	case "content_block_start":
		// tool_use 块开始：发出带函数名的 tool_calls 首帧，
		// 后续 input_json_delta 增量拼接 arguments（与 OpenAI 流式工具调用语义一致）。
		var d struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil
		}
		if d.ContentBlock.Type != "tool_use" {
			return nil
		}
		t.toolIndex++
		return chunk(map[string]interface{}{
			"tool_calls": []map[string]interface{}{{
				"index": t.toolIndex,
				"id":    d.ContentBlock.ID,
				"type":  "function",
				"function": map[string]interface{}{
					"name":      d.ContentBlock.Name,
					"arguments": "",
				},
			}},
		}, nil)
	case "content_block_stop":
		return nil
	case "content_block_delta":
		var d struct {
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &d); err != nil {
			return nil
		}
		switch d.Delta.Type {
		case "text_delta":
			if d.Delta.Text == "" {
				return nil
			}
			return chunk(map[string]interface{}{"content": d.Delta.Text}, nil)
		case "input_json_delta":
			if d.Delta.PartialJSON == "" {
				return nil
			}
			return chunk(map[string]interface{}{
				"tool_calls": []map[string]interface{}{{
					"index":    t.toolIndex,
					"function": map[string]interface{}{"arguments": d.Delta.PartialJSON},
				}},
			}, nil)
		}
		return nil
	case "message_delta":
		var d struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		_ = json.Unmarshal([]byte(data), &d)
		finish := map[string]string{"end_turn": "stop", "stop_sequence": "stop", "max_tokens": "length", "tool_use": "tool_calls"}[d.Delta.StopReason]
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

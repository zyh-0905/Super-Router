package quality

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"smart-router/internal/protocol"
)

// RunChat 执行一次聊天探测（非流式或流式），返回 ChatEvidence。
// 首字节超时、无 [DONE]、非法 JSON 等异常只影响证据字段，不 panic。
// 凭据绝不写入错误信息（上游错误响应体最多截断 300 rune，且不包含请求头）。
// client 为共享出站客户端（含 SSRF 重定向校验，H3）；nil 时退回裸客户端（测试）。
func RunChat(ctx context.Context, ch *Channel, sc ProbeScenario, timeout time.Duration, client *http.Client) (*ChatEvidence, error) {
	ev := &ChatEvidence{RequestedModel: sc.Model}
	start := time.Now()

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := buildChatRequest(ch, sc)
	if err != nil {
		return ev, err
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, protocol.ChatEndpoint(ch.BaseURL, ch.Protocol), bytes.NewReader(body))
	if err != nil {
		return ev, fmt.Errorf("create chat request: %w", err)
	}
	if protocol.IsAnthropic(ch.Protocol) {
		for k, v := range protocol.AnthropicHeaders(ch.APIKey) {
			req.Header.Set(k, v)
		}
	} else {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}

	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			return ev, fmt.Errorf("first byte timeout: %w", reqCtx.Err())
		}
		return ev, fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()
	ev.HTTPStatus = resp.StatusCode
	ev.TTFBMS = int(time.Since(start).Milliseconds())

	if resp.StatusCode != http.StatusOK {
		// 上游错误响应体可能回显凭据：只读状态码与长度，绝不透传响应体
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = body
		return ev, fmt.Errorf("upstream %d", resp.StatusCode)
	}

	if sc.Stream {
		err = consumeStream(ch, resp.Body, ev)
	} else {
		err = consumeNonStream(ch, resp.Body, ev)
	}
	ev.TotalMS = int(time.Since(start).Milliseconds())
	return ev, err
}

// buildChatRequest 构造请求体（OpenAI 原生或转 Anthropic）。
func buildChatRequest(ch *Channel, sc ProbeScenario) ([]byte, error) {
	reqMap := scenarioToOpenAI(sc)
	if protocol.IsAnthropic(ch.Protocol) {
		return json.Marshal(protocol.OpenAIToAnthropic(reqMap))
	}
	return json.Marshal(reqMap)
}

// scenarioToOpenAI 探测场景 → OpenAI 请求 map。
func scenarioToOpenAI(sc ProbeScenario) map[string]interface{} {
	req := map[string]interface{}{
		"model":       sc.Model,
		"max_tokens":  sc.MaxTokens,
		"temperature": 0,
		"stream":      sc.Stream,
	}
	messages := make([]interface{}, 0, len(sc.Messages))
	for _, m := range sc.Messages {
		content := interface{}(m.Content)
		if len(m.Parts) > 1 {
			blocks := make([]map[string]interface{}, 0, len(m.Parts))
			for _, p := range m.Parts {
				switch p.Type {
				case "text":
					blocks = append(blocks, map[string]interface{}{"type": "text", "text": p.Text})
				case "image_url":
					blocks = append(blocks, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{"url": p.ImageURL},
					})
				}
			}
			content = blocks
		}
		messages = append(messages, map[string]interface{}{"role": m.Role, "content": content})
	}
	req["messages"] = messages
	if len(sc.Tools) > 0 {
		req["tools"] = sc.Tools
		if sc.ForceToolChoice {
			req["tool_choice"] = map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": sc.Tools[0].Function.Name}}
		}
	}
	return req
}

// consumeNonStream 解析非流式响应（Anthropic 经 AnthropicToOpenAI 转换）。
func consumeNonStream(ch *Channel, body io.Reader, ev *ChatEvidence) error {
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return fmt.Errorf("read chat response: %w", err)
	}
	if protocol.IsAnthropic(ch.Protocol) {
		converted, err := protocol.AnthropicToOpenAI(raw)
		if err != nil {
			return fmt.Errorf("convert anthropic response: %w", err)
		}
		raw = converted
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content interface{} `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("decode chat response: %w", err)
	}
	ev.ActualModel = resp.Model
	if len(resp.Choices) > 0 {
		ev.Text = contentAsText(resp.Choices[0].Message.Content)
	}
	// usage 缺失时 Present=false；全部存在才算 Present
	if resp.Usage.PromptTokens > 0 || resp.Usage.CompletionTokens > 0 || resp.Usage.TotalTokens > 0 {
		ev.Usage = TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			Present:          true,
		}
	}
	return nil
}

// consumeStream 解析 SSE 流（OpenAI 原生；Anthropic 经 streamTransformer 转换）。
func consumeStream(ch *Channel, body io.Reader, ev *ChatEvidence) error {
	var reader io.Reader = body
	var closeFn func() error
	if protocol.IsAnthropic(ch.Protocol) {
		rc, ok := body.(io.ReadCloser)
		if !ok {
			return fmt.Errorf("anthropic stream body not closable")
		}
		transformed := protocol.NewAnthropicStreamTransformer(rc, ev.RequestedModel)
		reader = transformed
		closeFn = transformed.Close
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var text strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		ev.StreamEvents++
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			ev.DoneReceived = true
			continue
		}
		// 每次循环用新的局部结构体：json.Unmarshal 会复用已有 slice 元素，
		// 缺失字段（如空 delta {}）会保留上次解析的残留值。
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content interface{} `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 非法 JSON 行跳过，不中断
		}
		if chunk.Model != "" {
			ev.ActualModel = chunk.Model
		}
		if chunk.Usage != nil {
			ev.Usage = TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
				Present:          true,
			}
		}
		if len(chunk.Choices) > 0 {
			if s := contentAsText(chunk.Choices[0].Delta.Content); s != "" {
				text.WriteString(s)
			}
		}
	}
	ev.Text = text.String()
	if closeFn != nil {
		_ = closeFn()
	}
	return scanner.Err()
}

// contentAsText 提取响应文本（字符串或内容块数组）。
func contentAsText(v interface{}) string {
	switch c := v.(type) {
	case string:
		return c
	case []interface{}:
		var sb strings.Builder
		for _, part := range c {
			pm, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if pm["type"] == "text" {
				if s, ok := pm["text"].(string); ok {
					sb.WriteString(s)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// truncateRunes 截断字符串到 n 个 rune。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

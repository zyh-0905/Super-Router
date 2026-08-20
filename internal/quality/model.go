package quality

import (
	"fmt"
	"sort"
)

// ErrNoMappedModel 站点无可用映射模型（拒绝创建任务）。
var ErrNoMappedModel = fmt.Errorf("no mapped model available for this channel")

// ResolveModel 模型解析顺序：
//  1. 显式模型（必须存在于该站点有效模型映射中）；
//  2. 站点 test_model；
//  3. 全局 probe_model（且必须存在于映射中）；
//  4. 按名称排序的第一个有效映射；
//  5. 无可用模型 → ErrNoMappedModel。
func ResolveModel(ch *Channel, explicit, probeModel string) (string, error) {
	if len(ch.ModelMapping) == 0 {
		return "", ErrNoMappedModel
	}
	if explicit != "" {
		if _, ok := ch.ModelMapping[explicit]; ok {
			return explicit, nil
		}
		return "", ErrNoMappedModel
	}
	if ch.TestModel != "" {
		if _, ok := ch.ModelMapping[ch.TestModel]; ok {
			return ch.TestModel, nil
		}
	}
	if probeModel != "" {
		if _, ok := ch.ModelMapping[probeModel]; ok {
			return probeModel, nil
		}
	}
	keys := make([]string, 0, len(ch.ModelMapping))
	for k := range ch.ModelMapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys[0], nil
}

// ProbeScenario 一次聊天探测的请求场景（最多两次聊天请求：非流式 + 流式）。
type ProbeScenario struct {
	Model           string
	Messages        []ProbeMessage
	MaxTokens       int
	Stream          bool
	Tools           []ProbeTool
	ForceToolChoice bool
}

// ProbeMessage 探测消息。
type ProbeMessage struct {
	Role  string
	Parts []ProbePart
	// 文本快捷字段（text-only 场景）
	Content string
}

// ProbePart 多模态内容块。
type ProbePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// ProbeTool 探测用工具定义。
type ProbeTool struct {
	Type     string        `json:"type"`
	Function ProbeFunction `json:"function"`
}

// ProbeFunction 函数定义。
type ProbeFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// tinyImageDataURL 体积极小的 1x1 PNG（data URL），只用于 vision 能力探测。
const tinyImageDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// probeQuestion 标准测试问题（短小、答案可解析）。
const probeQuestion = "Reply with exactly the word: pong"

// BuildProbeScenario 按站点能力构造探测场景：
//   - text-only：普通文本；
//   - vision：附带固定、体积极小的 data URL 图片内容块；
//   - tools：仅附带固定函数定义，不强制 tool_choice——
//     强制工具调用会让模型只返回 tool_calls 而非文本（下游 stream/behavior
//     阶段误判空内容），且部分中转站拒绝嵌套格式的 tool_choice（400）。
//     能力探测只验证端点能接受工具定义即可。
//   - tools + vision：分别放入同一请求，不新增第三次聊天请求。
func BuildProbeScenario(capabilities []string, model string, maxTokens int) ProbeScenario {
	sc := ProbeScenario{Model: model, MaxTokens: maxTokens}
	hasVision := false
	hasTools := false
	for _, c := range capabilities {
		switch c {
		case "vision":
			hasVision = true
		case "tools", "function_calling":
			hasTools = true
		}
	}

	parts := []ProbePart{{Type: "text", Text: probeQuestion}}
	if hasVision {
		parts = append(parts, ProbePart{Type: "image_url", ImageURL: tinyImageDataURL})
	}
	sc.Messages = []ProbeMessage{{
		Role:    "user",
		Parts:   parts,
		Content: probeQuestion,
	}}
	if hasTools {
		sc.Tools = []ProbeTool{{
			Type: "function",
			Function: ProbeFunction{
				Name:        "quality_probe_ping",
				Description: "Return pong to verify tool calling works.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		}}
	}
	return sc
}

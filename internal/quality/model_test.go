package quality

import (
	"testing"
)

func channelWith(mapping map[string]string, testModel string) *Channel {
	return &Channel{
		ID:           1,
		Name:         "Test",
		BaseURL:      "https://api.example.com",
		Protocol:     "openai",
		TestModel:    testModel,
		ModelMapping: mapping,
	}
}

func TestResolveModelExplicitModel(t *testing.T) {
	ch := channelWith(map[string]string{"gpt-4o": "gpt-4o-up", "claude": "claude-up"}, "claude")
	got, err := ResolveModel(ch, "gpt-4o", "")
	if err != nil || got != "gpt-4o" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveModelTestModelWins(t *testing.T) {
	ch := channelWith(map[string]string{"gpt-4o": "gpt-4o-up", "claude": "claude-up"}, "claude")
	got, err := ResolveModel(ch, "", "gpt-4o-global")
	if err != nil || got != "claude" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveModelGlobalProbeModel(t *testing.T) {
	ch := channelWith(map[string]string{"gpt-4o": "gpt-4o-up"}, "")
	got, err := ResolveModel(ch, "", "gpt-4o")
	if err != nil || got != "gpt-4o" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveModelGlobalProbeModelNotMapped(t *testing.T) {
	// 全局 probe_model 不在映射中 → 跳过，使用第一个有效映射
	ch := channelWith(map[string]string{"claude-sonnet": "cs"}, "")
	got, err := ResolveModel(ch, "", "gpt-4o")
	if err != nil || got != "claude-sonnet" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveModelFirstSortedMapping(t *testing.T) {
	// 三者均不可用 → 按名称排序的第一个有效映射
	ch := channelWith(map[string]string{"zeta": "z", "alpha": "a", "beta": "b"}, "")
	got, err := ResolveModel(ch, "", "unmapped-model")
	if err != nil || got != "alpha" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestResolveModelNoMappedModel(t *testing.T) {
	ch := channelWith(map[string]string{}, "")
	if _, err := ResolveModel(ch, "", "gpt-4o"); err == nil {
		t.Fatal("expected ErrNoMappedModel")
	}
	// 显式模型不在映射中 → 同样拒绝（不绕过映射校验）
	ch = channelWith(map[string]string{"other": "x"}, "")
	if _, err := ResolveModel(ch, "gpt-4o", ""); err == nil {
		t.Fatal("explicit model must be validated against mapping too")
	}
}

func TestBuildProbeScenarioTextOnly(t *testing.T) {
	sc := BuildProbeScenario(nil, "gpt-4o", 32)
	if len(sc.Messages) == 0 || sc.Messages[0].Role != "user" || sc.Messages[0].Content == "" {
		t.Fatalf("messages = %+v", sc.Messages)
	}
	if sc.MaxTokens != 32 || sc.Stream {
		t.Fatalf("scenario = %+v", sc)
	}
	if sc.Tools != nil {
		t.Fatalf("text-only scenario must not include tools")
	}
}

func TestBuildProbeScenarioVisionIncludesImage(t *testing.T) {
	sc := BuildProbeScenario([]string{"vision"}, "gpt-4o", 32)
	found := false
	for _, m := range sc.Messages {
		for _, part := range m.Parts {
			if part.Type == "image_url" && part.ImageURL != "" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("vision scenario missing image part: %+v", sc.Messages)
	}
}

func TestBuildProbeScenarioTools(t *testing.T) {
	sc := BuildProbeScenario([]string{"tools"}, "gpt-4o", 32)
	if sc.Tools == nil || len(sc.Tools) == 0 {
		t.Fatalf("tools scenario = %+v", sc)
	}
	// 不强制 tool_choice：强制后模型只返回工具调用而非文本，
	// 会污染下游 stream/behavior 阶段的文本判定
	if sc.ForceToolChoice {
		t.Fatalf("scenario must not force tool_choice: %+v", sc)
	}
}

func TestBuildProbeScenarioToolsAndVisionCombined(t *testing.T) {
	// tools + vision 分别放入同一请求，不新增第三次请求
	sc := BuildProbeScenario([]string{"tools", "vision"}, "gpt-4o", 32)
	if sc.Tools == nil || len(sc.Tools) == 0 {
		t.Fatal("tools missing in combined scenario")
	}
	foundImage := false
	for _, m := range sc.Messages {
		for _, part := range m.Parts {
			if part.Type == "image_url" {
				foundImage = true
			}
		}
	}
	if !foundImage {
		t.Fatal("image missing in combined scenario")
	}
}

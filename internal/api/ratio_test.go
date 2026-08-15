package api

import (
	"testing"

	"smart-router/internal/checker"
)

func TestClampProbeTokens(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, defaultManualProbeTokens},
		{-5, defaultManualProbeTokens},
		{16, 16},
		{64, 64},
		{256, 256},
		{512, maxManualProbeTokens},
	}
	for _, tc := range cases {
		if got := clampProbeTokens(tc.in); got != tc.want {
			t.Errorf("clampProbeTokens(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDriftPctUsesTokenWeightedBlend(t *testing.T) {
	// 声明：prompt 1.0、completion 3.0；实测 2.0；输入 10 / 输出 90 tokens
	// 加权声明 = (1.0*10 + 3.0*90)/100 = 2.8 → 漂移 = (2.0-2.8)/2.8 = -28.57%
	res := &checker.ProbeResult{
		RealRatio:        2.0,
		PromptTokens:     10,
		CompletionTokens: 90,
	}
	declared := map[string]interface{}{"prompt_ratio": 1.0, "completion_ratio": 3.0}
	if got := driftPct(res, declared); got < -28.6 || got > -28.5 {
		t.Errorf("driftPct = %v, want about -28.57", got)
	}
}

func TestDriftPctFallbackAndGuards(t *testing.T) {
	// 无 token 拆分信息 → 回退 prompt_ratio 对比
	res := &checker.ProbeResult{RealRatio: 1.5}
	declared := map[string]interface{}{"prompt_ratio": 1.0, "completion_ratio": 3.0}
	if got := driftPct(res, declared); got != 50.0 {
		t.Errorf("fallback driftPct = %v, want 50", got)
	}
	// 实测为 0 → 0
	if got := driftPct(&checker.ProbeResult{RealRatio: 0}, declared); got != 0 {
		t.Errorf("zero-ratio driftPct = %v, want 0", got)
	}
	// 声明为 0 → 0（无对比基准）
	zero := map[string]interface{}{"prompt_ratio": 0.0, "completion_ratio": 0.0}
	if got := driftPct(&checker.ProbeResult{RealRatio: 1.5}, zero); got != 0 {
		t.Errorf("zero-declared driftPct = %v, want 0", got)
	}
}

func TestValidGroupModels(t *testing.T) {
	if err := validGroupModels([]string{}, ""); err == nil {
		t.Error("empty models must be rejected")
	}
	if err := validGroupModels([]string{"gpt-5.5"}, ""); err == nil {
		t.Error("empty default model must be rejected")
	}
	if err := validGroupModels([]string{"gpt-5.5"}, "gpt-5.5-mini"); err == nil {
		t.Error("default model outside members must be rejected")
	}
	if err := validGroupModels([]string{"gpt-5.5", "gpt-5.5-mini"}, "gpt-5.5-mini"); err != nil {
		t.Errorf("valid group rejected: %v", err)
	}
}

func TestEstimateProbeCost(t *testing.T) {
	// 官方输出价 30/1M：(5000+256)×30/1M ≈ 0.1577
	cost := estimateProbeCost(256, 5, 30)
	if cost < 0.15 || cost > 0.16 {
		t.Fatalf("official cost = %v, want about 0.1577", cost)
	}
	// 无官方价：保守 $30/1M
	cost = estimateProbeCost(64, 0, 0)
	if cost < 0.15 || cost > 0.16 {
		t.Fatalf("fallback cost = %v, want about 0.1519", cost)
	}
	// 只有输入价：用输入价
	cost = estimateProbeCost(64, 1.5, 0)
	if cost < 0.007 || cost > 0.008 {
		t.Fatalf("input-only cost = %v, want about 0.0076", cost)
	}
}

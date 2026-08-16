package api

import "testing"

func TestNormalizeBalancedWeights(t *testing.T) {
	// 正常归一化
	w := normalizeBalancedWeights(map[string]float64{"cost": 3, "reliability": 2, "latency": 2, "load": 1})
	if w == nil {
		t.Fatal("valid weights must normalize")
	}
	sum := 0.0
	for _, v := range w {
		sum += v
	}
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("normalized sum = %f, want 1", sum)
	}
	if w["cost"] != 0.375 {
		t.Fatalf("cost weight = %f, want 0.375", w["cost"])
	}

	// 缺失键按 0，其余归一化
	w = normalizeBalancedWeights(map[string]float64{"cost": 1})
	if w == nil || w["cost"] != 1.0 {
		t.Fatalf("single key should normalize to 1: %+v", w)
	}

	// 负权重拒绝
	if normalizeBalancedWeights(map[string]float64{"cost": -1}) != nil {
		t.Fatal("negative weight must be rejected")
	}

	// 全零拒绝
	if normalizeBalancedWeights(map[string]float64{"cost": 0, "load": 0}) != nil {
		t.Fatal("all-zero weights must be rejected")
	}

	// nil 拒绝
	if normalizeBalancedWeights(nil) != nil {
		t.Fatal("nil weights must be rejected")
	}

	// 未知键忽略
	w = normalizeBalancedWeights(map[string]float64{"cost": 1, "hack": 9})
	if w == nil || w["cost"] != 1.0 {
		t.Fatalf("unknown keys must be ignored: %+v", w)
	}
}

func TestExtractBalancedWeights(t *testing.T) {
	// 嵌套形式
	w := extractBalancedWeights([]byte(`{"balanced_weights":{"cost":0.4,"reliability":0.3,"latency":0.2,"load":0.1}}`))
	if w["cost"] != 0.4 || w["load"] != 0.1 {
		t.Fatalf("nested extract: %+v", w)
	}
	// 扁平键形式
	w = extractBalancedWeights([]byte(`{"balanced_weights.cost":0.5,"balanced_weights.reliability":0.5}`))
	if w["cost"] != 0.5 || w["reliability"] != 0.5 || w["latency"] != 0 {
		t.Fatalf("flat extract: %+v", w)
	}
	// 空配置
	w = extractBalancedWeights([]byte(`{}`))
	for _, v := range w {
		if v != 0 {
			t.Fatalf("empty config must yield zero weights: %+v", w)
		}
	}
}

func TestWeightsToPercent(t *testing.T) {
	// 合计非 1 的权重换算为合计 100 的百分比
	p := weightsToPercent(map[string]float64{"cost": 60, "reliability": 20, "latency": 15, "load": 5})
	if p["cost"] != 60 || p["reliability"] != 20 || p["latency"] != 15 || p["load"] != 5 {
		t.Fatalf("percent mismatch: %+v", p)
	}
	// 全零 → 均衡默认 25/25/25/25
	p = weightsToPercent(map[string]float64{"cost": 0, "load": 0})
	if p["cost"] != 25 || p["load"] != 25 {
		t.Fatalf("zero-sum fallback: %+v", p)
	}
	// 合计不足 100 时按比例放大到 100
	p = weightsToPercent(map[string]float64{"cost": 1, "reliability": 1})
	if p["cost"] != 50 || p["reliability"] != 50 {
		t.Fatalf("scale to 100: %+v", p)
	}
}

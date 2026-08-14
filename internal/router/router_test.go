package router

import (
	"testing"
	"time"
)

// TestCustomPriorityStrategy 测试自定义优先级策略
func TestCustomPriorityStrategy(t *testing.T) {
	candidates := []*ChannelHealth{
		{
			ID:               1,
			Name:             "primary_high",
			Role:             "primary",
			UserPriority:     100,
			ReliabilityScore: 0.95,
			TTFTP50:          map[string]int{"gpt-4o": 800},
		},
		{
			ID:               2,
			Name:             "primary_low",
			Role:             "primary",
			UserPriority:     50,
			ReliabilityScore: 0.98,
			TTFTP50:          map[string]int{"gpt-4o": 500},
		},
		{
			ID:               3,
			Name:             "backup",
			Role:             "backup",
			UserPriority:     100,
			ReliabilityScore: 0.99,
			TTFTP50:          map[string]int{"gpt-4o": 300},
		},
	}

	req := &FilterRequest{Model: "gpt-4o"}
	policy := &Policy{Strategy: "custom_priority", Config: map[string]interface{}{}}

	strategy := &CustomPriorityStrategy{}
	scored := strategy.Sort(candidates, req, policy)

	// 验证排序：primary 优先，然后按 user_priority
	if scored[0].Channel.ID != 1 {
		t.Errorf("Expected channel 1 (primary_high) first, got %d", scored[0].Channel.ID)
	}
	if scored[1].Channel.ID != 2 {
		t.Errorf("Expected channel 2 (primary_low) second, got %d", scored[1].Channel.ID)
	}
	if scored[2].Channel.ID != 3 {
		t.Errorf("Expected channel 3 (backup) third, got %d", scored[2].Channel.ID)
	}
}

// TestPriceFirstStrategy 测试低价优先策略
func TestPriceFirstStrategy(t *testing.T) {
	candidates := []*ChannelHealth{
		{
			ID:               1,
			Name:             "expensive",
			RealRatio:        map[string]float64{"gpt-4o": 2.0}, // 2倍倍率
			ReliabilityScore: 0.95,
		},
		{
			ID:               2,
			Name:             "cheap",
			RealRatio:        map[string]float64{"gpt-4o": 0.5}, // 0.5倍倍率
			ReliabilityScore: 0.90,
		},
		{
			ID:               3,
			Name:             "moderate",
			RealRatio:        map[string]float64{"gpt-4o": 1.0}, // 1倍倍率
			ReliabilityScore: 0.98,
		},
	}

	req := &FilterRequest{
		Model:          "gpt-4o",
		EstimatedInput: 1000,
		MaxOutput:      2000,
	}
	policy := &Policy{
		Strategy: "price_first",
		Config:   map[string]interface{}{"expected_output_ratio": 2.0},
	}

	strategy := &PriceFirstStrategy{}
	scored := strategy.Sort(candidates, req, policy)

	// 验证排序：便宜的优先
	if scored[0].Channel.ID != 2 {
		t.Errorf("Expected channel 2 (cheap) first, got %d", scored[0].Channel.ID)
	}
	if scored[1].Channel.ID != 3 {
		t.Errorf("Expected channel 3 (moderate) second, got %d", scored[1].Channel.ID)
	}
	if scored[2].Channel.ID != 1 {
		t.Errorf("Expected channel 1 (expensive) third, got %d", scored[2].Channel.ID)
	}
}

// TestLatencyFirstStrategy 测试延迟优先策略
func TestLatencyFirstStrategy(t *testing.T) {
	candidates := []*ChannelHealth{
		{
			ID:               1,
			Name:             "slow",
			TTFTP50:          map[string]int{"gpt-4o": 2000},
			TTFTP95:          map[string]int{"gpt-4o": 3000},
			ReliabilityScore: 0.95,
		},
		{
			ID:               2,
			Name:             "fast",
			TTFTP50:          map[string]int{"gpt-4o": 500},
			TTFTP95:          map[string]int{"gpt-4o": 800},
			ReliabilityScore: 0.90,
		},
		{
			ID:               3,
			Name:             "moderate",
			TTFTP50:          map[string]int{"gpt-4o": 1000},
			TTFTP95:          map[string]int{"gpt-4o": 1500},
			ReliabilityScore: 0.98,
		},
	}

	req := &FilterRequest{Model: "gpt-4o"}
	policy := &Policy{Strategy: "latency_first", Config: map[string]interface{}{}}

	strategy := &LatencyFirstStrategy{}
	scored := strategy.Sort(candidates, req, policy)

	// 验证排序：快的优先
	if scored[0].Channel.ID != 2 {
		t.Errorf("Expected channel 2 (fast) first, got %d", scored[0].Channel.ID)
	}
	if scored[1].Channel.ID != 3 {
		t.Errorf("Expected channel 3 (moderate) second, got %d", scored[1].Channel.ID)
	}
	if scored[2].Channel.ID != 1 {
		t.Errorf("Expected channel 1 (slow) third, got %d", scored[2].Channel.ID)
	}
}

// TestReliabilityFirstStrategy 测试成功率优先策略
func TestReliabilityFirstStrategy(t *testing.T) {
	candidates := []*ChannelHealth{
		{
			ID:               1,
			Name:             "unstable",
			ReliabilityScore: 0.80,
			TTFTP50:          map[string]int{"gpt-4o": 500},
		},
		{
			ID:               2,
			Name:             "very_stable",
			ReliabilityScore: 0.99,
			TTFTP50:          map[string]int{"gpt-4o": 1000},
		},
		{
			ID:               3,
			Name:             "stable",
			ReliabilityScore: 0.95,
			TTFTP50:          map[string]int{"gpt-4o": 800},
		},
	}

	req := &FilterRequest{Model: "gpt-4o"}
	policy := &Policy{Strategy: "reliability_first", Config: map[string]interface{}{}}

	strategy := &ReliabilityFirstStrategy{}
	scored := strategy.Sort(candidates, req, policy)

	// 验证排序：可靠性高的优先
	if scored[0].Channel.ID != 2 {
		t.Errorf("Expected channel 2 (very_stable) first, got %d", scored[0].Channel.ID)
	}
	if scored[1].Channel.ID != 3 {
		t.Errorf("Expected channel 3 (stable) second, got %d", scored[1].Channel.ID)
	}
	if scored[2].Channel.ID != 1 {
		t.Errorf("Expected channel 1 (unstable) third, got %d", scored[2].Channel.ID)
	}
}

// TestBalancedStrategy 测试综合策略
func TestBalancedStrategy(t *testing.T) {
	candidates := []*ChannelHealth{
		{
			ID:               1,
			Name:             "balanced",
			RealRatio:        map[string]float64{"gpt-4o": 1.0},
			ReliabilityScore: 0.95,
			TTFTP50:          map[string]int{"gpt-4o": 800},
		},
		{
			ID:               2,
			Name:             "cheap_but_slow",
			RealRatio:        map[string]float64{"gpt-4o": 0.5},
			ReliabilityScore: 0.90,
			TTFTP50:          map[string]int{"gpt-4o": 2000},
		},
		{
			ID:               3,
			Name:             "fast_but_expensive",
			RealRatio:        map[string]float64{"gpt-4o": 2.0},
			ReliabilityScore: 0.98,
			TTFTP50:          map[string]int{"gpt-4o": 400},
		},
	}

	req := &FilterRequest{
		Model:          "gpt-4o",
		EstimatedInput: 1000,
		MaxOutput:      2000,
	}
	policy := &Policy{
		Strategy: "balanced",
		Config: map[string]interface{}{
			"balanced_weights.cost":        0.35,
			"balanced_weights.reliability": 0.30,
			"balanced_weights.latency":     0.25,
			"balanced_weights.load":        0.10,
			"expected_output_ratio":        2.0,
		},
	}

	strategy := &BalancedStrategy{}
	scored := strategy.Sort(candidates, req, policy)

	// 验证：所有渠道都有得分
	if len(scored) != 3 {
		t.Errorf("Expected 3 scored channels, got %d", len(scored))
	}

	// 验证得分在合理范围内 [0, 1]
	for _, s := range scored {
		if s.Score < 0 || s.Score > 1 {
			t.Errorf("Channel %d score %.4f out of range [0, 1]", s.Channel.ID, s.Score)
		}
	}
}

// TestHardFilter 测试硬过滤
func TestHardFilter(t *testing.T) {
	policy := &Policy{
		Strategy: "custom_priority",
		Config: map[string]interface{}{
			"max_price_cap": 1.0,  // 成本上限 $1
			"max_ttft_ms":   1000, // 延迟上限 1000ms
		},
	}
	filter := NewHardFilter(policy)

	req := &FilterRequest{
		Model:          "gpt-4o",
		Capabilities:   []string{"tools"},
		EstimatedInput: 1000,
		MaxOutput:      2000,
	}

	// 测试用例 1：禁用的渠道
	ch1 := &ChannelHealth{
		ID:           1,
		Enabled:      false,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
	}
	if exclusion := filter.Filter(ch1, req); exclusion == nil {
		t.Error("Expected disabled channel to be excluded")
	} else if exclusion.Reason != ExcludeUserDisabled {
		t.Errorf("Expected reason %s, got %s", ExcludeUserDisabled, exclusion.Reason)
	}

	// 测试用例 2：不支持模型
	ch2 := &ChannelHealth{
		ID:           2,
		Enabled:      true,
		ModelMapping: map[string]string{"claude": "claude-sonnet"},
	}
	if exclusion := filter.Filter(ch2, req); exclusion == nil {
		t.Error("Expected unsupported model to be excluded")
	} else if exclusion.Reason != ExcludeModelNotSupported {
		t.Errorf("Expected reason %s, got %s", ExcludeModelNotSupported, exclusion.Reason)
	}

	// 测试用例 3：能力缺失
	ch3 := &ChannelHealth{
		ID:           3,
		Enabled:      true,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
		Capabilities: []string{}, // 不支持 tools
	}
	if exclusion := filter.Filter(ch3, req); exclusion == nil {
		t.Error("Expected missing capability to be excluded")
	} else if exclusion.Reason != ExcludeCapabilityMissing {
		t.Errorf("Expected reason %s, got %s", ExcludeCapabilityMissing, exclusion.Reason)
	}

	// 测试用例 4：熔断状态 open
	ch4 := &ChannelHealth{
		ID:           4,
		Enabled:      true,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
		Capabilities: []string{"tools"},
		CircuitState: "open",
		RealRatio:    map[string]float64{"gpt-4o": 0.5},
	}
	if exclusion := filter.Filter(ch4, req); exclusion == nil {
		t.Error("Expected circuit open to be excluded")
	} else if exclusion.Reason != ExcludeCircuitOpen {
		t.Errorf("Expected reason %s, got %s", ExcludeCircuitOpen, exclusion.Reason)
	}

	// 测试用例 5：通过所有过滤
	ch5 := &ChannelHealth{
		ID:           5,
		Enabled:      true,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
		Capabilities: []string{"tools", "vision"},
		CircuitState: "closed",
		RealRatio:    map[string]float64{"gpt-4o": 0.5},
	}
	if exclusion := filter.Filter(ch5, req); exclusion != nil {
		t.Errorf("Expected channel to pass, but got excluded with reason: %s", exclusion.Reason)
	}
}

// TestStableSorting 测试稳定排序（相同输入必须产生相同输出）
func TestStableSorting(t *testing.T) {
	candidates := []*ChannelHealth{
		{ID: 1, Name: "ch1", ReliabilityScore: 0.95, TTFTP50: map[string]int{"gpt-4o": 800}},
		{ID: 2, Name: "ch2", ReliabilityScore: 0.95, TTFTP50: map[string]int{"gpt-4o": 800}},
		{ID: 3, Name: "ch3", ReliabilityScore: 0.95, TTFTP50: map[string]int{"gpt-4o": 800}},
	}

	req := &FilterRequest{Model: "gpt-4o"}
	policy := &Policy{Strategy: "reliability_first", Config: map[string]interface{}{}}

	strategy := &ReliabilityFirstStrategy{}

	// 运行 1000 次，验证结果一致
	var firstOrder []int
	for i := 0; i < 1000; i++ {
		scored := strategy.Sort(candidates, req, policy)

		if i == 0 {
			// 记录第一次的顺序
			for _, s := range scored {
				firstOrder = append(firstOrder, s.Channel.ID)
			}
		} else {
			// 验证后续顺序与第一次一致
			for j, s := range scored {
				if s.Channel.ID != firstOrder[j] {
					t.Errorf("Iteration %d: order mismatch at position %d, expected %d, got %d",
						i, j, firstOrder[j], s.Channel.ID)
				}
			}
		}
	}

	// 验证稳定排序：相同得分按 ID 升序
	if firstOrder[0] != 1 || firstOrder[1] != 2 || firstOrder[2] != 3 {
		t.Errorf("Expected stable order [1,2,3], got %v", firstOrder)
	}
}

// TestCircuitStateCooling 测试冷却中的熔断状态
func TestCircuitStateCooling(t *testing.T) {
	policy := &Policy{
		Strategy: "custom_priority",
		Config:   map[string]interface{}{},
	}
	filter := NewHardFilter(policy)

	req := &FilterRequest{
		Model:          "gpt-4o",
		EstimatedInput: 1000,
		MaxOutput:      2000,
	}

	// 冷却中（未到期）
	ch1 := &ChannelHealth{
		ID:           1,
		Enabled:      true,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
		CircuitState: "cooling",
		CoolingUntil: time.Now().Add(10 * time.Minute), // 还有 10 分钟
		RealRatio:    map[string]float64{"gpt-4o": 0.5},
	}
	if exclusion := filter.Filter(ch1, req); exclusion == nil {
		t.Error("Expected cooling channel to be excluded")
	} else if exclusion.Reason != ExcludeCircuitCooling {
		t.Errorf("Expected reason %s, got %s", ExcludeCircuitCooling, exclusion.Reason)
	}

	// 冷却已过期
	ch2 := &ChannelHealth{
		ID:           2,
		Enabled:      true,
		ModelMapping: map[string]string{"gpt-4o": "gpt-4o"},
		CircuitState: "cooling",
		CoolingUntil: time.Now().Add(-10 * time.Minute), // 10 分钟前已过期
		RealRatio:    map[string]float64{"gpt-4o": 0.5},
	}
	if exclusion := filter.Filter(ch2, req); exclusion != nil {
		t.Errorf("Expected expired cooling channel to pass, but got excluded with reason: %s", exclusion.Reason)
	}
}

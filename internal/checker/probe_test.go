package checker

import "testing"

func TestComputeRealRatioOfficial(t *testing.T) {
	// 官网输入 $2.5/1M、输出 $10/1M；10 输入 + 90 输出 tokens
	// 应扣 = 10*2.5/1M + 90*10/1M = 0.000925；实际 0.00185 → 2.0x official
	ratio, basis := computeRealRatio(0.00185, 10, 90, 2.5, 10.0)
	if basis != BasisOfficial {
		t.Fatalf("basis = %s, want official", basis)
	}
	if ratio < 1.99 || ratio > 2.01 {
		t.Fatalf("ratio = %v, want about 2.0", ratio)
	}
}

func TestComputeRealRatioBaselineFallback(t *testing.T) {
	// 无官网价：100 tokens 扣费 $0.001 → 0.001/100/0.00001 = 1.0x（$10/1M 基准）
	ratio, basis := computeRealRatio(0.001, 50, 50, 0, 0)
	if basis != BasisBaseline {
		t.Fatalf("basis = %s, want baseline", basis)
	}
	if ratio < 0.99 || ratio > 1.01 {
		t.Fatalf("ratio = %v, want about 1.0", ratio)
	}
}

func TestComputeRealRatioGuards(t *testing.T) {
	// 零 token / 零扣费 → 0
	if r, _ := computeRealRatio(0.001, 0, 0, 2.5, 10); r != 0 {
		t.Fatalf("zero tokens: ratio = %v, want 0", r)
	}
	if r, _ := computeRealRatio(0, 50, 50, 2.5, 10); r != 0 {
		t.Fatalf("zero cost: ratio = %v, want 0", r)
	}
	// 官网价只有一半（输入价缺失）→ 回退 baseline
	if _, b := computeRealRatio(0.001, 50, 50, 0, 10); b != BasisBaseline {
		t.Fatalf("partial official price must fall back to baseline, got %s", b)
	}
}

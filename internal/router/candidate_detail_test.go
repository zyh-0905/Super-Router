package router

import (
	"math"
	"testing"
)

func TestCompositeRelativePositive(t *testing.T) {
	// 差距 0.1% 的两个正分候选：应显示 100 与 ~99.9，而不是 100 与 0
	if got := compositeRelative(361.2, 361.2); got != 100 {
		t.Fatalf("best = %v, want 100", got)
	}
	got := compositeRelative(360.8, 361.2)
	if math.Abs(got-99.889) > 0.01 {
		t.Fatalf("near-best = %v, want ~99.89", got)
	}
	// 明显更差者
	if got := compositeRelative(180.6, 361.2); math.Abs(got-50) > 0.01 {
		t.Fatalf("half = %v, want ~50", got)
	}
	// 0 分候选在正分集合中记 0
	if got := compositeRelative(0, 361.2); got != 0 {
		t.Fatalf("zero score = %v, want 0", got)
	}
}

func TestCompositeRelativeNegative(t *testing.T) {
	// 负分策略（-费用）：-0.21 最优；-0.52 应得 0.21/0.52 ≈ 40.4
	if got := compositeRelative(-0.21, -0.21); got != 100 {
		t.Fatalf("best = %v, want 100", got)
	}
	got := compositeRelative(-0.52, -0.21)
	if math.Abs(got-40.384) > 0.01 {
		t.Fatalf("worse = %v, want ~40.38", got)
	}
}

func TestCompositeRelativeZeroBest(t *testing.T) {
	// best == 0：并列最优（score==0）记 100，其余（负分）记 0
	if got := compositeRelative(0, 0); got != 100 {
		t.Fatalf("tie at zero = %v, want 100", got)
	}
	if got := compositeRelative(-0.5, 0); got != 0 {
		t.Fatalf("below zero best = %v, want 0", got)
	}
}

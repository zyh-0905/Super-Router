package api

import (
	"testing"
	"time"
)

func testCircuitConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		InitialCoolingSeconds:    30,
		MaxCoolingSeconds:        600,
		CoolingBackoff:           []int{30, 60, 120, 300, 600},
		RecoverySuccessThreshold: 3,
	}
}

func TestTransitionCircuitState(t *testing.T) {
	cfg := testCircuitConfig()
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Minute)

	// closed + 样本判定应开闸 → open（带冷却）
	state, cooling := transitionCircuitState("closed", time.Time{}, false, 1, 0, true, cfg)
	if state != "open" || cooling == nil || cooling.Before(time.Now()) {
		t.Fatalf("closed+shouldOpen: state=%s cooling=%v", state, cooling)
	}
	// closed + 不应开闸 → 保持 closed
	state, cooling = transitionCircuitState("closed", time.Time{}, false, 1, 0, false, cfg)
	if state != "closed" || cooling != nil {
		t.Fatalf("closed+!shouldOpen: state=%s cooling=%v", state, cooling)
	}

	// open 冷却未到期 + 成功 → 保持 open（成功不能关闭开闸状态）
	state, _ = transitionCircuitState("open", future, true, 0, 1, false, cfg)
	if state != "open" {
		t.Fatalf("open(cooling)+success: state=%s, want open", state)
	}
	// open 冷却已到期 + 探测成功 → degraded（冷却清除）
	state, cooling = transitionCircuitState("open", past, true, 0, 1, false, cfg)
	if state != "degraded" || cooling != nil {
		t.Fatalf("open(expired)+success: state=%s cooling=%v", state, cooling)
	}
	// open 冷却已到期 + 探测失败 → 重新 open 并进入下一级冷却
	state, cooling = transitionCircuitState("open", past, false, 2, 0, false, cfg)
	if state != "open" || cooling == nil {
		t.Fatalf("open(expired)+failure: state=%s cooling=%v", state, cooling)
	}

	// half_open + 成功 → degraded
	state, cooling = transitionCircuitState("half_open", time.Time{}, true, 0, 1, false, cfg)
	if state != "degraded" || cooling != nil {
		t.Fatalf("half_open+success: state=%s cooling=%v", state, cooling)
	}
	// half_open + 失败 → open（指数退避）
	state, cooling = transitionCircuitState("half_open", time.Time{}, false, 1, 0, false, cfg)
	if state != "open" || cooling == nil {
		t.Fatalf("half_open+failure: state=%s cooling=%v", state, cooling)
	}

	// degraded 连续成功达到阈值 → closed
	state, _ = transitionCircuitState("degraded", time.Time{}, true, 0, 3, false, cfg)
	if state != "closed" {
		t.Fatalf("degraded+recovered: state=%s, want closed", state)
	}
	// degraded 成功但未达阈值 → 保持 degraded
	state, _ = transitionCircuitState("degraded", time.Time{}, true, 0, 2, false, cfg)
	if state != "degraded" {
		t.Fatalf("degraded+partial: state=%s, want degraded", state)
	}
	// degraded 失败且样本判定开闸 → open
	state, cooling = transitionCircuitState("degraded", time.Time{}, false, 1, 0, true, cfg)
	if state != "open" || cooling == nil {
		t.Fatalf("degraded+failure+shouldOpen: state=%s cooling=%v", state, cooling)
	}
	// degraded 失败但样本不足 → 保持 degraded
	state, _ = transitionCircuitState("degraded", time.Time{}, false, 1, 0, false, cfg)
	if state != "degraded" {
		t.Fatalf("degraded+failure+!shouldOpen: state=%s, want degraded", state)
	}
}

func TestNextCoolingDurationEscalation(t *testing.T) {
	cfg := testCircuitConfig()
	cases := map[int]time.Duration{
		0:  30 * time.Second,
		1:  30 * time.Second,
		2:  60 * time.Second,
		3:  120 * time.Second,
		4:  300 * time.Second,
		5:  600 * time.Second,
		99: 600 * time.Second, // 超出退避表 → 封顶
	}
	for failures, want := range cases {
		if got := nextCoolingDuration(failures, cfg); got != want {
			t.Errorf("nextCoolingDuration(%d) = %v, want %v", failures, got, want)
		}
	}
}

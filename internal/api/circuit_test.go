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
	state, cooling, level := transitionCircuitState("closed", time.Time{}, 0, false, 1, 0, true, cfg)
	if state != "open" || cooling == nil || cooling.Before(time.Now()) || level != 0 {
		t.Fatalf("closed+shouldOpen: state=%s cooling=%v level=%d", state, cooling, level)
	}
	// closed + 不应开闸 → 保持 closed
	state, cooling, _ = transitionCircuitState("closed", time.Time{}, 0, false, 1, 0, false, cfg)
	if state != "closed" || cooling != nil {
		t.Fatalf("closed+!shouldOpen: state=%s cooling=%v", state, cooling)
	}

	// open 冷却未到期 + 成功 → 保持 open（成功不能关闭开闸状态）
	state, _, _ = transitionCircuitState("open", future, 0, true, 0, 1, false, cfg)
	if state != "open" {
		t.Fatalf("open(cooling)+success: state=%s, want open", state)
	}
	// open 冷却已到期 + 探测成功 → degraded（冷却清除）
	state, cooling, _ = transitionCircuitState("open", past, 0, true, 0, 1, false, cfg)
	if state != "degraded" || cooling != nil {
		t.Fatalf("open(expired)+success: state=%s cooling=%v", state, cooling)
	}
	// open 冷却已到期 + 探测失败 → 重新 open 并进入下一级冷却
	state, cooling, _ = transitionCircuitState("open", past, 0, false, 2, 0, false, cfg)
	if state != "open" || cooling == nil {
		t.Fatalf("open(expired)+failure: state=%s cooling=%v", state, cooling)
	}

	// half_open + 成功 → degraded
	state, cooling, _ = transitionCircuitState("half_open", time.Time{}, 0, true, 0, 1, false, cfg)
	if state != "degraded" || cooling != nil {
		t.Fatalf("half_open+success: state=%s cooling=%v", state, cooling)
	}
	// half_open + 失败 → open（指数退避）
	state, cooling, _ = transitionCircuitState("half_open", time.Time{}, 0, false, 1, 0, false, cfg)
	if state != "open" || cooling == nil {
		t.Fatalf("half_open+failure: state=%s cooling=%v", state, cooling)
	}

	// degraded 连续成功达到阈值 → closed
	state, _, level = transitionCircuitState("degraded", time.Time{}, 0, true, 0, 3, false, cfg)
	if state != "closed" || level != 0 {
		t.Fatalf("degraded+recovered: state=%s level=%d, want closed/0", state, level)
	}
	// degraded 成功但未达阈值 → 保持 degraded
	state, _, _ = transitionCircuitState("degraded", time.Time{}, 0, true, 0, 2, false, cfg)
	if state != "degraded" {
		t.Fatalf("degraded+partial: state=%s, want degraded", state)
	}
	// degraded 失败且样本判定开闸 → open（H4：延续历史档位升一级，而非最短冷却）
	state, cooling, level = transitionCircuitState("degraded", time.Time{}, 2, false, 1, 0, true, cfg)
	if state != "open" || cooling == nil || level != 3 {
		t.Fatalf("degraded+failure+shouldOpen: state=%s cooling=%v level=%d", state, cooling, level)
	}
	// 冷却时长必须对应 CoolingBackoff[3]=300s，而不是初始 30s
	want := nowPlus(300 * time.Second)
	if cooling == nil || cooling.Before(want.Add(-time.Second)) || cooling.After(want.Add(time.Second)) {
		t.Fatalf("degraded+failure cooling=%v, want ~%v (backoff level 3)", cooling, want)
	}
	// 档位封顶：level 超表后用最大冷却
	_, cooling, level = transitionCircuitState("degraded", time.Time{}, 99, false, 1, 0, true, cfg)
	wantMax := nowPlus(600 * time.Second)
	if level != 100 || cooling == nil || cooling.Before(wantMax.Add(-time.Second)) || cooling.After(wantMax.Add(time.Second)) {
		t.Fatalf("degraded+failure(max level): cooling=%v level=%d, want ~600s/100", cooling, level)
	}
	// degraded 失败但样本不足 → 保持 degraded（档位保持）
	state, _, level = transitionCircuitState("degraded", time.Time{}, 2, false, 1, 0, false, cfg)
	if state != "degraded" || level != 2 {
		t.Fatalf("degraded+failure+!shouldOpen: state=%s level=%d, want degraded/2", state, level)
	}
}

// nowPlus 生成与 transitionCircuitState 内部 time.Now() 几乎同时的期望时间，
// 用 1s 容差比较。
func nowPlus(d time.Duration) time.Time {
	return time.Now().Add(d)
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

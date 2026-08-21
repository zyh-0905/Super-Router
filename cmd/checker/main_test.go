package main

import (
	"errors"
	"testing"
	"time"
)

func TestProbeDueUsesFailureBackoff(t *testing.T) {
	lastAttempt := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	lastRun := map[string]time.Time{
		"probe":        lastAttempt,
		"probe_failed": lastAttempt,
	}

	if probeDue(lastAttempt.Add(time.Hour), lastRun, time.Hour, 6*time.Hour) {
		t.Fatal("failed probe became due at the normal interval")
	}
	if !probeDue(lastAttempt.Add(6*time.Hour), lastRun, time.Hour, 6*time.Hour) {
		t.Fatal("failed probe did not become due after the configured backoff")
	}
}

func TestRemainingProbeBudgetUsesActualCost(t *testing.T) {
	got := remainingProbeBudget(5, 0.37)
	if got != 4.63 {
		t.Fatalf("remaining budget = %v, want 4.63", got)
	}
}

func TestProbeBudgetLeftFailsClosedWhenSpentCannotBeRead(t *testing.T) {
	remaining, ok := probeBudgetLeft(5, 0, errors.New("database unavailable"))
	if ok {
		t.Fatal("budget query failure must not permit paid probes")
	}
	if remaining != 0 {
		t.Fatalf("remaining budget = %v, want 0", remaining)
	}
}

func TestAccountProbeResultDeductsCostEvenWhenProbeReturnsError(t *testing.T) {
	remaining, failed := accountProbeResult(5, 0.37, errors.New("store result"))
	if remaining != 4.63 {
		t.Fatalf("remaining budget = %v, want 4.63", remaining)
	}
	if !failed {
		t.Fatal("probe error must still be reported as a failure")
	}
}

// TestProbeBudgetGate A1：并发探针的进程内粗闸门。
// 结算失败禁用本 tick 剩余探测；内存预算耗尽不再发起新探测。
func TestProbeBudgetGate(t *testing.T) {
	var s scheduler
	s.probeBudgetMicro.Store(1000)
	if !s.probeBudgetOK() {
		t.Fatal("positive budget must allow probes")
	}
	s.probeBudgetMicro.Store(0)
	if s.probeBudgetOK() {
		t.Fatal("zero budget must gate probes")
	}
	s.probeBudgetMicro.Store(1000)
	s.probeDisabled.Store(true)
	if s.probeBudgetOK() {
		t.Fatal("disabled tick must gate probes even with budget left")
	}
	s.probeDisabled.Store(false)
	if !s.probeBudgetOK() {
		t.Fatal("gate must recover after disable flag cleared")
	}
}

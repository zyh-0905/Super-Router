package quality

import (
	"testing"
)

func TestPublicRunIDRoundTrip(t *testing.T) {
	if got := PublicRunID(123); got != "qc_123" {
		t.Fatal(got)
	}
	id, err := ParseRunID("qc_123")
	if err != nil || id != 123 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if _, err := ParseRunID("123"); err == nil {
		t.Fatal("bare id must be rejected")
	}
	if _, err := ParseRunID("qc_abc"); err == nil {
		t.Fatal("non-numeric id must be rejected")
	}
}

func TestOverallStatusPrecedence(t *testing.T) {
	got := Summarize([]StageResult{
		{Stage: "connectivity", Status: StatusPassed},
		{Stage: "stream", Status: StatusAttention},
	})
	if got != OverallAttention {
		t.Fatal(got)
	}
}

func TestSummarizeCriticalFailureWins(t *testing.T) {
	got := Summarize([]StageResult{
		{Stage: "connectivity", Status: StatusFailed},
		{Stage: "stream", Status: StatusAttention},
	})
	if got != OverallFailed {
		t.Fatal(got)
	}
}

func TestSummarizeAllPassedIsGood(t *testing.T) {
	got := Summarize([]StageResult{
		{Stage: "connectivity", Status: StatusPassed},
		{Stage: "protocol", Status: StatusPassed},
		{Stage: "stream", Status: StatusPassed},
		{Stage: "usage", Status: StatusPassed},
		{Stage: "behavior", Status: StatusPassed},
	})
	if got != OverallGood {
		t.Fatal(got)
	}
}

func TestSummarizeEmptyIsUnknown(t *testing.T) {
	if got := Summarize(nil); got != OverallUnknown {
		t.Fatal(got)
	}
}

func TestSummarizeSkippedDoesNotDowngrade(t *testing.T) {
	got := Summarize([]StageResult{
		{Stage: "connectivity", Status: StatusPassed},
		{Stage: "usage", Status: StatusSkipped},
		{Stage: "behavior", Status: StatusSkipped},
	})
	if got != OverallGood {
		t.Fatal(got)
	}
}

func TestRunStatuses(t *testing.T) {
	terminal := map[RunStatus]bool{
		RunCompleted: true, RunFailed: true, RunCancelled: true, RunExpired: true,
	}
	for s, want := range terminal {
		if IsTerminal(s) != want {
			t.Fatalf("IsTerminal(%s) = %v, want %v", s, IsTerminal(s), want)
		}
	}
	if IsTerminal(RunQueued) || IsTerminal(RunRunning) || IsTerminal(RunCancelRequested) {
		t.Fatal("non-terminal status misclassified")
	}
}

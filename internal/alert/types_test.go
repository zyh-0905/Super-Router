package alert

import (
	"testing"
	"time"
)

func TestAlertKeyIsStable(t *testing.T) {
	got := StableKey(AlertInput{Type: "low_balance", ChannelID: 3})
	if got != "low_balance:channel-3" {
		t.Fatal(got)
	}
}

func TestAlertKeyIncludesModelAndGroup(t *testing.T) {
	got := StableKey(AlertInput{Type: "circuit_open", ChannelID: 3, Model: "gpt-5.5", GroupID: 2})
	if got != "circuit_open:channel-3:model-gpt-5.5:group-2" {
		t.Fatal(got)
	}
}

func TestAlertKeyChannelOnlySkipsGroup(t *testing.T) {
	got := StableKey(AlertInput{Type: "channel_disabled", ChannelID: 7, GroupID: 0})
	if got != "channel_disabled:channel-7" {
		t.Fatal(got)
	}
}

func TestSeverityRank(t *testing.T) {
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityWarning) {
		t.Fatal("rank order invalid")
	}
	if SeverityRank("unknown") >= SeverityRank(SeverityWarning) {
		t.Fatal("unknown severity must rank below warning")
	}
}

func TestSortAlertsStable(t *testing.T) {
	now := time.Now()
	a := []Alert{
		{Key: "k1", Severity: SeverityWarning, FirstSeenAt: now.Add(-time.Minute)},
		{Key: "k2", Severity: SeverityCritical, FirstSeenAt: now.Add(-time.Hour)},
		{Key: "k3", Severity: SeverityCritical, FirstSeenAt: now},
	}
	SortAlerts(a)
	if a[0].Key != "k3" || a[1].Key != "k2" || a[2].Key != "k1" {
		t.Fatalf("unexpected order: %s %s %s", a[0].Key, a[1].Key, a[2].Key)
	}
}

func TestLegacyID(t *testing.T) {
	cases := []struct {
		a    Alert
		want string
	}{
		{Alert{Key: "low_balance:channel-3", ChannelID: intPtr(3)}, "bal_3"},
		{Alert{Key: "ratio_exceeded:channel-5:model-claude-sonnet-5", ChannelID: intPtr(5), Model: "claude-sonnet-5"}, "ratio_5_claude-sonnet-5"},
		{Alert{Key: "circuit_open:channel-3:model-gpt-5.5:group-2", ChannelID: intPtr(3), Model: "gpt-5.5"}, "cb_3_gpt-5.5"},
		{Alert{Key: "channel_disabled:channel-7", ChannelID: intPtr(7)}, "dis_7"},
		{Alert{Key: "pricing_sync_failed:channel-4", ChannelID: intPtr(4)}, "pricing_4"},
		{Alert{Key: "quality_check_failed:channel-5:model-m:stream", ChannelID: intPtr(5), Model: "m"}, "qc_5_m"},
	}
	for _, c := range cases {
		if got := LegacyID(c.a); got != c.want {
			t.Errorf("LegacyID(%q) = %q, want %q", c.a.Key, got, c.want)
		}
	}
}

func TestFormatDurationChinese(t *testing.T) {
	if got := FormatDuration(19 * time.Minute); got != "19 分钟" {
		t.Fatal(got)
	}
	if got := FormatDuration(2*time.Hour + 18*time.Minute); got != "2 小时 18 分钟" {
		t.Fatal(got)
	}
	if got := FormatDuration(42 * time.Minute); got != "42 分钟" {
		t.Fatal(got)
	}
}

func TestOverallSeverity(t *testing.T) {
	a := []Alert{
		{Severity: SeverityWarning},
		{Severity: SeverityCritical},
	}
	if got := OverallSeverity(a); got != SeverityCritical {
		t.Fatal(got)
	}
	if got := OverallSeverity(nil); got != "" {
		t.Fatal(got)
	}
}

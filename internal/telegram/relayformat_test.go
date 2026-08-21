package telegram

import (
	"strings"
	"testing"
	"time"
)

func TestHostOnlyStripsPathAndScheme(t *testing.T) {
	if got := hostOnly("https://api.relay.example.com/v1"); got != "api.relay.example.com" {
		t.Fatal(got)
	}
	if got := hostOnly("not a url"); got != "" {
		t.Fatal(got)
	}
}

func TestFormatRelayListEscapesAndMarks(t *testing.T) {
	bal := 2.5
	ratio := 1.2
	items := []RelaySummary{
		{ID: 3, Name: "Relay <A>", Host: "a.example.com", Healthy: true, Balance: &bal, Ratio: &ratio, CircuitState: "closed"},
		{ID: 4, Name: "Relay B", Healthy: false},
	}
	msg := FormatRelayList(items)
	if !strings.Contains(msg, "Relay &lt;A&gt;") {
		t.Fatalf("HTML not escaped: %s", msg)
	}
	if !strings.Contains(msg, "✅") || !strings.Contains(msg, "❌") {
		t.Fatalf("health marks missing: %s", msg)
	}
	if !strings.Contains(msg, "$2.50") || !strings.Contains(msg, "1.2000x") {
		t.Fatalf("metrics missing: %s", msg)
	}
}

func TestFormatRelayListEmpty(t *testing.T) {
	msg := FormatRelayList(nil)
	if !strings.Contains(msg, "暂无有效检测结果") {
		t.Fatal(msg)
	}
}

func TestFormatRelayDetailNoCredentialLeak(t *testing.T) {
	it := RelayDetail{
		RelaySummary: RelaySummary{ID: 5, Name: "Claude Relay", Host: "claude.example.com", Healthy: true, Balance: f64(3.14), CircuitState: "open"},
		Protocol:     "anthropic", RelayType: "newapi",
		Groups: []string{"默认分组"}, Requests24h: 100, SuccessRate: 0.97, AverageMS: 800, P95MS: 1200,
	}
	msg := FormatRelayDetail(it)
	if !strings.Contains(msg, "Anthropic 原生") {
		t.Fatalf("protocol missing: %s", msg)
	}
	if !strings.Contains(msg, "已熔断") {
		t.Fatalf("circuit missing: %s", msg)
	}
	for _, secret := range []string{"sk-", "Bearer", "token"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("credential leaked: %s", msg)
		}
	}
}

func TestFormatBalanceHealthRatioLists(t *testing.T) {
	now := time.Now()
	if got := FormatBalanceList(nil, nil); !strings.Contains(got, "暂无有效检测结果") {
		t.Fatal(got)
	}
	bal := 1.5
	bs := []BalanceSummary{{ChannelID: 1, Name: "A", Balance: &bal, CheckedAt: &now, MemberCount: 3}}
	if got := FormatBalanceList(bs, &now); !strings.Contains(got, "$1.50") {
		t.Fatal(got)
	}
	if got := FormatBalanceList(bs, &now); !strings.Contains(got, "3 个站点") {
		t.Fatal("station member count missing:\n" + got)
	}
	// 详情：不包含成员各自的余额数字
	if got := FormatBalanceDetail("supeai.cc", &bal, &now, []BalanceMember{
		{ChannelID: 31, Name: "supe-claude-MAX", Enabled: true},
		{ChannelID: 24, Name: "supe-Kiro-低缓", Enabled: false},
	}); !strings.Contains(got, "$1.50") || !strings.Contains(got, "supe-claude-MAX") || !strings.Contains(got, "⛔") {
		t.Fatal("detail format wrong:\n" + got)
	}
	lat := 300
	hs := []HealthSummary{{ChannelID: 1, Name: "A", Alive: true, LatencyMS: &lat}}
	if got := FormatHealthList(hs); !strings.Contains(got, "300ms") {
		t.Fatal(got)
	}
	r := 2.5
	rs := []RatioSummary{{ChannelID: 1, Name: "A", Model: "gpt-4o", Ratio: &r, Limit: 2.0}}
	if got := FormatRatioList(rs); !strings.Contains(got, "2.5000x") || !strings.Contains(got, "上限 2.0000x") {
		t.Fatal(got)
	}
}

func f64(v float64) *float64 { return &v }

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
	if !strings.Contains(msg, "🟢") || !strings.Contains(msg, "🔴") {
		t.Fatalf("health marks missing: %s", msg)
	}
	if !strings.Contains(msg, "$2.50") || !strings.Contains(msg, "1.20x") {
		t.Fatalf("metrics missing: %s", msg)
	}
	if !strings.Contains(msg, "💡 详情：/relay") {
		t.Fatalf("hint missing: %s", msg)
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
	bs := []BalanceSummary{{StationID: 5, ChannelID: 1, Name: "A", Balance: &bal, CheckedAt: &now, MemberCount: 3}}
	list := FormatBalanceList(bs, &now)
	if !strings.Contains(list, "$1.50") {
		t.Fatal(list)
	}
	if !strings.Contains(list, "3 个站点") {
		t.Fatal("station member count missing:\n" + list)
	}
	if !strings.Contains(list, "更新于") || !strings.Contains(list, "［ID 5］") {
		t.Fatal("list header or station id missing:\n" + list)
	}
	// 渠道 ID 不得出现在列表里（明细接口要的是中转站 ID）
	if strings.Contains(list, "ChannelID") {
		t.Fatal("list should show station id, not channel id:\n" + list)
	}
	// 详情：不包含成员各自的余额数字
	detail := FormatBalanceDetail("supeai.cc", &bal, &now, []BalanceMember{
		{ChannelID: 31, Name: "supe-claude-MAX", Enabled: true},
		{ChannelID: 24, Name: "supe-Kiro-低缓", Enabled: false},
	})
	if !strings.Contains(detail, "$1.50") || !strings.Contains(detail, "supe-claude-MAX") || !strings.Contains(detail, "⛔") || !strings.Contains(detail, "[31]") {
		t.Fatal("detail format wrong:\n" + detail)
	}
	lat := 300
	hs := []HealthSummary{{ChannelID: 1, Name: "A", Alive: true, LatencyMS: &lat}}
	if got := FormatHealthList(hs); !strings.Contains(got, "300ms") {
		t.Fatal(got)
	}
	r := 2.5
	rs := []RatioSummary{{ChannelID: 1, Name: "A", Model: "gpt-4o", Ratio: &r, Limit: 2.0}}
	if got := FormatRatioList(rs); !strings.Contains(got, "2.50x") || !strings.Contains(got, "上限 2.00x") {
		t.Fatal(got)
	}
	// 详情：超限标记
	if got := FormatRatioDetail("A", 2.0, []RatioDetailItem{
		{Model: "gpt-4o", Ratio: 2.5, Basis: "official", CheckedAt: now},
		{Model: "gpt-4o-mini", Ratio: 1.2, Basis: "baseline", CheckedAt: now},
	}); !strings.Contains(got, "2.50x") || !strings.Contains(got, "超限") || !strings.Contains(got, "官网价基准") {
		t.Fatal("ratio detail wrong:\n" + got)
	}
}

func f64(v float64) *float64 { return &v }

package api

import "testing"

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.247kan.com", "https://api.247kan.com"},
		{"https://api.247kan.com/", "https://api.247kan.com"},        // 去尾斜杠
		{"HTTPS://API.247kan.COM", "https://api.247kan.com"},         // scheme/host 小写
		{"https://api.247kan.com/v1", "https://api.247kan.com/v1"},   // 路径保留
		{"https://api.247kan.com/v1/", "https://api.247kan.com/v1"},  // 路径尾斜杠去除
		{"https://api.247kan.com?x=1", "https://api.247kan.com?x=1"}, // 查询保留
		{"https://SupeAI.cc", "https://supeai.cc"},                   // host 小写
		{"  https://api.247kan.com  ", "https://api.247kan.com"},     // 空白修剪
		{"api.247kan.com", "api.247kan.com"},                         // 无 scheme 退化为朴素去尾斜杠
		{"not a url at all", "not a url at all"},
	}
	for _, c := range cases {
		if got := normalizeBaseURL(c.in); got != c.want {
			t.Fatalf("normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAutoStationName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.247kan.com", "api.247kan.com"},
		{"https://api.247kan.com/v1", "api.247kan.com/v1"},
		{"https://SupeAI.cc/api/v1", "supeai.cc/api/v1"},
	}
	for _, c := range cases {
		if got := autoStationName(c.in); got != c.want {
			t.Fatalf("autoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSortStationsStable(t *testing.T) {
	stations := []*relayStation{
		{DisplayName: "zeta"}, {DisplayName: "alpha"}, {DisplayName: "mid"},
	}
	sortStations(stations)
	if stations[0].DisplayName != "alpha" || stations[1].DisplayName != "mid" || stations[2].DisplayName != "zeta" {
		t.Fatalf("sorted = %v", []string{stations[0].DisplayName, stations[1].DisplayName, stations[2].DisplayName})
	}
}

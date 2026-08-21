package telegram

import (
	"context"
	"strings"
	"testing"

	"smart-router/internal/checker"
	"smart-router/internal/quality"
)

// ===== 纯函数测试 =====

func TestParseSiteTestArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantModel  string
		wantTokens int
		wantUsage  bool // usageErr 非空
	}{
		{name: "empty", args: nil, wantModel: "", wantTokens: 0},
		{name: "model only", args: []string{"claude-opus-4-8"}, wantModel: "claude-opus-4-8"},
		{name: "tokens only", args: []string{"64"}, wantTokens: 64},
		{name: "model then tokens", args: []string{"claude-opus-4-8", "64"}, wantModel: "claude-opus-4-8", wantTokens: 64},
		{name: "tokens then model", args: []string{"64", "claude-opus-4-8"}, wantModel: "claude-opus-4-8", wantTokens: 64},
		{name: "negative tokens", args: []string{"-5"}, wantUsage: true},
		{name: "zero tokens", args: []string{"0"}, wantUsage: true},
		{name: "dup tokens", args: []string{"16", "32"}, wantUsage: true},
		{name: "extra arg", args: []string{"a", "b", "c"}, wantUsage: true},
		{name: "non-numeric third arg", args: []string{"m", "64", "x"}, wantUsage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, tokens, usageErr := parseSiteTestArgs(tc.args)
			if (usageErr != "") != tc.wantUsage {
				t.Fatalf("usageErr = %q, wantUsage=%v", usageErr, tc.wantUsage)
			}
			if usageErr == "" {
				if model != tc.wantModel || tokens != tc.wantTokens {
					t.Fatalf("model=%q tokens=%d, want %q/%d", model, tokens, tc.wantModel, tc.wantTokens)
				}
			}
		})
	}
}

func TestResolveSiteTestModel(t *testing.T) {
	up := &checker.Upstream{TestModel: "claude-opus-4-8"}
	mapping := map[string]string{"b-model": "B", "a-model": "A"}

	if got := resolveSiteTestModel("explicit", up, mapping); got != "explicit" {
		t.Fatalf("explicit model = %q", got)
	}
	if got := resolveSiteTestModel("", up, mapping); got != "claude-opus-4-8" {
		t.Fatalf("test_model fallback = %q", got)
	}
	up.TestModel = ""
	if got := resolveSiteTestModel("", up, mapping); got != "a-model" {
		t.Fatalf("first mapping key = %q, want a-model（字典序最小）", got)
	}
	if got := resolveSiteTestModel("", up, nil); got != "" {
		t.Fatalf("empty mapping = %q", got)
	}
}

func TestClampSiteTestTokens(t *testing.T) {
	if got := clampSiteTestTokens(0); got != siteTestDefaultTokens {
		t.Fatalf("clamp(0) = %d, want %d", got, siteTestDefaultTokens)
	}
	if got := clampSiteTestTokens(9999); got != siteTestMaxTokens {
		t.Fatalf("clamp(9999) = %d, want %d", got, siteTestMaxTokens)
	}
	if got := clampSiteTestTokens(256); got != 256 {
		t.Fatalf("clamp(256) = %d", got)
	}
}

func TestChatTimeout(t *testing.T) {
	up := &checker.Upstream{TimeoutTotalMS: 0}
	if got := chatTimeout(up); got != siteTestChatTimeout {
		t.Fatalf("default timeout = %v, want %v", got, siteTestChatTimeout)
	}
	up.TimeoutTotalMS = 30000
	if got := chatTimeout(up); got != 30e9 {
		t.Fatalf("channel timeout = %v, want 30s", got)
	}
	up.TimeoutTotalMS = 999999999 // 超上限
	if got := chatTimeout(up); got != siteTestMaxChatTimeout {
		t.Fatalf("capped timeout = %v, want %v", got, siteTestMaxChatTimeout)
	}
}

// ===== 报告格式化测试 =====

func TestFormatSiteTestReportFull(t *testing.T) {
	rep := siteTestReport{
		channelName:   "247-claudemax",
		protocol:      "anthropic",
		model:         "claude-opus-4-8",
		upstreamModel: "claude-opus-4-8",
		maxTokens:     128,
		balanceOK:     true,
		before:        48.62, mid: 48.60, after: 48.58,
		nonStream: chatOutcome{ev: &quality.ChatEvidence{
			HTTPStatus: 200, TTFBMS: 3000, TotalMS: 8000,
			Usage: quality.TokenUsage{PromptTokens: 8, CompletionTokens: 50, TotalTokens: 58, Present: true},
		}},
		stream: chatOutcome{ev: &quality.ChatEvidence{
			HTTPStatus: 200, TTFBMS: 2500, TotalMS: 9000, StreamEvents: 12, DoneReceived: true,
			Usage: quality.TokenUsage{PromptTokens: 8, CompletionTokens: 60, TotalTokens: 68, Present: true},
		}},
		costTotal: 0.04,
		ratioOK:   true,
		ratio:     2.5, basis: checker.BasisOfficial, estInPerM: 12.5, estOutPerM: 62.5,
	}
	out := formatSiteTestReport(rep)
	for _, want := range []string{"🧪", "247-claudemax", "anthropic", "claude-opus-4-8", "max_tokens：128",
		"$48.6200", "$48.5800", "✅ 200", "TTFT 3000ms", "SSE 12 事件", "[DONE] ✓",
		"余额差合计：$0.0400", "实测倍率：2.5000x", "官网价基准", "输入 $12.50/1M"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatSiteTestReportPartial(t *testing.T) {
	rep := siteTestReport{
		channelName:   "supe-claude-MAX",
		protocol:      "anthropic",
		model:         "claude-sonnet-5",
		upstreamModel: "claude-sonnet-5",
		maxTokens:     128,
		balanceErr:    "余额接口不可用",
		nonStream:     chatOutcome{err: context.DeadlineExceeded},
		stream:        chatOutcome{ev: &quality.ChatEvidence{HTTPStatus: 429}},
		ratioErr:      "余额读取失败，无法计算倍率",
	}
	out := formatSiteTestReport(rep)
	for _, want := range []string{"💰 余额：✗", "余额接口不可用",
		"非流式：✗", "context deadline exceeded", "流式：✗ upstream 429",
		"📐 实测倍率：✗", "余额读取失败，无法计算倍率"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}

func TestFormatSiteTestReportHTMLEScape(t *testing.T) {
	rep := siteTestReport{
		channelName:   `<bad> & "site"`,
		protocol:      "openai",
		model:         "m",
		upstreamModel: "m",
		maxTokens:     128,
		balanceErr:    `<err>`,
		nonStream:     chatOutcome{err: nil},
		ratioErr:      `<r>`,
	}
	out := formatSiteTestReport(rep)
	if strings.Contains(out, "<bad>") || strings.Contains(out, "<err>") || strings.Contains(out, "<r>") {
		t.Fatalf("dynamic fields not escaped:\n%s", out)
	}
	if !strings.Contains(out, "&lt;bad&gt;") {
		t.Fatalf("escaped name missing:\n%s", out)
	}
}

func TestFormatChatLineMissingEvidence(t *testing.T) {
	if out := formatChatLine("流式", chatOutcome{}); !strings.Contains(out, "✗ 无结果") {
		t.Fatalf("out = %q", out)
	}
}

func TestTruncateRunesSafe(t *testing.T) {
	s := "你好世界abcdef"
	if got := truncateRunesSafe(s, 4); got != "你好世界…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncateRunesSafe("abc", 10); got != "abc" {
		t.Fatalf("short string = %q", got)
	}
}

// ===== finalize 边界测试 =====

func TestSiteTestReportFinalize(t *testing.T) {
	// 余额失败 → 倍率失败
	rep := siteTestReport{balanceOK: false, balanceErr: "e"}
	rep.finalize(context.Background(), &SiteTestRunner{})
	if rep.ratioOK || !strings.Contains(rep.ratioErr, "余额读取失败") {
		t.Fatalf("ratio = %v/%q, want fail with balance error", rep.ratioOK, rep.ratioErr)
	}

	// 余额差为 0 → 提示提高 max_tokens
	rep = siteTestReport{balanceOK: true, before: 10, after: 10}
	rep.finalize(context.Background(), &SiteTestRunner{})
	if rep.ratioOK || !strings.Contains(rep.ratioErr, "max_tokens") {
		t.Fatalf("ratio = %v/%q, want zero-cost hint", rep.ratioOK, rep.ratioErr)
	}

	// 无 usage → 无法计算
	rep = siteTestReport{balanceOK: true, before: 10, after: 9.5,
		nonStream: chatOutcome{ev: &quality.ChatEvidence{HTTPStatus: 200}}}
	rep.finalize(context.Background(), &SiteTestRunner{})
	if rep.ratioOK || !strings.Contains(rep.ratioErr, "usage") {
		t.Fatalf("ratio = %v/%q, want usage error", rep.ratioOK, rep.ratioErr)
	}
}

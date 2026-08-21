package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/migrate"
	"smart-router/internal/quality"
	"smart-router/internal/safenet"
	"smart-router/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// newSiteTestDB 连接 TEST_DATABASE_URL 指定的真实 PG 并跑迁移（未设置则跳过）。
// 与 quality_test.go 的 setupQualityTestDB 同一口径。
func newSiteTestDB(t *testing.T) *store.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := migrate.Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &store.DB{Pool: pool}
}

// TestDefaultSiteTestMessage 消息默认值与透传。
func TestDefaultSiteTestMessage(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "hi"},
		{"hi", "hi"},
		{"你好，介绍一下你自己", "你好，介绍一下你自己"},
		{"  ", "  "}, // 仅空白不替换（显式输入）
	}
	for _, c := range cases {
		if got := defaultSiteTestMessage(c.in); got != c.want {
			t.Fatalf("defaultSiteTestMessage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestClampSiteTestTokens token 上限归一化。
func TestClampSiteTestTokens(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 128}, {-5, 128}, {64, 64}, {128, 128}, {512, 512}, {1024, 512},
	}
	for _, c := range cases {
		if got := clampSiteTestTokens(c.in); got != c.want {
			t.Fatalf("clampSiteTestTokens(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestFirstMappingKey 映射首个键（字典序最小，确定性）。
func TestFirstMappingKey(t *testing.T) {
	if got := firstMappingKey(nil); got != "" {
		t.Fatalf("firstMappingKey(nil) = %q, want empty", got)
	}
	if got := firstMappingKey(map[string]string{"b": "B", "a": "A", "c": "C"}); got != "a" {
		t.Fatalf("firstMappingKey = %q, want a", got)
	}
}

// TestEvidenceToSection 证据转换（成功/失败/HTTP 错误）。
func TestEvidenceToSection(t *testing.T) {
	// 成功（非流式）
	ev := &quality.ChatEvidence{
		HTTPStatus: 200, TTFBMS: 812, TotalMS: 1530, Text: "hi",
		Usage: quality.TokenUsage{PromptTokens: 12, CompletionTokens: 35, TotalTokens: 47, Present: true},
	}
	s := evidenceToSection(ev, nil)
	if !s.OK || s.TTFBMS != 812 || s.TotalMS != 1530 || s.TotalTokens != 47 || !s.UsagePresent {
		t.Fatalf("evidenceToSection(success) = %+v", s)
	}
	// 流式证据
	sev := &quality.ChatEvidence{HTTPStatus: 200, TTFBMS: 300, TotalMS: 900, StreamEvents: 42, DoneReceived: true}
	ss := evidenceToSection(sev, nil)
	if ss.StreamEvents != 42 || !ss.DoneReceived {
		t.Fatalf("evidenceToSection(stream) = %+v", ss)
	}
	// 请求错误（错误信息不含凭据）
	fs := evidenceToSection(nil, context.DeadlineExceeded)
	if fs.OK || fs.Error == "" {
		t.Fatalf("evidenceToSection(err) = %+v", fs)
	}
	// 上游非 200
	us := evidenceToSection(&quality.ChatEvidence{HTTPStatus: 401}, nil)
	if us.OK || us.Status != 401 || !strings.Contains(us.Error, "401") {
		t.Fatalf("evidenceToSection(401) = %+v", us)
	}
}

// TestBuildRatio 倍率分区聚合（余额失败/零成本/无 usage/baseline 兜底）。
func TestBuildRatio(t *testing.T) {
	h := &SiteTestHandler{}

	// 余额失败
	r := h.buildRatio(context.Background(), false, 0, chatSection{}, chatSection{}, "m")
	if r.OK || r.Error == "" {
		t.Fatalf("buildRatio(balance fail) = %+v", r)
	}
	// 余额差为 0
	r = h.buildRatio(context.Background(), true, 0, chatSection{}, chatSection{}, "m")
	if r.OK {
		t.Fatalf("buildRatio(zero cost) = %+v", r)
	}
	// 无 usage
	r = h.buildRatio(context.Background(), true, 0.006, chatSection{OK: true}, chatSection{OK: true}, "m")
	if r.OK {
		t.Fatalf("buildRatio(no usage) = %+v", r)
	}
	// 无官网价 → baseline + warning
	r = h.buildRatio(context.Background(), true, 0.006,
		chatSection{OK: true, UsagePresent: true, PromptTokens: 12, CompletionTokens: 35},
		chatSection{OK: true, UsagePresent: true, PromptTokens: 12, CompletionTokens: 33}, "m")
	if !r.OK || r.Basis != checker.BasisBaseline || r.Warning == "" {
		t.Fatalf("buildRatio(baseline) = %+v", r)
	}
}

// TestBuildRatioOfficial 有官网价时 official 口径与推算单价。
func TestBuildRatioOfficial(t *testing.T) {
	h := &SiteTestHandler{db: newSiteTestDB(t)}
	// 价格库先写一行
	if _, err := h.db.Pool.Exec(context.Background(),
		`INSERT INTO model_prices (model, input_price_per_m, output_price_per_m) VALUES ('gpt-test', 2.5, 10.0) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed model_prices: %v", err)
	}
	r := h.buildRatio(context.Background(), true, 0.005,
		chatSection{OK: true, UsagePresent: true, PromptTokens: 100, CompletionTokens: 400},
		chatSection{}, "gpt-test")
	if !r.OK || r.Basis != checker.BasisOfficial || r.RealRatio <= 0 {
		t.Fatalf("buildRatio(official) = %+v", r)
	}
	if r.EstimatedInputPerM <= 0 || r.EstimatedOutputPerM <= 0 {
		t.Fatalf("estimated prices missing: %+v", r)
	}
	// cost 0.005 = 100/1M*2.5*ratio + 400/1M*10*ratio → ratio ≈ 1.176
	// 只断言正数与 official 口径，具体数值由 ComputeRealRatio 单测覆盖
}

// TestRunChatIntegration 用 httptest 假上游验证聊天探测（非流式 + 流式）。
func TestRunChatIntegration(t *testing.T) {
	// 假上游：非流式返回 JSON，流式返回 SSE。
	// 加 50ms 延迟：本机回环响应 <1ms，TTFB/总耗时毫秒值会取整为 0 无法断言。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"model\":\"gpt-test\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2,\"total_tokens\":14}}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-test","choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":12,"completion_tokens":35,"total_tokens":47}}`))
	}))
	defer srv.Close()

	h := &SiteTestHandler{safenetOpts: safenet.Options{AllowHTTP: true, AllowPrivate: true}}
	ch := &quality.Channel{BaseURL: srv.URL, Protocol: "openai", APIKey: "sk-test"}
	sc := quality.ProbeScenario{Model: "gpt-test", Messages: []quality.ProbeMessage{{Role: "user", Content: "hi"}}, MaxTokens: 32}

	// 非流式
	ev, err := quality.RunChat(context.Background(), ch, sc, siteTestChatTimeout, h.testClient())
	if err != nil {
		t.Fatalf("non-stream chat: %v", err)
	}
	if ev.TTFBMS <= 0 || ev.TotalMS <= 0 || ev.Usage.TotalTokens != 47 || ev.Text != "hi" {
		t.Fatalf("non-stream evidence = %+v", ev)
	}
	// 流式
	sc.Stream = true
	ev, err = quality.RunChat(context.Background(), ch, sc, siteTestChatTimeout, h.testClient())
	if err != nil {
		t.Fatalf("stream chat: %v", err)
	}
	if !ev.DoneReceived || ev.Text != "hi" || ev.Usage.PromptTokens != 12 {
		t.Fatalf("stream evidence = %+v", ev)
	}
}

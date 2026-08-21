package quality

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"smart-router/internal/migrate"
	"smart-router/internal/safenet"
	"smart-router/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// newFullMockUpstream 完整 mock 上游：/v1/models + 非流式 + 流式聊天。
func newFullMockUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"gpt-4o"}]}`)
		case r.URL.Path == "/v1/chat/completions":
			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if stream, _ := req["stream"].(bool); stream {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				for _, c := range []string{
					`data: {"id":"c1","model":"gpt-4o","choices":[{"delta":{"content":"po"}}]}` + "\n\n",
					`data: {"id":"c2","model":"gpt-4o","choices":[{"delta":{"content":"ng"}}]}` + "\n\n",
					"data: [DONE]\n\n",
				} {
					fmt.Fprint(w, c)
					flusher.Flush()
				}
				return
			}
			fmt.Fprint(w, openaiNonStreamResponse)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setupExecutorDB 建测试库并注册站点。
func setupExecutorDB(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func TestExecutorFullHappyPath(t *testing.T) {
	srv := newFullMockUpstream(t)
	pool := setupExecutorDB(t)
	ctx := context.Background()

	// 注册站点
	var channelID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key, model_mapping)
		VALUES ('Executor Test Relay', $1, '', 'sk-test', '{"gpt-4o":"gpt-4o"}')
		ON CONFLICT (name) DO UPDATE SET base_url = EXCLUDED.base_url, api_key = EXCLUDED.api_key, model_mapping = EXCLUDED.model_mapping
		RETURNING id
	`, srv.URL).Scan(&channelID); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}

	repo := NewPostgresRepository(&store.DB{Pool: pool})
	exec := NewExecutor(&store.DB{Pool: pool}, repo, nil, "", "gpt-4o", zap.NewNop())
	// 测试上游为本地 httptest（http+环回）：放宽 SSRF 校验，与开发配置口径一致
	exec.SetSafenetOptions(safenet.Options{AllowHTTP: true, AllowPrivate: true})

	run, err := repo.Create(ctx, channelID, "gpt-4o", "full", "hash")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, err := repo.ClaimNext(ctx, "worker-test")
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}

	if err := exec.Execute(ctx, claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, results, err := repo.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RunCompleted {
		t.Fatalf("status = %s, want completed (error: %s)", got.Status, got.Error)
	}
	if got.OverallStatus != OverallGood {
		t.Fatalf("overall = %s, want good", got.OverallStatus)
	}
	// 五个阶段全部有结果
	stageSet := map[string]bool{}
	for _, r := range results {
		stageSet[r.Stage] = true
	}
	for _, s := range FullStages {
		if !stageSet[s] {
			t.Fatalf("stage %s missing from results: %+v", s, results)
		}
	}
	// 清理
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_results WHERE run_id = $1`, run.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, run.ID)
}

func TestExecutorUpstreamFailureOverallFailed(t *testing.T) {
	// 上游 401 → connectivity/protocol/stream 均失败 → overall failed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	}))
	t.Cleanup(srv.Close)
	pool := setupExecutorDB(t)
	ctx := context.Background()

	var channelID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key, model_mapping)
		VALUES ('Executor Fail Relay', $1, '', 'sk-bad', '{"gpt-4o":"gpt-4o"}')
		ON CONFLICT (name) DO UPDATE SET base_url = EXCLUDED.base_url
		RETURNING id
	`, srv.URL).Scan(&channelID); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}

	repo := NewPostgresRepository(&store.DB{Pool: pool})
	exec := NewExecutor(&store.DB{Pool: pool}, repo, nil, "", "gpt-4o", zap.NewNop())
	exec.SetSafenetOptions(safenet.Options{AllowHTTP: true, AllowPrivate: true})

	run, err := repo.Create(ctx, channelID, "gpt-4o", "full", "hash")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, _ := repo.ClaimNext(ctx, "worker-test")
	if err := exec.Execute(ctx, claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, _, _ := repo.Get(ctx, run.ID)
	if got.Status != RunCompleted {
		t.Fatalf("status = %s", got.Status)
	}
	if got.OverallStatus != OverallFailed {
		t.Fatalf("overall = %s, want failed", got.OverallStatus)
	}

	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_results WHERE run_id = $1`, run.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, run.ID)
}

func TestExecutorBasicStopsAfterStream(t *testing.T) {
	srv := newFullMockUpstream(t)
	pool := setupExecutorDB(t)
	ctx := context.Background()

	var channelID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key, model_mapping)
		VALUES ('Executor Basic Relay', $1, '', 'sk-test', '{"gpt-4o":"gpt-4o"}')
		ON CONFLICT (name) DO UPDATE SET base_url = EXCLUDED.base_url
		RETURNING id
	`, srv.URL).Scan(&channelID); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}

	repo := NewPostgresRepository(&store.DB{Pool: pool})
	exec := NewExecutor(&store.DB{Pool: pool}, repo, nil, "", "gpt-4o", zap.NewNop())
	exec.SetSafenetOptions(safenet.Options{AllowHTTP: true, AllowPrivate: true})

	run, err := repo.Create(ctx, channelID, "gpt-4o", "basic", "hash")
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimed, _ := repo.ClaimNext(ctx, "worker-test")
	if err := exec.Execute(ctx, claimed); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, results, _ := repo.Get(ctx, run.ID)
	if got.Status != RunCompleted {
		t.Fatalf("status = %s", got.Status)
	}
	if len(results) != 3 {
		t.Fatalf("basic depth must have 3 stages, got %d: %+v", len(results), results)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_results WHERE run_id = $1`, run.ID)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, run.ID)
}

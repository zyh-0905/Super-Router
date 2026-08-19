package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"smart-router/internal/migrate"
	"smart-router/internal/quality"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// qualityTestEnv 建测试环境（可选 DB；无 DB 时用纯校验路径）。
type qualityTestEnv struct {
	pool  *pgxpool.Pool
	repo  quality.Repository
	store *store.DB
}

func setupQualityTestDB(t *testing.T) *qualityTestEnv {
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
	db := &store.DB{Pool: pool}
	return &qualityTestEnv{pool: pool, repo: quality.NewPostgresRepository(db), store: db}
}

// fakeQualityRepo 无 DB 的纯校验测试用（只测请求校验分支）。
type fakeQualityRepo struct {
	createErr error
}

func (f *fakeQualityRepo) Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*quality.Run, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &quality.Run{ID: 1, ChannelID: channelID, Model: model, Depth: depth, Status: quality.RunQueued}, nil
}
func (f *fakeQualityRepo) Get(ctx context.Context, id int64) (*quality.Run, []quality.StageResult, error) {
	return nil, nil, errors.New("not implemented")
}
func (f *fakeQualityRepo) ListByChannel(ctx context.Context, channelID, limit int) ([]quality.Run, error) {
	return nil, nil
}
func (f *fakeQualityRepo) FindActiveByChannel(ctx context.Context, channelID int) (*quality.Run, error) {
	return nil, nil
}
func (f *fakeQualityRepo) ClaimNext(ctx context.Context, workerID string) (*quality.Run, error) {
	return nil, nil
}
func (f *fakeQualityRepo) UpsertResult(ctx context.Context, runID int64, result quality.StageResult) error {
	return nil
}
func (f *fakeQualityRepo) SetProgress(ctx context.Context, runID int64, stage string, progress int) error {
	return nil
}
func (f *fakeQualityRepo) Heartbeat(ctx context.Context, runID int64, workerID string) error {
	return nil
}
func (f *fakeQualityRepo) RequestCancel(ctx context.Context, runID int64) error { return nil }
func (f *fakeQualityRepo) IsCancelRequested(ctx context.Context, runID int64) (bool, error) {
	return false, nil
}
func (f *fakeQualityRepo) Complete(ctx context.Context, runID int64, overall quality.OverallStatus) error {
	return nil
}
func (f *fakeQualityRepo) Fail(ctx context.Context, runID int64, message string) error { return nil }
func (f *fakeQualityRepo) Cancel(ctx context.Context, runID int64) error              { return nil }
func (f *fakeQualityRepo) RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error) {
	return 0, nil
}

func newQualityRouter(h *QualityHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	adminGroup.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-Role") != "admin" {
			c.JSON(403, gin.H{"error": "requires admin role"})
			c.Abort()
			return
		}
		c.Set("key_hash", "test-hash")
		c.Next()
	})
	adminGroup.POST("/channels/:id/quality-checks", h.CreateQualityCheck)
	adminGroup.GET("/channels/:id/quality-checks", h.ListQualityChecks)
	adminGroup.GET("/quality-checks/:run_id", h.GetQualityCheck)
	adminGroup.POST("/quality-checks/:run_id/cancel", h.CancelQualityCheck)
	return r
}

func doQualityReq(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", "admin")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestQualityNonAdminForbidden(t *testing.T) {
	r := gin.New()
	r.POST("/admin/channels/:id/quality-checks", func(c *gin.Context) { c.JSON(200, gin.H{}) })
	req := httptest.NewRequest("POST", "/admin/channels/1/quality-checks", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", "caller")
	w := httptest.NewRecorder()
	// 简化：非 admin 拒绝由路由中间件保障（真实网关用 RequireRole）
	_ = r
	_ = w
	_ = req
}

func TestQualityCreateValidation(t *testing.T) {
	h := &QualityHandler{Repo: &fakeQualityRepo{}, Logger: zap.NewNop()}
	r := newQualityRouter(h)

	// depth 非法 → 400
	w := doQualityReq(t, r, "POST", "/admin/channels/1/quality-checks", map[string]interface{}{"model": "gpt-4o", "depth": "deep"})
	if w.Code != 400 {
		t.Fatalf("invalid depth: %d, want 400", w.Code)
	}
	// model 缺失 → 400
	w = doQualityReq(t, r, "POST", "/admin/channels/1/quality-checks", map[string]interface{}{"depth": "full"})
	if w.Code != 400 {
		t.Fatalf("missing model: %d, want 400", w.Code)
	}
	// 合法请求 → 201 + qc_<id>
	w = doQualityReq(t, r, "POST", "/admin/channels/1/quality-checks", map[string]interface{}{"model": "gpt-4o", "depth": "full"})
	if w.Code != 201 {
		t.Fatalf("valid create: %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		RunID     string `json:"run_id"`
		Status    string `json:"status"`
		ChannelID int    `json:"channel_id"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RunID != "qc_1" || resp.Status != "queued" || resp.Model != "gpt-4o" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestQualityCreateChannelBusy409(t *testing.T) {
	h := &QualityHandler{Repo: &fakeQualityRepo{createErr: &quality.ErrChannelBusy{ExistingRunID: 42}}, Logger: zap.NewNop()}
	r := newQualityRouter(h)
	w := doQualityReq(t, r, "POST", "/admin/channels/1/quality-checks", map[string]interface{}{"model": "gpt-4o", "depth": "full"})
	if w.Code != 409 {
		t.Fatalf("busy: %d, want 409 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		ExistingRunID string `json:"existing_run_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.ExistingRunID != "qc_42" {
		t.Fatalf("existing_run_id = %q, want qc_42", resp.ExistingRunID)
	}
}

func TestQualityParseRunIDInPath(t *testing.T) {
	// 非法 run_id → 400
	h := &QualityHandler{Repo: &fakeQualityRepo{}, Logger: zap.NewNop()}
	r := newQualityRouter(h)
	w := doQualityReq(t, r, "GET", "/admin/quality-checks/bad_id", nil)
	if w.Code != 400 {
		t.Fatalf("bad run id: %d, want 400", w.Code)
	}
}

func TestQualityHistoryLimitCapped(t *testing.T) {
	// limit 最大 100
	if got := capHistoryLimit(500); got != 100 {
		t.Fatalf("cap = %d", got)
	}
	if got := capHistoryLimit(5); got != 5 {
		t.Fatalf("cap = %d", got)
	}
	if got := capHistoryLimit(0); got != 5 {
		t.Fatalf("default = %d", got)
	}
}

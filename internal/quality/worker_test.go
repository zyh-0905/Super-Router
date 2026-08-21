package quality

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"smart-router/internal/store"

	"go.uber.org/zap"
)

// fakeRepo 内存假 Repository（Worker 调度语义测试）。
type fakeRepo struct {
	mu       sync.Mutex
	claims   []*Run
	next     []*Run
	failures map[int64]string
	execErr  error
}

func (f *fakeRepo) Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeRepo) Get(ctx context.Context, id int64) (*Run, []StageResult, error) { return nil, nil, nil }
func (f *fakeRepo) ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error) {
	return nil, nil
}
func (f *fakeRepo) FindActiveByChannel(ctx context.Context, channelID int) (*Run, error) {
	return nil, nil
}
func (f *fakeRepo) ClaimNext(ctx context.Context, workerID string) (*Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.next) == 0 {
		return nil, nil
	}
	r := f.next[0]
	f.next = f.next[1:]
	r.Status = RunRunning
	r.WorkerID = workerID
	f.claims = append(f.claims, r)
	return r, nil
}
func (f *fakeRepo) UpsertResult(ctx context.Context, runID int64, workerID string, result StageResult) error {
	return nil
}
func (f *fakeRepo) SetProgress(ctx context.Context, runID int64, stage string, progress int) error {
	return nil
}
func (f *fakeRepo) Heartbeat(ctx context.Context, runID int64, workerID string) error { return nil }
func (f *fakeRepo) RequestCancel(ctx context.Context, runID int64) error             { return nil }
func (f *fakeRepo) CancelIfRequested(ctx context.Context, runID int64, workerID string) (bool, error) {
	return false, nil
}
func (f *fakeRepo) Complete(ctx context.Context, runID int64, workerID string, overall OverallStatus) error {
	return nil
}
func (f *fakeRepo) Fail(ctx context.Context, runID int64, workerID string, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures == nil {
		f.failures = map[int64]string{}
	}
	f.failures[runID] = message
	return nil
}
func (f *fakeRepo) Cancel(ctx context.Context, runID int64, workerID string) error { return nil }
func (f *fakeRepo) RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error) {
	return 0, nil
}

// fakeExecutor 立即成功/失败的执行器。
type fakeExecutor struct {
	err error
}

func (f *fakeExecutor) Execute(ctx context.Context, run *Run) error { return f.err }

func TestWorkerClaimAndExecute(t *testing.T) {
	repo := &fakeRepo{next: []*Run{
		{ID: 1, ChannelID: 1, Model: "gpt-4o", Depth: "basic"},
		{ID: 2, ChannelID: 1, Model: "gpt-4o", Depth: "basic"},
	}}
	exec := &fakeExecutor{}
	w := NewWorker(repo, exec, "w1", zap.NewNop())

	ctx := context.Background()
	run, _ := repo.ClaimNext(ctx, "w1")
	w.executeTask(ctx, run)
	run2, _ := repo.ClaimNext(ctx, "w1")
	w.executeTask(ctx, run2)

	if len(repo.claims) != 2 {
		t.Fatalf("claims = %d", len(repo.claims))
	}
}

func TestWorkerExecuteFailureMarksFailed(t *testing.T) {
	repo := &fakeRepo{next: []*Run{{ID: 3, ChannelID: 1, Model: "gpt-4o", Depth: "basic"}}}
	exec := &fakeExecutor{err: errors.New("boom")}
	w := NewWorker(repo, exec, "w1", zap.NewNop())

	ctx := context.Background()
	run, _ := repo.ClaimNext(ctx, "w1")
	w.executeTask(ctx, run)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if msg, ok := repo.failures[3]; !ok || msg != "boom" {
		t.Fatalf("failures = %v, want run 3 marked failed with boom", repo.failures)
	}
}

func TestPublisherChannelFormat(t *testing.T) {
	if got := RunChannel(42); got != "quality:run:42" {
		t.Fatal(got)
	}
}

func TestPublisherWithoutRedisFails(t *testing.T) {
	p := NewRedisPublisher(nil)
	if err := p.Publish(context.Background(), Event{Type: "x", RunID: "qc_1"}); err == nil {
		t.Fatal("expected error without redis")
	}
}

func TestPublisherInvalidRunID(t *testing.T) {
	p := &RedisPublisher{}
	if err := p.Publish(context.Background(), Event{Type: "x", RunID: "bad"}); err == nil {
		t.Fatal("expected invalid run id error")
	}
}

func TestWorkerStaleRecoveryOnStartup(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool := setupTestPool(t)
	ctx := context.Background()

	var channelID int
	_ = pool.QueryRow(ctx, `SELECT id FROM upstreams WHERE name = 'Quality Test Relay'`).Scan(&channelID)
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, attempt_count, worker_id, heartbeat_at)
		VALUES ($1, 'gpt-4o', 'basic', 'running', 1, 'dead', NOW() - INTERVAL '10 minutes')
		RETURNING id
	`, channelID).Scan(&id); err != nil {
		t.Fatalf("insert: %v", err)
	}

	repo := NewPostgresRepository(&store.DB{Pool: pool})
	w := NewWorker(repo, &Executor{}, "w1", zap.NewNop())
	n, err := w.RecoverStaleOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, id)
}

package quality

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"smart-router/internal/migrate"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// setupTestPool 连接测试库并执行迁移。
func setupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := migrate.Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 种子站点（外键）
	if _, err := pool.Exec(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key)
		VALUES ('Quality Test Relay', 'https://q.example.com', '', '')
		ON CONFLICT (name) DO NOTHING
	`); err != nil {
		t.Fatalf("seed upstream: %v", err)
	}
	var channelID int
	_ = pool.QueryRow(ctx, `SELECT id FROM upstreams WHERE name = 'Quality Test Relay'`).Scan(&channelID)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_results`)
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs`)
	return pool
}

func TestRepositoryLifecycle(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := &PostgresRepository{Pool: pool}

	var channelID int
	if err := pool.QueryRow(ctx, `SELECT id FROM upstreams WHERE name = 'Quality Test Relay'`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}

	// Create → queued
	run, err := repo.Create(ctx, channelID, "gpt-4o", "full", "hash1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.Status != RunQueued {
		t.Fatalf("status = %s", run.Status)
	}

	// 活跃任务唯一约束 → ErrChannelBusy
	_, err = repo.Create(ctx, channelID, "gpt-4o", "full", "hash1")
	var busy *ErrChannelBusy
	if !errors.As(err, &busy) {
		t.Fatalf("second create err = %v, want ErrChannelBusy", err)
	}

	// ClaimNext → running + attempt_count=1
	claimed, err := repo.ClaimNext(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != run.ID || claimed.Status != RunRunning || claimed.AttemptCount != 1 {
		t.Fatalf("claimed = %+v", claimed)
	}

	// 无更多任务时 ClaimNext 返回 nil
	if next, err := repo.ClaimNext(ctx, "worker-1"); err != nil || next != nil {
		t.Fatalf("empty claim = %+v, %v", next, err)
	}

	// UpsertResult + Get
	res := StageResult{Stage: StageConnectivity, CheckName: "models", Status: StatusPassed, LatencyMS: i32(42), Details: map[string]interface{}{"latency_ms": 42}}
	if err := repo.UpsertResult(ctx, run.ID, "worker-1", res); err != nil {
		t.Fatalf("upsert result: %v", err)
	}
	got, results, err := repo.Get(ctx, run.ID)
	if err != nil || len(results) != 1 || results[0].Status != StatusPassed {
		t.Fatalf("get = %+v %+v %v", got, results, err)
	}

	// 所有权谓词：其它 worker 不可写结果/终态（C3）
	if err := repo.UpsertResult(ctx, run.ID, "intruder", res); !errors.Is(err, ErrLostOwnership) {
		t.Fatalf("intruder upsert err = %v, want ErrLostOwnership", err)
	}
	if err := repo.Complete(ctx, run.ID, "intruder", OverallGood); !errors.Is(err, ErrLostOwnership) {
		t.Fatalf("intruder complete err = %v, want ErrLostOwnership", err)
	}

	// Heartbeat + Complete
	if err := repo.Heartbeat(ctx, run.ID, "worker-1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := repo.Complete(ctx, run.ID, "worker-1", OverallGood); err != nil {
		t.Fatalf("complete: %v", err)
	}
	got, _, _ = repo.Get(ctx, run.ID)
	if got.Status != RunCompleted || got.OverallStatus != OverallGood {
		t.Fatalf("after complete = %+v", got)
	}

	// ListByChannel
	list, err := repo.ListByChannel(ctx, channelID, 5)
	if err != nil || len(list) != 1 || list[0].ID != run.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestStaleRecovery(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := &PostgresRepository{Pool: pool}

	var channelID int
	if err := pool.QueryRow(ctx, `SELECT id FROM upstreams WHERE name = 'Quality Test Relay'`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}

	// 手动插入超时 running 任务（attempt_count 未超限）
	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, attempt_count, worker_id, heartbeat_at)
		VALUES ($1, 'gpt-4o', 'basic', 'running', 1, 'dead-worker', NOW() - INTERVAL '10 minutes')
		RETURNING id
	`, channelID).Scan(&id); err != nil {
		t.Fatalf("insert stale: %v", err)
	}

	recovered, err := repo.RecoverStale(ctx, time.Now().Add(-2*time.Minute), 2)
	if err != nil {
		t.Fatalf("recover stale: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	var status RunStatus
	_ = pool.QueryRow(ctx, `SELECT status FROM quality_check_runs WHERE id = $1`, id).Scan(&status)
	if status != RunQueued {
		t.Fatalf("status after recovery = %s, want queued", status)
	}
	// 回收后该任务仍占用活跃唯一索引，先清理再测超限场景
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, id)

	// 超过重试上限 → expired
	var id2 int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, attempt_count, worker_id, heartbeat_at)
		VALUES ($1, 'gpt-4o', 'basic', 'running', 2, 'dead-worker', NOW() - INTERVAL '10 minutes')
		RETURNING id
	`, channelID).Scan(&id2); err != nil {
		t.Fatalf("insert stale2: %v", err)
	}
	if _, err := repo.RecoverStale(ctx, time.Now().Add(-2*time.Minute), 2); err != nil {
		t.Fatalf("recover stale2: %v", err)
	}
	_ = pool.QueryRow(ctx, `SELECT status FROM quality_check_runs WHERE id = $1`, id2).Scan(&status)
	if status != RunExpired {
		t.Fatalf("status after over-limit recovery = %s, want expired", status)
	}

	// 清理：删除测试任务
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id IN ($1, $2)`, id, id2)
}

func TestClaimNextConcurrentNoDuplicate(t *testing.T) {
	pool := setupTestPool(t)
	ctx := context.Background()
	repo := &PostgresRepository{Pool: pool}

	var channelID int
	if err := pool.QueryRow(ctx, `SELECT id FROM upstreams WHERE name = 'Quality Test Relay'`).Scan(&channelID); err != nil {
		t.Fatalf("channel: %v", err)
	}
	// 8 个不同站点各插入一个排队任务（活跃唯一索引按站点隔离）
	ids := make([]int64, 0, 8)
	for i := 0; i < 8; i++ {
		var chID int
		if err := pool.QueryRow(ctx, `
			INSERT INTO upstreams (name, base_url, access_token, api_key)
			VALUES ($1, 'https://q.example.com', '', '')
			ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		`, "Quality Relay "+string(rune('A'+i))).Scan(&chID); err != nil {
			t.Fatalf("seed channel %d: %v", i, err)
		}
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO quality_check_runs (channel_id, model, depth, status)
			VALUES ($1, 'gpt-4o', 'basic', 'queued')
			RETURNING id
		`, chID).Scan(&id); err != nil {
			t.Fatalf("insert queued: %v", err)
		}
		ids = append(ids, id)
	}

	// 4 个 worker 并发领取，无重复
	const workers = 4
	claimed := make(chan int64, len(ids))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			for {
				run, err := repo.ClaimNext(ctx, workerID)
				if err != nil || run == nil {
					return
				}
				claimed <- run.ID
			}
		}(string(rune('a' + w)))
	}
	wg.Wait()
	close(claimed)

	seen := map[int64]bool{}
	for id := range claimed {
		if seen[id] {
			t.Fatalf("duplicate claim of run %d", id)
		}
		seen[id] = true
	}
	if len(seen) != len(ids) {
		t.Fatalf("claimed %d of %d runs", len(seen), len(ids))
	}

	// 清理
	for _, id := range ids {
		_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE id = $1`, id)
	}
}

func i32(v int) *int { return &v }

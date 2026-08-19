package alert

import (
	"context"
	"os"
	"testing"
	"time"

	"smart-router/internal/migrate"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// TestSQLReconcileLifecycle 告警生命周期 PostgreSQL 集成测试：
// 新出现 / 持续计数 / 升级 / 恢复 / 恢复后再现 / advisory lock 互斥。
// 需要 TEST_DATABASE_URL 指向已执行迁移的测试库。
func TestSQLReconcileLifecycle(t *testing.T) {
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
	defer pool.Close()

	// 自包含：先执行全部迁移（幂等）
	if err := migrate.Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	// 清理历史告警，保证测试确定性
	if _, err := pool.Exec(ctx, `DELETE FROM alert_events`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// 种子数据：分组 + 两个站点（alert_events.channel_id 外键）
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_groups (name) VALUES ('集成测试分组')
		ON CONFLICT (name) DO NOTHING
	`); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key)
		VALUES ('Test Relay A', 'https://a.example.com', '', ''),
		       ('Test Relay B', 'https://b.example.com', '', '')
		ON CONFLICT (name) DO NOTHING
	`); err != nil {
		t.Fatalf("seed upstreams: %v", err)
	}

	store := &SQLStore{Pool: pool}
	now := time.Now()

	low := Alert{
		Key: "low_balance:channel-1", Type: TypeLowBalance, Severity: SeverityCritical,
		ChannelID: intPtr(1), Title: "余额不足", Message: "余额不足: A 剩余 $0.5",
		CurrentValue: fPtr(0.5), ThresholdValue: fPtr(1.0), Unit: "USD",
		FirstSeenAt: now, LastSeenAt: now,
	}
	ratio := Alert{
		Key: "ratio_exceeded:channel-2:model-m", Type: TypeRatioExceeded, Severity: SeverityWarning,
		ChannelID: intPtr(2), Model: "m", Title: "倍率超限",
		Message:      "倍率超标: B m 实测 2.5x 超过上限 2.0x",
		CurrentValue: fPtr(2.5), ThresholdValue: fPtr(2.0), Unit: "x",
		FirstSeenAt: now, LastSeenAt: now,
	}

	// 1. 新 key 创建 active
	if err := store.Reconcile(ctx, []Alert{low, ratio}, now); err != nil {
		t.Fatalf("reconcile #1: %v", err)
	}
	assertActiveCount(t, pool, 2)

	var occ int
	var sev string
	if err := pool.QueryRow(ctx, `
		SELECT occurrence_count, severity FROM alert_events WHERE alert_key = 'low_balance:channel-1'
	`).Scan(&occ, &sev); err != nil {
		t.Fatalf("query low: %v", err)
	}
	if occ != 1 || sev != "critical" {
		t.Fatalf("low occurrence=%d sev=%s, want 1/critical", occ, sev)
	}

	// 2. 相同 key 更新 occurrence_count，不创建重复 active
	later := now.Add(time.Minute)
	if err := store.Reconcile(ctx, []Alert{low, ratio}, later); err != nil {
		t.Fatalf("reconcile #2: %v", err)
	}
	assertActiveCount(t, pool, 2)
	var lastSeen time.Time
	if err := pool.QueryRow(ctx, `
		SELECT occurrence_count, last_seen_at FROM alert_events WHERE alert_key = 'low_balance:channel-1'
	`).Scan(&occ, &lastSeen); err != nil {
		t.Fatalf("query low #2: %v", err)
	}
	if occ != 2 {
		t.Fatalf("occurrence after repeat = %d, want 2", occ)
	}
	if !lastSeen.After(now) {
		t.Fatalf("last_seen_at not advanced: %v", lastSeen)
	}

	// 3. warning → critical 升级并记录 escalated_at
	ratioUp := ratio
	ratioUp.Severity = SeverityCritical
	if err := store.Reconcile(ctx, []Alert{low, ratioUp}, later.Add(time.Minute)); err != nil {
		t.Fatalf("reconcile #3: %v", err)
	}
	var mdJSON []byte
	if err := pool.QueryRow(ctx, `
		SELECT severity, COALESCE(metadata::text, '{}')
		FROM alert_events WHERE alert_key = 'ratio_exceeded:channel-2:model-m'
	`).Scan(&sev, &mdJSON); err != nil {
		t.Fatalf("query ratio: %v", err)
	}
	if sev != "critical" {
		t.Fatalf("severity after escalate = %s, want critical", sev)
	}
	if !contains(mdJSON, "escalated_at") {
		t.Fatalf("metadata missing escalated_at: %s", mdJSON)
	}

	// 4. active 本轮消失 → recovered
	if err := store.Reconcile(ctx, []Alert{low}, later.Add(2*time.Minute)); err != nil {
		t.Fatalf("reconcile #4: %v", err)
	}
	assertActiveCount(t, pool, 1)
	var status string
	var recoveredAt *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT status, recovered_at FROM alert_events WHERE alert_key = 'ratio_exceeded:channel-2:model-m'
	`).Scan(&status, &recoveredAt); err != nil {
		t.Fatalf("query recovered ratio: %v", err)
	}
	if status != "recovered" || recoveredAt == nil {
		t.Fatalf("status=%s recovered_at=%v, want recovered with timestamp", status, recoveredAt)
	}

	// 5. 恢复后再次出现 → 新 active 周期（两行：recovered + active）
	if err := store.Reconcile(ctx, []Alert{low, ratio}, later.Add(3*time.Minute)); err != nil {
		t.Fatalf("reconcile #5: %v", err)
	}
	assertActiveCount(t, pool, 2)
	var totalCycles int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM alert_events WHERE alert_key = 'ratio_exceeded:channel-2:model-m'
	`).Scan(&totalCycles); err != nil {
		t.Fatalf("count cycles: %v", err)
	}
	if totalCycles != 2 {
		t.Fatalf("cycles = %d, want 2 (recovered + new active)", totalCycles)
	}
	// 新周期从 1 重新计数
	if err := pool.QueryRow(ctx, `
		SELECT occurrence_count FROM alert_events
		WHERE alert_key = 'ratio_exceeded:channel-2:model-m' AND status = 'active'
	`).Scan(&occ); err != nil {
		t.Fatalf("query new cycle: %v", err)
	}
	if occ != 1 {
		t.Fatalf("new cycle occurrence = %d, want 1", occ)
	}

	// 6. advisory lock 互斥：他方持锁时 Reconcile 阻塞等待，不并发修改
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock($1)`, reconcileLockKey); err != nil {
		t.Fatalf("blocker lock: %v", err)
	}

	// 校验锁确实由 blocker 会话持有：另一会话 try_lock 必须失败
	var gotLock bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_try_advisory_lock($1)
	`, reconcileLockKey).Scan(&gotLock); err != nil {
		t.Fatalf("probe lock hold: %v", err)
	}
	if gotLock {
		_, _ = pool.Exec(ctx, `SELECT pg_advisory_unlock($1)`, reconcileLockKey)
		t.Fatal("blocker does not hold the advisory lock")
	}

	// 走完整 Reconciler 路径（WithReconcileLock → Reconcile），验证锁互斥
	r := &Reconciler{Eval: &fakeEvaluator{alerts: []Alert{low}}, Store: store, Logger: zap.NewNop()}
	done := make(chan error, 1)
	go func() {
		done <- r.Reconcile(ctx)
	}()
	select {
	case err := <-done:
		t.Fatalf("reconcile completed while lock held (err=%v)", err)
	case <-time.After(400 * time.Millisecond):
		// 预期：锁被占用，reconcile 阻塞
	}
	blocker.Exec(ctx, `SELECT pg_advisory_unlock($1)`, reconcileLockKey)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reconcile after unlock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile did not complete after unlock")
	}
}

func assertActiveCount(t *testing.T, pool *pgxpool.Pool, want int) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM alert_events WHERE status = 'active'
	`).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if n != want {
		t.Fatalf("active count = %d, want %d", n, want)
	}
}

func contains(s []byte, sub string) bool {
	return len(s) >= len(sub) && indexOfBytes(s, []byte(sub)) >= 0
}

func indexOfBytes(s, sub []byte) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range sub {
			if s[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

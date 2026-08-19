package migrate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// TestUpFromEmptyDatabase 迁移集成测试（PostgreSQL）：
// 从空库执行全部 up 迁移并验收关键对象存在。
// 需要 TEST_DATABASE_URL 环境变量（如 postgres://gateway:gateway_pass@localhost:5432/scratch），
// 未设置时自动跳过。计划A/B 的迁移验收（020/021）复用该测试。
func TestUpFromEmptyDatabase(t *testing.T) {
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

	if err := Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	// 空库执行后必须存在的基础对象
	for _, table := range []string{
		"public.upstreams",
		"public.alert_events",
		"public.telegram_config",
		"public.telegram_subscribers",
		"public.telegram_delivery_logs",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s missing after migration", table)
		}
	}

	// 020 索引验收
	for _, idx := range []string{
		"idx_alert_events_active_key",
		"idx_alert_events_channel_time",
		"idx_alert_events_status_time",
		"idx_telegram_subscribers_enabled",
		"idx_telegram_delivery_logs_subscriber_time",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, idx).Scan(&exists); err != nil {
			t.Fatalf("check index %s: %v", idx, err)
		}
		if !exists {
			t.Errorf("index %s missing after migration", idx)
		}
	}

	// telegram_config 单行种子
	var cfgCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM telegram_config`).Scan(&cfgCount); err != nil {
		t.Fatalf("count telegram_config: %v", err)
	}
	if cfgCount != 1 {
		t.Errorf("telegram_config seed row count = %d, want 1", cfgCount)
	}

	// 幂等：重复执行 Up 不报错
	if err := Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate up (idempotent re-run): %v", err)
	}
}

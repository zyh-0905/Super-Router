// Package migrate 提供启动时执行的版本化数据库迁移（P2-12）。
//
// 特性：
//   - pg_advisory_lock 保证多实例（Gateway/Checker/Replay 并发启动）串行迁移；
//   - schema_migrations 记录已应用版本；
//   - 存量数据卷基线探测：initdb 时代手工执行的迁移没有版本记录，
//     通过每版本的“效果探测”SQL 识别已应用状态并登记，避免重复执行失败；
//   - 仅自动执行 *.up.sql；down 迁移保留给人工回滚，绝不自动执行。
package migrate

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"smart-router/migrations"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// advisoryLockKey 迁移专用 advisory lock 键（任意固定大整数）。
const advisoryLockKey = 746213081

// canary 每版本的“效果探测”SQL：返回 true 表示该迁移的效果已存在（存量库基线）。
var canary = map[string]string{
	"001_init":                  "SELECT to_regclass('public.upstreams') IS NOT NULL",
	"002_groups":                "SELECT to_regclass('public.channel_groups') IS NOT NULL",
	"003_balance":               "SELECT to_regclass('public.balance_checks') IS NOT NULL",
	"004_custom_balance_api":    "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='balance_api_url')",
	"005_balance_token":         "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='balance_api_token')",
	"006_probe_source":          "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='probe_results' AND column_name='source')",
	"007_ratio_groups":          "SELECT to_regclass('public.channel_ratio_groups') IS NOT NULL",
	"008_ratio_limit":           "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='ratio_limit')",
	"009_model_prices":          "SELECT to_regclass('public.model_prices') IS NOT NULL",
	"010_candidate_details":     "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='decision_logs' AND column_name='candidate_details')",
	"011_retention_indexes":     "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='idx_health_checks_upstream_time')",
	"012_protocol":              "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='protocol')",
	"013_relay_type":            "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='relay_type')",
	"014_test_model":            "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='test_model')",
	"015_circuit_groups":        "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='circuit_states' AND column_name='group_id')",
	"016_replay_context":        "SELECT to_regclass('public.snapshot_archive') IS NOT NULL",
	"017_policies_unique":       "SELECT EXISTS (SELECT 1 FROM pg_index WHERE indrelid='routing_policies'::regclass AND indnullsnotdistinct)",
	"018_group_strategy_config": "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='channel_groups' AND column_name='strategy_config')",
	"019_sub2api_auto_login":    "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='balance_login_email') AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='upstreams' AND column_name='balance_login_password')",
	"020_alert_telegram":         "SELECT to_regclass('public.alert_events') IS NOT NULL AND to_regclass('public.telegram_config') IS NOT NULL",
	"021_quality_checks":         "SELECT to_regclass('public.quality_check_runs') IS NOT NULL AND to_regclass('public.quality_check_results') IS NOT NULL",
	"022_retention_group_indexes": "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname='idx_request_history_group_time')",
	"025_telegram_chat_id_text":  "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='telegram_subscribers' AND column_name='chat_id_num')",
}

// Up 执行所有未应用的 up 迁移（幂等、带锁、逐版本事务）。
func Up(ctx context.Context, pool *pgxpool.Pool, logger *zap.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	versions, err := listUpVersions()
	if err != nil {
		return err
	}

	for _, v := range versions {
		applied, err := isApplied(ctx, conn, v)
		if err != nil {
			return fmt.Errorf("check %s: %w", v, err)
		}
		if applied {
			continue
		}
		// 存量基线：效果已存在但无版本记录 → 登记为已应用
		if cq, ok := canary[v]; ok {
			var exists bool
			if err := conn.QueryRow(ctx, cq).Scan(&exists); err == nil && exists {
				if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, v); err != nil {
					return fmt.Errorf("baseline %s: %w", v, err)
				}
				logger.Info("Migration baseline detected (already applied by initdb)", zap.String("version", v))
				continue
			}
		}

		sql, err := migrations.FS.ReadFile(v + ".up.sql")
		if err != nil {
			return fmt.Errorf("read %s.up.sql: %w", v, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin %s: %w", v, err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", v, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, v); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", v, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", v, err)
		}
		logger.Info("Migration applied", zap.String("version", v))
	}

	return nil
}

func listUpVersions() ([]string, error) {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var versions []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		versions = append(versions, strings.TrimSuffix(name, ".up.sql"))
	}
	sort.Strings(versions)
	return versions, nil
}

func isApplied(ctx context.Context, conn *pgxpool.Conn, version string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists)
	return exists, err
}

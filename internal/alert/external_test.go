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

func TestQualityFailureKeyStable(t *testing.T) {
	got := StableKey(AlertInput{Type: TypeQualityCheckFailed, ChannelID: 5, Model: "claude-sonnet-5"})
	if got != "quality_check_failed:channel-5:model-claude-sonnet-5" {
		t.Fatal(got)
	}
}

// TestQualityAlertSinkLifecycle 质量失败告警集成测试：
// 关键阶段失败创建 warning active；同 key 再失败更新 occurrence；
// 后续同模型 full 检测关键阶段通过则恢复该 key。
func TestQualityAlertSinkLifecycle(t *testing.T) {
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
	if err := migrate.Up(ctx, pool, zap.NewNop()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 种子站点
	var channelID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key)
		VALUES ('Quality Alert Relay', 'https://qa.example.com', '', '')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// 清理该站点历史告警
	_, _ = pool.Exec(ctx, `DELETE FROM alert_events WHERE channel_id = $1`, channelID)

	store := &SQLStore{Pool: pool}
	sink := NewQualityAlertSink(store)
	model := "claude-sonnet-5"
	key := StableKey(AlertInput{Type: TypeQualityCheckFailed, ChannelID: channelID, Model: model})

	// 1. 关键阶段失败 → warning active
	if err := sink.QualityFailure(ctx, channelID, model, "stream", "stream failed", map[string]interface{}{"run_id": 1}); err != nil {
		t.Fatalf("quality failure: %v", err)
	}
	var sev, status string
	var occ int
	if err := pool.QueryRow(ctx, `
		SELECT severity, status, occurrence_count FROM alert_events
		WHERE alert_key = $1
	`, key).Scan(&sev, &status, &occ); err != nil {
		t.Fatalf("query alert: %v", err)
	}
	if sev != "warning" || status != "active" || occ != 1 {
		t.Fatalf("sev=%s status=%s occ=%d, want warning/active/1", sev, status, occ)
	}

	// 2. 同 key 再失败 → occurrence 增加，不创建重复 active
	if err := sink.QualityFailure(ctx, channelID, model, "protocol", "protocol failed", map[string]interface{}{"run_id": 2}); err != nil {
		t.Fatalf("quality failure #2: %v", err)
	}
	var count int
	_ = pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM alert_events
		WHERE alert_key = $1 AND status = 'active'
	`, key).Scan(&count)
	if count != 1 {
		t.Fatalf("active count = %d, want 1 (no duplicate)", count)
	}
	_ = pool.QueryRow(ctx, `
		SELECT occurrence_count FROM alert_events
		WHERE alert_key = $1 AND status = 'active'
	`, key).Scan(&occ)
	if occ != 2 {
		t.Fatalf("occurrence = %d, want 2", occ)
	}

	// 3. 后续关键阶段全部通过 → 恢复该 key
	if err := sink.ResolveQualityFailures(ctx, channelID, model, []string{"connectivity", "protocol", "stream"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	err = pool.QueryRow(ctx, `
		SELECT status FROM alert_events
		WHERE alert_key = $1 AND status = 'active'
	`, key).Scan(&status)
	// 无 active 行 = 已恢复
	if err == nil {
		t.Fatal("alert still active after resolve")
	}

	// 清理
	_, _ = pool.Exec(ctx, `DELETE FROM alert_events WHERE channel_id = $1`, channelID)
}

package telegram

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"smart-router/internal/migrate"
	"smart-router/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// TestQualityLatestFormat 格式与权限纯逻辑测试（无 DB）。
func TestQualityLatestNoHistory(t *testing.T) {
	msg := formatQualityMessage("Test Relay", nil, nil)
	if !strings.Contains(msg, "暂无质量检测结果") {
		t.Fatal(msg)
	}
}

func TestFormatQualityCompleted(t *testing.T) {
	now := time.Now()
	run := qualityRunView{
		Model:         "claude-sonnet-5",
		Status:        "completed",
		OverallStatus: "attention",
		CreatedAt:     now,
		FinishedAt:    &now,
	}
	results := []qualityResultView{
		{Stage: "connectivity", Status: "passed"},
		{Stage: "protocol", Status: "passed"},
		{Stage: "stream", Status: "passed"},
		{Stage: "usage", Status: "passed"},
		{Stage: "behavior", Status: "attention"},
	}
	msg := formatQualityMessage("Claude Relay B", &run, results)
	for _, want := range []string{
		"🧪", "Claude Relay B", "claude-sonnet-5", "需要关注",
		"连接：通过", "协议：通过", "流式：通过", "Usage：通过", "模型行为：需要关注",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestFormatQualityRunning(t *testing.T) {
	run := qualityRunView{Model: "gpt-4o", Status: "running", CurrentStage: "stream", Progress: 60}
	msg := formatQualityMessage("Relay", &run, nil)
	if !strings.Contains(msg, "检测中") || !strings.Contains(msg, "流式") || !strings.Contains(msg, "60%") {
		t.Fatal(msg)
	}
}

// TestQualityQueryIntegration 集成：/quality 只读查询 + 分组过滤。
func TestQualityQueryIntegration(t *testing.T) {
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

	// 种子站点 + 分组 + 质量任务
	var channelID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key)
		VALUES ('Quality Query Relay', 'https://qq.example.com', '', '')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	var groupID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO channel_groups (name) VALUES ('Quality Query Group')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&groupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	_, _ = pool.Exec(ctx, `
		INSERT INTO channel_group_members (channel_id, group_id) VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`, channelID, groupID)

	// 清理旧任务
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE channel_id = $1`, channelID)
	var runID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, overall_status)
		VALUES ($1, 'gpt-4o', 'full', 'completed', 'good')
		RETURNING id
	`, channelID).Scan(&runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	_, _ = pool.Exec(ctx, `
		INSERT INTO quality_check_results (run_id, stage, check_name, status)
		VALUES ($1, 'connectivity', 'models', 'passed'),
		       ($1, 'protocol', 'conversion', 'passed'),
		       ($1, 'stream', 'integrity', 'passed')
	`, runID)

	q := NewSQLQueryService(&store.DB{Pool: pool})

	// 未绑定分组：可查
	msg, err := q.QualityLatest(ctx, channelID, nil)
	if err != nil {
		t.Fatalf("quality latest: %v", err)
	}
	if !strings.Contains(msg, "Quality Query Relay") || !strings.Contains(msg, "良好") {
		t.Fatalf("msg = %s", msg)
	}

	// 绑定正确分组：可查
	msg, err = q.QualityLatest(ctx, channelID, []int{groupID})
	if err != nil {
		t.Fatalf("quality latest (group): %v", err)
	}
	if !strings.Contains(msg, "Quality Query Relay") {
		t.Fatalf("group-filtered msg = %s", msg)
	}

	// 绑定其他分组：拒绝
	other := groupID + 9999
	msg, err = q.QualityLatest(ctx, channelID, []int{other})
	if err != nil {
		t.Fatalf("quality latest (other group): %v", err)
	}
	if !strings.Contains(msg, "无权") {
		t.Fatalf("expected denial, got %s", msg)
	}

	// 无历史站点：暂无结果
	var ch2 int
	_ = pool.QueryRow(ctx, `
		INSERT INTO upstreams (name, base_url, access_token, api_key)
		VALUES ('Quality Empty Relay', 'https://qe.example.com', '', '')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING id
	`).Scan(&ch2)
	msg, err = q.QualityLatest(ctx, ch2, nil)
	if err != nil {
		t.Fatalf("quality latest (empty): %v", err)
	}
	if !strings.Contains(msg, "暂无质量检测结果") {
		t.Fatalf("expected empty result, got %s", msg)
	}

	// 清理
	_, _ = pool.Exec(ctx, `DELETE FROM quality_check_runs WHERE channel_id IN ($1, $2)`, channelID, ch2)
}

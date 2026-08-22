package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// qualityRunView 最近一次质量任务的只读视图。
type qualityRunView struct {
	Model         string
	Status        string
	OverallStatus string
	CurrentStage  string
	Progress      int
	CreatedAt     time.Time
	FinishedAt    *time.Time
}

// qualityResultView 单阶段结果视图。
type qualityResultView struct {
	Stage  string
	Status string
}

// QualityLatest /quality <channel_id>：返回最近一次质量检测摘要。
// 只读数据库，不启动新任务、不触发上游调用。分组过滤与其它查询一致。
func (q *SQLQueryService) QualityLatest(ctx context.Context, channelID int, groupIDs []int) (string, error) {
	if !q.channelInGroups(ctx, channelID, groupIDs) {
		return "⛔ 无权查看该站点。", nil
	}

	var name string
	if err := q.DB.Pool.QueryRow(ctx, `
		SELECT name FROM upstreams WHERE id = $1
	`, channelID).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "🔍 站点不存在，请检查 ID（见 /relay 列表）。", nil
		}
		return "", fmt.Errorf("query channel name: %w", err)
	}

	var run qualityRunView
	err := q.DB.Pool.QueryRow(ctx, `
		SELECT model, status, COALESCE(overall_status, ''), current_stage, progress, created_at, finished_at
		FROM quality_check_runs
		WHERE channel_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, channelID).Scan(&run.Model, &run.Status, &run.OverallStatus, &run.CurrentStage, &run.Progress, &run.CreatedAt, &run.FinishedAt)
	if err != nil {
		// 无任何历史任务
		return formatQualityMessage(name, nil, nil), nil
	}

	rows, err := q.DB.Pool.Query(ctx, `
		SELECT stage, status FROM quality_check_results
		WHERE run_id = (
			SELECT id FROM quality_check_runs
			WHERE channel_id = $1
			ORDER BY created_at DESC LIMIT 1
		)
		ORDER BY id
	`, channelID)
	if err != nil {
		return "", fmt.Errorf("query quality results: %w", err)
	}
	defer rows.Close()
	var results []qualityResultView
	for rows.Next() {
		var r qualityResultView
		if rows.Scan(&r.Stage, &r.Status) == nil {
			results = append(results, r)
		}
	}

	return formatQualityMessage(name, &run, results), nil
}

// formatQualityMessage 质量检测摘要消息（HTML，动态字段转义）。
func formatQualityMessage(name string, run *qualityRunView, results []qualityResultView) string {
	if run == nil {
		return fmt.Sprintf("🧪 <b>%s</b> 最近一次接口质量检测\n暂无质量检测结果。", EscapeHTML(name))
	}
	var b strings.Builder
	b.WriteString("🧪 <b>接口质量检测</b>\n")
	b.WriteString("━━━━━━━━━━━━\n")
	b.WriteString("🏢 站点：" + EscapeHTML(name) + "\n")
	b.WriteString("🤖 模型：" + EscapeHTML(run.Model) + "\n")

	statusLabel := map[string]string{
		"queued": "排队中", "running": "检测中", "cancel_requested": "正在停止",
		"completed": "已完成", "failed": "失败", "cancelled": "已取消", "expired": "已过期",
	}[run.Status]
	if run.Status == "running" || run.Status == "queued" || run.Status == "cancel_requested" {
		b.WriteString("📌 状态：" + statusLabel)
		if run.Status == "running" {
			b.WriteString(fmt.Sprintf(" ｜ 进度 %d%%", run.Progress))
		}
		b.WriteString("\n")
		if run.CurrentStage != "" {
			b.WriteString("⏭ 当前阶段：" + EscapeHTML(stageLabelZh(run.CurrentStage)) + "\n")
		}
		return b.String()
	}

	overallLabel := map[string]string{
		"good": "🟢 良好", "attention": "🟠 需要关注",
		"failed": "🔴 异常", "unknown": "⚪ 无法判断",
	}[run.OverallStatus]
	if overallLabel == "" {
		overallLabel = run.OverallStatus
	}
	b.WriteString("📊 结果：" + overallLabel + "\n")

	for _, r := range results {
		b.WriteString(EscapeHTML(stageLabelZh(r.Stage)) + "：" + resultLabelZh(r.Status) + "\n")
	}

	if run.FinishedAt != nil && !run.CreatedAt.IsZero() {
		d := run.FinishedAt.Sub(run.CreatedAt)
		b.WriteString(fmt.Sprintf("⏱ 总耗时：%.2f 秒", d.Seconds()))
	}
	return b.String()
}

func stageLabelZh(stage string) string {
	switch stage {
	case "connectivity":
		return "连接"
	case "protocol":
		return "协议"
	case "stream":
		return "流式"
	case "usage":
		return "Usage"
	case "behavior":
		return "模型行为"
	}
	return stage
}

func resultLabelZh(status string) string {
	switch status {
	case "passed":
		return "通过"
	case "attention":
		return "需要关注"
	case "failed":
		return "异常"
	case "unknown":
		return "无法判断"
	case "skipped":
		return "跳过"
	case "running":
		return "检测中"
	}
	return status
}

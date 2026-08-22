package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"smart-router/internal/alert"
	"smart-router/internal/store"
)

// AlertQueries 告警只读查询（Telegram /alerts /status 与小时汇总共用）。
type AlertQueries struct {
	svc *alert.Service
}

// NewAlertQueries 创建告警查询（内部使用共享 alert.Service 读取持久化事件）。
func NewAlertQueries(db *store.DB) *AlertQueries {
	return &AlertQueries{svc: alert.NewService(db)}
}

// ListActive 返回格式化的活跃告警列表（criticalOnly 只看严重告警）。
func (a *AlertQueries) ListActive(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error) {
	alerts, err := a.svc.ActiveForGroups(ctx, groupIDs)
	if err != nil {
		return "", err
	}
	if criticalOnly {
		filtered := alerts[:0]
		for _, al := range alerts {
			if al.Severity == alert.SeverityCritical {
				filtered = append(filtered, al)
			}
		}
		alerts = filtered
	}
	return FormatAlerts(alerts, time.Now()), nil
}

// FormatAlerts 格式化告警列表（Telegram 消息）。
func FormatAlerts(alerts []alert.Alert, now time.Time) string {
	if len(alerts) == 0 {
		return "✅ <b>当前没有活跃告警</b> 🎉"
	}
	var b strings.Builder
	critical, warning := 0, 0
	for _, a := range alerts {
		if a.Severity == alert.SeverityCritical {
			critical++
		} else {
			warning++
		}
	}
	b.WriteString(fmt.Sprintf("🚨 <b>活跃告警</b> ｜ 🔴 %d ｜ 🟠 %d\n", critical, warning))
	b.WriteString("━━━━━━━━━━━━\n")
	for i, a := range alerts {
		if i > 0 {
			b.WriteString("━━━━━━━━━━━━\n")
		}
		icon := "🟠"
		if a.Severity == alert.SeverityCritical {
			icon = "🔴"
		}
		b.WriteString(fmt.Sprintf("%s <b>%s</b>", icon, EscapeHTML(a.Title)))
		if a.ChannelName != "" {
			b.WriteString(" ｜ " + EscapeHTML(a.ChannelName))
		}
		b.WriteString("\n")
		if a.Model != "" {
			b.WriteString("🤖 " + EscapeHTML(a.Model) + "\n")
		}
		if a.CurrentValue != nil && a.ThresholdValue != nil {
			b.WriteString(fmt.Sprintf("📊 %.4g%s ｜ 阈值 %.4g%s\n", *a.CurrentValue, a.Unit, *a.ThresholdValue, a.Unit))
		}
		if !a.FirstSeenAt.IsZero() {
			b.WriteString("⏳ 持续 " + alert.FormatDuration(now.Sub(a.FirstSeenAt)) + "\n")
		}
		b.WriteString("🔑 " + EscapeHTML(a.Key) + "\n")
	}
	b.WriteString("💡 单条详情：/alert &lt;key&gt;")
	return b.String()
}

// Detail 单条告警详情（/alert <key>，含已恢复的告警）。
// 未找到或无权查看返回「未找到」提示（不区分两者，避免探测越权存在性）。
func (a *AlertQueries) Detail(ctx context.Context, key string, groupIDs []int) (string, error) {
	al, err := a.svc.GetByKeyForGroups(ctx, key, groupIDs)
	if err != nil {
		return "", err
	}
	if al == nil {
		return "🔍 <b>未找到该告警</b>\nKey：" + EscapeHTML(key), nil
	}
	var b strings.Builder
	icon := "🟠"
	if al.Severity == alert.SeverityCritical {
		icon = "🔴"
	}
	b.WriteString(fmt.Sprintf("%s <b>%s</b>\n", icon, EscapeHTML(al.Title)))
	b.WriteString("━━━━━━━━━━━━\n")
	if al.ChannelName != "" {
		b.WriteString("🏢 站点：" + EscapeHTML(al.ChannelName) + "\n")
	}
	if al.Model != "" {
		b.WriteString("🤖 模型：" + EscapeHTML(al.Model) + "\n")
	}
	b.WriteString("📌 状态：" + statusLabel(al.Status) + "\n")
	if al.Message != "" {
		b.WriteString("📝 " + EscapeHTML(al.Message) + "\n")
	}
	if al.CurrentValue != nil && al.ThresholdValue != nil {
		b.WriteString(fmt.Sprintf("📊 当前 %.4g%s ｜ 阈值 %.4g%s\n", *al.CurrentValue, al.Unit, *al.ThresholdValue, al.Unit))
	}
	if al.Impact != "" {
		b.WriteString("💥 影响：" + EscapeHTML(al.Impact) + "\n")
	}
	if al.Recommendation != "" {
		b.WriteString("💡 建议：" + EscapeHTML(al.Recommendation) + "\n")
	}
	if !al.FirstSeenAt.IsZero() {
		b.WriteString("🕐 首次出现：" + al.FirstSeenAt.In(displayLoc).Format("2006-01-02 15:04") + "\n")
	}
	if al.RecoveredAt != nil {
		b.WriteString("✅ 恢复时间：" + al.RecoveredAt.In(displayLoc).Format("2006-01-02 15:04") + "\n")
	} else {
		b.WriteString("⏳ 持续：" + alert.FormatDuration(time.Since(al.FirstSeenAt)) + "\n")
	}
	b.WriteString("🔑 Key：" + EscapeHTML(al.Key))
	return b.String(), nil
}

func statusLabel(s alert.AlertStatus) string {
	if s == alert.StatusRecovered {
		return "✅ 已恢复"
	}
	return "⏳ 活跃中"
}

// RegisterAlertQueries 将告警查询注入命令服务（Worker 装配时调用一次）。
func RegisterAlertQueries(db *store.DB) {
	q := NewAlertQueries(db)
	SetAlertsQuery(q.ListActive)
	SetAlertDetailQuery(q.Detail)
}

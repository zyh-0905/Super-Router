package telegram

import (
	"fmt"
	"strings"
	"time"

	"smart-router/internal/alert"
)

// SystemOverview 汇总消息中的系统概况（可用站点/熔断/告警统计）。
type SystemOverview struct {
	TotalChannels  int
	ActiveChannels int
	OpenCircuits   int
	ActiveAlerts   int
	CriticalAlerts int
	WarningAlerts  int
}

// FormatReport 组装每小时告警汇总消息（HTML）。
// 固定顺序：标题、窗口、系统概况、新出现、升级、持续中、恢复、查询提示。
// 窗口内无变化时发送心跳式摘要（不发送空消息）。
func FormatReport(now time.Time, windowHours int, overview SystemOverview, changes alert.AlertChanges, includeRecovered, includeOngoing bool, webBaseURL string) string {
	var b strings.Builder
	b.WriteString("🛰 <b>Smart Router 告警汇总</b>\n")
	b.WriteString("🕐 " + now.Format("2006-01-02 15:04") + " ｜ " + FormatWindow(windowHours) + "\n")
	b.WriteString("━━━━━━━━━━━━\n")

	b.WriteString("📊 <b>系统概况</b>\n")
	b.WriteString(fmt.Sprintf("🏢 可用中转站：%d / %d\n", overview.ActiveChannels, overview.TotalChannels))
	b.WriteString(fmt.Sprintf("⚡ 当前熔断：%d\n", overview.OpenCircuits))
	b.WriteString(fmt.Sprintf("🚨 活跃告警：%d ｜ 🔴 %d ｜ 🟠 %d\n", overview.ActiveAlerts, overview.CriticalAlerts, overview.WarningAlerts))

	if changes.Total() == 0 {
		b.WriteString("\n✅ " + FormatWindow(windowHours) + "没有新的告警变化。\n")
		writeFooter(&b, webBaseURL)
		return b.String()
	}

	if len(changes.New) > 0 {
		b.WriteString("\n━━━━━━━━━━━━\n🆕 <b>新出现</b>\n")
		for _, a := range changes.New {
			writeAlertDetail(&b, a, now)
		}
	}
	if len(changes.Escalated) > 0 {
		b.WriteString("━━━━━━━━━━━━\n⬆️ <b>严重度升级</b>\n")
		for _, a := range changes.Escalated {
			b.WriteString("🟠 → 🔴 " + EscapeHTML(a.ChannelName))
			if a.Model != "" {
				b.WriteString(" ｜ 🤖 " + EscapeHTML(a.Model))
			}
			b.WriteString("\n   类型：" + typeLabel(a.Type) + "\n")
			b.WriteString("\n")
		}
	}
	if includeOngoing && len(changes.Ongoing) > 0 {
		b.WriteString("━━━━━━━━━━━━\n🔁 <b>持续中</b>\n")
		for _, a := range changes.Ongoing {
			b.WriteString(fmt.Sprintf("🔴 %s", EscapeHTML(a.ChannelName)))
			if a.Model != "" {
				b.WriteString(" ｜ 🤖 " + EscapeHTML(a.Model))
			}
			if !a.FirstSeenAt.IsZero() {
				b.WriteString(fmt.Sprintf("\n⏳ 持续 %s", alert.FormatDuration(now.Sub(a.FirstSeenAt))))
			}
			b.WriteString("\n\n")
		}
	}
	if includeRecovered && len(changes.Recovered) > 0 {
		b.WriteString("━━━━━━━━━━━━\n✅ <b>已恢复</b>\n")
		for _, a := range changes.Recovered {
			b.WriteString("🟢 " + EscapeHTML(a.ChannelName) + " ｜ " + typeLabel(a.Type) + "\n")
			if a.RecoveredAt != nil && !a.FirstSeenAt.IsZero() {
				b.WriteString(fmt.Sprintf("⏳ 故障持续：%s\n", alert.FormatDuration(a.RecoveredAt.Sub(a.FirstSeenAt))))
			}
			b.WriteString("\n")
		}
	}

	writeFooter(&b, webBaseURL)
	return b.String()
}

func writeAlertDetail(b *strings.Builder, a alert.Alert, now time.Time) {
	icon := "🔴"
	if a.Severity == alert.SeverityWarning {
		icon = "🟠"
	}
	b.WriteString(fmt.Sprintf("%s %s ｜ %s", icon, EscapeHTML(a.ChannelName), typeLabel(a.Type)))
	if a.Model != "" {
		b.WriteString(" ｜ 🤖 " + EscapeHTML(a.Model))
	}
	b.WriteString("\n")
	if a.CurrentValue != nil && a.ThresholdValue != nil {
		b.WriteString(fmt.Sprintf("📊 当前 %.4g%s ｜ 上限 %.4g%s\n", *a.CurrentValue, a.Unit, *a.ThresholdValue, a.Unit))
	}
	if !a.FirstSeenAt.IsZero() {
		b.WriteString("⏳ 持续 " + alert.FormatDuration(now.Sub(a.FirstSeenAt)) + "\n")
	}
	if a.Impact != "" {
		b.WriteString("💥 影响：" + EscapeHTML(a.Impact) + "\n")
	}
	if a.Recommendation != "" {
		b.WriteString("💡 建议：" + EscapeHTML(a.Recommendation) + "\n")
	}
	b.WriteString("\n")
}

func writeFooter(b *strings.Builder, webBaseURL string) {
	b.WriteString("━━━━━━━━━━━━\n")
	b.WriteString("💡 查询：/alerts ｜ /relay ｜ /balance ｜ /health ｜ /ratio")
	if webBaseURL != "" {
		b.WriteString("\n🌐 控制台：" + EscapeHTML(webBaseURL))
	}
	b.WriteString("\n")
}

// typeLabel 告警类型中文标签。
func typeLabel(t string) string {
	switch t {
	case alert.TypeLowBalance:
		return "低余额"
	case alert.TypeRatioExceeded:
		return "倍率超限"
	case alert.TypeCircuitOpen:
		return "熔断开闸"
	case alert.TypeCircuitDegraded:
		return "熔断降级"
	case alert.TypeChannelDisabled:
		return "站点禁用"
	case alert.TypePricingSyncFailed:
		return "价格同步失败"
	case alert.TypeQualityCheckFailed:
		return "接口质量检测失败"
	}
	return t
}

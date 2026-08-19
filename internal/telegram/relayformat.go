package telegram

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// hostOnly 只显示 Base URL 域名（不泄露完整地址）。
func hostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	return u.Host
}

// fmtTime 统一时间格式。
func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

// fmtBalance 余额展示（nil = 无数据）。
func fmtBalance(b *float64) string {
	if b == nil {
		return "暂无"
	}
	return fmt.Sprintf("$%.2f", *b)
}

// fmtCircuitState 熔断状态中文标签。
func fmtCircuitState(s string) string {
	switch s {
	case "", "closed":
		return "正常"
	case "open":
		return "已熔断"
	case "half_open":
		return "半开探测"
	case "degraded":
		return "降级"
	}
	return s
}

// FormatRelayList /relay 站点总览。
func FormatRelayList(items []RelaySummary) string {
	if len(items) == 0 {
		return "📡 <b>中转站列表</b>\n暂无有效检测结果，请启动 checker 后重试。"
	}
	var b strings.Builder
	b.WriteString("📡 <b>中转站列表</b>\n")
	for _, it := range items {
		state := "✅"
		if !it.Healthy {
			state = "❌"
		} else if it.CircuitState != "" && it.CircuitState != "closed" {
			state = "⚠️"
		}
		line := fmt.Sprintf("%s <b>%d</b> %s", state, it.ID, EscapeHTML(it.Name))
		if it.Host != "" {
			line += " · " + EscapeHTML(it.Host)
		}
		if it.Balance != nil {
			line += fmt.Sprintf(" · 余额 $%.2f", *it.Balance)
		}
		if it.Ratio != nil {
			line += fmt.Sprintf(" · 倍率 %.4fx", *it.Ratio)
		}
		if it.CircuitState != "" && it.CircuitState != "closed" {
			line += " · " + EscapeHTML(fmtCircuitState(it.CircuitState))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// FormatRelayDetail /relay <id> 单站点详情。
func FormatRelayDetail(it RelayDetail) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📡 <b>%s</b>（ID %d）\n", EscapeHTML(it.Name), it.ID))
	b.WriteString("━━━━━━━━━━━━━━━━\n")
	if it.Host != "" {
		b.WriteString("域名：" + EscapeHTML(it.Host) + "\n")
	}
	proto := "OpenAI 兼容"
	if it.Protocol == "anthropic" {
		proto = "Anthropic 原生"
	}
	b.WriteString("协议：" + proto + "\n")
	if it.RelayType != "" {
		b.WriteString("类型：" + EscapeHTML(relayTypeLabel(it.RelayType)) + "\n")
	}
	if len(it.Groups) > 0 {
		b.WriteString("分组：" + EscapeHTML(strings.Join(it.Groups, "、")) + "\n")
	}
	healthy := "离线"
	if it.Healthy {
		healthy = "存活"
	}
	b.WriteString(fmt.Sprintf("健康：%s · 平均延迟 %dms · P95 %dms\n", healthy, it.AverageMS, it.P95MS))
	b.WriteString(fmt.Sprintf("24h：%d 请求 · 成功率 %.1f%%\n", it.Requests24h, it.SuccessRate*100))
	b.WriteString("熔断：" + EscapeHTML(fmtCircuitState(it.CircuitState)) + "\n")
	b.WriteString("余额：" + fmtBalance(it.Balance) + "\n")
	return b.String()
}

// relayTypeLabel 中转站类型标签。
func relayTypeLabel(t string) string {
	switch t {
	case "newapi":
		return "New API"
	case "sub2api":
		return "Sub2API"
	case "custom":
		return "自定义"
	}
	return t
}

// FormatBalanceList /balance 全量紧凑列表。
func FormatBalanceList(items []BalanceSummary, checkedAt *time.Time) string {
	if len(items) == 0 {
		return "💰 <b>余额列表</b>\n暂无有效检测结果。"
	}
	var b strings.Builder
	b.WriteString("💰 <b>余额列表</b>")
	if checkedAt != nil {
		b.WriteString("（数据时间 " + checkedAt.Format("2006-01-02 15:04") + "）")
	}
	b.WriteString("\n")
	for _, it := range items {
		b.WriteString(fmt.Sprintf("%d %s：%s\n", it.ChannelID, EscapeHTML(it.Name), fmtBalance(it.Balance)))
	}
	return b.String()
}

// FormatHealthList /health 全量紧凑列表。
func FormatHealthList(items []HealthSummary) string {
	if len(items) == 0 {
		return "🩺 <b>健康列表</b>\n暂无有效检测结果。"
	}
	var b strings.Builder
	b.WriteString("🩺 <b>健康列表</b>\n")
	for _, it := range items {
		state := "✅"
		if !it.Alive {
			state = "❌"
		}
		line := fmt.Sprintf("%s %d %s", state, it.ChannelID, EscapeHTML(it.Name))
		if it.LatencyMS != nil {
			line += fmt.Sprintf(" · %dms", *it.LatencyMS)
		}
		if it.CircuitState != "" && it.CircuitState != "closed" {
			line += " · " + EscapeHTML(fmtCircuitState(it.CircuitState))
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// FormatRatioList /ratio 全量紧凑列表。
func FormatRatioList(items []RatioSummary) string {
	if len(items) == 0 {
		return "📐 <b>倍率列表</b>\n暂无有效检测结果。"
	}
	var b strings.Builder
	b.WriteString("📐 <b>倍率列表</b>\n")
	for _, it := range items {
		line := fmt.Sprintf("%d %s · %s：", it.ChannelID, EscapeHTML(it.Name), EscapeHTML(it.Model))
		if it.Ratio == nil {
			line += "暂无实测"
		} else {
			line += fmt.Sprintf("%.4fx", *it.Ratio)
			if it.Limit > 0 {
				line += fmt.Sprintf("（上限 %.4fx）", it.Limit)
			}
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

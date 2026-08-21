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

// FormatBalanceList /balance 全量列表：按中转站归并汇总
// （同 base_url 成员站点归为一行，余额为账户余额）。
func FormatBalanceList(items []BalanceSummary, checkedAt *time.Time) string {
	if len(items) == 0 {
		return "💰 <b>中转站余额</b>\n暂无有效检测结果。"
	}
	var b strings.Builder
	b.WriteString("💰 <b>中转站余额</b>\n")
	b.WriteString("🕐 更新于 " + formatCheckedAt(checkedAt) + "\n")
	for i, it := range items {
		b.WriteString("━━━━━━━━━━━━\n")
		b.WriteString("🏢 <b>" + EscapeHTML(it.Name) + "</b>")
		if it.StationID > 0 {
			b.WriteString(fmt.Sprintf(" ［ID %d］", it.StationID))
		}
		b.WriteString("\n")
		b.WriteString("💵 <b>" + fmtBalance(it.Balance) + "</b>")
		if it.MemberCount > 0 {
			b.WriteString(fmt.Sprintf(" ｜ %d 个站点", it.MemberCount))
		}
		b.WriteString("\n")
		_ = i
	}
	b.WriteString("━━━━━━━━━━━━\n")
	b.WriteString("💡 明细：/balance &lt;ID&gt;")
	return b.String()
}

// FormatBalanceDetail /balance <id> 中转站详情：
// 账户余额 + 成员站点名列表（不展示成员各自余额）。
func FormatBalanceDetail(name string, balance *float64, checkedAt *time.Time, members []BalanceMember) string {
	var b strings.Builder
	b.WriteString("🏢 <b>" + EscapeHTML(name) + "</b>\n")
	b.WriteString("💵 账户余额：<b>" + fmtBalance(balance) + "</b>")
	if checkedAt != nil {
		b.WriteString("　🕐 " + formatCheckedAt(checkedAt))
	}
	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━\n")
	b.WriteString(fmt.Sprintf("👥 成员站点（%d 个）：\n", len(members)))
	if len(members) == 0 {
		b.WriteString("（无授权范围内的成员站点）\n")
	}
	for _, m := range members {
		state := "✅"
		if !m.Enabled {
			state = "⛔"
		}
		b.WriteString(fmt.Sprintf("%s [%d] %s\n", state, m.ChannelID, EscapeHTML(m.Name)))
	}
	return b.String()
}

// formatCheckedAt 检测时间紧凑展示：今天只显示时分，跨天显示月日时分。
func formatCheckedAt(t *time.Time) string {
	if t == nil {
		return "—"
	}
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
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

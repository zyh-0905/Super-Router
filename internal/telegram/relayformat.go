package telegram

// Bot 输出的统一设计语言：
//   - 标题：emoji + <b>粗体</b>，第二行 🕐 更新时间（当天只显示时分）；
//   - 行内分隔：竖线「｜」（全角，避免与 Telegram 表格语法冲突）；
//   - 卡片/条目之间用 ━━━━ 分隔线；
//   - 底部统一 💡 提示行（下一步命令）；
//   - 紧凑列表不换行（/relay /health /ratio 列表），详情分节换行。

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

// fmtTime 统一时间格式（完整时间戳场景）。
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

// healthIcon 健康状态图标。
func healthIcon(healthy bool) string {
	if healthy {
		return "🟢"
	}
	return "🔴"
}

// FormatRelayList /relay 站点总览（紧凑列表）。
func FormatRelayList(items []RelaySummary) string {
	var b strings.Builder
	b.WriteString("📡 <b>中转站列表</b>")
	if len(items) == 0 {
		return b.String() + "\n暂无有效检测结果，请启动 checker 后重试。"
	}
	b.WriteString("（" + fmt.Sprintf("%d 站", len(items)) + "）\n")
	for _, it := range items {
		icon := healthIcon(it.Healthy)
		if it.Healthy && it.CircuitState != "" && it.CircuitState != "closed" {
			icon = "🟠"
		}
		line := fmt.Sprintf("%s <b>%d</b> %s", icon, it.ID, EscapeHTML(it.Name))
		if it.Host != "" {
			line += " ｜ " + EscapeHTML(it.Host)
		}
		if it.Balance != nil {
			line += fmt.Sprintf(" ｜ 💵 $%.2f", *it.Balance)
		}
		if it.Ratio != nil {
			line += fmt.Sprintf(" ｜ %.2fx", *it.Ratio)
		}
		if it.CircuitState != "" && it.CircuitState != "closed" {
			line += " ｜ ⚡ " + EscapeHTML(fmtCircuitState(it.CircuitState))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("💡 详情：/relay &lt;ID&gt;")
	return b.String()
}

// FormatRelayDetail /relay <id> 单站点详情。
func FormatRelayDetail(it RelayDetail) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("📡 <b>%s</b> ［ID %d］\n", EscapeHTML(it.Name), it.ID))
	b.WriteString("━━━━━━━━━━━━\n")
	if it.Host != "" {
		b.WriteString("🌐 域名：" + EscapeHTML(it.Host) + "\n")
	}
	proto := "OpenAI 兼容"
	if it.Protocol == "anthropic" {
		proto = "Anthropic 原生"
	}
	b.WriteString("🔌 协议：" + proto)
	if it.RelayType != "" {
		b.WriteString(" ｜ " + EscapeHTML(relayTypeLabel(it.RelayType)))
	}
	b.WriteString("\n")
	if len(it.Groups) > 0 {
		b.WriteString("📁 分组：" + EscapeHTML(strings.Join(it.Groups, "、")) + "\n")
	}
	healthy := "离线"
	if it.Healthy {
		healthy = "存活"
	}
	b.WriteString("🩺 健康：" + healthy +
		fmt.Sprintf(" ｜ ⏱ %dms ｜ P95 %dms\n", it.AverageMS, it.P95MS))
	b.WriteString(fmt.Sprintf("📈 24h：%d 请求 ｜ 成功率 %.1f%%\n", it.Requests24h, it.SuccessRate*100))
	b.WriteString("⚡ 熔断：" + EscapeHTML(fmtCircuitState(it.CircuitState)) + "\n")
	b.WriteString("💵 余额：" + fmtBalance(it.Balance) + "\n")
	b.WriteString("💡 测试：/sitetest " + fmt.Sprintf("%d", it.ID) + " ｜ 倍率：/ratio " + fmt.Sprintf("%d", it.ID))
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
	for _, it := range items {
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
	b.WriteString("💡 站点详情：/relay &lt;ID&gt;")
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

// FormatHealthList /health 全量列表（紧凑）。
func FormatHealthList(items []HealthSummary) string {
	var b strings.Builder
	b.WriteString("🩺 <b>健康列表</b>")
	if len(items) == 0 {
		return b.String() + "\n暂无有效检测结果。"
	}
	b.WriteString("（" + fmt.Sprintf("%d 站", len(items)) + "）\n")
	for _, it := range items {
		line := fmt.Sprintf("%s <b>%d</b> %s", healthIcon(it.Alive), it.ChannelID, EscapeHTML(it.Name))
		if it.LatencyMS != nil {
			line += fmt.Sprintf(" ｜ ⏱ %dms", *it.LatencyMS)
		}
		if it.CircuitState != "" && it.CircuitState != "closed" {
			line += " ｜ ⚡ " + EscapeHTML(fmtCircuitState(it.CircuitState))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("💡 详情：/health &lt;ID&gt;")
	return b.String()
}

// FormatHealthDetail /health <id> 最近历史（结构化数据由 query 层提供）。
func FormatHealthDetail(name string, items []HealthHistoryItem) string {
	var b strings.Builder
	b.WriteString("🩺 <b>" + EscapeHTML(name) + "</b> 最近健康\n")
	b.WriteString("━━━━━━━━━━━━\n")
	if len(items) == 0 {
		b.WriteString("暂无有效检测结果。\n")
		return b.String()
	}
	for _, it := range items {
		state := healthIcon(it.Alive) + " 存活"
		if !it.Alive {
			state = "🔴 离线"
		}
		lat := "—"
		if it.LatencyMS != nil {
			lat = fmt.Sprintf("%dms", *it.LatencyMS)
		}
		b.WriteString(fmt.Sprintf("%s ｜ ⏱ %s ｜ 🕐 %s\n", state, lat, formatCheckedAt(&it.CheckedAt)))
	}
	return b.String()
}

// FormatRatioList /ratio 全量列表（紧凑）。
func FormatRatioList(items []RatioSummary) string {
	var b strings.Builder
	b.WriteString("📐 <b>倍率列表</b>")
	if len(items) == 0 {
		return b.String() + "\n暂无有效检测结果。"
	}
	b.WriteString("（" + fmt.Sprintf("%d 站", len(items)) + "）\n")
	for _, it := range items {
		line := fmt.Sprintf("<b>%d</b> %s ｜ %s：", it.ChannelID, EscapeHTML(it.Name), EscapeHTML(it.Model))
		if it.Ratio == nil {
			line += "暂无实测"
		} else {
			line += fmt.Sprintf("%.2fx", *it.Ratio)
			if it.Limit > 0 {
				line += fmt.Sprintf("（上限 %.2fx）", it.Limit)
			}
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("💡 详情：/ratio &lt;ID&gt;")
	return b.String()
}

// FormatRatioDetail /ratio <id> 站点各模型实测倍率（结构化数据由 query 层提供）。
func FormatRatioDetail(name string, limit float64, items []RatioDetailItem) string {
	var b strings.Builder
	b.WriteString("📐 <b>" + EscapeHTML(name) + "</b> 实测倍率")
	if limit > 0 {
		b.WriteString(fmt.Sprintf("（上限 %.2fx）", limit))
	}
	b.WriteString("\n")
	b.WriteString("━━━━━━━━━━━━\n")
	if len(items) == 0 {
		b.WriteString("暂无有效检测结果。\n")
		return b.String()
	}
	for _, it := range items {
		over := ""
		if limit > 0 && it.Ratio > limit {
			over = " 🔴超限"
		}
		b.WriteString(fmt.Sprintf("📐 %s：<b>%.2fx</b>（%s）｜ 🕐 %s%s\n",
			EscapeHTML(it.Model), it.Ratio, basisLabel(it.Basis), formatCheckedAt(&it.CheckedAt), over))
	}
	return b.String()
}

func basisLabel(b string) string {
	if b == "official" {
		return "官网价基准"
	}
	return "混合基准"
}

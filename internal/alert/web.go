package alert

import (
	"strconv"
	"time"
)

// WebAlert 是告警 DTO → 旧版 Web JSON 结构的适配（保持前端 AlertsView/AlertPopup 兼容）。
// 字段语义与旧 buildAlerts 输出完全一致，并附加持久化带来的新字段。
type WebAlert struct {
	ID             string `json:"id"`
	Name           string `json:"name"`    // 旧版完整文案（如 "余额不足: X 剩余 $0.50（阈值 $1.00）"）
	Channel        string `json:"channel"` // 站点名
	Model          string `json:"model,omitempty"`
	Sev            string `json:"sev"` // critical | warning
	Ago            string `json:"ago"` // 旧版相对时间（"刚刚"/"N分钟前"/…）
	Key            string `json:"alert_key"`
	Type           string `json:"alert_type"`
	Title          string `json:"title"`
	Impact         string `json:"impact,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	AdminPath      string `json:"admin_path,omitempty"`
	FirstSeenAt    string `json:"first_seen_at"`
	LastSeenAt     string `json:"last_seen_at"`
	Occurrence     int    `json:"occurrence_count"`
}

// ToWebAlert 单条 DTO → 旧版 Web 结构。
func ToWebAlert(a Alert, now time.Time) WebAlert {
	channel := a.ChannelName
	ago := "—"
	if !a.LastSeenAt.IsZero() {
		ago = formatAgo(a.LastSeenAt, now)
	}
	return WebAlert{
		ID:             LegacyID(a),
		Name:           a.Message,
		Channel:        channel,
		Model:          a.Model,
		Sev:            string(a.Severity),
		Ago:            ago,
		Key:            a.Key,
		Type:           a.Type,
		Title:          a.Title,
		Impact:         a.Impact,
		Recommendation: a.Recommendation,
		AdminPath:      a.AdminPath,
		FirstSeenAt:    formatRFC3339(a.FirstSeenAt),
		LastSeenAt:     formatRFC3339(a.LastSeenAt),
		Occurrence:     a.OccurrenceCount,
	}
}

// ToWebAlerts 批量转换。
func ToWebAlerts(alerts []Alert, now time.Time) []WebAlert {
	out := make([]WebAlert, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, ToWebAlert(a, now))
	}
	return out
}

// formatAgo 旧版相对时间文案（与 api.formatAgo 语义一致）。
func formatAgo(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "分钟前"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "小时前"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "天前"
	}
}

func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

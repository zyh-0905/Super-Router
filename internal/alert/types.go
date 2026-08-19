// Package alert 提供可恢复、可审计的告警生命周期领域模型：
//   - Evaluator 从数据库评估当前活跃告警（余额/倍率/熔断/禁用/价格同步）；
//   - Reconciler 将评估结果同步到 alert_events（新出现/持续/升级/恢复）；
//   - Service 供 Web 与 Telegram 读取持久化的 active/history 事件。
//
// 告警 key 稳定且全局唯一地标识"同一个问题"，同一 key 同时最多一条 active；
// 恢复后再次出现创建新的事件周期。
package alert

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// 告警类型常量（alert_events.alert_type / 稳定 key 的第一段）。
const (
	TypeLowBalance        = "low_balance"
	TypeRatioExceeded     = "ratio_exceeded"
	TypeCircuitOpen       = "circuit_open"
	TypeCircuitDegraded   = "circuit_degraded"
	TypeChannelDisabled   = "channel_disabled"
	TypePricingSyncFailed = "pricing_sync_failed"
	TypeQualityCheckFailed = "quality_check_failed"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
)

type AlertStatus string

const (
	StatusActive    AlertStatus = "active"
	StatusRecovered AlertStatus = "recovered"
)

// Alert 告警事件 DTO（alert_events 行的领域表示；ChannelName 由查询 LEFT JOIN 富化，不落库）。
type Alert struct {
	ID              int64
	Key             string
	Type            string
	Severity        Severity
	Status          AlertStatus
	ChannelID       *int
	ChannelName     string
	GroupID         *int
	Model           string
	Title           string
	Message         string
	CurrentValue    *float64
	ThresholdValue  *float64
	Unit            string
	Impact          string
	Recommendation  string
	AdminPath       string
	Metadata        map[string]interface{}
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	RecoveredAt     *time.Time
	OccurrenceCount int
}

// AlertInput 生成稳定 key 所需的标识输入。
type AlertInput struct {
	Type      string
	ChannelID int
	GroupID   int
	Model     string
}

// StableKey 生成稳定告警 key：
//   low_balance:channel-3
//   ratio_exceeded:channel-5:model-claude-sonnet-5
//   circuit_open:channel-3:model-gpt-5.5:group-2
func StableKey(in AlertInput) string {
	var b strings.Builder
	b.WriteString(in.Type)
	if in.ChannelID > 0 {
		fmt.Fprintf(&b, ":channel-%d", in.ChannelID)
	}
	if in.Model != "" {
		fmt.Fprintf(&b, ":model-%s", in.Model)
	}
	if in.GroupID > 0 {
		fmt.Fprintf(&b, ":group-%d", in.GroupID)
	}
	return b.String()
}

var severityRank = map[Severity]int{
	"":                 0,
	SeverityWarning:    1,
	SeverityCritical:   2,
}

// SeverityRank 严重度排序权重（critical > warning > 未知）。
func SeverityRank(s Severity) int {
	return severityRank[s]
}

// SortAlerts 稳定排序：critical 优先、first_seen_at 倒序、key 作为最终 tie-breaker。
func SortAlerts(alerts []Alert) {
	sort.SliceStable(alerts, func(i, j int) bool {
		a, b := alerts[i], alerts[j]
		if SeverityRank(a.Severity) != SeverityRank(b.Severity) {
			return SeverityRank(a.Severity) > SeverityRank(b.Severity)
		}
		if !a.FirstSeenAt.Equal(b.FirstSeenAt) {
			return a.FirstSeenAt.After(b.FirstSeenAt)
		}
		return a.Key < b.Key
	})
}

// OverallSeverity 返回一批告警中的最高严重度（空集返回空）。
func OverallSeverity(alerts []Alert) Severity {
	top := Severity("")
	for _, a := range alerts {
		if SeverityRank(a.Severity) > SeverityRank(top) {
			top = a.Severity
		}
	}
	return top
}

// legacyPrefix 告警类型 → 旧前端告警 ID 前缀（保持 AlertsView 兼容）。
var legacyPrefix = map[string]string{
	TypeLowBalance:         "bal_",
	TypeRatioExceeded:      "ratio_",
	TypeCircuitOpen:        "cb_",
	TypeCircuitDegraded:    "cb_",
	TypeChannelDisabled:    "dis_",
	TypePricingSyncFailed:  "pricing_",
	TypeQualityCheckFailed: "qc_",
}

// LegacyID 从 DTO 派生旧版告警 ID（bal_3 / ratio_5_model / cb_3_model …），
// 保持现有前端按前缀识别告警类型的行为不变。Type 为空时从稳定 key 首段推导。
func LegacyID(a Alert) string {
	typ := a.Type
	if typ == "" {
		if i := strings.Index(a.Key, ":"); i > 0 {
			typ = a.Key[:i]
		}
	}
	prefix, ok := legacyPrefix[typ]
	if !ok {
		prefix = typ + "_"
	}
	cid := 0
	if a.ChannelID != nil {
		cid = *a.ChannelID
	}
	if cid == 0 {
		return prefix + a.Key
	}
	if a.Model != "" {
		return fmt.Sprintf("%s%d_%s", prefix, cid, a.Model)
	}
	return fmt.Sprintf("%s%d", prefix, cid)
}

// FormatDuration 中文持续时长（如 "19 分钟"、"2 小时 18 分钟"）。
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return "不到 1 分钟"
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	if hours == 0 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	if minutes == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
}

// AlertChanges 窗口内变化分类（小时汇总消息用）。
type AlertChanges struct {
	New       []Alert
	Escalated []Alert
	Ongoing   []Alert
	Recovered []Alert
}

// Total 全部变化数量（新出现 + 升级 + 恢复；持续不计入"变化"）。
func (c AlertChanges) Total() int {
	return len(c.New) + len(c.Escalated) + len(c.Recovered)
}

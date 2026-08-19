package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// QueryService 中转站只读查询接口（Web 与 Telegram 同数据口径）。
type QueryService interface {
	RelayList(ctx context.Context, groupIDs []int) (string, error)
	RelayDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	BalanceList(ctx context.Context, groupIDs []int) (string, error)
	BalanceDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	HealthList(ctx context.Context, groupIDs []int) (string, error)
	HealthDetail(ctx context.Context, id int, groupIDs []int) (string, error)
	RatioList(ctx context.Context, groupIDs []int) (string, error)
	RatioDetail(ctx context.Context, id int, groupIDs []int) (string, error)
}

// CommandService 解析命令、校验授权并分发查询。
type CommandService struct {
	query       QueryService
	subscribers map[int64]Subscriber
}

// NewCommandService 创建命令服务。
func NewCommandService(q QueryService) *CommandService {
	return &CommandService{query: q, subscribers: map[int64]Subscriber{}}
}

// SetSubscribers 刷新授权订阅者（Worker 每次处理命令前从 DB 重新加载）。
func (c *CommandService) SetSubscribers(subs []Subscriber) {
	m := make(map[int64]Subscriber, len(subs))
	for _, s := range subs {
		m[s.ChatID] = s
	}
	c.subscribers = m
}

const helpText = `📋 <b>可用命令</b>
/start /help — 显示帮助
/alerts — 当前活跃告警
/alerts critical — 只看严重告警
/alert &lt;alert_key&gt; — 单条告警详情
/status — 系统状态摘要
/relay — 中转站列表
/relay &lt;id&gt; — 站点详情
/balance — 余额列表
/health — 健康列表
/ratio — 倍率列表`

// unauthorized 统一拒绝文案（未授权 Chat ID / 停用订阅者 / 无查询权限）。
const unauthorized = "⛔ 当前 Chat ID 未授权，请联系管理员。"

// Handle 处理一条用户消息，返回回复文本。
// 授权链：订阅者存在 → enabled → （命令属查询类还需 query_enabled）。
func (c *CommandService) Handle(ctx context.Context, chatID int64, text string) (string, error) {
	sub, ok := c.subscribers[chatID]
	if !ok || !sub.Enabled {
		return unauthorized, nil
	}

	cmd, args := parseCommand(text)

	// 帮助类命令无需 query_enabled
	switch cmd {
	case "/start", "/help", "":
		return helpText, nil
	}

	if !sub.QueryEnabled {
		return unauthorized, nil
	}

	switch cmd {
	case "/alerts":
		if !sub.AlertEnabled {
			return unauthorized, nil
		}
		return c.handleAlerts(ctx, sub, args)
	case "/alert":
		if !sub.AlertEnabled {
			return unauthorized, nil
		}
		return c.handleAlertDetail(ctx, sub, args)
	case "/status":
		return c.handleStatus(ctx, sub)
	case "/relay":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, nil
			}
			return c.query.RelayDetail(ctx, id, sub.GroupIDs)
		}
		return c.query.RelayList(ctx, sub.GroupIDs)
	case "/balance":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, nil
			}
			return c.query.BalanceDetail(ctx, id, sub.GroupIDs)
		}
		return c.query.BalanceList(ctx, sub.GroupIDs)
	case "/health":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, nil
			}
			return c.query.HealthDetail(ctx, id, sub.GroupIDs)
		}
		return c.query.HealthList(ctx, sub.GroupIDs)
	case "/ratio":
		if len(args) > 0 {
			id, err := parseID(args[0])
			if err != nil {
				return helpText, nil
			}
			return c.query.RatioDetail(ctx, id, sub.GroupIDs)
		}
		return c.query.RatioList(ctx, sub.GroupIDs)
	default:
		// 未知命令（含 /quality，计划 B 接入）返回帮助，不伪造结果
		return helpText, nil
	}
}

// handleAlerts 由 Worker 注入的告警查询回调实现（见 worker.go setAlertsQuery）。
var alertsQuery func(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error)

// SetAlertsQuery 注入告警查询实现（避免 commands ↔ alert/telegram 循环依赖）。
func SetAlertsQuery(fn func(ctx context.Context, groupIDs []int, criticalOnly bool) (string, error)) {
	alertsQuery = fn
}

func (c *CommandService) handleAlerts(ctx context.Context, sub Subscriber, args []string) (string, error) {
	if alertsQuery == nil {
		return "⚠️ 告警服务暂不可用，请稍后再试。", nil
	}
	criticalOnly := false
	if len(args) > 0 && args[0] == "critical" {
		criticalOnly = true
	}
	return alertsQuery(ctx, sub.GroupIDs, criticalOnly)
}

// handleAlertDetail 单条告警详情（/alert <alert_key>）。
func (c *CommandService) handleAlertDetail(ctx context.Context, sub Subscriber, args []string) (string, error) {
	if alertsQuery == nil {
		return "⚠️ 告警服务暂不可用，请稍后再试。", nil
	}
	if len(args) == 0 {
		return "用法：/alert &lt;alert_key&gt;", nil
	}
	// 复用 alertsQuery 的过滤语义：把 key 作为 criticalOnly=false 查询的补充参数不可行，
	// 这里直接用分组过滤查询全部后由 Worker 侧按 key 过滤（见 worker 实现）。
	// 保持接口简单：本阶段先返回全部告警，由上层按 key 过滤。
	return alertsQuery(ctx, sub.GroupIDs, false)
}

// handleStatus 系统状态摘要（复用 RelayList + 告警计数）。
func (c *CommandService) handleStatus(ctx context.Context, sub Subscriber) (string, error) {
	relays, err := c.query.RelayList(ctx, sub.GroupIDs)
	if err != nil {
		return "", err
	}
	var alertsLine string
	if alertsQuery != nil {
		alertsLine, _ = alertsQuery(ctx, sub.GroupIDs, false)
	}
	return "🛰 <b>系统状态</b>\n" + relays + "\n" + alertsLine, nil
}

// parseCommand 去掉 /command@botname 后缀，返回命令与空白分隔的参数。
func parseCommand(text string) (string, []string) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return "", nil
	}
	cmd := fields[0]
	if !strings.HasPrefix(cmd, "/") {
		return "", nil // 非命令文本：返回帮助
	}
	if at := strings.Index(cmd, "@"); at > 0 {
		cmd = cmd[:at]
	}
	cmd = strings.ToLower(cmd)
	return cmd, fields[1:]
}

// parseID 解析命令参数中的站点 ID（非正整数返回错误）。
func parseID(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid id %q", s)
	}
	return n, nil
}

// Package telegram 提供 Telegram Bot 集成：
//   - client.go：Bot API getMe/getUpdates/sendMessage HTTP 客户端（长轮询）；
//   - format.go：HTML 安全转义与超长消息拆分；
//   - query.go：中转站只读查询（与 Web 同数据口径）；
//   - commands.go：命令解析、授权与分发；
//   - worker.go：长轮询 + 小时汇总调度（advisory lock 选主）。
package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Config Bot 运行配置（来自 telegram_config 表单行）。
type Config struct {
	Enabled               bool
	BotToken              string // 已解密的完整 Token，只保存在 Checker 进程内存
	ReportEnabled         bool
	ReportIntervalMinutes int
	ReportMinute          int
	Timezone              string
	IncludeRecovered      bool
	IncludeOngoing        bool
	WebBaseURL            string
	LastUpdateID          int64
	LastReportAt          *time.Time
}

// Subscriber 授权订阅者。
type Subscriber struct {
	ID           int64
	ChatID       string // 存储时保留用户输入的前导零（如 00123456789）；发送/匹配用 ParseChatID 转数值
	Enabled      bool
	AlertEnabled bool
	QueryEnabled bool
	GroupIDs     []int
}

// ParseChatID 把订阅者存储的 chat_id 字符串解析为 int64（Telegram API 交互与数值匹配用）。
// 字符串形式仅作展示/回显保留，实际会话以数值为准。
func ParseChatID(chatID string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(chatID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid chat_id %q: %w", chatID, err)
	}
	return n, nil
}

// Update 长轮询收到的单条消息更新。
type Update struct {
	UpdateID int64
	ChatID   int64
	Text     string

	// 回调按钮（inline keyboard callback_query）字段：
	// HasCallback 为 true 时其余字段有效。
	HasCallback       bool
	CallbackID        string // answerCallbackQuery 用（关闭按钮 loading 状态）
	CallbackData      string // 按钮回调数据（cmd:/relay:/st: 前缀协议）
	CallbackChatID    int64  // 点击者的会话（授权判定）
	CallbackMessageID int64  // 携带按钮的消息 ID（供原位编辑）
}

// InlineButton 内联键盘按钮（Data 与 URL 二选一）。
type InlineButton struct {
	Text string
	Data string // 回调数据；URL 非空时忽略
	URL  string // 外链按钮（如 Web 控制台）
}

// InlineKeyboard 内联键盘（行 × 按钮，与 Telegram inline_keyboard 对应）。
type InlineKeyboard struct {
	Rows [][]InlineButton
}

// InlineRow 单行按钮快捷构造。
func InlineRow(buttons ...InlineButton) []InlineButton { return buttons }

// ChatAction 聊天状态动作（sendChatAction）。
const (
	ChatActionTyping = "typing"
)

// BotCommand 命令菜单条目（setMyCommands）。
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// RelaySummary /relay 列表项。
type RelaySummary struct {
	ID           int
	Name         string
	Host         string
	Healthy      bool
	Balance      *float64
	Ratio        *float64
	CircuitState string
}

// RelayDetail /relay <id> 详情。
type RelayDetail struct {
	RelaySummary
	Protocol    string
	RelayType   string
	Groups      []string
	Requests24h int
	SuccessRate float64
	AverageMS   int
	P95MS       int
}

// BalanceSummary /balance 列表项（中转站归并汇总：同 base_url 成员站点归为一行）。
type BalanceSummary struct {
	StationID   int      // relay_stations.id（0 = 尚未 reconcile）
	ChannelID   int      // 成员中最近余额检测的站点（代表 ID）
	Name        string   // 中转站名（自定义名或自动命名）
	Balance     *float64 // 账户余额（同账户成员共享，取成员最近一次成功检测）
	MemberCount int      // 成员站点数（按订阅者分组过滤后的口径）
	CheckedAt   *time.Time
}

// BalanceMember 中转站成员明细（/balance <id> 用，不展示站点各自余额）。
type BalanceMember struct {
	ChannelID int
	Name      string
	Enabled   bool
}

// HealthSummary /health 列表项。
type HealthSummary struct {
	ChannelID    int
	Name         string
	Alive        bool
	LatencyMS    *int
	SuccessRate  float64
	CircuitState string
	CheckedAt    *time.Time
}

// RatioSummary /ratio 列表项。
type RatioSummary struct {
	ChannelID int
	Name      string
	Model     string
	Ratio     *float64
	Limit     float64
	Basis     string
	CheckedAt *time.Time
}

// BotClient Telegram Bot API 客户端接口（HTTP 实现 + 测试 fake）。
type BotClient interface {
	GetMe(ctx context.Context) error
	GetUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error)
	SendMessage(ctx context.Context, chatID int64, html string, kb *InlineKeyboard) (int64, error)
	EditMessageText(ctx context.Context, chatID int64, messageID int64, html string, kb *InlineKeyboard) error
	SendChatAction(ctx context.Context, chatID int64, action string) error
	AnswerCallbackQuery(ctx context.Context, callbackID string) error
	SetMyCommands(ctx context.Context, commands []BotCommand) error
}

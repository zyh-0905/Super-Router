// Package telegram 提供 Telegram Bot 集成：
//   - client.go：Bot API getMe/getUpdates/sendMessage HTTP 客户端（长轮询）；
//   - format.go：HTML 安全转义与超长消息拆分；
//   - query.go：中转站只读查询（与 Web 同数据口径）；
//   - commands.go：命令解析、授权与分发；
//   - worker.go：长轮询 + 小时汇总调度（advisory lock 选主）。
package telegram

import (
	"context"
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
	ChatID       int64
	Enabled      bool
	AlertEnabled bool
	QueryEnabled bool
	GroupIDs     []int
}

// Update 长轮询收到的单条消息更新。
type Update struct {
	UpdateID int64
	ChatID   int64
	Text     string
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

// BalanceSummary /balance 列表项。
type BalanceSummary struct {
	ChannelID int
	Name      string
	Balance   *float64
	Currency  string
	Source    string
	CheckedAt *time.Time
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
	SendMessage(ctx context.Context, chatID int64, html string) (int64, error)
}

package replay

import (
	"time"
)

// DecisionLog 决策日志记录
type DecisionLog struct {
	ID               int64     `json:"id"`
	RequestID        string    `json:"request_id"`
	TokenIDHash      string    `json:"token_id_hash"`
	Model            string    `json:"model"`
	IsStream         bool      `json:"is_stream"`
	PolicyVersion    string    `json:"policy_version"`
	Strategy         string    `json:"strategy"`
	Epoch            int64     `json:"epoch"`
	SnapshotChecksum string    `json:"snapshot_checksum"`
	CandidateOrder   []int     `json:"candidate_order"`
	SelectedChannel  int       `json:"selected_channel"`
	DecisionReason   string    `json:"decision_reason"`
	DecidedAt        time.Time `json:"decided_at"`
}

// ReplayRequest 重放请求
type ReplayRequest struct {
	RequestIDs  []string  // 要重放的请求 ID 列表
	StartTime   time.Time // 时间范围开始
	EndTime     time.Time // 时间范围结束
	Limit       int       // 最大重放数量
	NewStrategy string    // 新策略名称（可选，用于对比）
}

// ReplayResult 重放结果
type ReplayResult struct {
	RequestID       string    `json:"request_id"`
	OriginalChannel int       `json:"original_channel"`
	OriginalReason  string    `json:"original_reason"`
	ReplayChannel   int       `json:"replay_channel"`
	ReplayReason    string    `json:"replay_reason"`
	ChannelChanged  bool      `json:"channel_changed"`
	Epoch           int64     `json:"epoch"`
	Model           string    `json:"model"`
	Strategy        string    `json:"strategy"`
	DecidedAt       time.Time `json:"decided_at"`
}

// Report 重放报告
type Report struct {
	TotalRequests       int            `json:"total_requests"`
	SuccessfulReplays   int            `json:"successful_replays"`
	FailedReplays       int            `json:"failed_replays"`
	ChannelChangedCount int            `json:"channel_changed_count"`
	ChannelChangedRate  float64        `json:"channel_changed_rate"`
	StrategyUsed        string         `json:"strategy_used"`
	TimeRange           string         `json:"time_range"`
	GeneratedAt         time.Time      `json:"generated_at"`
	Details             []ReplayResult `json:"details"`
}

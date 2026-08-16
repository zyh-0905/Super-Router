package replay

import (
	"encoding/json"
	"time"
)

// DecisionLog 决策日志记录（含重放所需的完整上下文，P1-05）
type DecisionLog struct {
	ID               int64           `json:"id"`
	RequestID        string          `json:"request_id"`
	TokenIDHash      string          `json:"token_id_hash"`
	Model            string          `json:"model"`
	IsStream         bool            `json:"is_stream"`
	PolicyVersion    string          `json:"policy_version"`
	Strategy         string          `json:"strategy"`
	Epoch            int64           `json:"epoch"`
	SnapshotChecksum string          `json:"snapshot_checksum"`
	CandidateOrder   []int           `json:"candidate_order"`
	SelectedChannel  *int            `json:"selected_channel"` // 渠道删除后为 NULL（ON DELETE SET NULL）
	DecisionReason   string          `json:"decision_reason"`
	GroupID          *int            `json:"group_id"`
	GroupIDs         []int           `json:"group_ids"`
	Capabilities     []string        `json:"capabilities"`
	EstimatedInput   int             `json:"estimated_input"`
	MaxOutput        int             `json:"max_output"`
	TimeoutMS        int             `json:"timeout_ms"`
	EffectivePolicy  json.RawMessage `json:"effective_policy"`
	DecidedAt        time.Time       `json:"decided_at"`
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
	RequestID       string `json:"request_id"`
	OriginalChannel int    `json:"original_channel"`
	OriginalReason  string `json:"original_reason"`
	ReplayChannel   int    `json:"replay_channel"`
	ReplayReason    string `json:"replay_reason"`
	ChannelChanged  bool   `json:"channel_changed"`
	Epoch           int64  `json:"epoch"`
	Model           string `json:"model"`
	Strategy        string `json:"strategy"`
	// Deterministic 为 true 时表示重放基于历史归档快照与生效策略快照，
	// 结果可作为“当时为何如此决策”的审计证据；false 表示部分输入已无法还原，
	// 结果为当前环境模拟，不具备审计确定性（P1-05）。
	Deterministic bool      `json:"deterministic"`
	DecidedAt     time.Time `json:"decided_at"`
}

// Report 重放报告
type Report struct {
	TotalRequests       int       `json:"total_requests"`
	SuccessfulReplays   int       `json:"successful_replays"`
	FailedReplays       int       `json:"failed_replays"`
	ChannelChangedCount int       `json:"channel_changed_count"`
	ChannelChangedRate  float64   `json:"channel_changed_rate"`
	StrategyUsed        string    `json:"strategy_used"`
	TimeRange           string    `json:"time_range"`
	GeneratedAt         time.Time `json:"generated_at"`
	// DeterministicCount 基于历史快照的确定性重放数量
	DeterministicCount int `json:"deterministic_count"`
	// SimulatedCount 当前环境模拟数量（快照归档或策略快照缺失）
	SimulatedCount int `json:"simulated_count"`
	// Note 非确定性说明（SimulatedCount > 0 时提示）
	Note    string         `json:"note,omitempty"`
	Details []ReplayResult `json:"details"`
}

package router

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"smart-router/internal/store"
)

// metrics 记录函数（避免循环依赖）
var (
	recordRoutingDuration      func(float64)
	recordSnapshotLoadDuration func(float64)
)

func init() {
	// 默认空实现，由 metrics 包在初始化时注入
	recordRoutingDuration = func(float64) {}
	recordSnapshotLoadDuration = func(float64) {}
}

// SetMetricsRecorders 设置 metrics 记录器（由 main 调用）
func SetMetricsRecorders(routingDuration, snapshotLoadDuration func(float64)) {
	recordRoutingDuration = routingDuration
	recordSnapshotLoadDuration = snapshotLoadDuration
}

// RouteRequest 路由请求
type RouteRequest struct {
	RequestID      string
	TokenID        string
	Model          string
	IsStream       bool
	Capabilities   []string
	EstimatedInput int
	MaxOutput      int
	TimeoutMS      int
	GroupID        *int  // 可选：限定在指定分组内路由
	GroupIDs       []int // 可选：限定在多个分组的并集内路由（Key 绑定组场景）
}

// AttemptRecord 请求尝试记录
type AttemptRecord struct {
	ChannelID       int       `json:"channel_id"`
	StartedAt       time.Time `json:"started_at"`
	DurationMS      int       `json:"duration_ms"`
	FirstByteCommit bool      `json:"first_byte_commit"`
	StatusCode      int       `json:"status_code,omitempty"`
	ErrorClass      string    `json:"error_class,omitempty"`
}

// RouteResult 路由结果
type RouteResult struct {
	SelectedChannel  *ChannelHealth
	AllScores        map[int]float64
	CandidateOrder   []int
	Excluded         []Exclusion
	DecisionReason   string
	Strategy         string
	PolicyVersion    string
	Epoch            int64
	SnapshotChecksum string
	GroupID          *int
	GroupName        string
}

// Router 路由决策器
type Router struct {
	db           *store.DB
	redis        *store.RedisClient
	policyLoader *PolicyLoader
}

func NewRouter(db *store.DB, redis *store.RedisClient) *Router {
	return &Router{
		db:           db,
		redis:        redis,
		policyLoader: NewPolicyLoader(db),
	}
}

// Route 执行路由决策（纯函数）
func (r *Router) Route(ctx context.Context, req RouteRequest) (*RouteResult, error) {
	// 记录路由决策开始时间
	startTime := time.Now()
	defer func() {
		// 记录路由决策耗时（使用匿名 import 避免循环依赖）
		duration := time.Since(startTime).Seconds()
		recordRoutingDuration(duration)
	}()

	// 阶段 A：请求标准化（已在 RouteRequest 中完成）

	// 阶段 B：读取分组（如指定）与策略
	var group *ChannelGroup
	if req.GroupID != nil {
		g, err := r.policyLoader.LoadGroup(ctx, *req.GroupID)
		if err != nil {
			return nil, fmt.Errorf("load group: %w", err)
		}
		if !g.Enabled {
			return nil, fmt.Errorf("group %s is disabled", g.Name)
		}
		group = g
	}

	policy, err := r.policyLoader.LoadPolicy(ctx, req.TokenID, req.Model, group)
	if err != nil {
		return nil, fmt.Errorf("load policy: %w", err)
	}

	// 阶段 C：构建手动候选集（从快照读取）
	snapshotStart := time.Now()
	snapshot, err := LoadSnapshot(ctx, r.db, r.redis)
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	recordSnapshotLoadDuration(time.Since(snapshotStart).Seconds())

	// 阶段 D：硬过滤
	filter := NewHardFilter(policy)
	filterReq := &FilterRequest{
		Model:          req.Model,
		Capabilities:   req.Capabilities,
		EstimatedInput: req.EstimatedInput,
		MaxOutput:      req.MaxOutput,
	}

	// 允许的分组集合（nil = 不限制）
	var allowedGroups map[int]bool
	if len(req.GroupIDs) > 0 {
		allowedGroups = make(map[int]bool, len(req.GroupIDs))
		for _, gid := range req.GroupIDs {
			allowedGroups[gid] = true
		}
	}

	var candidates []*ChannelHealth
	var excluded []Exclusion

	for _, ch := range snapshot.Channels {
		// 分组过滤：渠道必须属于允许集合中的至少一个分组
		if allowedGroups != nil {
			inGroup := false
			for _, gid := range ch.GroupIDs {
				if allowedGroups[gid] {
					inGroup = true
					break
				}
			}
			if !inGroup {
				excluded = append(excluded, Exclusion{ChannelID: ch.ID, Reason: "group_not_allowed"})
				continue
			}
		}

		if exclusion := filter.Filter(ch, filterReq); exclusion != nil {
			excluded = append(excluded, *exclusion)
		} else {
			candidates = append(candidates, ch)
		}
	}

	// 如果没有候选，返回错误
	if len(candidates) == 0 {
		return &RouteResult{
			Excluded:         excluded,
			PolicyVersion:    policy.Version,
			Epoch:            snapshot.Epoch,
			SnapshotChecksum: snapshot.ContentHash,
			GroupID:          req.GroupID,
			DecisionReason:   "no_active_channel_after_filtering",
		}, fmt.Errorf("no active channel available")
	}

	// 阶段 E：策略排序
	strategy := GetStrategy(policy.Strategy)
	scored := strategy.Sort(candidates, filterReq, policy)

	// 构建结果
	result := &RouteResult{
		SelectedChannel:  scored[0].Channel,
		AllScores:        make(map[int]float64),
		CandidateOrder:   make([]int, 0, len(scored)),
		Excluded:         excluded,
		Strategy:         policy.Strategy,
		PolicyVersion:    policy.Version,
		Epoch:            snapshot.Epoch,
		SnapshotChecksum: snapshot.ContentHash,
		GroupID:          req.GroupID,
	}

	if group != nil {
		result.GroupName = group.Name
	}

	for _, s := range scored {
		result.AllScores[s.Channel.ID] = s.Score
		result.CandidateOrder = append(result.CandidateOrder, s.Channel.ID)
	}

	result.DecisionReason = fmt.Sprintf(
		"selected channel %s (id=%d) using strategy=%s, score=%.4f%s",
		result.SelectedChannel.Name,
		result.SelectedChannel.ID,
		policy.Strategy,
		result.AllScores[result.SelectedChannel.ID],
		groupSuffix(group),
	)

	return result, nil
}

func groupSuffix(group *ChannelGroup) string {
	if group == nil {
		return ""
	}
	return fmt.Sprintf(", group=%s(id=%d)", group.Name, group.ID)
}

// LogDecision 记录决策日志到数据库
func (r *Router) LogDecision(ctx context.Context, req RouteRequest, result *RouteResult) error {
	// 序列化为 JSON
	candidateOrderJSON, _ := toJSON(result.CandidateOrder)
	excludedJSON, _ := toJSON(result.Excluded)
	allScoresJSON, _ := toJSON(result.AllScores)
	attemptsJSON := `[]` // 尝试记录在实际调用时填充

	var selectedChannelID *int
	if result.SelectedChannel != nil {
		selectedChannelID = &result.SelectedChannel.ID
	}

	// 写入数据库
	_, err := r.db.Pool.Exec(ctx, `
		INSERT INTO decision_logs (
			request_id, token_id_hash, model, is_stream,
			policy_version, strategy, epoch, snapshot_checksum,
			candidate_order, excluded, all_scores,
			attempts, selected_channel, decision_reason, group_id, decided_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NOW())
	`,
		req.RequestID,
		hashTokenID(req.TokenID),
		req.Model,
		req.IsStream,
		result.PolicyVersion,
		result.Strategy,
		result.Epoch,
		result.SnapshotChecksum,
		candidateOrderJSON,
		excludedJSON,
		allScoresJSON,
		attemptsJSON,
		selectedChannelID,
		result.DecisionReason,
		result.GroupID,
	)

	return err
}

// ===== 辅助函数 =====

func toJSON(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}", err
	}
	return string(data), nil
}

func hashTokenID(tokenID string) string {
	// 简单的 SHA256 哈希
	hash := sha256.Sum256([]byte(tokenID))
	return fmt.Sprintf("%x", hash)
}

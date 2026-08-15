package replay

import (
	"context"
	"fmt"
	"time"

	"smart-router/internal/router"
	"smart-router/internal/store"
)

// Replayer 重放器
type Replayer struct {
	db     *store.DB
	redis  *store.RedisClient
	router *router.Router
}

// NewReplayer 创建重放器
func NewReplayer(db *store.DB, redis *store.RedisClient) *Replayer {
	return &Replayer{
		db:     db,
		redis:  redis,
		router: router.NewRouter(db, redis),
	}
}

// SetPolicyDefaults 注入系统级策略默认值（与网关保持一致，重放结果才可比）
func (r *Replayer) SetPolicyDefaults(d router.PolicyDefaults) {
	r.router.SetPolicyDefaults(d)
}

// Replay 执行重放
func (r *Replayer) Replay(ctx context.Context, req ReplayRequest) (*Report, error) {
	// 1. 加载历史决策日志
	decisions, err := r.loadDecisions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("load decisions: %w", err)
	}

	if len(decisions) == 0 {
		return &Report{
			TotalRequests: 0,
			GeneratedAt:   time.Now(),
			StrategyUsed:  req.NewStrategy,
			TimeRange:     fmt.Sprintf("%s to %s", req.StartTime.Format(time.RFC3339), req.EndTime.Format(time.RFC3339)),
		}, nil
	}

	// 2. 重放每个决策
	results := make([]ReplayResult, 0, len(decisions))
	successCount := 0
	failedCount := 0
	changedCount := 0

	for _, decision := range decisions {
		result, err := r.replayOne(ctx, decision, req.NewStrategy)
		if err != nil {
			// 记录失败但继续处理
			failedCount++
			continue
		}

		successCount++
		if result.ChannelChanged {
			changedCount++
		}
		results = append(results, *result)
	}

	// 3. 生成报告
	changedRate := 0.0
	if successCount > 0 {
		changedRate = float64(changedCount) / float64(successCount)
	}

	report := &Report{
		TotalRequests:       len(decisions),
		SuccessfulReplays:   successCount,
		FailedReplays:       failedCount,
		ChannelChangedCount: changedCount,
		ChannelChangedRate:  changedRate,
		StrategyUsed:        req.NewStrategy,
		TimeRange:           fmt.Sprintf("%s to %s", req.StartTime.Format(time.RFC3339), req.EndTime.Format(time.RFC3339)),
		GeneratedAt:         time.Now(),
		Details:             results,
	}

	return report, nil
}

// loadDecisions 从数据库加载历史决策
func (r *Replayer) loadDecisions(ctx context.Context, req ReplayRequest) ([]DecisionLog, error) {
	query := `
		SELECT id, request_id, token_id_hash, model, is_stream,
		       policy_version, strategy, epoch, snapshot_checksum,
		       candidate_order, selected_channel, decision_reason, decided_at
		FROM decision_logs
		WHERE 1=1
	`
	args := []interface{}{}
	argIdx := 1

	// 按请求 ID 过滤
	if len(req.RequestIDs) > 0 {
		query += fmt.Sprintf(" AND request_id = ANY($%d)", argIdx)
		args = append(args, req.RequestIDs)
		argIdx++
	} else {
		// 按时间范围过滤
		if !req.StartTime.IsZero() {
			query += fmt.Sprintf(" AND decided_at >= $%d", argIdx)
			args = append(args, req.StartTime)
			argIdx++
		}
		if !req.EndTime.IsZero() {
			query += fmt.Sprintf(" AND decided_at <= $%d", argIdx)
			args = append(args, req.EndTime)
			argIdx++
		}
	}

	query += " ORDER BY decided_at ASC"

	// 限制数量
	if req.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, req.Limit)
	}

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var decisions []DecisionLog
	for rows.Next() {
		var d DecisionLog
		var candidateOrder []byte

		err := rows.Scan(
			&d.ID,
			&d.RequestID,
			&d.TokenIDHash,
			&d.Model,
			&d.IsStream,
			&d.PolicyVersion,
			&d.Strategy,
			&d.Epoch,
			&d.SnapshotChecksum,
			&candidateOrder,
			&d.SelectedChannel,
			&d.DecisionReason,
			&d.DecidedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		// 解析 candidate_order JSON
		if err := parseJSONArray(candidateOrder, &d.CandidateOrder); err != nil {
			return nil, fmt.Errorf("parse candidate_order: %w", err)
		}

		decisions = append(decisions, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	return decisions, nil
}

// replayOne 重放单个决策
func (r *Replayer) replayOne(ctx context.Context, decision DecisionLog, newStrategy string) (*ReplayResult, error) {
	// 构建路由请求（使用历史数据）
	// 说明：决策日志只存 token 的 SHA256 哈希，无法还原原始 token，
	// 因此 Token 级专属策略在重放时查不到（会落到分组/系统默认）——已知限制。
	routeReq := router.RouteRequest{
		RequestID:        decision.RequestID + "-replay",
		Model:            decision.Model,
		IsStream:         decision.IsStream,
		OverrideStrategy: newStrategy,
		// 其他字段（能力/预算/分组）决策日志未记录，使用默认值
	}

	// 重新执行路由决策
	routeResult, err := r.router.Route(ctx, routeReq)
	if err != nil {
		return nil, fmt.Errorf("route: %w", err)
	}

	// 对比结果
	replayChannel := 0
	if routeResult.SelectedChannel != nil {
		replayChannel = routeResult.SelectedChannel.ID
	}

	originalChannel := 0
	if decision.SelectedChannel != nil {
		originalChannel = *decision.SelectedChannel
	}

	result := &ReplayResult{
		RequestID:       decision.RequestID,
		OriginalChannel: originalChannel,
		OriginalReason:  decision.DecisionReason,
		ReplayChannel:   replayChannel,
		ReplayReason:    routeResult.DecisionReason,
		ChannelChanged:  originalChannel != replayChannel,
		Epoch:           decision.Epoch,
		Model:           decision.Model,
		Strategy:        decision.Strategy,
		DecidedAt:       decision.DecidedAt,
	}

	return result, nil
}

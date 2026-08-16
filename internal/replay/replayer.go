package replay

import (
	"context"
	"encoding/json"
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

// Replay 执行重放（P1-05：优先使用历史归档快照与生效策略快照做确定性重放；
// 归档/策略缺失的决策标记为当前环境模拟，并在报告中明确非确定性）。
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
	deterministicCount := 0

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
		if result.Deterministic {
			deterministicCount++
		}
		results = append(results, *result)
	}

	// 3. 生成报告
	changedRate := 0.0
	if successCount > 0 {
		changedRate = float64(changedCount) / float64(successCount)
	}

	note := ""
	if deterministicCount < successCount {
		note = fmt.Sprintf("其中 %d 条决策因历史快照归档/策略快照缺失而基于当前环境模拟，其结果不可作为当时决策的审计证据（非确定性）", successCount-deterministicCount)
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
		DeterministicCount:  deterministicCount,
		SimulatedCount:      successCount - deterministicCount,
		Note:                note,
		Details:             results,
	}

	return report, nil
}

// loadDecisions 从数据库加载历史决策（含重放上下文）
func (r *Replayer) loadDecisions(ctx context.Context, req ReplayRequest) ([]DecisionLog, error) {
	query := `
		SELECT id, request_id, token_id_hash, model, is_stream,
		       policy_version, strategy, epoch, snapshot_checksum,
		       candidate_order, selected_channel, decision_reason, group_id,
		       COALESCE(capabilities::text, '[]'), estimated_input, max_output, timeout_ms,
		       COALESCE(group_ids::text, '[]'), COALESCE(effective_policy::text, '{}'),
		       decided_at
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
		var candidateOrder, capabilitiesJSON, groupIDsJSON, effectivePolicyJSON []byte

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
			&d.GroupID,
			&capabilitiesJSON,
			&d.EstimatedInput,
			&d.MaxOutput,
			&d.TimeoutMS,
			&groupIDsJSON,
			&effectivePolicyJSON,
			&d.DecidedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}

		// 解析 JSON 字段
		if err := parseJSONArray(candidateOrder, &d.CandidateOrder); err != nil {
			return nil, fmt.Errorf("parse candidate_order: %w", err)
		}
		if err := parseJSONArray(capabilitiesJSON, &d.Capabilities); err != nil {
			return nil, fmt.Errorf("parse capabilities: %w", err)
		}
		if err := parseJSONArray(groupIDsJSON, &d.GroupIDs); err != nil {
			return nil, fmt.Errorf("parse group_ids: %w", err)
		}
		d.EffectivePolicy = effectivePolicyJSON

		decisions = append(decisions, d)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	return decisions, nil
}

// loadArchivedSnapshot 按内容哈希加载历史归档快照（P1-05）。
// 未找到时返回 nil（调用方标记为当前环境模拟）。
func (r *Replayer) loadArchivedSnapshot(ctx context.Context, checksum string) *router.HealthSnapshot {
	if checksum == "" {
		return nil
	}
	var payload []byte
	err := r.db.Pool.QueryRow(ctx, `
		SELECT payload::text FROM snapshot_archive WHERE checksum = $1
	`, checksum).Scan(&payload)
	if err != nil {
		return nil
	}
	var snapshot router.HealthSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}

// replayOne 重放单个决策（确定性：历史归档快照 + 生效策略快照 + 完整路由上下文）
func (r *Replayer) replayOne(ctx context.Context, decision DecisionLog, newStrategy string) (*ReplayResult, error) {
	deterministic := true

	// 历史归档快照（不可变）；缺失 → 当前环境模拟
	archived := r.loadArchivedSnapshot(ctx, decision.SnapshotChecksum)
	if archived == nil {
		deterministic = false
	}

	// 生效策略快照；缺失 → 回退当前策略查找链（非确定性）
	var overridePolicy *router.Policy
	if len(decision.EffectivePolicy) > 0 {
		var p router.Policy
		if err := json.Unmarshal(decision.EffectivePolicy, &p); err == nil && p.Strategy != "" {
			overridePolicy = &p
		} else {
			deterministic = false
		}
	} else {
		deterministic = false
	}

	routeReq := router.RouteRequest{
		RequestID:        decision.RequestID + "-replay",
		Model:            decision.Model,
		IsStream:         decision.IsStream,
		Capabilities:     decision.Capabilities,
		EstimatedInput:   decision.EstimatedInput,
		MaxOutput:        decision.MaxOutput,
		TimeoutMS:        decision.TimeoutMS,
		GroupID:          decision.GroupID,
		GroupIDs:         decision.GroupIDs,
		OverridePolicy:   overridePolicy,
		OverrideStrategy: newStrategy,
		ReplaySnapshot:   archived,
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
		Deterministic:   deterministic,
		DecidedAt:       decision.DecidedAt,
	}

	return result, nil
}

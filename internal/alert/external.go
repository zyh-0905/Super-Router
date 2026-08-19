package alert

import (
	"context"
	"fmt"
	"time"
)

// QualityAlertSink 实现 quality.AlertSink：
//   - QualityFailure 为关键阶段失败创建/更新 warning 级 active 告警；
//   - ResolveQualityFailures 在同模型关键阶段全部通过后恢复对应 key。
//
// 关键阶段失败的告警 key 稳定：
//
//	quality_check_failed:channel-5:model-claude-sonnet-5
//
// Behavior attention 不创建告警（只有明确失败的关键阶段才会走到这里）。
type QualityAlertSink struct {
	store *SQLStore
}

// NewQualityAlertSink 创建质量失败告警写入器。
func NewQualityAlertSink(store *SQLStore) *QualityAlertSink {
	return &QualityAlertSink{store: store}
}

// QualityFailure 写入质量检测硬失败告警。
// 同 key 再失败只更新 occurrence 与 last_seen_at；行为 attention 不产生 Critical。
func (q *QualityAlertSink) QualityFailure(ctx context.Context, channelID int, model, stage, message string, metadata map[string]interface{}) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("alert store unavailable")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	if _, ok := metadata["stage"]; !ok {
		metadata["stage"] = stage
	}
	a := Alert{
		Key: StableKey(AlertInput{Type: TypeQualityCheckFailed, ChannelID: channelID, Model: model}),
		Type: TypeQualityCheckFailed, Severity: SeverityWarning,
		ChannelID: intPtr(channelID), Model: model,
		Title:     "接口质量检测失败",
		Message:   fmt.Sprintf("质量检测失败: %s (%s)", model, message),
		Impact:         "该站点×模型的接口可能异常，路由质量受影响",
		Recommendation: "在站点详情查看质量检测结果，检查上游服务状态",
		AdminPath:      "/channels",
		Metadata:       metadata,
	}
	now := time.Now()
	a.FirstSeenAt = now
	a.LastSeenAt = now
	return q.store.Reconcile(ctx, []Alert{a}, now)
}

// ResolveQualityFailures 同模型关键阶段全部通过时恢复质量失败告警。
// passedStages 为该模型最近一次 full 检测通过的关键阶段集合；
// 只要 passedStages 覆盖全部关键阶段就恢复（否则保留 active）。
func (q *QualityAlertSink) ResolveQualityFailures(ctx context.Context, channelID int, model string, passedStages []string) error {
	if q == nil || q.store == nil {
		return fmt.Errorf("alert store unavailable")
	}
	criticalCovered := map[string]bool{
		"connectivity": false,
		"protocol":     false,
		"stream":       false,
	}
	for _, s := range passedStages {
		if _, ok := criticalCovered[s]; ok {
			criticalCovered[s] = true
		}
	}
	allPassed := true
	for _, covered := range criticalCovered {
		if !covered {
			allPassed = false
			break
		}
	}
	if !allPassed {
		return nil // 仍有关键阶段未通过，保留告警
	}

	key := StableKey(AlertInput{Type: TypeQualityCheckFailed, ChannelID: channelID, Model: model})
	now := time.Now()
	_, err := q.store.Pool.Exec(ctx, `
		UPDATE alert_events
		SET status = 'recovered', recovered_at = $2
		WHERE alert_key = $1 AND status = 'active'
	`, key, now)
	if err != nil {
		return fmt.Errorf("resolve quality failure: %w", err)
	}
	return nil
}

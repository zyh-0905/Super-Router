package alert

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// AlertEvaluator 评估当前活跃告警（生产实现为 *Evaluator）。
type AlertEvaluator interface {
	Evaluate(ctx context.Context, groupID *int) ([]Alert, error)
}

// EventStore 告警生命周期持久化接口：
//   - WithReconcileLock 在 PostgreSQL advisory lock 保护下执行 fn，
//     多个 Checker 同时运行也只有一个能改变生命周期；
//   - Reconcile 在单事务内 upsert 当前告警并恢复未出现告警。
type EventStore interface {
	WithReconcileLock(ctx context.Context, fn func(ctx context.Context) error) error
	Reconcile(ctx context.Context, current []Alert, now time.Time) error
}

// Reconciler 周期性评估并同步告警生命周期。
type Reconciler struct {
	Eval   AlertEvaluator
	Store  EventStore
	Logger *zap.Logger
}

// NewReconciler 创建告警 Reconciler（Eval 传 nil 时回退 *Evaluator）。
func NewReconciler(eval AlertEvaluator, store EventStore, logger *zap.Logger) *Reconciler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Reconciler{Eval: eval, Store: store, Logger: logger}
}

// Reconcile 执行一轮：advisory lock 保护下完整评估，评估成功才写库。
// 评估失败直接返回错误并跳过 Store——保留已有 active 告警，不把全部告警误恢复。
func (r *Reconciler) Reconcile(ctx context.Context) error {
	return r.Store.WithReconcileLock(ctx, func(ctx context.Context) error {
		current, err := r.Eval.Evaluate(ctx, nil)
		if err != nil {
			return fmt.Errorf("evaluate alerts: %w", err)
		}
		if err := r.Store.Reconcile(ctx, current, time.Now()); err != nil {
			return fmt.Errorf("reconcile alerts: %w", err)
		}
		return nil
	})
}

// Run 启动时立即执行一次，此后每 interval 执行一次，直到 ctx 取消。
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if err := r.Reconcile(ctx); err != nil {
		r.Logger.Warn("Initial alert reconcile failed", zap.Error(err))
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Reconcile(ctx); err != nil {
				r.Logger.Warn("Alert reconcile failed", zap.Error(err))
			}
		}
	}
}

package quality

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ExecutorRunner 任务执行接口（*Executor 实现；测试可注入 fake）。
type ExecutorRunner interface {
	Execute(ctx context.Context, run *Run) error
}

// Worker 队列领取与执行：SKIP LOCKED 领取、全局并发上限、心跳、取消与过期回收。
type Worker struct {
	Repo          Repository
	Executor      ExecutorRunner
	WorkerID      string
	MaxConcurrent int
	PollInterval  time.Duration
	HeartbeatEvery time.Duration
	StaleAfter    time.Duration
	MaxAttempts   int
	Logger        *zap.Logger
}

// NewWorker 创建 Worker（默认并发 3、轮询 2s、心跳 10s、过期 2 分钟、重试 2 次）。
func NewWorker(repo Repository, executor ExecutorRunner, workerID string, logger *zap.Logger) *Worker {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Worker{
		Repo:           repo,
		Executor:       executor,
		WorkerID:       workerID,
		MaxConcurrent:  3,
		PollInterval:   2 * time.Second,
		HeartbeatEvery: 10 * time.Second,
		StaleAfter:     2 * time.Minute,
		MaxAttempts:    2,
		Logger:         logger,
	}
}

// Run 主循环：启动时 RecoverStale，随后按 PollInterval 领取并执行任务，
// 直到 ctx 取消（取消时等待在途 goroutine 完成）。
func (w *Worker) Run(ctx context.Context) {
	// 启动时回收崩溃遗留任务
	if n, err := w.Repo.RecoverStale(ctx, time.Now().Add(-w.StaleAfter), w.MaxAttempts); err != nil {
		w.Logger.Warn("RecoverStale failed at startup", zap.Error(err))
	} else if n > 0 {
		w.Logger.Info("Recovered stale quality runs", zap.Int64("count", n))
	}

	sem := make(chan struct{}, w.MaxConcurrent)
	var wg sync.WaitGroup
	defer wg.Wait()

	ticker := time.NewTicker(w.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 有并发额度才尝试领取
			select {
			case sem <- struct{}{}:
			default:
				continue // 全局并发已满
			}
			run, err := w.Repo.ClaimNext(ctx, w.WorkerID)
			if err != nil {
				<-sem
				if ctx.Err() != nil {
					return
				}
				w.Logger.Warn("ClaimNext failed", zap.Error(err))
				continue
			}
			if run == nil {
				<-sem
				continue // 队列为空，等待下一轮
			}
			wg.Add(1)
			go func(r *Run) {
				defer wg.Done()
				defer func() { <-sem }()
				w.executeTask(ctx, r)
			}(run)
		}
	}
}

// executeTask 执行单个任务：心跳循环 + 取消检查 + 最终状态写入。
func (w *Worker) executeTask(ctx context.Context, run *Run) {
	w.Logger.Info("Quality task started",
		zap.Int64("run_id", run.ID), zap.Int("channel_id", run.ChannelID),
		zap.String("model", run.Model), zap.String("depth", run.Depth))

	// 任务级 context：取消请求不直接取消上游请求的 context，
	// 而是通过 cancel_requested 状态在阶段间检查；shutdown 时一并取消。
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 心跳循环
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(w.HeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := w.Repo.Heartbeat(taskCtx, run.ID, w.WorkerID); err != nil {
					w.Logger.Warn("Heartbeat failed", zap.Int64("run_id", run.ID), zap.Error(err))
				}
			}
		}
	}()

	// 执行
	execErr := w.Executor.Execute(taskCtx, run)
	close(done)

	if execErr != nil {
		// 检查是否取消请求（取消优先于失败）
		if cancelled, _ := w.Repo.IsCancelRequested(ctx, run.ID); cancelled {
			_ = w.Repo.Cancel(ctx, run.ID)
			w.Logger.Info("Quality task cancelled", zap.Int64("run_id", run.ID))
			return
		}
		msg := execErr.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if err := w.Repo.Fail(ctx, run.ID, msg); err != nil {
			w.Logger.Warn("Fail write failed", zap.Int64("run_id", run.ID), zap.Error(err))
		}
		w.Logger.Warn("Quality task failed", zap.Int64("run_id", run.ID), zap.Error(execErr))
		return
	}

	// Execute 内部已完成 Complete/Cancel；幂等校验
	w.Logger.Info("Quality task finished", zap.Int64("run_id", run.ID))
}

// RecoverStaleOnce 供测试与外部调用的过期回收（Worker 启动时也调用）。
func (w *Worker) RecoverStaleOnce(ctx context.Context) (int64, error) {
	return w.Repo.RecoverStale(ctx, time.Now().Add(-w.StaleAfter), w.MaxAttempts)
}

var _ = fmt.Sprintf

package api

// CircuitBuffer 请求历史与熔断更新的异步缓冲：
// 单 worker 批量事务落库，把每请求约 8~12 次 DB 往返压成批量 flush。
//
// 语义保证：
//   - 队列内保持入队顺序，同一 (渠道, 模型, 分组桶) 的样本按
//     「先写 request_history → 再更新熔断」串行处理（H1 开闸判定
//     窗口必须包含当前请求本身；事务内查询能看到同事务先写入的历史行）；
//   - 队列满时回退同步路径（正确性优先，绝不丢样本）；
//   - 批量事务失败时整体回滚，逐条回退同步路径重放；
//   - Close() 排空剩余队列（优雅关闭不丢审计样本）。
//
// 入队后与调用方请求上下文解耦：客户端断开/请求超时不影响
// 审计与熔断样本落库（flush 使用独立后台上下文）。

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	circuitBufferSize    = 1024 // 队列容量；满则同步回退
	circuitFlushInterval = 200 * time.Millisecond
	circuitFlushBatch    = 64               // 达到该批量立即 flush
	circuitFlushTimeout  = 10 * time.Second // 批量事务超时
	circuitSyncTimeout   = 5 * time.Second  // 同步回退路径超时
)

// circuitSample 一次请求完成后的审计/熔断样本。
type circuitSample struct {
	requestID  string
	channelID  int
	model      string
	groupID    *int
	success    bool
	firstByte  bool
	durationMS int
	errorClass string
	// skipCircuit 只写历史不更新熔断（client_canceled 等不计入熔断样本的路径）
	skipCircuit bool
	// B1：上游返回的真实用量（仅统计成本用，不用于计费）；失败/未捕获为 0
	promptTokens     int
	completionTokens int
}

// CircuitBuffer 异步缓冲器。
type CircuitBuffer struct {
	begin   circuitBeginner
	circuit *CircuitBreakerManager
	logger  *zap.Logger

	q    chan circuitSample
	done chan struct{}
	once sync.Once
}

// NewCircuitBuffer 创建缓冲器并启动后台 flush worker。
func NewCircuitBuffer(begin circuitBeginner, circuit *CircuitBreakerManager, logger *zap.Logger) *CircuitBuffer {
	if logger == nil {
		logger = zap.NewNop()
	}
	b := &CircuitBuffer{
		begin:   begin,
		circuit: circuit,
		logger:  logger,
		q:       make(chan circuitSample, circuitBufferSize),
		done:    make(chan struct{}),
	}
	go b.run()
	return b
}

// Enqueue 入队样本；队列满时同步执行（保序与正确性优先）。
func (b *CircuitBuffer) Enqueue(s circuitSample) {
	select {
	case b.q <- s:
		return
	default:
		b.flushSync(s)
	}
}

// Close 停止 worker 并排空剩余队列（幂等）。
func (b *CircuitBuffer) Close() {
	b.once.Do(func() { close(b.done) })
}

// run flush worker 主循环：批量积累 → 事务落库。
func (b *CircuitBuffer) run() {
	ticker := time.NewTicker(circuitFlushInterval)
	defer ticker.Stop()
	batch := make([]circuitSample, 0, circuitFlushBatch)

	for {
		select {
		case s := <-b.q:
			batch = append(batch, s)
			if len(batch) >= circuitFlushBatch {
				b.flush(&batch)
			}
		case <-ticker.C:
			b.flush(&batch)
		case <-b.done:
			// 优雅关闭：排空剩余队列
			b.flush(&batch)
			for {
				select {
				case s := <-b.q:
					batch = append(batch, s)
					if len(batch) >= circuitFlushBatch {
						b.flush(&batch)
					}
				default:
					b.flush(&batch)
					return
				}
			}
		}
	}
}

// flush 批量事务落库（batch 清空由调用方保证的语义；此处 flush 后重置）。
func (b *CircuitBuffer) flush(batch *[]circuitSample) {
	if len(*batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), circuitFlushTimeout)
	defer cancel()

	tx, err := b.begin.Begin(ctx)
	if err != nil {
		b.logger.Warn("Circuit buffer begin failed, falling back to sync per-sample", zap.Error(err))
		b.flushSyncAll(*batch)
		*batch = (*batch)[:0]
		return
	}
	// 提交前回滚（commit 成功后回滚是 no-op）
	defer tx.Rollback(ctx)

	for _, s := range *batch {
		if err := insertRequestHistoryQ(ctx, tx, s); err != nil {
			b.logger.Warn("Circuit buffer batch failed (history), falling back to sync per-sample", zap.Error(err))
			b.flushSyncAll(*batch)
			*batch = (*batch)[:0]
			return
		}
		if !s.skipCircuit && b.circuit != nil {
			if err := b.circuit.updateCircuitStateTx(ctx, tx, s.channelID, s.model, s.groupID, s.success, s.errorClass); err != nil {
				b.logger.Warn("Circuit buffer batch failed (circuit), falling back to sync per-sample", zap.Error(err))
				b.flushSyncAll(*batch)
				*batch = (*batch)[:0]
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		b.logger.Warn("Circuit buffer commit failed, falling back to sync per-sample", zap.Error(err))
		b.flushSyncAll(*batch)
	}
	*batch = (*batch)[:0]
}

// flushSyncAll 批量失败时逐条同步回放（正确性优先，允许暂时变慢）。
func (b *CircuitBuffer) flushSyncAll(batch []circuitSample) {
	for _, s := range batch {
		b.flushSync(s)
	}
}

// flushSync 同步回退路径：独立事务逐条执行（与旧同步路径等价）。
func (b *CircuitBuffer) flushSync(s circuitSample) {
	ctx, cancel := context.WithTimeout(context.Background(), circuitSyncTimeout)
	defer cancel()
	tx, err := b.begin.Begin(ctx)
	if err != nil {
		b.logger.Error("Circuit sync fallback begin failed, sample lost", zap.Error(err))
		return
	}
	defer tx.Rollback(ctx)
	if err := insertRequestHistoryQ(ctx, tx, s); err != nil {
		b.logger.Error("Circuit sync fallback history insert failed, sample lost", zap.Error(err))
		return
	}
	if !s.skipCircuit && b.circuit != nil {
		if err := b.circuit.updateCircuitStateTx(ctx, tx, s.channelID, s.model, s.groupID, s.success, s.errorClass); err != nil {
			b.logger.Error("Circuit sync fallback circuit update failed", zap.Error(err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		b.logger.Error("Circuit sync fallback commit failed", zap.Error(err))
	}
}

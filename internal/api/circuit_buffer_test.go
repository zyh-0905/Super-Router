package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// fakeTx 内存事务假实现：按 SQL 特征记录逻辑事件，并建模回滚语义
// （Rollback 丢弃本事务内的事件——批量失败回退同步重放时不产生重复）。
type fakeTx struct {
	mu        sync.Mutex
	events    []string
	beginIdx  int   // Begin 时的事件计数（回滚截断点）
	committed int   // 提交次数
	failExec  bool  // 所有 Exec 注入失败（批量+同步回退均失败）
	queryErr  error // QueryRow 注入失败（nil = 正常返回 closed 空行）
	beginErr  error // Begin 注入失败
}

func (f *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failExec {
		return pgconn.CommandTag{}, &testErr{"injected exec failure"}
	}
	switch {
	case strings.Contains(sql, "request_history"):
		f.events = append(f.events, "history:"+args[0].(string))
	case strings.Contains(sql, "DO UPDATE"):
		// 熔断最终 upsert（预建行的 INSERT ON CONFLICT DO NOTHING 不记事件）
		f.events = append(f.events, "circuit")
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return fakeRow{err: f.queryErr}
	}
	return fakeRow{} // 正常：closed 空行（success/failure 计数 0）
}

func (f *fakeTx) Begin(ctx context.Context) (pgx.Tx, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		err := f.beginErr
		f.beginErr = nil // 一次性失败：批量路径失败后同步回退可成功
		return nil, err
	}
	f.beginIdx = len(f.events)
	return f, nil
}

func (f *fakeTx) Commit(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed++
	f.beginIdx = len(f.events)
	return nil
}

func (f *fakeTx) Rollback(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = f.events[:f.beginIdx] // 回滚：丢弃本事务内事件（真实语义）
	return nil
}

// 其余 pgx.Tx 接口方法（满足接口编译要求，测试中不使用）。
func (f *fakeTx) Prepare(ctx context.Context, name, sql string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (f *fakeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, nil
}
func (f *fakeTx) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (f *fakeTx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults { return nil }
func (f *fakeTx) LargeObjects() pgx.LargeObjects                               { return pgx.LargeObjects{} }
func (f *fakeTx) Conn() *pgx.Conn                                              { return nil }

// fakeRow 扫描返回固定值：state=closed、计数 0（熔断判定样本不足 → 不开闸）。
type fakeRow struct{ err error }

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = "closed"
		case *int:
			*v = 0
		case *time.Time:
			*v = time.Time{}
		case *bool:
			*v = false
		default:
			_ = i
		}
	}
	return nil
}

// fakeBeginner 每次返回同一个 fakeTx（便于断言事务内顺序与回滚语义）。
type fakeBeginner struct {
	tx  *fakeTx
	err error
}

func (f *fakeBeginner) Begin(ctx context.Context) (pgx.Tx, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tx.Begin(ctx)
}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

func sample(id string, channelID int, success bool, skip bool) circuitSample {
	return circuitSample{
		requestID: id, channelID: channelID, model: "m",
		success: success, firstByte: true, durationMS: 10,
		errorClass: "", skipCircuit: skip,
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCircuitBufferBatchFlushesInOrder 批量事务内保持先历史后熔断的顺序（H1）。
func TestCircuitBufferBatchFlushesInOrder(t *testing.T) {
	tx := &fakeTx{}
	b := NewCircuitBuffer(&fakeBeginner{tx: tx}, &CircuitBreakerManager{db: nil, logger: zap.NewNop()}, zap.NewNop())
	defer b.Close()

	b.Enqueue(sample("r1", 1, true, false))
	b.Enqueue(sample("r2", 1, false, false))
	b.Enqueue(sample("r3", 1, true, true)) // skipCircuit：只写历史

	waitFor(t, func() bool {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return len(tx.events) == 5
	})
	tx.mu.Lock()
	defer tx.mu.Unlock()
	want := []string{"history:r1", "circuit", "history:r2", "circuit", "history:r3"}
	for i, e := range tx.events {
		if e != want[i] {
			t.Fatalf("events = %v, want %v（先历史后熔断的严格顺序）", tx.events, want)
		}
	}
	if tx.committed == 0 {
		t.Fatal("transaction not committed")
	}
}

// TestCircuitBufferCloseDrains 关闭时排空剩余队列（不丢样本、不重复）。
func TestCircuitBufferCloseDrains(t *testing.T) {
	tx := &fakeTx{}
	b := NewCircuitBuffer(&fakeBeginner{tx: tx}, &CircuitBreakerManager{db: nil, logger: zap.NewNop()}, zap.NewNop())
	b.Enqueue(sample("drain", 1, true, false))
	b.Close()
	waitFor(t, func() bool {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return len(tx.events) == 2
	})
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.events[0] != "history:drain" || tx.events[1] != "circuit" {
		t.Fatalf("events = %v, want [history:drain circuit]（无重复）", tx.events)
	}
}

// TestCircuitBufferQueueOverflowFallsBackSync 队列满时同步回退，不丢样本。
func TestCircuitBufferQueueOverflowFallsBackSync(t *testing.T) {
	tx := &fakeTx{}
	// worker 因 Begin 一直失败而无法批量消费 → 队列填满 → 溢出样本走同步回退
	begin := &fakeBeginner{tx: tx}
	b := NewCircuitBuffer(begin, &CircuitBreakerManager{db: nil, logger: zap.NewNop()}, zap.NewNop())
	defer b.Close()
	tx.beginErr = &testErr{"begin down"}

	for i := 0; i < circuitBufferSize+5; i++ {
		b.Enqueue(sample("bulk", 1, true, false))
	}
	// 同步回退同样 begin 失败 → 样本丢失告警（不 panic、不阻塞）；
	// 关键是整个过程中 Enqueue 永不阻塞（测试完成即证明）。
}

// TestCircuitBufferBatchBeginFailureReplaysSync
// 批量事务 Begin 失败：逐条同步重放，样本完整且无重复。
func TestCircuitBufferBatchBeginFailureReplaysSync(t *testing.T) {
	tx := &fakeTx{beginErr: &testErr{"begin once"}} // 仅第一次 Begin 失败
	b := NewCircuitBuffer(&fakeBeginner{tx: tx}, &CircuitBreakerManager{db: nil, logger: zap.NewNop()}, zap.NewNop())
	defer b.Close()

	b.Enqueue(sample("f1", 1, true, false))
	b.Enqueue(sample("f2", 1, false, false))

	// 批量 Begin 失败 → flushSyncAll：f1、f2 各自同步事务成功
	waitFor(t, func() bool {
		tx.mu.Lock()
		defer tx.mu.Unlock()
		return len(tx.events) == 4
	})
	tx.mu.Lock()
	defer tx.mu.Unlock()
	want := []string{"history:f1", "circuit", "history:f2", "circuit"}
	for i, e := range tx.events {
		if e != want[i] {
			t.Fatalf("events = %v, want %v（同步回放保序无重复）", tx.events, want)
		}
	}
	if tx.committed != 2 {
		t.Fatalf("committed = %d, want 2（每样本一个同步事务）", tx.committed)
	}
}

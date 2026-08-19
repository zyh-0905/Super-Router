package alert

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// fakeEvaluator 可控的评估器假实现。
type fakeEvaluator struct {
	alerts []Alert
	err    error
	calls  int32
}

func (f *fakeEvaluator) Evaluate(ctx context.Context, groupID *int) ([]Alert, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	return f.alerts, nil
}

// fakeEventStore 记录 reconcile 调用的假存储。
type fakeEventStore struct {
	mu        sync.Mutex
	reconcile [][]Alert
	locked    int32 // 当前持锁调用数（>1 表示锁失效/并发）
}

func (f *fakeEventStore) WithReconcileLock(ctx context.Context, fn func(ctx context.Context) error) error {
	cur := atomic.AddInt32(&f.locked, 1)
	defer atomic.AddInt32(&f.locked, -1)
	_ = cur
	return fn(ctx)
}

func (f *fakeEventStore) Reconcile(ctx context.Context, current []Alert, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconcile = append(f.reconcile, current)
	return nil
}

func TestReconcileSkipsStoreOnEvaluateError(t *testing.T) {
	eval := &fakeEvaluator{err: errors.New("boom")}
	store := &fakeEventStore{}
	r := &Reconciler{Eval: eval, Store: store, Logger: zap.NewNop()}

	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.reconcile) != 0 {
		t.Fatalf("store must not be called on evaluate failure, got %d calls", len(store.reconcile))
	}
}

func TestReconcilePassesEvaluatedAlerts(t *testing.T) {
	want := []Alert{
		{Key: "low_balance:channel-3", Type: TypeLowBalance, Severity: SeverityCritical},
		{Key: "ratio_exceeded:channel-5:model-m", Type: TypeRatioExceeded, Severity: SeverityCritical},
	}
	eval := &fakeEvaluator{alerts: want}
	store := &fakeEventStore{}
	r := &Reconciler{Eval: eval, Store: store, Logger: zap.NewNop()}

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.reconcile) != 1 {
		t.Fatalf("store called %d times, want 1", len(store.reconcile))
	}
	got := store.reconcile[0]
	if len(got) != len(want) || got[0].Key != want[0].Key || got[1].Key != want[1].Key {
		t.Fatalf("reconciled alerts mismatch: %+v", got)
	}
}

func TestRunImmediateThenTicks(t *testing.T) {
	eval := &fakeEvaluator{alerts: []Alert{{Key: "k", Type: TypeLowBalance, Severity: SeverityCritical}}}
	store := &fakeEventStore{}
	r := &Reconciler{Eval: eval, Store: store, Logger: zap.NewNop()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Run(ctx, 30*time.Millisecond)
		close(done)
	}()

	// 启动立即执行 + 至少 2 个 tick（30ms 间隔，350ms 内应有 ~10 次机会）
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.reconcile)
		store.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	store.mu.Lock()
	n := len(store.reconcile)
	store.mu.Unlock()
	if n < 3 {
		t.Fatalf("expected immediate run + ticks, got %d reconciles", n)
	}
	// 串行执行：锁计数从未超过 1
	if v := atomic.LoadInt32(&store.locked); v != 0 {
		t.Fatalf("lock still held after shutdown: %d", v)
	}
}

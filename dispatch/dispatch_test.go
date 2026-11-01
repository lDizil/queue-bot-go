package dispatch

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFIFOWithinKeyAndParallelAcrossKeys(t *testing.T) {
	d := newTestDispatcher(t, 2, 64)
	var (
		started = make(chan struct{})
		release = make(chan struct{})
		other   = make(chan struct{})
		orderMu sync.Mutex
		order   []int
		active  atomic.Int32
		overlap atomic.Bool
	)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	for i := range 32 {
		if err := d.Submit(t.Context(), "first-topic", func(ctx context.Context) {
			if active.Add(1) != 1 {
				overlap.Store(true)
			}
			defer active.Add(-1)
			if i == 0 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
			orderMu.Lock()
			order = append(order, i)
			orderMu.Unlock()
		}); err != nil {
			t.Fatal(err)
		}
	}
	await(t, started)
	if err := d.Submit(t.Context(), "second-topic", func(context.Context) { close(other) }); err != nil {
		t.Fatal(err)
	}
	await(t, other)
	releaseOnce.Do(func() { close(release) })
	shutdown(t, d)
	var expected []int
	for i := range 32 {
		expected = append(expected, i)
	}
	if overlap.Load() || !slices.Equal(order, expected) {
		t.Fatalf("same-key actions overlapped or changed order: overlap=%v order=%v", overlap.Load(), order)
	}
}

func TestWorkerPoolLimitsConcurrentActions(t *testing.T) {
	d := newTestDispatcher(t, 2, 32)
	var (
		started = make(chan struct{}, 32)
		release = make(chan struct{})
		active  atomic.Int32
		maximum atomic.Int32
	)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	for i := range 16 {
		if err := d.Submit(t.Context(), string(rune('a'+i)), func(ctx context.Context) {
			current := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); current > old; old = maximum.Load() {
				if maximum.CompareAndSwap(old, current) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		await(t, started)
	}
	releaseOnce.Do(func() { close(release) })
	shutdown(t, d)
	if got := maximum.Load(); got != 2 {
		t.Fatalf("concurrent actions=%d, want exactly 2 workers", got)
	}
}

func TestBackpressureCanBeCanceledWithoutAcceptingAction(t *testing.T) {
	d := newTestDispatcher(t, 1, 1)
	var (
		started = make(chan struct{})
		release = make(chan struct{})
		ran     atomic.Bool
	)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	if err := d.Submit(t.Context(), "topic", func(ctx context.Context) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	if err := d.Submit(t.Context(), "topic", func(context.Context) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- d.Submit(ctx, "third", func(context.Context) { ran.Store(true) }) }()
	cancel()
	if err := awaitError(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("full-queue admission error=%v, want cancellation", err)
	}
	releaseOnce.Do(func() { close(release) })
	shutdown(t, d)
	if ran.Load() {
		t.Fatal("canceled producer action was accepted")
	}
}

func TestShutdownWakesProducerAndDrainsAcceptedActions(t *testing.T) {
	d := newTestDispatcher(t, 1, 1)
	var (
		started           = make(chan struct{})
		release           = make(chan struct{})
		drained           = make(chan struct{})
		canceledExecution atomic.Bool
	)
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	admissionCtx, cancelAdmission := context.WithCancel(t.Context())
	defer cancelAdmission()
	if err := d.Submit(admissionCtx, "topic", func(ctx context.Context) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		canceledExecution.Store(ctx.Err() != nil)
	}); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	if err := d.Submit(admissionCtx, "topic", func(ctx context.Context) {
		if ctx.Err() != nil {
			canceledExecution.Store(true)
		}
		close(drained)
	}); err != nil {
		t.Fatal(err)
	}
	cancelAdmission()
	producer := make(chan error, 1)
	go func() {
		producer <- d.Submit(t.Context(), "third", func(context.Context) { t.Error("unaccepted action ran") })
	}()
	stopped := make(chan error, 1)
	go func() { stopped <- d.Shutdown(t.Context()) }()
	if err := awaitError(t, producer); !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked producer error=%v, want ErrClosed", err)
	}
	releaseOnce.Do(func() { close(release) })
	await(t, drained)
	if err := awaitError(t, stopped); err != nil {
		t.Fatal(err)
	}
	if canceledExecution.Load() {
		t.Fatal("polling cancellation canceled an already accepted action")
	}
}

func TestShutdownDeadlineCancelsExecutionAndStillVisitsAcceptedActions(t *testing.T) {
	d := newTestDispatcher(t, 1, 1)
	var (
		started  = make(chan struct{})
		canceled = make(chan struct{})
		visited  = make(chan struct{})
	)
	if err := d.Submit(t.Context(), "topic", func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(canceled)
	}); err != nil {
		t.Fatal(err)
	}
	await(t, started)
	if err := d.Submit(t.Context(), "topic", func(ctx context.Context) {
		if ctx.Err() == nil {
			t.Error("deadline did not cancel queued execution context")
		}
		close(visited)
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error=%v, want expired grace context", err)
	}
	await(t, canceled)
	await(t, visited)
	shutdown(t, d)
}

func newTestDispatcher(t *testing.T, workers, capacity int) *Dispatcher {
	t.Helper()
	d := New(workers, capacity)
	t.Cleanup(func() { shutdown(t, d) })
	return d
}

func shutdown(t *testing.T, d *Dispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Errorf("shutdown dispatcher: %v", err)
	}
}

func await(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for action")
	}
}

func awaitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for producer or shutdown")
		return nil
	}
}

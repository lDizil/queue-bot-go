package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/settings"
)

func TestReconfigureCancelsChangedPendingRun(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	messageID := 123
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0), ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, QueueMesID: &messageID}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	clock := fixedClock{now: moscowDate(7, 9, 57)}
	controller := &mockController{store: store, now: clock.Now}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = clock
	canceled := false
	old := &scheduleWorker{active: true, cancel: func() { canceled = true }}
	s.workers[1] = old
	if err := manager.Update(ctx, func(v *settings.Values) error { v.Week1Type = "even"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconfigure(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	replacement := s.workers[1]
	s.mu.Unlock()
	s.RemoveSchedule(1)
	current, err := store.GetScheduleEntry(ctx, 1)
	if err != nil || !canceled || replacement == nil || replacement == old || current.ActiveOpensAt != nil || current.QueueMesID != nil || controller.cancelPendingCalls != 1 {
		t.Fatalf("pending run not replanned: canceled=%v replacement=%p row=%+v calls=%d err=%v", canceled, replacement, current, controller.cancelPendingCalls, err)
	}
}

func TestReconfigurePreservesUnchangedPendingAndOneShotWorkers(t *testing.T) {
	for _, temporary := range []bool{false, true} {
		manager := testManager(t)
		opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
		row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0), ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, IsTemporary: temporary, Notified5min: true, Notified1min: true}
		store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
		controller := &mockController{store: store}
		s := NewScheduler(context.Background(), store, controller, manager)
		s.clock = fixedClock{now: moscowDate(7, 9, 59)}
		canceled := false
		worker := &scheduleWorker{active: true, cancel: func() { canceled = true }}
		s.workers[1] = worker
		if err := manager.Update(context.Background(), func(v *settings.Values) error {
			v.TotalSlotsInQueue = 40
			if temporary {
				v.Week1Type = "even"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Reconfigure(); err != nil {
			t.Fatal(err)
		}
		current, err := store.GetScheduleEntry(context.Background(), 1)
		if err != nil || canceled || s.workers[1] != worker || controller.cancelPendingCalls != 0 || !current.Notified5min || !current.Notified1min {
			t.Fatalf("unchanged or one-shot run restarted: temporary=%v canceled=%v calls=%d row=%+v err=%v", temporary, canceled, controller.cancelPendingCalls, current, err)
		}
	}
}

type prepareBarrierController struct {
	*mockController
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (c *prepareBarrierController) Prepare(ctx context.Context, id int, opensAt, closesAt time.Time) error {
	close(c.started)
	<-ctx.Done()
	close(c.stopped)
	<-c.release
	c.store.update(id, func(row *db.Schedule) { row.ActiveOpensAt, row.ActiveClosesAt = &opensAt, &closesAt })
	return ctx.Err()
}

func TestReconfigureWaitsForLatePrepareBeforeCancelingItsRun(t *testing.T) {
	manager := testManager(t)
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	clock := fixedClock{now: moscowDate(7, 9, 55)}
	controller := &prepareBarrierController{mockController: &mockController{store: store, now: clock.Now}, started: make(chan struct{}), stopped: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = clock
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(controller.release) }); cancel(); s.RemoveSchedule(1) })
	s.ScheduleNext(row)
	awaitSchedulerEvent(t, controller.started)
	if err := manager.Update(ctx, func(v *settings.Values) error { v.Week1Type = "even"; return nil }); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- s.Reconfigure() }()
	awaitSchedulerEvent(t, controller.stopped)
	select {
	case err := <-result:
		t.Fatalf("reconfiguration did not wait for uncertain Prepare commit: %v", err)
	default:
	}
	release.Do(func() { close(controller.release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconfiguration is blocked after old worker finished")
	}
	s.RemoveSchedule(1)
	current, err := store.GetScheduleEntry(ctx, 1)
	if err != nil || current.ActiveOpensAt != nil || controller.cancelPendingCalls != 1 {
		t.Fatalf("late Prepare resurrected the previous calendar: row=%+v cancels=%d err=%v", current, controller.cancelPendingCalls, err)
	}
}

func awaitSchedulerEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduler event")
	}
}

type openingBoundaryController struct {
	*mockController
}

func (c *openingBoundaryController) CancelPending(ctx context.Context, id int, opensAt, closesAt time.Time) error {
	c.mockController.now = func() time.Time { return opensAt }
	return c.mockController.CancelPending(ctx, id, opensAt, closesAt)
}

func TestReconfigurePreservesRunWhenOpeningRacesCancellation(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0), ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &openingBoundaryController{mockController: &mockController{store: store}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = fixedClock{now: moscowDate(7, 9, 57)}
	s.workers[1] = &scheduleWorker{active: true, cancel: func() {}}
	if err := manager.Update(ctx, func(v *settings.Values) error { v.Week1Type = "even"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconfigure(); err != nil {
		t.Fatal(err)
	}
	s.RemoveSchedule(1)
	current, err := store.GetScheduleEntry(ctx, 1)
	if err != nil || current.ActiveOpensAt == nil || !current.ActiveOpensAt.Equal(opensAt) || controller.cancelPendingCalls != 1 {
		t.Fatalf("opened run was canceled: %+v calls=%d err=%v", current, controller.cancelPendingCalls, err)
	}
	if _, err := s.processActive(ctx, current, closesAt, &scheduleWorker{}); err != nil {
		t.Fatal(err)
	}
	if controller.closeCalls != 1 {
		t.Fatal("the preserved run lost its closing event")
	}
}

func TestFailedPendingCancellationRestartsPersistedRun(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0), ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	cause := errors.New("database temporarily unavailable")
	controller := &mockController{store: store, cancelPendingErr: cause}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = fixedClock{now: moscowDate(7, 9, 57)}
	old := &scheduleWorker{active: true, cancel: func() {}}
	s.workers[1] = old
	if err := manager.Update(ctx, func(v *settings.Values) error { v.Week1Type = "even"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconfigure(); !errors.Is(err, cause) {
		t.Fatalf("cancellation failure lost its cause: %v", err)
	}
	s.mu.Lock()
	replacement := s.workers[1]
	s.mu.Unlock()
	s.RemoveSchedule(1)
	current, err := store.GetScheduleEntry(ctx, 1)
	if err != nil || replacement == nil || replacement == old || current.ActiveOpensAt == nil || !current.ActiveOpensAt.Equal(opensAt) {
		t.Fatalf("failed cancellation lost the persisted run: worker=%p row=%+v err=%v", replacement, current, err)
	}
	if _, err := s.processActive(ctx, current, closesAt, &scheduleWorker{}); err != nil {
		t.Fatal(err)
	}
	if controller.closeCalls != 1 {
		t.Fatal("cancellation failure lost the closing event")
	}
}

func TestRestartReconcilesPublishedMessageOnceWithoutRepeatingReminders(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	messageID := 99
	row := db.Schedule{ID: 1, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, QueueMesID: &messageID, Notified5min: true, Notified1min: true, NotifiedOpen: true}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store}
	s := NewScheduler(context.Background(), store, controller, manager)
	worker := &scheduleWorker{}
	for range 3 {
		if _, err := s.processActive(context.Background(), row, opensAt.Add(time.Minute), worker); err != nil {
			t.Fatal(err)
		}
	}
	if controller.refreshCalls != 1 || controller.notifyCalls != 0 {
		t.Fatalf("restart reconciliation: renders=%d reminders=%d", controller.refreshCalls, controller.notifyCalls)
	}
}

type startupBarrierStore struct {
	*mockScheduleStore
	listed  chan struct{}
	release chan struct{}
}

func (store *startupBarrierStore) GetStartupSchedules(ctx context.Context) ([]db.Schedule, error) {
	rows, err := store.mockScheduleStore.GetStartupSchedules(ctx)
	close(store.listed)
	select {
	case <-store.release:
		return rows, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestReconfigureCannotRemoveConcurrentlyCreatedOneShotWorker(t *testing.T) {
	manager := testManager(t)
	rows := &mockScheduleStore{rows: make(map[int]db.Schedule)}
	store := &startupBarrierStore{mockScheduleStore: rows, listed: make(chan struct{}), release: make(chan struct{})}
	controller := &mockController{store: rows}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = fixedClock{now: moscowDate(7, 9, 0)}
	reconfigured := make(chan error, 1)
	go func() { reconfigured <- s.Reconfigure() }()
	awaitSchedulerEvent(t, store.listed)
	created := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		created <- s.RunInstant(ctx, -100, 7, "One shot")
	}()
	awaitSchedulerEvent(t, started)
	select {
	case err := <-created:
		t.Errorf("one-shot creation bypassed the scheduler lifecycle transition: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(store.release)
	for _, result := range []<-chan error{reconfigured, created} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("scheduler lifecycle transition did not finish")
		}
	}
	s.mu.Lock()
	worker := s.workers[1]
	s.mu.Unlock()
	s.RemoveSchedule(1)
	if worker == nil {
		t.Fatal("concurrent reconfiguration removed the new one-shot worker")
	}
}

type latePrepareStore struct {
	*mockScheduleStore
	mu      sync.Mutex
	pending bool
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	barrier sync.Once
}

func (store *latePrepareStore) SyncQueueState(ctx context.Context, id int) (db.Schedule, error) {
	store.mu.Lock()
	pending := store.pending
	store.mu.Unlock()
	if pending {
		store.barrier.Do(func() { close(store.entered) })
		select {
		case <-store.done:
		case <-ctx.Done():
			return db.Schedule{}, ctx.Err()
		}
	}
	return store.mockScheduleStore.GetScheduleEntry(ctx, id)
}

type latePrepareController struct {
	*mockController
	store   *latePrepareStore
	started chan struct{}
}

func (controller *latePrepareController) Prepare(_ context.Context, id int, opensAt, closesAt time.Time) error {
	controller.store.mu.Lock()
	controller.store.pending = true
	controller.store.mu.Unlock()
	go func() {
		<-controller.store.release
		controller.store.mockScheduleStore.update(id, func(row *db.Schedule) { row.ActiveOpensAt, row.ActiveClosesAt = &opensAt, &closesAt })
		close(controller.store.done)
	}()
	close(controller.started)
	return errors.New("Prepare commit outcome is not yet known")
}

func TestReconfigureWaitsForPrepareBackendAfterWorkerHasAlreadyStopped(t *testing.T) {
	for _, opened := range []bool{false, true} {
		name := "cancel still pending"
		if opened {
			name = "preserve newly opened"
		}
		t.Run(name, func(t *testing.T) {
			manager := testManager(t)
			row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}
			rows := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
			store := &latePrepareStore{mockScheduleStore: rows, entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			clock := fixedClock{now: moscowDate(7, 9, 57)}
			controller := &latePrepareController{mockController: &mockController{store: rows, now: clock.Now}, store: store, started: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := NewScheduler(ctx, store, controller, manager)
			s.clock = clock
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(store.release) }); cancel(); s.RemoveSchedule(1) })
			s.ScheduleNext(row)
			awaitSchedulerEvent(t, controller.started)
			s.mu.Lock()
			old := s.workers[1]
			s.mu.Unlock()
			if err := manager.Update(ctx, func(v *settings.Values) error { v.Week1Type = "even"; return nil }); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- s.Reconfigure() }()
			awaitSchedulerEvent(t, store.entered)
			select {
			case <-old.done:
			default:
				t.Fatal("terminal barrier was reached before the old worker stopped")
			}
			select {
			case err := <-result:
				t.Fatalf("reconfiguration used an unsettled database snapshot: %v", err)
			default:
			}
			if opened {
				s.clock = fixedClock{now: moscowDate(7, 10, 0)}
			}
			release.Do(func() { close(store.release) })
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("reconfiguration did not finish after the backend reached COMMIT")
			}
			s.mu.Lock()
			replacement := s.workers[1]
			s.mu.Unlock()
			s.RemoveSchedule(1)
			current, err := rows.GetScheduleEntry(ctx, 1)
			if err != nil || replacement == nil || replacement == old || (current.ActiveOpensAt != nil) != opened {
				t.Fatalf("late Prepare did not reconcile with the new calendar: opened=%v row=%+v err=%v", opened, current, err)
			}
			if opened {
				if _, err := s.processActive(ctx, current, *current.ActiveClosesAt, &scheduleWorker{}); err != nil {
					t.Fatal(err)
				}
				if controller.closeCalls != 1 {
					t.Fatal("preserved late-committed run lost its closing event")
				}
			}
		})
	}
}

type retryPastClosingClock struct {
	now        time.Time
	afterRetry time.Time
	waited     bool
}

func (clock *retryPastClosingClock) Now() time.Time { return clock.now }

func (clock *retryPastClosingClock) After(time.Duration) <-chan time.Time {
	if clock.waited {
		return make(chan time.Time)
	}
	clock.waited = true
	clock.now = clock.afterRetry
	ready := make(chan time.Time, 1)
	ready <- clock.now
	return ready
}

type closingLatePrepareController struct {
	*latePrepareController
	closed chan struct{}
}

func (controller *closingLatePrepareController) Close(ctx context.Context, id int) error {
	if err := controller.mockController.Close(ctx, id); err != nil {
		return err
	}
	close(controller.closed)
	return nil
}

func TestFailedPrepareRechecksTerminalStateBeforeSleepingPastItsClosingTime(t *testing.T) {
	manager := testManager(t)
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(10, 1)}
	rows := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	store := &latePrepareStore{mockScheduleStore: rows, entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	controller := &closingLatePrepareController{latePrepareController: &latePrepareController{mockController: &mockController{store: rows}, store: store, started: make(chan struct{})}, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = &retryPastClosingClock{now: moscowDate(7, 10, 0), afterRetry: moscowDate(7, 10, 2)}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(store.release) }); cancel(); s.RemoveSchedule(1) })
	s.ScheduleNext(row)
	awaitSchedulerEvent(t, store.entered)
	release.Do(func() { close(store.release) })
	awaitSchedulerEvent(t, controller.closed)
	s.RemoveSchedule(1)
	current, err := rows.GetScheduleEntry(ctx, 1)
	if err != nil || current.ActiveOpensAt != nil || controller.closeCalls != 1 {
		t.Fatalf("the late committed run kept a reservation after its closing time: row=%+v calls=%d err=%v", current, controller.closeCalls, err)
	}
}

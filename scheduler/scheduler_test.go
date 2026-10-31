package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/jackc/pgx/v5"
)

type memorySettings struct{}

func (memorySettings) LoadSettings(context.Context) (settings.Values, bool, error) {
	return settings.Values{}, false, nil
}
func (memorySettings) SaveSettings(context.Context, settings.Values) error { return nil }

func testManager(t *testing.T) *settings.Manager {
	t.Helper()
	values := settings.Defaults()
	values.Week1Date, values.Week1Type = "2026-09-07", "odd"
	manager, err := settings.NewManager(context.Background(), memorySettings{}, values)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

type mockScheduleStore struct {
	mu     sync.Mutex
	rows   map[int]db.Schedule
	nextID int
}

func (m *mockScheduleStore) GetStartupSchedules(context.Context) ([]db.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []db.Schedule
	for _, row := range m.rows {
		rows = append(rows, row)
	}
	return rows, nil
}
func (m *mockScheduleStore) GetScheduleEntry(ctx context.Context, id int) (db.Schedule, error) {
	if err := ctx.Err(); err != nil {
		return db.Schedule{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[id]
	if !ok {
		return db.Schedule{}, pgx.ErrNoRows
	}
	return row, nil
}
func (m *mockScheduleStore) SyncQueueState(ctx context.Context, id int) (db.Schedule, error) {
	return m.GetScheduleEntry(ctx, id)
}
func (m *mockScheduleStore) AddTemporarySchedule(ctx context.Context, row db.Schedule) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	row.ID, row.IsTemporary = m.nextID, true
	row.ActiveThreadID = &row.ThreadID
	m.rows[row.ID] = row
	return row.ID, nil
}
func (m *mockScheduleStore) SetLegacyQueueRunBounds(_ context.Context, id int, opensAt, closesAt time.Time, capacity, columns int) (db.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[id]
	row.ActiveOpensAt, row.ActiveClosesAt, row.ActiveThreadID = &opensAt, &closesAt, &row.ThreadID
	row.ActiveCapacity, row.ActiveColumns = capacity, columns
	m.rows[id] = row
	return row, nil
}
func (m *mockScheduleStore) update(id int, change func(*db.Schedule)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[id]
	change(&row)
	m.rows[id] = row
}
func (m *mockScheduleStore) SetNotified5min(_ context.Context, id int) error {
	m.update(id, func(row *db.Schedule) { row.Notified5min = true })
	return nil
}
func (m *mockScheduleStore) SetNotified1min(_ context.Context, id int) error {
	m.update(id, func(row *db.Schedule) { row.Notified1min = true })
	return nil
}
func (m *mockScheduleStore) SetNotifiedOpen(_ context.Context, id int) error {
	m.update(id, func(row *db.Schedule) { row.NotifiedOpen = true })
	return nil
}

type mockController struct {
	prepareErr                                          error
	store                                               *mockScheduleStore
	prepareCalls, refreshCalls, closeCalls, notifyCalls int
	closeFailures, refreshFailures                      int
	lastChatID                                          int64
	messageDate                                         time.Time
	messageDateErr                                      error
	cancelPendingCalls                                  int
	cancelPendingErr                                    error
	now                                                 func() time.Time
}

func (m *mockController) Prepare(ctx context.Context, id int, opensAt, closesAt time.Time) error {
	m.prepareCalls++
	if m.prepareErr != nil {
		return m.prepareErr
	}
	m.store.update(id, func(row *db.Schedule) { row.ActiveOpensAt, row.ActiveClosesAt = &opensAt, &closesAt })
	return nil
}
func (m *mockController) CancelPending(ctx context.Context, id int, opensAt, closesAt time.Time) error {
	m.cancelPendingCalls++
	if m.cancelPendingErr != nil {
		return m.cancelPendingErr
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	row := m.store.rows[id]
	now := time.Now()
	if m.now != nil {
		now = m.now()
	}
	if row.IsTemporary || row.ActiveOpensAt == nil || !now.Before(*row.ActiveOpensAt) {
		return nil
	}
	if !row.ActiveOpensAt.Equal(opensAt) || row.ActiveClosesAt == nil || !row.ActiveClosesAt.Equal(closesAt) {
		return db.ErrQueueStateChanged
	}
	row.ActiveOpensAt, row.ActiveClosesAt, row.QueueMesID = nil, nil, nil
	row.Notified5min, row.Notified1min, row.NotifiedOpen = false, false, false
	m.store.rows[id] = row
	return nil
}
func (m *mockController) LegacyMessageDate(context.Context, int) (time.Time, error) {
	return m.messageDate, m.messageDateErr
}
func (m *mockController) Refresh(ctx context.Context, id int) error {
	m.refreshCalls++
	if m.refreshFailures > 0 {
		m.refreshFailures--
		return errors.New("Telegram unavailable")
	}
	m.store.update(id, func(row *db.Schedule) { messageID := 99; row.QueueMesID = &messageID })
	return nil
}
func (m *mockController) Close(ctx context.Context, id int) error {
	m.closeCalls++
	if m.closeFailures > 0 {
		m.closeFailures--
		return errors.New("Telegram unavailable")
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	if m.store.rows[id].IsTemporary {
		delete(m.store.rows, id)
	} else {
		row := m.store.rows[id]
		row.ActiveOpensAt, row.ActiveClosesAt, row.QueueMesID = nil, nil, nil
		m.store.rows[id] = row
	}
	return nil
}
func (m *mockController) Notify(ctx context.Context, row db.Schedule, text string) error {
	m.notifyCalls++
	m.lastChatID = row.ChatID
	return nil
}

type advancingClock struct {
	now   time.Time
	waits []time.Duration
}

func (c *advancingClock) Now() time.Time { return c.now }
func (c *advancingClock) After(d time.Duration) <-chan time.Time {
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

func makeTimeOnly(hour, minute int) time.Time {
	return time.Date(0, 1, 1, hour, minute, 0, 0, time.UTC)
}
func moscowDate(day, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, utils.Moscow())
}

func TestNextRunUsesMondayParityAndCrossMidnight(t *testing.T) {
	values := testManager(t).Values()
	cases := []struct {
		name                     string
		schedule                 db.Schedule
		now, wantOpen, wantClose time.Time
	}{
		{"before anchor", db.Schedule{DayOfWeek: "monday", WeekType: "even", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}, moscowDate(1, 12, 0), moscowDate(14, 10, 0), moscowDate(14, 11, 0)},
		{"anchor week from Wednesday", db.Schedule{DayOfWeek: "sunday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}, moscowDate(9, 12, 0), moscowDate(13, 10, 0), moscowDate(13, 11, 0)},
		{"running after midnight", db.Schedule{DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(23, 0), EndTime: makeTimeOnly(1, 0)}, moscowDate(8, 0, 30), moscowDate(7, 23, 0), moscowDate(8, 1, 0)},
		{"ended today", db.Schedule{DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}, moscowDate(7, 11, 0), moscowDate(21, 10, 0), moscowDate(21, 11, 0)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opensAt, closesAt, err := nextRun(test.schedule, values, test.now)
			if err != nil || !opensAt.Equal(test.wantOpen) || !closesAt.Equal(test.wantClose) {
				t.Fatalf("bounds = %v — %v, err = %v; want %v — %v", opensAt, closesAt, err, test.wantOpen, test.wantClose)
			}
		})
	}
}

func TestPendingPublicationDoesNotDependOnReminderFlag(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	schedule := db.Schedule{ID: 1, ChatID: -100123, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, Notified5min: true, Notified1min: true}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: schedule}}
	controller := &mockController{store: store, refreshFailures: 1}
	s := NewScheduler(context.Background(), store, controller, manager)
	worker := &scheduleWorker{}
	now := opensAt.Add(-30 * time.Second)
	if _, err := s.processActive(context.Background(), schedule, now, worker); err == nil {
		t.Fatal("expected first publication failure")
	}
	if _, err := s.processActive(context.Background(), schedule, now, worker); err != nil {
		t.Fatal(err)
	}
	restored, _ := store.GetScheduleEntry(context.Background(), 1)
	if controller.refreshCalls != 2 || controller.notifyCalls != 0 || restored.QueueMesID == nil {
		t.Fatalf("refresh=%d, notify=%d, row=%+v", controller.refreshCalls, controller.notifyCalls, restored)
	}
}

func TestRestartMidRunRestoresMessageWithoutOverdueReminders(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	row := db.Schedule{ID: 1, ChatID: -100123, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store}
	s := NewScheduler(context.Background(), store, controller, manager)
	if _, err := s.processActive(context.Background(), row, opensAt.Add(20*time.Minute), &scheduleWorker{}); err != nil {
		t.Fatal(err)
	}
	restored, _ := store.GetScheduleEntry(context.Background(), 1)
	if controller.notifyCalls != 0 || controller.refreshCalls != 1 || !restored.NotifiedOpen || !restored.Notified5min || !restored.Notified1min {
		t.Fatalf("reminders=%d, refresh=%d, row=%+v", controller.notifyCalls, controller.refreshCalls, restored)
	}
}

func TestCloseRetriesBeyondThreeFailuresAndRemovesTemporaryRun(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	row := db.Schedule{ID: 1, IsTemporary: true, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store, closeFailures: 4}
	s := NewScheduler(context.Background(), store, controller, manager)
	clock := &advancingClock{now: closesAt.Add(time.Minute)}
	s.clock = clock
	worker := &scheduleWorker{}
	s.workers[1] = worker
	s.run(context.Background(), 1, worker)
	if controller.closeCalls != 5 {
		t.Fatalf("close calls = %d, want 5", controller.closeCalls)
	}
	if len(clock.waits) != 4 || clock.waits[3] != 8*time.Second {
		t.Fatalf("retry delays = %v", clock.waits)
	}
	if _, err := store.GetScheduleEntry(context.Background(), 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("temporary row retained: %v", err)
	}
	if len(s.workers) != 0 {
		t.Fatal("finished worker retained")
	}
}

func TestReconfigureKeepsActiveBoundsAndWorker(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	row := db.Schedule{ID: 1, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	s := NewScheduler(context.Background(), store, &mockController{store: store}, manager)
	canceled := false
	worker := &scheduleWorker{active: true, cancel: func() { canceled = true }}
	s.workers[1] = worker
	if err := manager.Update(context.Background(), func(v *settings.Values) error { v.Week1Type = "even"; v.TotalSlotsInQueue = 40; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconfigure(); err != nil {
		t.Fatal(err)
	}
	current, _ := store.GetScheduleEntry(context.Background(), 1)
	if canceled || s.workers[1] != worker || !current.ActiveOpensAt.Equal(opensAt) || !current.ActiveClosesAt.Equal(closesAt) {
		t.Fatalf("active run was replanned: canceled=%v, row=%+v", canceled, current)
	}
}

func TestLegacyRecoveryRestoresCurrentRunAndClosesStaleMessage(t *testing.T) {
	manager := testManager(t)
	messageID := 9
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(23, 0), EndTime: makeTimeOnly(1, 0), QueueMesID: &messageID, Notified1min: true, ThreadID: 5}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store, messageDate: moscowDate(7, 23, 30)}
	s := NewScheduler(context.Background(), store, controller, manager)
	s.clock = fixedClock{now: moscowDate(8, 0, 30)}
	restored, err := s.restoreLegacy(context.Background(), row)
	if err != nil || restored.ActiveOpensAt == nil || !restored.ActiveOpensAt.Equal(moscowDate(7, 23, 0)) || restored.QueueMesID == nil || !restored.Notified1min {
		t.Fatalf("legacy = %+v, err = %v", restored, err)
	}
	if _, err := s.processActive(context.Background(), restored, moscowDate(8, 2, 0), &scheduleWorker{}); err != nil {
		t.Fatal(err)
	}
	closed, err := store.GetScheduleEntry(context.Background(), 1)
	if err != nil || closed.QueueMesID != nil || controller.closeCalls != 1 {
		t.Fatalf("closed = %+v, calls = %d, err = %v", closed, controller.closeCalls, err)
	}
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time                       { return c.now }
func (c fixedClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

type blockingController struct{ blocked, independent chan struct{} }

func (*blockingController) CancelPending(context.Context, int, time.Time, time.Time) error {
	return nil
}
func (*blockingController) LegacyMessageDate(context.Context, int) (time.Time, error) {
	return time.Time{}, errors.New("date unavailable")
}
func (*blockingController) Prepare(context.Context, int, time.Time, time.Time) error { return nil }
func (b *blockingController) Refresh(ctx context.Context, id int) error {
	if id == 1 {
		close(b.blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	close(b.independent)
	return nil
}
func (*blockingController) Close(context.Context, int) error                  { return nil }
func (*blockingController) Notify(context.Context, db.Schedule, string) error { return nil }

func TestBlockedQueueDoesNotStopAnotherGroup(t *testing.T) {
	manager := testManager(t)
	opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
	store := &mockScheduleStore{rows: map[int]db.Schedule{
		1: {ID: 1, ChatID: -1001, ThreadID: 5, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt},
		2: {ID: 2, ChatID: -1002, ThreadID: 5, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt},
	}}
	controller := &blockingController{blocked: make(chan struct{}), independent: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	s.clock = fixedClock{now: opensAt.Add(time.Minute)}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, event := range []chan struct{}{controller.blocked, controller.independent} {
		select {
		case <-event:
		case <-time.After(time.Second):
			t.Fatal("independent queue was blocked")
		}
	}
}

type retryObservingClock struct {
	now     time.Time
	observe func()
}

func (c retryObservingClock) Now() time.Time { return c.now }
func (c retryObservingClock) After(time.Duration) <-chan time.Time {
	c.observe()
	return make(chan time.Time)
}

func TestFailedPrepareDoesNotKeepWorkerActive(t *testing.T) {
	manager := testManager(t)
	row := db.Schedule{ID: 1, DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store, prepareErr: db.ErrTopicBusy}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(ctx, store, controller, manager)
	worker := &scheduleWorker{}
	s.workers[1] = worker
	s.clock = retryObservingClock{now: moscowDate(7, 9, 55), observe: func() {
		s.mu.Lock()
		active := worker.active
		s.mu.Unlock()
		if active {
			t.Error("failed Prepare kept worker active and prevented reconfiguration")
		}
		cancel()
	}}
	s.run(ctx, 1, worker)
	if controller.prepareCalls != 1 {
		t.Fatalf("prepare calls = %d", controller.prepareCalls)
	}
}

func TestEqualOpeningClosingClocksAreRejected(t *testing.T) {
	row := db.Schedule{DayOfWeek: "monday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(10, 0)}
	if _, _, err := nextRun(row, testManager(t).Values(), moscowDate(7, 9, 0)); err == nil {
		t.Fatal("identical times must not silently create a 24h queue")
	}
}

func TestLegacyPublishedRunSurvivesChangedCalendarParity(t *testing.T) {
	manager := testManager(t)
	if err := manager.Update(context.Background(), func(v *settings.Values) error { v.Week1Date = "2026-09-11"; return nil }); err != nil {
		t.Fatal(err)
	}
	messageID := 789
	row := db.Schedule{ID: 1, DayOfWeek: "tuesday", WeekType: "odd", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0), QueueMesID: &messageID}
	store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
	controller := &mockController{store: store, messageDate: moscowDate(15, 10, 0)}
	s := NewScheduler(context.Background(), store, controller, manager)
	s.clock = fixedClock{now: moscowDate(15, 10, 30)}
	restored, err := s.restoreLegacy(context.Background(), row)
	if err != nil || restored.ActiveOpensAt == nil || !restored.ActiveOpensAt.Equal(moscowDate(15, 10, 0)) || restored.QueueMesID == nil || controller.closeCalls != 0 {
		t.Fatalf("live legacy run closed after parity correction: row=%+v close=%d err=%v", restored, controller.closeCalls, err)
	}
}

func TestLegacyRunUsesOriginalMessageDate(t *testing.T) {
	for _, test := range []struct {
		name                  string
		start, end            time.Time
		sentAt, now, wantOpen time.Time
		closed                bool
	}{
		{"live overnight", makeTimeOnly(23, 0), makeTimeOnly(1, 0), moscowDate(7, 23, 30), moscowDate(8, 0, 30), moscowDate(7, 23, 0), false},
		{"old run at same clock hours", makeTimeOnly(23, 0), makeTimeOnly(1, 0), moscowDate(7, 23, 30), moscowDate(15, 0, 30), moscowDate(7, 23, 0), true},
		{"preview before midnight", makeTimeOnly(0, 0), makeTimeOnly(1, 0), moscowDate(7, 23, 59), moscowDate(8, 0, 30), moscowDate(8, 0, 0), false},
		{"repost after midnight", makeTimeOnly(23, 0), makeTimeOnly(1, 0), moscowDate(8, 0, 15), moscowDate(8, 0, 30), moscowDate(7, 23, 0), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			messageID := 789
			row := db.Schedule{ID: 1, IsTemporary: true, StartTime: test.start, EndTime: test.end, QueueMesID: &messageID}
			store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
			controller := &mockController{store: store, messageDate: test.sentAt}
			s := NewScheduler(context.Background(), store, controller, testManager(t))
			s.clock = fixedClock{now: test.now}
			restored, err := s.restoreLegacy(context.Background(), row)
			if test.closed {
				if !errors.Is(err, pgx.ErrNoRows) || controller.closeCalls != 1 {
					t.Fatalf("expired temporary legacy run remains: row=%+v calls=%d err=%v", restored, controller.closeCalls, err)
				}
				return
			}
			if err != nil || restored.ActiveOpensAt == nil || !restored.ActiveOpensAt.Equal(test.wantOpen) || restored.QueueMesID == nil {
				t.Fatalf("legacy run bounds = %+v, err=%v", restored, err)
			}
			done, err := s.processActive(context.Background(), restored, test.now, &scheduleWorker{})
			if err != nil || done != test.closed || (controller.closeCalls != 0) != test.closed {
				t.Fatalf("legacy close: done=%v calls=%d err=%v", done, controller.closeCalls, err)
			}
			if test.closed {
				if _, err := store.GetScheduleEntry(context.Background(), row.ID); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("expired temporary run remains: %v", err)
				}
			}
		})
	}
}

func TestUnavailableLegacyDatePreservesMessageAndParticipants(t *testing.T) {
	for _, dateErr := range []error{nil, errors.New("Telegram unavailable")} {
		messageID := 789
		row := db.Schedule{ID: 1, IsTemporary: true, StartTime: makeTimeOnly(23, 0), EndTime: makeTimeOnly(1, 0), QueueMesID: &messageID}
		store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
		controller := &mockController{store: store, messageDateErr: dateErr}
		s := NewScheduler(context.Background(), store, controller, testManager(t))
		if _, err := s.restoreLegacy(context.Background(), row); err == nil {
			t.Fatal("missing original date was guessed")
		}
		preserved, err := store.GetScheduleEntry(context.Background(), row.ID)
		if err != nil || preserved.ActiveOpensAt != nil || preserved.QueueMesID == nil || *preserved.QueueMesID != messageID || controller.closeCalls != 0 {
			t.Fatalf("unknown legacy run was changed: %+v err=%v", preserved, err)
		}
	}
}

func TestLegacyRunRejectsAmbiguousInvalidClocksAndMessageDate(t *testing.T) {
	for _, test := range []struct {
		name   string
		row    db.Schedule
		sentAt time.Time
	}{
		{"equal clocks", db.Schedule{IsTemporary: true, StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(10, 0)}, moscowDate(7, 10, 30)},
		{"message outside run", db.Schedule{IsTemporary: true, StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}, moscowDate(7, 12, 0)},
		{"wrong recurring weekday", db.Schedule{DayOfWeek: "monday", StartTime: makeTimeOnly(10, 0), EndTime: makeTimeOnly(11, 0)}, moscowDate(8, 10, 30)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := legacyRunBounds(test.row, test.sentAt); err == nil {
				t.Fatal("an unsupported legacy run date was guessed")
			}
		})
	}
}

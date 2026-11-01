package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/utils"
)

var errLostWriteResponse = errors.New("database write response was lost")
var errReadbackUnavailable = errors.New("database readback is temporarily unavailable")

func TestServiceReconcilesAppliedMutationAfterLostCommitResponse(t *testing.T) {
	for _, operation := range []string{"join", "join-first", "leave", "complete", "swap"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			fixture := newServiceFixture(t)
			ref := fixture.openQueue(t, -100, 7)
			var alice, bob db.QueueEntry
			if operation != "join" && operation != "join-first" {
				alice = fixture.join(t, ref, 101, 1)
				bob = fixture.join(t, ref, 202, 2)
			}
			fixture.service.Stop()
			store := newUncertainStore(fixture.observed)
			store.fail(operation, true, 0)
			service := queue.NewService(fixture.ctx, store, fixture.settings, fixture.messenger)
			t.Cleanup(service.Stop)
			var err error
			switch operation {
			case "join":
				_, err = service.Join(fixture.ctx, ref, participant(101), 1)
			case "join-first":
				_, err = service.Join(fixture.ctx, ref, participant(101), 0)
			case "leave":
				err = service.Leave(fixture.ctx, ref, participant(101))
			case "complete":
				_, err = service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "uncertain-completion")
			case "swap":
				err = service.Swap(fixture.ctx, ref, alice.ID, bob.ID, 1, 2)
			}
			if !errors.Is(err, errLostWriteResponse) {
				t.Fatalf("write error cause was lost: %v", err)
			}
			if operation == "complete" {
				completed, err := service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "uncertain-completion")
				if err != nil || !completed {
					t.Fatalf("replayed callback undid the committed completion: completed=%t, err=%v", completed, err)
				}
			}
			updated := fixture.messenger.await(t, func(event delivery) bool {
				if event.Kind != "edit" || event.MessageID != ref.MessageID {
					return false
				}
				entries := event.Snapshot.Entries
				switch operation {
				case "join", "join-first":
					return len(entries) == 1 && entries[0].UserID == 101
				case "leave":
					return len(entries) == 1 && entries[0].UserID == 202
				case "complete":
					return len(entries) == 2 && entries[0].Completed
				case "swap":
					return len(entries) == 2 && entries[0].UserID == 202 && entries[1].UserID == 101
				}
				return false
			})
			if updated.Snapshot.Status != utils.QueueOpen || store.attempts(operation) != 1 {
				t.Fatalf("recovery repeated the mutation or changed queue state: status=%v attempts=%d", updated.Snapshot.Status, store.attempts(operation))
			}
		})
	}
}

func TestServicePublicationReadbackKeepsCommittedMessage(t *testing.T) {
	for _, operation := range []string{"publish", "repost", "close-replacement"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			fixture := newServiceFixture(t)
			var ref queue.Reference
			if operation == "publish" {
				id := fixture.addSchedule(t, -100, 7)
				now := time.Now()
				if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				ref = queue.Reference{ScheduleID: id, ChatID: -100}
			} else {
				ref = fixture.openQueue(t, -100, 7)
				fixture.join(t, ref, 101, 1)
			}
			fixture.service.Stop()
			store := newUncertainStore(fixture.observed)
			store.fail("publish", true, 0)
			var messenger queue.Messenger = fixture.messenger
			if operation == "close-replacement" {
				messenger = &missingMessageMessenger{Messenger: messenger, messageID: ref.MessageID, missing: true}
			}
			service := queue.NewService(fixture.ctx, store, fixture.settings, messenger)
			t.Cleanup(service.Stop)
			var err error
			switch operation {
			case "publish":
				err = service.Refresh(fixture.ctx, ref.ScheduleID)
			case "repost":
				err = service.Repost(fixture.ctx, ref)
			case "close-replacement":
				err = service.Close(fixture.ctx, ref.ScheduleID)
			}
			if !errors.Is(err, errLostWriteResponse) {
				t.Fatalf("publication lost the original write cause: %v", err)
			}
			current, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID)
			if err != nil || current.QueueMesID == nil || *current.QueueMesID == ref.MessageID {
				t.Fatalf("applied message ID was not retained: %+v, err=%v", current, err)
			}
			currentID := *current.QueueMesID
			if operation == "close-replacement" {
				if err := service.Close(fixture.ctx, ref.ScheduleID); err != nil {
					t.Fatalf("closing retry could not finish the committed replacement: %v", err)
				}
			} else {
				fixture.messenger.await(t, func(event delivery) bool {
					return event.Kind == "edit" && event.MessageID == currentID && event.Snapshot.Status == utils.QueueOpen
				})
				if err := service.Refresh(fixture.ctx, ref.ScheduleID); err != nil {
					t.Fatal(err)
				}
			}
			var sends int
			var retiredOld bool
			for _, event := range fixture.messenger.history() {
				if event.Kind == "retire" && event.MessageID == currentID {
					t.Fatal("lost write response deactivated the committed current message")
				}
				if event.Kind == "retire" && event.MessageID == ref.MessageID {
					retiredOld = true
				}
				if event.Kind == "send" {
					sends++
				}
			}
			wantSends := 2
			if operation == "publish" {
				wantSends = 1
			}
			if sends != wantSends || (operation == "repost" && !retiredOld) {
				t.Fatalf("recovery republished the queue or left its old keyboard: sends=%d retiredOld=%t", sends, retiredOld)
			}
		})
	}
}

func TestServiceUnresolvedPublicationWaitsForReadbackBeforeSendingAgain(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id := fixture.addSchedule(t, -100, 7)
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fixture.service.Stop()
	store := newUncertainStore(fixture.observed)
	store.fail("publish", true, 2)
	service := queue.NewService(fixture.ctx, store, fixture.settings, fixture.messenger)
	t.Cleanup(service.Stop)
	if err := service.Refresh(fixture.ctx, id); !errors.Is(err, errLostWriteResponse) {
		t.Fatalf("initial error: %v", err)
	}
	if err := service.Refresh(fixture.ctx, id); !errors.Is(err, errReadbackUnavailable) {
		t.Fatalf("refresh did not wait for authoritative readback: %v", err)
	}
	fixture.messenger.await(t, func(event delivery) bool { return event.Kind == "edit" && event.Snapshot.Schedule.ID == id })
	var sends int
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" {
			sends++
		}
		if event.Kind == "retire" {
			t.Fatal("unresolved publication was retired before readback")
		}
	}
	if sends != 1 {
		t.Fatalf("unresolved publication produced duplicate messages: %d", sends)
	}
}

func TestServiceRejectedPublicationClosesOrphanAndRetriesFromDatabase(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id := fixture.addSchedule(t, -100, 7)
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fixture.service.Stop()
	store := newUncertainStore(fixture.observed)
	store.fail("publish", false, 0)
	service := queue.NewService(fixture.ctx, store, fixture.settings, fixture.messenger)
	t.Cleanup(service.Stop)
	if err := service.Refresh(fixture.ctx, id); !errors.Is(err, errLostWriteResponse) {
		t.Fatal(err)
	}
	first := fixture.messenger.await(t, func(event delivery) bool { return event.Kind == "send" })
	fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.MessageID == first.MessageID && event.Snapshot.Status == utils.QueueClosed
	})
	if err := service.Refresh(fixture.ctx, id); err != nil {
		t.Fatal(err)
	}
	active, err := service.ActiveTopic(fixture.ctx, -100, 7)
	if err != nil || active.QueueMesID == nil || *active.QueueMesID == first.MessageID {
		t.Fatalf("replacement was not made current: %+v, err=%v", active, err)
	}
	var sends int
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("recovery produced unexpected publications: %d", sends)
	}
}

func TestServiceCancelPendingRetriesPreviewCleanupWithoutClosingNextRun(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id, opensAt, closesAt, messageID := preparePending(t, fixture)
	fixture.messenger.failEdits(id, 2)
	if err := fixture.service.CancelPending(fixture.ctx, id, opensAt, closesAt); err != nil {
		t.Fatalf("Telegram failure rolled back cancellation: %v", err)
	}
	canceled, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || canceled.ActiveOpensAt != nil || canceled.QueueMesID != nil {
		t.Fatalf("pending run stayed active: %+v, err=%v", canceled, err)
	}
	newOpensAt, newClosesAt := opensAt.Add(time.Hour), closesAt.Add(time.Hour)
	if err := fixture.service.Prepare(fixture.ctx, id, newOpensAt, newClosesAt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Refresh(fixture.ctx, id); err == nil {
		t.Fatal("second injected preview cleanup failure was swallowed")
	}
	next, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || next.QueueMesID == nil || *next.QueueMesID == messageID {
		t.Fatalf("next run did not publish independently of cleanup: %+v, err=%v", next, err)
	}
	closed := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.MessageID == messageID && event.Snapshot.Status == utils.QueueClosed
	})
	if closed.Snapshot.Schedule.ActiveOpensAt == nil || !closed.Snapshot.Schedule.ActiveOpensAt.Equal(opensAt) {
		t.Fatal("cleanup used the next run instead of the canceled preview")
	}
	var refreshedCurrent bool
	for _, event := range fixture.messenger.history() {
		if event.Kind == "edit" && event.MessageID == *next.QueueMesID && event.Snapshot.Status == utils.QueuePending {
			refreshedCurrent = true
		}
		if event.Kind == "edit" && event.MessageID == *next.QueueMesID && event.Snapshot.Status == utils.QueueClosed {
			t.Fatal("late cleanup closed the replacement run")
		}
	}
	if !refreshedCurrent {
		t.Fatal("preview cleanup blocked rendering the current run")
	}
}

func TestServiceCancelPendingReadsBackLostCommitBeforeClosingPreview(t *testing.T) {
	for _, applied := range []bool{false, true} {
		name := "rejected"
		if applied {
			name = "applied"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newServiceFixture(t)
			id, opensAt, closesAt, messageID := preparePending(t, fixture)
			fixture.service.Stop()
			store := newUncertainStore(fixture.observed)
			store.fail("finish", applied, 1)
			service := queue.NewService(fixture.ctx, store, fixture.settings, fixture.messenger)
			t.Cleanup(service.Stop)
			if err := service.CancelPending(fixture.ctx, id, opensAt, closesAt); !errors.Is(err, errLostWriteResponse) {
				t.Fatalf("cancel lost the commit error: %v", err)
			}
			wantStatus := utils.QueuePending
			if applied {
				wantStatus = utils.QueueClosed
			}
			fixture.messenger.await(t, func(event delivery) bool {
				return event.Kind == "edit" && event.MessageID == messageID && event.Snapshot.Status == wantStatus
			})
			actual, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
			if err != nil || (actual.ActiveOpensAt == nil) != applied {
				t.Fatalf("recovery changed database outcome: %+v, err=%v", actual, err)
			}
			if !applied {
				for _, event := range fixture.messenger.history() {
					if event.Kind == "edit" && event.MessageID == messageID && event.Snapshot.Status == utils.QueueClosed {
						t.Fatal("failed cancellation closed a still-active preview")
					}
				}
			}
			if store.attempts("finish") != 1 {
				t.Fatal("background reconciliation repeated cancellation")
			}
		})
	}
}

func TestServiceCancelPendingChecksRunIdentityAndPreservesOpenedQueue(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id, opensAt, closesAt, _ := preparePending(t, fixture)
	if err := fixture.service.CancelPending(fixture.ctx, id, opensAt.Add(time.Minute), closesAt); !errors.Is(err, db.ErrQueueStateChanged) {
		t.Fatalf("mismatching run identity was accepted: %v", err)
	}
	opened := fixture.openQueue(t, -100, 8)
	fixture.join(t, opened, 101, 1)
	actual, err := fixture.repository.GetScheduleEntry(fixture.ctx, opened.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CancelPending(fixture.ctx, opened.ScheduleID, *actual.ActiveOpensAt, *actual.ActiveClosesAt); err != nil {
		t.Fatal(err)
	}
	if entries := fixture.entries(t, opened.ScheduleID); len(entries) != 1 || entries[0].UserID != 101 {
		t.Fatalf("reconfiguration cleared an already opened queue: %+v", entries)
	}
	values := fixture.settings.Values()
	temporaryID, err := fixture.repository.AddTemporarySchedule(fixture.ctx, db.Schedule{
		ChatID: -100, ThreadID: 9, StartTime: opensAt, EndTime: closesAt,
		ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt,
		ActiveCapacity: values.TotalSlotsInQueue, ActiveColumns: values.AmountOfButtonsInRow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CancelPending(fixture.ctx, temporaryID, opensAt, closesAt); err != nil {
		t.Fatal(err)
	}
	temporary, err := fixture.repository.GetScheduleEntry(fixture.ctx, temporaryID)
	if err != nil || !temporary.IsTemporary || temporary.ActiveOpensAt == nil {
		t.Fatalf("reconfiguration canceled a pending one-shot queue: %+v, err=%v", temporary, err)
	}
}

func TestServiceLegacyMessageDateRequiresOriginalMetadata(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	if _, err := fixture.service.LegacyMessageDate(fixture.ctx, ref.ScheduleID); !errors.Is(err, queue.ErrLegacyDateUnavailable) {
		t.Fatalf("unsupported messenger guessed a date: %v", err)
	}
	fixture.service.Stop()
	original := time.Date(2026, 2, 12, 9, 42, 0, 0, time.UTC)
	messenger := &datedMessenger{Messenger: fixture.messenger, date: original}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	date, err := service.LegacyMessageDate(fixture.ctx, ref.ScheduleID)
	if err != nil || !date.Equal(original) || messenger.messageID != ref.MessageID {
		t.Fatalf("original message identity/date changed: date=%v message=%d err=%v", date, messenger.messageID, err)
	}
	for _, invalid := range []time.Time{{}, time.Unix(0, 0)} {
		messenger.date = invalid
		if _, err := service.LegacyMessageDate(fixture.ctx, ref.ScheduleID); !errors.Is(err, queue.ErrLegacyDateUnavailable) {
			t.Fatalf("missing original date was guessed: %v", err)
		}
	}
}

func preparePending(t *testing.T, fixture *serviceFixture) (int, time.Time, time.Time, int) {
	t.Helper()
	id := fixture.addSchedule(t, -100, 7)
	opensAt := time.Now().Add(time.Minute)
	closesAt := opensAt.Add(time.Hour)
	if err := fixture.service.Prepare(fixture.ctx, id, opensAt, closesAt); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Refresh(fixture.ctx, id); err != nil {
		t.Fatal(err)
	}
	actual, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || actual.QueueMesID == nil {
		t.Fatalf("pending preview: %+v, err=%v", actual, err)
	}
	return id, *actual.ActiveOpensAt, *actual.ActiveClosesAt, *actual.QueueMesID
}

var _ queue.LegacyMessenger = (*datedMessenger)(nil)

type datedMessenger struct {
	queue.Messenger
	date      time.Time
	messageID int
}

func (messenger *datedMessenger) QueueMessageDate(_ context.Context, _ db.Schedule, messageID int) (time.Time, error) {
	messenger.messageID = messageID
	return messenger.date, nil
}

type writeFault struct {
	apply        bool
	readFailures int
}

type uncertainStore struct {
	*observedStore
	mu          sync.Mutex
	faults      map[string]writeFault
	calls       map[string]int
	failedReads int
}

var _ queue.Store = (*uncertainStore)(nil)

func newUncertainStore(store *observedStore) *uncertainStore {
	return &uncertainStore{observedStore: store, faults: make(map[string]writeFault), calls: make(map[string]int)}
}

func (store *uncertainStore) fail(operation string, apply bool, readFailures int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.faults[operation] = writeFault{apply: apply, readFailures: readFailures}
}

func (store *uncertainStore) attempts(operation string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.calls[operation]
}

func (store *uncertainStore) write(operation string, apply func() error) error {
	store.mu.Lock()
	store.calls[operation]++
	fault, failed := store.faults[operation]
	delete(store.faults, operation)
	store.mu.Unlock()
	if !failed || fault.apply {
		if err := apply(); err != nil {
			return err
		}
	}
	if failed {
		store.mu.Lock()
		store.failedReads += fault.readFailures
		store.mu.Unlock()
		return errLostWriteResponse
	}
	return nil
}

func (store *uncertainStore) readError() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failedReads > 0 {
		store.failedReads--
		return errReadbackUnavailable
	}
	return nil
}

func (store *uncertainStore) GetScheduleEntry(ctx context.Context, id int) (db.Schedule, error) {
	if err := store.readError(); err != nil {
		return db.Schedule{}, err
	}
	return store.observedStore.GetScheduleEntry(ctx, id)
}

func (store *uncertainStore) SyncQueueState(ctx context.Context, id int) (db.Schedule, error) {
	if err := store.readError(); err != nil {
		return db.Schedule{}, err
	}
	return store.DBRepository.SyncQueueState(ctx, id)
}

func (store *uncertainStore) JoinQueue(ctx context.Context, entry db.QueueEntry) error {
	return store.write("join", func() error { return store.DBRepository.JoinQueue(ctx, entry) })
}

func (store *uncertainStore) JoinFirstFreeSlotWithName(ctx context.Context, userID int64, username, displayName string, scheduleID, totalSlots int) (int, error) {
	var position int
	err := store.write("join-first", func() error {
		var err error
		position, err = store.DBRepository.JoinFirstFreeSlotWithName(ctx, userID, username, displayName, scheduleID, totalSlots)
		return err
	})
	return position, err
}

func (store *uncertainStore) LeaveFromQueue(ctx context.Context, userID int64, scheduleID int) error {
	return store.write("leave", func() error { return store.DBRepository.LeaveFromQueue(ctx, userID, scheduleID) })
}

func (store *uncertainStore) SetCompleted(ctx context.Context, scheduleID, entryID, position int, completed bool) error {
	return store.write("complete", func() error { return store.DBRepository.SetCompleted(ctx, scheduleID, entryID, position, completed) })
}

func (store *uncertainStore) SwapQueueEntries(ctx context.Context, scheduleID, requesterID, targetID, requesterPosition, targetPosition int) error {
	return store.write("swap", func() error {
		return store.DBRepository.SwapQueueEntries(ctx, scheduleID, requesterID, targetID, requesterPosition, targetPosition)
	})
}

func (store *uncertainStore) SetQueueMessageID(ctx context.Context, scheduleID, messageID int) error {
	return store.write("publish", func() error { return store.DBRepository.SetQueueMessageID(ctx, scheduleID, messageID) })
}

func (store *uncertainStore) FinishQueueRun(ctx context.Context, scheduleID int) error {
	return store.write("finish", func() error { return store.DBRepository.FinishQueueRun(ctx, scheduleID) })
}

func TestServiceWaitsForTerminalWriteBeforeRenderingOrCleaningMessages(t *testing.T) {
	for _, operation := range []string{"join", "publish"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			fixture := newServiceFixture(t)
			var id int
			var ref queue.Reference
			if operation == "join" {
				ref = fixture.openQueue(t, -100, 7)
				id = ref.ScheduleID
			} else {
				id = fixture.addSchedule(t, -100, 7)
				now := time.Now()
				if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			fixture.service.Stop()
			store := &lateCommitStore{observedStore: fixture.observed, operation: operation,
				entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
			defer store.unblock()
			service := queue.NewService(fixture.ctx, store, fixture.settings, fixture.messenger)
			t.Cleanup(service.Stop)
			result := make(chan error, 1)
			go func() {
				if operation == "join" {
					_, err := service.Join(fixture.ctx, ref, participant(101), 1)
					result <- err
					return
				}
				result <- service.Refresh(fixture.ctx, id)
			}()
			awaitChannel(t, store.entered, "terminal database write barrier")
			for _, event := range fixture.messenger.history() {
				if event.Kind == "edit" || event.Kind == "retire" {
					t.Fatalf("unresolved commit changed a message before the barrier: %+v", event)
				}
			}
			store.unblock()
			if err := awaitChannel(t, result, "initial write error"); !errors.Is(err, errLostWriteResponse) {
				t.Fatalf("terminal barrier changed the original error: %v", err)
			}
			fixture.messenger.await(t, func(event delivery) bool {
				return event.Kind == "edit" && event.Snapshot.Status == utils.QueueOpen &&
					(operation == "publish" || len(event.Snapshot.Entries) == 1)
			})
			var sends int
			for _, event := range fixture.messenger.history() {
				if event.Kind == "send" {
					sends++
				}
				if event.Kind == "retire" || event.Snapshot.Status == utils.QueueClosed {
					t.Fatalf("late committed message was deactivated: %+v", event)
				}
			}
			if sends != 1 {
				t.Fatalf("late commit produced duplicate queue messages: %d", sends)
			}
		})
	}
}

func TestServiceOldPreviewCleanupCannotStarveCurrentRunRendering(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id, opensAt, closesAt, oldMessageID := preparePending(t, fixture)
	gate := fixture.messenger.block(t, id, utils.QueueClosed)
	cancelCtx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	canceled := make(chan error, 1)
	go func() { canceled <- fixture.service.CancelPending(cancelCtx, id, opensAt, closesAt) }()
	awaitChannel(t, gate.started, "first preview cleanup attempt")
	cancel()
	if err := awaitChannel(t, canceled, "database cancellation despite unavailable Telegram"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	refreshed := make(chan error, 1)
	go func() { refreshed <- fixture.service.Refresh(fixture.ctx, id) }()
	current := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "send" && event.MessageID != oldMessageID && event.Snapshot.Status == utils.QueueOpen
	})
	gate.unblock()
	if err := awaitChannel(t, refreshed, "current run publication and old preview cleanup"); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.service.ActiveTopic(fixture.ctx, -100, 7)
	if err != nil || active.QueueMesID == nil || *active.QueueMesID != current.MessageID {
		t.Fatalf("old preview cleanup replaced the current run: %+v, err=%v", active, err)
	}
}

type lateCommitStore struct {
	*observedStore
	operation string
	entered   chan struct{}
	release   chan struct{}
	done      chan struct{}
	once      sync.Once
	barrier   sync.Once
	released  sync.Once
	applyErr  error
}

var _ queue.Store = (*lateCommitStore)(nil)

func (store *lateCommitStore) unblock() {
	store.released.Do(func() { close(store.release) })
}

func (store *lateCommitStore) lateWrite(apply func() error) error {
	var delayed bool
	store.once.Do(func() {
		delayed = true
		go func() {
			<-store.release
			store.applyErr = apply()
			close(store.done)
		}()
	})
	if delayed {
		return errLostWriteResponse
	}
	return apply()
}

func (store *lateCommitStore) JoinQueue(ctx context.Context, entry db.QueueEntry) error {
	if store.operation != "join" {
		return store.DBRepository.JoinQueue(ctx, entry)
	}
	return store.lateWrite(func() error { return store.DBRepository.JoinQueue(context.WithoutCancel(ctx), entry) })
}

func (store *lateCommitStore) SetQueueMessageID(ctx context.Context, id, messageID int) error {
	if store.operation != "publish" {
		return store.DBRepository.SetQueueMessageID(ctx, id, messageID)
	}
	return store.lateWrite(func() error { return store.DBRepository.SetQueueMessageID(context.WithoutCancel(ctx), id, messageID) })
}

func (store *lateCommitStore) SyncQueueState(ctx context.Context, id int) (db.Schedule, error) {
	store.barrier.Do(func() { close(store.entered) })
	select {
	case <-store.done:
	case <-ctx.Done():
		return db.Schedule{}, ctx.Err()
	}
	if store.applyErr != nil {
		return db.Schedule{}, store.applyErr
	}
	return store.DBRepository.SyncQueueState(ctx, id)
}

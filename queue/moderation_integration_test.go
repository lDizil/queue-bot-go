package queue_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"queuebot/access"
	"queuebot/db"
	"queuebot/queue"
)

type testPermissions func(int64, access.Permission) (bool, error)

func (permissions testPermissions) Allowed(_ context.Context, id int64, permission access.Permission) (bool, error) {
	return permissions(id, permission)
}

func runReference(t *testing.T, fixture *serviceFixture, ref queue.Reference) queue.RunReference {
	t.Helper()
	schedule, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID)
	if err != nil || schedule.ActiveOpensAt == nil {
		t.Fatalf("missing current run: schedule=%+v err=%v", schedule, err)
	}
	return queue.RunReference{ScheduleID: ref.ScheduleID, ChatID: ref.ChatID, OpensAt: *schedule.ActiveOpensAt}
}

func TestModerationUsesEntryIdentityAfterSwapAndRejoin(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	if err := fixture.service.Swap(fixture.ctx, ref, alice.ID, bob.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if len(entries) != 1 || entries[0].ID != bob.ID || entries[0].Position != 1 {
		t.Fatalf("removal followed the old position instead of Alice: %+v", entries)
	}
	newAlice := fixture.join(t, ref, 101, 2)
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); !errors.Is(err, queue.ErrChanged) {
		t.Fatalf("stale removal accepted after rejoin: %v", err)
	}
	entries = fixture.entries(t, ref.ScheduleID)
	if len(entries) != 2 || entries[1].ID != newAlice.ID {
		t.Fatalf("stale removal deleted a new entry: %+v", entries)
	}
	other := fixture.openQueue(t, -200, 7)
	if err := fixture.service.Remove(fixture.ctx, run, 999, fixture.join(t, other, 303, 1).ID, 0); !errors.Is(err, queue.ErrChanged) {
		t.Fatalf("entry from another schedule accepted: %v", err)
	}
	wrongChat := run
	wrongChat.ChatID = other.ChatID
	if err := fixture.service.Remove(fixture.ctx, wrongChat, 999, bob.ID, 0); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("wrong chat accepted: %v", err)
	}
}

func TestModerationRunSurvivesRepostAndRejectsNextRun(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	if err := fixture.service.Repost(fixture.ctx, ref); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.service.InspectRun(fixture.ctx, run)
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].ID != alice.ID {
		t.Fatalf("repost invalidated run management: snapshot=%+v err=%v", snapshot, err)
	}
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); err != nil {
		t.Fatalf("repost invalidated participant removal: %v", err)
	}
	if err := fixture.service.Close(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.InspectRun(fixture.ctx, run); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("closed run remains inspectable as open: %v", err)
	}
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, ref.ScheduleID, now.Add(-time.Second), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.repository.JoinQueue(fixture.ctx, db.QueueEntry{UserID: 101, ScheduleID: ref.ScheduleID, Position: 1}); err != nil {
		t.Fatal(err)
	}
	nextAlice, err := fixture.repository.GetQueueEntryByUser(fixture.ctx, 101, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Remove(fixture.ctx, run, 999, nextAlice.ID, 0); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("old run reference reached a new run: %v", err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 {
		t.Fatalf("old menu deleted the next run participant: %+v", entries)
	}
}

func TestModerationFourQuickRemovalsDoNotRenumberSurvivor(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	var ids []int
	for position := 1; position <= 5; position++ {
		ids = append(ids, fixture.join(t, ref, int64(position+100), position).ID)
	}
	var workers sync.WaitGroup
	failures := make(chan error, 4)
	for _, id := range ids[:4] {
		workers.Add(1)
		go func() {
			defer workers.Done()
			failures <- fixture.service.Remove(fixture.ctx, run, 999, id, 0)
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 || entries[0].ID != ids[4] || entries[0].Position != 5 {
		t.Fatalf("quick removals deleted or renumbered the survivor: %+v", entries)
	}
}

func TestModerationOnlyListsCurrentlyOpenRunsInEveryChat(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.openQueue(t, -100, 7)
	second := fixture.openQueue(t, -200, 7)
	pending := fixture.addSchedule(t, -100, 8)
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, pending, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired := fixture.addSchedule(t, -100, 9)
	if err := fixture.service.Prepare(fixture.ctx, expired, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	fixture.addSchedule(t, -100, 10)
	closesAt := now.Add(time.Hour)
	temporary, err := fixture.repository.AddTemporarySchedule(fixture.ctx, db.Schedule{ChatID: -300, ThreadID: 7, Title: "Temporary open queue", StartTime: now, EndTime: now.Add(time.Hour), ActiveOpensAt: &now, ActiveClosesAt: &closesAt, ActiveCapacity: 10, ActiveColumns: 3})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := fixture.service.OpenQueues(fixture.ctx)
	if err != nil || len(opened) != 3 || opened[0].Schedule.ID != first.ScheduleID || opened[1].Schedule.ID != second.ScheduleID || opened[2].Schedule.ID != temporary {
		t.Fatalf("open run list includes pending/expired or loses another chat: %+v err=%v", opened, err)
	}
}

func TestModerationAndForeignCompletionFailClosedWithSeparatePermissions(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	cause := errors.New("permission storage unavailable")
	fixture.service.SetAuthorizer(testPermissions(func(int64, access.Permission) (bool, error) { return true, cause }))
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); !errors.Is(err, queue.ErrForbidden) || !errors.Is(err, cause) {
		t.Fatalf("permission error was not closed or lost its cause: %v", err)
	}
	fixture.service.SetAuthorizer(nil)
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("nil authorizer permitted removal: %v", err)
	}
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(999), alice.ID, 1, nil, "no-authorizer"); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("nil authorizer permitted foreign completion: %v", err)
	}
	fixture.service.SetAuthorizer(testPermissions(func(_ int64, permission access.Permission) (bool, error) { return permission == access.MarkOthers, nil }))
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(999), alice.ID, 1, nil, "mark-only"); err != nil {
		t.Fatalf("mark permission did not allow completion: %v", err)
	}
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("mark permission also allowed removal: %v", err)
	}
	fixture.service.SetAuthorizer(nil)
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "self"); err != nil {
		t.Fatalf("self completion unexpectedly needs foreign permission: %v", err)
	}
}

func TestModerationRechecksPermissionAfterWaitingForQueueLock(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	fixture.service.Stop()
	blocked := &moderationBarrierStore{observedStore: fixture.observed, entered: make(chan struct{}), release: make(chan struct{})}
	service := queue.NewService(fixture.ctx, blocked, fixture.settings, fixture.messenger)
	t.Cleanup(service.Stop)
	t.Cleanup(blocked.unblock)
	var allowed atomic.Bool
	allowed.Store(true)
	checked := make(chan struct{}, 1)
	service.SetAuthorizer(testPermissions(func(int64, access.Permission) (bool, error) {
		checked <- struct{}{}
		return allowed.Load(), nil
	}))
	reading := make(chan error, 1)
	go func() { _, err := service.InspectRun(fixture.ctx, run); reading <- err }()
	awaitChannel(t, blocked.entered, "first operation holding queue state lock")
	removing := make(chan error, 1)
	go func() { removing <- service.Remove(fixture.ctx, run, 999, alice.ID, 0) }()
	select {
	case <-checked:
		t.Error("permission was checked before obtaining queue state lock")
	case <-time.After(100 * time.Millisecond):
	}
	allowed.Store(false)
	blocked.unblock()
	if err := awaitChannel(t, reading, "first operation released"); err != nil {
		t.Fatal(err)
	}
	if err := awaitChannel(t, removing, "queued removal"); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("revoked administrator removed participant after waiting: %v", err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 || entries[0].ID != alice.ID {
		t.Fatalf("revoked operation changed participants: %+v", entries)
	}
}

type moderationBarrierStore struct {
	*observedStore
	once        sync.Once
	releaseOnce sync.Once
	entered     chan struct{}
	release     chan struct{}
}

func (store *moderationBarrierStore) GetScheduleEntry(ctx context.Context, id int) (db.Schedule, error) {
	store.once.Do(func() {
		close(store.entered)
		select {
		case <-store.release:
		case <-ctx.Done():
		}
	})
	return store.observedStore.GetScheduleEntry(ctx, id)
}
func (store *moderationBarrierStore) unblock() { store.releaseOnce.Do(func() { close(store.release) }) }

func TestModerationBlockSurvivesRestartAndManualUnblockAllowsRejoin(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	before := time.Now()
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 37*time.Second); err != nil {
		t.Fatal(err)
	}
	blocks, err := fixture.service.Blocks(fixture.ctx, run, 999)
	if err != nil || len(blocks) != 1 || blocks[0].UserID != 101 || blocks[0].DisplayName != alice.DisplayName {
		t.Fatalf("removed participant missing from blocks: %+v err=%v", blocks, err)
	}
	if remaining := blocks[0].BlockedUntil.Sub(before); remaining < 37*time.Second || remaining > 39*time.Second {
		t.Fatalf("explicit removal duration ignored: %v", remaining)
	}
	if err := fixture.service.Swap(fixture.ctx, ref, alice.ID, bob.ID, 1, 2); !errors.Is(err, db.ErrQueueStateChanged) {
		t.Fatalf("swap restored removed entry: %v", err)
	}
	fixture.service.Stop()
	restored := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, fixture.messenger)
	restored.SetAuthorizer(testPermissions(func(id int64, _ access.Permission) (bool, error) { return id == 999, nil }))
	t.Cleanup(restored.Stop)
	for _, position := range []int{0, 1} {
		_, err := restored.Join(fixture.ctx, ref, participant(101), position)
		var blocked *db.RejoinBlockedError
		if !errors.As(err, &blocked) || !blocked.Until.Equal(blocks[0].BlockedUntil) {
			t.Fatalf("restart lost rejoin block for position %d: %v", position, err)
		}
		text := queue.ErrorText(err)
		if !strings.Contains(text, "Попробуйте через ") || !strings.Contains(text, "сек.") {
			t.Fatalf("rejoin response lost remaining wait: %q", text)
		}
	}
	persisted, err := restored.Blocks(fixture.ctx, run, 999)
	if err != nil || len(persisted) != 1 || persisted[0] != blocks[0] {
		t.Fatalf("restart changed block list: %+v err=%v", persisted, err)
	}
	if err := restored.Unblock(fixture.ctx, run, 999, blocks[0].ID); err != nil {
		t.Fatal(err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 || entries[0].ID != bob.ID {
		t.Fatalf("unblock restored or changed a queue entry: %+v", entries)
	}
	if _, err := restored.Join(fixture.ctx, ref, participant(101), 0); err != nil {
		t.Fatalf("manual unblock did not allow immediate rejoin: %v", err)
	}
	if err := restored.Unblock(fixture.ctx, run, 999, blocks[0].ID); !errors.Is(err, queue.ErrBlockChanged) {
		t.Fatalf("old unblock accepted: %v", err)
	}
}

func TestModerationBlocksRequirePermissionAndCurrentRun(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	alice := fixture.join(t, ref, 101, 1)
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	blocks, err := fixture.service.Blocks(fixture.ctx, run, 999)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("missing block: %+v err=%v", blocks, err)
	}
	fixture.service.SetAuthorizer(testPermissions(func(_ int64, permission access.Permission) (bool, error) { return permission == access.MarkOthers, nil }))
	if _, err := fixture.service.Blocks(fixture.ctx, run, 999); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("mark permission allowed listing blocks: %v", err)
	}
	if err := fixture.service.Unblock(fixture.ctx, run, 999, blocks[0].ID); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("mark permission allowed unblocking: %v", err)
	}
	cause := errors.New("permissions unavailable")
	fixture.service.SetAuthorizer(testPermissions(func(int64, access.Permission) (bool, error) { return true, cause }))
	if err := fixture.service.Unblock(fixture.ctx, run, 999, blocks[0].ID); !errors.Is(err, queue.ErrForbidden) || !errors.Is(err, cause) {
		t.Fatalf("unblock did not fail closed with permission cause: %v", err)
	}
	fixture.service.SetAuthorizer(testPermissions(func(id int64, _ access.Permission) (bool, error) { return id == 999, nil }))
	wrongChat := run
	wrongChat.ChatID--
	if _, err := fixture.service.Blocks(fixture.ctx, wrongChat, 999); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("wrong chat listed blocks: %v", err)
	}
	if err := fixture.service.Unblock(fixture.ctx, wrongChat, 999, blocks[0].ID); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("wrong chat unblocked participant: %v", err)
	}
	if err := fixture.service.Close(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Blocks(fixture.ctx, run, 999); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("closed run still lists blocks: %v", err)
	}
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, ref.ScheduleID, now.Add(-time.Second), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Unblock(fixture.ctx, run, 999, blocks[0].ID); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("old run reached next run unblock: %v", err)
	}
	if err := fixture.service.Refresh(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	schedule, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	ref.MessageID = *schedule.QueueMesID
	fixture.join(t, ref, 101, 1)
}

func TestModerationBlockDurationAndVoluntaryLeave(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	run := runReference(t, fixture, ref)
	fixture.join(t, ref, 101, 1)
	if err := fixture.service.Leave(fixture.ctx, ref, participant(101)); err != nil {
		t.Fatal(err)
	}
	alice := fixture.join(t, ref, 101, 1)
	for _, duration := range []time.Duration{-time.Nanosecond, 24*time.Hour + time.Nanosecond} {
		if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, duration); !errors.Is(err, queue.ErrBlockDuration) {
			t.Fatalf("invalid duration %v accepted: %v", duration, err)
		}
	}
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	alice = fixture.join(t, ref, 101, 1)
	if err := fixture.service.Remove(fixture.ctx, run, 999, alice.ID, 24*time.Hour); err != nil {
		t.Fatalf("maximum duration rejected: %v", err)
	}
}

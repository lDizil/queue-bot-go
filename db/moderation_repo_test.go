package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRemoveQueueEntryRequiresScheduleEntryAndCurrentRun(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	scheduleID := mustCreateTemporarySchedule(t, repo, 202701)
	otherID := mustCreateTemporarySchedule(t, repo, 202702)
	now := time.Now()
	schedule, err := repo.BeginQueueRun(ctx, scheduleID, now.Add(-time.Minute), now.Add(time.Hour), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: scheduleID, Position: 1}); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.GetQueueEntryByUser(ctx, 101, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		scheduleID int
		opensAt    time.Time
		entryID    int
	}{
		{otherID, *schedule.ActiveOpensAt, entry.ID},
		{scheduleID, schedule.ActiveOpensAt.Add(-time.Hour), entry.ID},
		{scheduleID, *schedule.ActiveOpensAt, entry.ID + 1},
	} {
		if err := repo.RemoveQueueEntry(ctx, target.scheduleID, target.opensAt, target.entryID, 0); !errors.Is(err, ErrQueueStateChanged) {
			t.Fatalf("stale or cross-schedule deletion accepted: %+v err=%v", target, err)
		}
	}
	if entries, err := repo.GetQueue(ctx, scheduleID); err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("guard changed the current participant: %+v err=%v", entries, err)
	}
	if err := repo.RemoveQueueEntry(ctx, scheduleID, *schedule.ActiveOpensAt, entry.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: scheduleID, Position: 1}); err != nil {
		t.Fatal(err)
	}
	replacement, err := repo.GetQueueEntryByUser(ctx, 101, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveQueueEntry(ctx, scheduleID, *schedule.ActiveOpensAt, entry.ID, 0); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("old entry removed a rejoined participant: %v", err)
	}
	if entries, err := repo.GetQueue(ctx, scheduleID); err != nil || len(entries) != 1 || entries[0].ID != replacement.ID {
		t.Fatalf("stale entry deletion removed replacement: %+v err=%v", entries, err)
	}
}

func TestRemoveQueueEntryRejectsPendingAndExpiredWindow(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	now := time.Now()
	for index, bounds := range []struct{ opensAt, closesAt time.Time }{
		{now.Add(time.Hour), now.Add(2 * time.Hour)},
		{now.Add(-2 * time.Hour), now.Add(-time.Hour)},
	} {
		id := mustCreateTemporarySchedule(t, repo, 202703+index)
		schedule, err := repo.BeginQueueRun(ctx, id, bounds.opensAt, bounds.closesAt, 10, 3)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}); err != nil {
			t.Fatal(err)
		}
		entry, err := repo.GetQueueEntryByUser(ctx, 101, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID, 0); !errors.Is(err, ErrQueueStateChanged) {
			t.Fatalf("removed participant outside current open window: %v", err)
		}
		if entries, err := repo.GetQueue(ctx, id); err != nil || len(entries) != 1 {
			t.Fatalf("closed window removal lost participant: %+v err=%v", entries, err)
		}
	}
}

func TestQueueBlocksExpireAndRejectStaleRemovalAndUnblock(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := t.Context()
	id := mustCreateTemporarySchedule(t, repo, 202705)
	now := time.Now()
	schedule, err := repo.BeginQueueRun(ctx, id, now.Add(-time.Minute), now.Add(time.Hour), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	join := func() QueueEntry {
		t.Helper()
		if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, Username: "alice", DisplayName: "Alice", ScheduleID: id, Position: 1}); err != nil {
			t.Fatal(err)
		}
		entry, err := repo.GetQueueEntryByUser(ctx, 101, id)
		if err != nil {
			t.Fatal(err)
		}
		return entry
	}
	entry := join()
	if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	blocks, err := repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("missing block: %+v err=%v", blocks, err)
	}
	first := blocks[0]
	if first.UserID != 101 || first.Username != "alice" || first.DisplayName != "Alice" || first.BlockedUntil.Before(now.Add(3*time.Minute)) {
		t.Fatalf("block lost the participant or duration: %+v", first)
	}
	if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID, time.Hour); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("stale removal accepted: %v", err)
	}
	blocks, err = repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt)
	if err != nil || len(blocks) != 1 || blocks[0] != first {
		t.Fatalf("stale removal changed the block: %+v err=%v", blocks, err)
	}
	for _, attempt := range []func() error{
		func() error { return repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}) },
		func() error { _, err := repo.JoinFirstFreeSlot(ctx, 101, "alice", id, 10); return err },
	} {
		var blocked *RejoinBlockedError
		if err := attempt(); !errors.As(err, &blocked) || !blocked.Until.Equal(first.BlockedUntil) {
			t.Fatalf("join bypassed block or lost expiry: %v", err)
		}
	}
	other := mustCreateTemporarySchedule(t, repo, 202706)
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: other, Position: 1}); err != nil {
		t.Fatalf("block leaked to another queue: %v", err)
	}
	for _, target := range []struct {
		scheduleID int
		opensAt    time.Time
	}{{other, *schedule.ActiveOpensAt}, {id, schedule.ActiveOpensAt.Add(-time.Hour)}} {
		if err := repo.UnblockQueueEntry(ctx, target.scheduleID, target.opensAt, first.ID); !errors.Is(err, ErrQueueStateChanged) {
			t.Fatalf("cross-scope unblock accepted: %v", err)
		}
	}
	if _, err := repo.pool.Exec(ctx, "UPDATE queue_rejoin_blocks SET blocked_until = clock_timestamp() - interval '1 second' WHERE id = $1", first.ID); err != nil {
		t.Fatal(err)
	}
	if blocks, err := repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt); err != nil || len(blocks) != 0 {
		t.Fatalf("expired block remains visible: %+v err=%v", blocks, err)
	}
	if err := repo.UnblockQueueEntry(ctx, id, *schedule.ActiveOpensAt, first.ID); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("expired block accepted: %v", err)
	}
	replacement := join()
	if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, replacement.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	blocks, err = repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt)
	if err != nil || len(blocks) != 1 || blocks[0].ID == first.ID {
		t.Fatalf("reblock reused previous identity: %+v err=%v", blocks, err)
	}
	if err := repo.UnblockQueueEntry(ctx, id, *schedule.ActiveOpensAt, first.ID); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("old unblock released a new block: %v", err)
	}
	var blocked *RejoinBlockedError
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}); !errors.As(err, &blocked) {
		t.Fatalf("stale unblock removed current block: %v", err)
	}
	if err := repo.UnblockQueueEntry(ctx, id, *schedule.ActiveOpensAt, blocks[0].ID); err != nil {
		t.Fatal(err)
	}
	join()
}

func TestQueueBlocksAreClearedBeforeNextRun(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := t.Context()
	id, err := repo.AddNewScheduleEntry(ctx, Schedule{ChatID: -202707, ThreadID: 202707, StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.DeleteScheduleEntry(context.Background(), id) })
	now := time.Now()
	schedule, err := repo.BeginQueueRun(ctx, id, now.Add(-time.Minute), now.Add(time.Hour), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.GetQueueEntryByUser(ctx, 101, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	blocks, err := repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("missing block: %+v err=%v", blocks, err)
	}
	if _, err := repo.BeginQueueRun(ctx, id, *schedule.ActiveOpensAt, *schedule.ActiveClosesAt, 10, 3); err != nil {
		t.Fatal(err)
	}
	if retained, err := repo.ListQueueBlocks(ctx, id, *schedule.ActiveOpensAt); err != nil || len(retained) != 1 {
		t.Fatalf("repeated preparation cleared active block: %+v err=%v", retained, err)
	}
	if _, err := repo.pool.Exec(ctx, "UPDATE schedules SET active_closes_at = clock_timestamp() - interval '1 second' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if err := repo.UnblockQueueEntry(ctx, id, *schedule.ActiveOpensAt, blocks[0].ID); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("unblock accepted a closed run: %v", err)
	}
	if err := repo.FinishQueueRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM queue_rejoin_blocks WHERE schedule_id = $1", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("finished run retained blocks: count=%d err=%v", count, err)
	}
	if _, err := repo.pool.Exec(ctx, `INSERT INTO queue_rejoin_blocks (schedule_id, active_opens_at, user_id, blocked_until)
 VALUES ($1, $2, 101, clock_timestamp() + interval '1 hour')`, id, schedule.ActiveOpensAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginQueueRun(ctx, id, schedule.ActiveOpensAt.Add(time.Second), now.Add(time.Hour), 10, 3); err != nil {
		t.Fatal(err)
	}
	if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM queue_rejoin_blocks WHERE schedule_id = $1", id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("new run retained old blocks: count=%d err=%v", count, err)
	}
	if err := repo.UnblockQueueEntry(ctx, id, *schedule.ActiveOpensAt, blocks[0].ID); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("old run unblocked the next run: %v", err)
	}
	if _, err := repo.JoinFirstFreeSlot(ctx, 101, "alice", id, 10); err != nil {
		t.Fatalf("old block reached the next run: %v", err)
	}
}

func TestJoinChecksBlockAfterWaitingForRemovalTransaction(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id := mustCreateTemporarySchedule(t, repo, 202708)
	now := time.Now()
	schedule, err := repo.BeginQueueRun(ctx, id, now.Add(-time.Minute), now.Add(time.Hour), 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.GetQueueEntryByUser(ctx, 101, id)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Rollback(context.Background())
	if err := lockQueue(ctx, gate, id); err != nil {
		t.Fatal(err)
	}
	waitForLockWaiters := func(want int) {
		t.Helper()
		for {
			var count int
			err := gate.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
 AND classid = 0 AND objid = $1::oid AND objsubid = 1 AND NOT granted`, id).Scan(&count)
			if err != nil {
				t.Fatalf("observe queue lock waiters: %v", err)
			}
			if count >= want {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("queue lock waiters=%d, want %d", count, want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	removed := make(chan error, 1)
	go func() { removed <- repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID, time.Minute) }()
	waitForLockWaiters(1)
	joined := make(chan error, 2)
	go func() { joined <- repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: id, Position: 1}) }()
	go func() { _, err := repo.JoinFirstFreeSlot(ctx, 101, "alice", id, 10); joined <- err }()
	waitForLockWaiters(3)
	if err := gate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var blocked *RejoinBlockedError
		if err := <-joined; !errors.As(err, &blocked) {
			t.Fatalf("join bypassed committed block after waiting: %v", err)
		}
	}
	if entries, err := repo.GetQueue(ctx, id); err != nil || len(entries) != 0 {
		t.Fatalf("blocked participant reappeared: %+v err=%v", entries, err)
	}
}

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
		if err := repo.RemoveQueueEntry(ctx, target.scheduleID, target.opensAt, target.entryID); !errors.Is(err, ErrQueueStateChanged) {
			t.Fatalf("stale or cross-schedule deletion accepted: %+v err=%v", target, err)
		}
	}
	if entries, err := repo.GetQueue(ctx, scheduleID); err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("guard changed the current participant: %+v err=%v", entries, err)
	}
	if err := repo.RemoveQueueEntry(ctx, scheduleID, *schedule.ActiveOpensAt, entry.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 101, ScheduleID: scheduleID, Position: 1}); err != nil {
		t.Fatal(err)
	}
	replacement, err := repo.GetQueueEntryByUser(ctx, 101, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.RemoveQueueEntry(ctx, scheduleID, *schedule.ActiveOpensAt, entry.ID); !errors.Is(err, ErrQueueStateChanged) {
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
		if err := repo.RemoveQueueEntry(ctx, id, *schedule.ActiveOpensAt, entry.ID); !errors.Is(err, ErrQueueStateChanged) {
			t.Fatalf("removed participant outside current open window: %v", err)
		}
		if entries, err := repo.GetQueue(ctx, id); err != nil || len(entries) != 1 {
			t.Fatalf("closed window removal lost participant: %+v err=%v", entries, err)
		}
	}
}

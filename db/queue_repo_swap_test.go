package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSwapQueuePositions_Success(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()

	scheduleID := mustCreateTemporarySchedule(t, repo, 202601)

	err := repo.JoinQueue(ctx, QueueEntry{UserID: 10101, Username: "alice", ScheduleID: scheduleID, Position: 2})
	if err != nil {
		t.Fatalf("JoinQueue requester: %v", err)
	}

	err = repo.JoinQueue(ctx, QueueEntry{UserID: 20202, Username: "bob", ScheduleID: scheduleID, Position: 5})
	if err != nil {
		t.Fatalf("JoinQueue target: %v", err)
	}

	err = repo.SwapQueuePositions(ctx, scheduleID, 10101, 20202, 2, 5)
	if err != nil {
		t.Fatalf("SwapQueuePositions: %v", err)
	}

	requester, err := repo.GetQueueEntryByUser(ctx, 10101, scheduleID)
	if err != nil {
		t.Fatalf("GetQueueEntryByUser requester: %v", err)
	}

	target, err := repo.GetQueueEntryByUser(ctx, 20202, scheduleID)
	if err != nil {
		t.Fatalf("GetQueueEntryByUser target: %v", err)
	}

	if requester.Position != 5 {
		t.Fatalf("expected requester position 5, got %d", requester.Position)
	}

	if target.Position != 2 {
		t.Fatalf("expected target position 2, got %d", target.Position)
	}
}

func TestSwapQueuePositions_StateChanged(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()

	scheduleID := mustCreateTemporarySchedule(t, repo, 202602)

	err := repo.JoinQueue(ctx, QueueEntry{UserID: 30303, Username: "carol", ScheduleID: scheduleID, Position: 3})
	if err != nil {
		t.Fatalf("JoinQueue requester: %v", err)
	}

	err = repo.JoinQueue(ctx, QueueEntry{UserID: 40404, Username: "dave", ScheduleID: scheduleID, Position: 6})
	if err != nil {
		t.Fatalf("JoinQueue target: %v", err)
	}

	err = repo.SwapQueuePositions(ctx, scheduleID, 30303, 40404, 1, 6)
	if !errors.Is(err, ErrSwapStateChanged) {
		t.Fatalf("expected ErrSwapStateChanged, got %v", err)
	}
}

func TestGetActiveScheduleIDByThread(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()

	threadID := 202603
	scheduleID := mustCreateTemporarySchedule(t, repo, threadID)

	err := repo.SetQueueMessageID(ctx, scheduleID, 777001)
	if err != nil {
		t.Fatalf("SetQueueMessageID: %v", err)
	}

	got, err := repo.GetActiveScheduleIDByThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetActiveScheduleIDByThread: %v", err)
	}

	if got != scheduleID {
		t.Fatalf("expected schedule id %d, got %d", scheduleID, got)
	}
}

func TestGetActiveScheduleIDByThread_PicksLatestQueueMessage(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()

	threadID := 202604
	scheduleID1 := mustCreateTemporarySchedule(t, repo, threadID)
	scheduleID2 := mustCreateTemporarySchedule(t, repo, threadID)

	err := repo.SetQueueMessageID(ctx, scheduleID1, 888001)
	if err != nil {
		t.Fatalf("SetQueueMessageID first: %v", err)
	}

	err = repo.SetQueueMessageID(ctx, scheduleID2, 888002)
	if err != nil {
		t.Fatalf("SetQueueMessageID second: %v", err)
	}

	got, err := repo.GetActiveScheduleIDByThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetActiveScheduleIDByThread: %v", err)
	}

	if got != scheduleID2 {
		t.Fatalf("expected latest schedule id %d, got %d", scheduleID2, got)
	}
}

func mustCreateTemporarySchedule(t *testing.T, repo *DBRepository, threadID int) int {
	t.Helper()

	ctx := context.Background()
	schedule := Schedule{
		StartTime:   time.Date(0, 1, 1, 9, 0, 0, 0, time.UTC),
		EndTime:     time.Date(0, 1, 1, 10, 0, 0, 0, time.UTC),
		ThreadID:    threadID,
		IsTemporary: true,
	}

	scheduleID, err := repo.AddTemporarySchedule(ctx, schedule)
	if err != nil {
		t.Fatalf("AddTemporarySchedule: %v", err)
	}

	t.Cleanup(func() {
		_ = repo.DeleteScheduleEntry(context.Background(), scheduleID)
	})

	return scheduleID
}

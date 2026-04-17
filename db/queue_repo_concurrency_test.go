package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func newTestDBRepo(t *testing.T) *DBRepository {
	t.Helper()

	databaseURL := os.Getenv("QUEUEBOT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set QUEUEBOT_TEST_DATABASE_URL to run db integration tests")
	}

	pool, err := SetUpDBConn(databaseURL)
	if err != nil {
		t.Fatalf("SetUpDBConn: %v", err)
	}

	t.Cleanup(pool.Close)

	return NewDBRepo(pool)
}

func TestJoinFirstFreeSlot_ConcurrentClicksTakeNearestSlots(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()

	schedule := Schedule{
		StartTime:   time.Date(0, 1, 1, 9, 0, 0, 0, time.UTC),
		EndTime:     time.Date(0, 1, 1, 10, 30, 0, 0, time.UTC),
		ThreadID:    99999,
		IsTemporary: true,
	}

	scheduleID, err := repo.AddTemporarySchedule(ctx, schedule)
	if err != nil {
		t.Fatalf("AddTemporarySchedule: %v", err)
	}

	t.Cleanup(func() {
		_ = repo.DeleteScheduleEntry(context.Background(), scheduleID)
	})

	const totalSlots = 8
	const concurrentClicks = 40

	positionsCh := make(chan int, concurrentClicks)
	errCh := make(chan error, concurrentClicks)

	var wg sync.WaitGroup
	for i := 0; i < concurrentClicks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			userID := int64(10_000_000 + i)
			username := fmt.Sprintf("u%d", i)

			position, err := repo.JoinFirstFreeSlot(ctx, userID, username, scheduleID, totalSlots)
			if err != nil {
				errCh <- err
				return
			}

			positionsCh <- position
		}(i)
	}

	wg.Wait()
	close(positionsCh)
	close(errCh)

	successPositions := make([]int, 0, totalSlots)
	for position := range positionsCh {
		successPositions = append(successPositions, position)
	}

	noRowsErrors := 0
	otherErrors := 0
	for err := range errCh {
		if errors.Is(err, pgx.ErrNoRows) {
			noRowsErrors++
			continue
		}
		otherErrors++
	}

	if otherErrors != 0 {
		t.Fatalf("unexpected db errors count: %d", otherErrors)
	}

	if len(successPositions) != totalSlots {
		t.Fatalf("expected %d successful joins, got %d", totalSlots, len(successPositions))
	}

	if noRowsErrors != concurrentClicks-totalSlots {
		t.Fatalf("expected %d ErrNoRows errors, got %d", concurrentClicks-totalSlots, noRowsErrors)
	}

	sort.Ints(successPositions)
	for i := 0; i < totalSlots; i++ {
		want := i + 1
		if successPositions[i] != want {
			t.Fatalf("expected slot %d at index %d, got %d", want, i, successPositions[i])
		}
	}

	queue, err := repo.GetQueue(ctx, scheduleID)
	if err != nil {
		t.Fatalf("GetQueue: %v", err)
	}

	if len(queue) != totalSlots {
		t.Fatalf("expected queue length %d, got %d", totalSlots, len(queue))
	}

	for i, entry := range queue {
		want := i + 1
		if entry.Position != want {
			t.Fatalf("expected queue position %d, got %d", want, entry.Position)
		}
	}
}

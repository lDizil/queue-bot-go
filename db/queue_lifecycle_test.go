package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"queuebot/settings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
)

func TestMixedJoinMethodsCannotDuplicateUser(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	id := mustCreateTemporarySchedule(t, repo, 202610)
	const count = 32
	start := make(chan struct{})
	results := make(chan error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				results <- repo.JoinQueue(ctx, QueueEntry{UserID: 7, ScheduleID: id, Position: i + 1})
				return
			}
			_, err := repo.JoinFirstFreeSlot(ctx, 7, "test", id, count)
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful joins = %d, want 1", successes)
	}
	entries, err := repo.GetQueue(ctx, id)
	if err != nil || len(entries) != 1 {
		t.Fatalf("queue = %+v, err = %v", entries, err)
	}
}

func TestCompletionFollowsEntryAndRejectsLeaveRejoin(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	id := mustCreateTemporarySchedule(t, repo, 202611)
	for _, entry := range []QueueEntry{{UserID: 1, ScheduleID: id, Position: 1, DisplayName: "Alice"}, {UserID: 2, ScheduleID: id, Position: 2}} {
		if err := repo.JoinQueue(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	alice, err := repo.GetQueueEntryByUser(ctx, 1, id)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := repo.GetQueueEntryByUser(ctx, 2, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCompleted(ctx, id, alice.ID, 1, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SwapQueueEntries(ctx, id, alice.ID, bob.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	moved, err := repo.GetQueueEntryByUser(ctx, 1, id)
	if err != nil || !moved.Completed || moved.Position != 2 || moved.DisplayName != "Alice" {
		t.Fatalf("moved entry = %+v, err = %v", moved, err)
	}
	if err := repo.SetCompleted(ctx, id, alice.ID, 1, false); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("stale position: %v", err)
	}
	if err := repo.LeaveFromQueue(ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 1, ScheduleID: id, Position: 2}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCompleted(ctx, id, alice.ID, 2, true); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("stale identity: %v", err)
	}
	if err := repo.SwapQueueEntries(ctx, id, alice.ID, bob.ID, 2, 1); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("stale swap identity: %v", err)
	}
	rejoined, err := repo.GetQueueEntryByUser(ctx, 1, id)
	if err != nil || rejoined.Completed {
		t.Fatalf("rejoined = %+v, err = %v", rejoined, err)
	}
}

func TestQueueRunSnapshotsAndTopicIsolation(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	makeSchedule := func(chat int64) int {
		id, err := repo.AddNewScheduleEntry(ctx, Schedule{ChatID: chat, ThreadID: 999, DayOfWeek: "monday", WeekType: "odd", StartTime: time.Now(), EndTime: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.DeleteScheduleEntry(context.Background(), id) })
		return id
	}
	firstID, secondID, sameTopicID := makeSchedule(-1001), makeSchedule(-1002), makeSchedule(-1001)
	opensAt := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	closesAt := opensAt.Add(time.Hour)
	for _, id := range []int{firstID, secondID} {
		if _, err := repo.BeginQueueRun(ctx, id, opensAt, closesAt, 30, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.BeginQueueRun(ctx, sameTopicID, opensAt, closesAt, 30, 5); !errors.Is(err, ErrTopicBusy) {
		t.Fatalf("overlapping topic = %v", err)
	}
	if err := repo.SetNotified5min(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginQueueRun(ctx, firstID, opensAt, closesAt, 40, 8); err != nil {
		t.Fatal(err)
	}
	if err := repo.ChangeThreadID(ctx, firstID, 888); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetQueueMessageID(ctx, firstID, 777); err != nil {
		t.Fatal(err)
	}
	active, err := repo.GetActiveScheduleByTopic(ctx, -1001, 999)
	if err != nil || active.ID != firstID || active.ActiveCapacity != 30 || active.ActiveColumns != 5 || !active.Notified5min || active.ActiveThreadID == nil || *active.ActiveThreadID != 999 {
		t.Fatalf("snapshot = %+v, err = %v", active, err)
	}
	if _, err := repo.GetActiveScheduleByTopic(ctx, -1001, 888); !errors.Is(err, ErrActiveQueueNotFound) {
		t.Fatalf("edited topic should apply next run: %v", err)
	}
	if _, err := repo.GetActiveScheduleByMessage(ctx, -1002, 777); !errors.Is(err, ErrActiveQueueNotFound) {
		t.Fatalf("cross-group message matched: %v", err)
	}
	if err := repo.JoinQueue(ctx, QueueEntry{UserID: 1, ScheduleID: firstID, Position: 1, Completed: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishQueueRun(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	finished, err := repo.GetScheduleEntry(ctx, firstID)
	if err != nil || finished.ActiveOpensAt != nil || finished.ActiveThreadID != nil || finished.QueueMesID != nil || finished.Notified5min {
		t.Fatalf("finished = %+v, err = %v", finished, err)
	}
	entries, err := repo.GetQueue(ctx, firstID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
	if _, err := repo.BeginQueueRun(ctx, sameTopicID, opensAt, closesAt, 30, 5); err != nil {
		t.Fatalf("topic was not released: %v", err)
	}
}

func TestTemporaryRunPersistsAbsoluteDatesAndDeletesOnFinish(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	opensAt := time.Date(2026, 12, 31, 23, 58, 0, 0, time.UTC)
	closesAt := opensAt.Add(90 * time.Minute)
	schedule := Schedule{ChatID: -1999, ThreadID: 202612, Title: "Group", StartTime: opensAt, EndTime: closesAt, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, ActiveCapacity: 30, ActiveColumns: 5}
	id, err := repo.AddTemporarySchedule(ctx, schedule)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.DeleteScheduleEntry(context.Background(), id) })
	restored, err := repo.GetScheduleEntry(ctx, id)
	if err != nil || restored.ActiveOpensAt == nil || !restored.ActiveOpensAt.Equal(opensAt) || !restored.ActiveClosesAt.Equal(closesAt) || restored.Title != "Group" {
		t.Fatalf("restored = %+v, err = %v", restored, err)
	}
	startup, err := repo.GetStartupSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range startup {
		if row.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("temporary run missing from startup")
	}
	if err := repo.FinishQueueRun(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetScheduleEntry(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("temporary row not removed: %v", err)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	old, found, err := repo.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if found {
			_ = repo.SaveSettings(context.Background(), old)
		} else {
			_, _ = repo.pool.Exec(context.Background(), "DELETE FROM runtime_settings")
		}
	})
	values := settings.Defaults()
	values.Week1Date = "2026-09-01"
	values.Week1Type = "even"
	if err := repo.SaveSettings(ctx, values); err != nil {
		t.Fatal(err)
	}
	got, found, err := repo.LoadSettings(ctx)
	if err != nil || !found || got != values {
		t.Fatalf("settings = %+v, found = %v, err = %v", got, found, err)
	}
}

func TestBeginRunCannotDiscardPublishedLegacyQueue(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	id := mustCreateTemporarySchedule(t, repo, 202613)
	if err := repo.JoinQueue(ctx, QueueEntry{ScheduleID: id, UserID: 99, Position: 1, Completed: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetQueueMessageID(ctx, id, 789); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetNotified5min(ctx, id); err != nil {
		t.Fatal(err)
	}
	opensAt := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	closesAt := opensAt.Add(time.Hour)
	if _, err := repo.BeginQueueRun(ctx, id, opensAt, closesAt, 30, 5); !errors.Is(err, ErrQueueStateChanged) {
		t.Fatalf("legacy queue overwritten: %v", err)
	}
	restored, err := repo.SetLegacyQueueRunBounds(ctx, id, opensAt, closesAt, 30, 5)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.GetQueue(ctx, id)
	if err != nil || len(entries) != 1 || !entries[0].Completed || restored.QueueMesID == nil || *restored.QueueMesID != 789 || !restored.Notified5min {
		t.Fatalf("legacy data lost: row=%+v entries=%+v err=%v", restored, entries, err)
	}
}

func TestSetQueueMessageIDRejectsDeletedSchedule(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	id := mustCreateTemporarySchedule(t, repo, 202614)
	if err := repo.DeleteScheduleEntry(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetQueueMessageID(ctx, id, 123); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing schedule accepted message: %v", err)
	}
}

func TestLifecycleMigrationPreservesLegacyDuplicatesAndRollsBack(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx := context.Background()
	schema := fmt.Sprintf("queuebot_migration_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := repo.pool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = repo.pool.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE") })
	databaseURL, err := url.Parse(os.Getenv("QUEUEBOT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := databaseURL.Query()
	query.Set("search_path", schema)
	databaseURL.RawQuery = query.Encode()
	pool, err := SetUpDBConn(databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations, err := migrate.New("file://../migrations", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = migrations.Close() })
	if err := migrations.Steps(7); err != nil {
		t.Fatal(err)
	}
	var scheduleID int
	if err := pool.QueryRow(ctx, `INSERT INTO schedules (day_of_week, week_type, start_time, end_time, thread_id)
  VALUES ('monday', 'odd', '10:00', '11:00', 123) RETURNING id`).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO queue_entries (user_id, schedule_id, position) VALUES (99, $1, 1), (99, $1, 2)`, scheduleID); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Up(); err != nil {
		t.Fatal(err)
	}
	checkLegacyRows := func() {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM queue_entries WHERE schedule_id = $1 AND user_id = 99", scheduleID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("legacy rows = %d, err = %v", count, err)
		}
	}
	checkLegacyRows()
	var uniqueIndex bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname = $1 AND indexname = 'queue_entries_schedule_user_unique')", schema).Scan(&uniqueIndex); err != nil {
		t.Fatal(err)
	}
	if uniqueIndex {
		t.Fatal("legacy duplicates should be preserved without a failing unique index")
	}
	if err := migrations.Steps(-1); err != nil {
		t.Fatalf("rollback without optional index: %v", err)
	}
	checkLegacyRows()
	if err := migrations.Up(); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	checkLegacyRows()
	legacyRepo := NewDBRepo(pool)
	if err := legacyRepo.JoinQueue(ctx, QueueEntry{ScheduleID: scheduleID, UserID: 99, Position: 3}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("new duplicate allowed in legacy database: %v", err)
	}
}

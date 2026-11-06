package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"queuebot/chats"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
)

func TestChatMigrationAndBootstrapPreserveLegacyRunsAndParticipants(t *testing.T) {
	repo, migration := newChatTestRepository(t, 8)
	ctx := t.Context()
	opensAt := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	closesAt := opensAt.Add(time.Hour)
	var ids []int
	for index, chatID := range []int64{-1001, 0, -2002} {
		row := Schedule{ChatID: chatID, ThreadID: 7 + index, DayOfWeek: "monday", WeekType: "odd", StartTime: opensAt, EndTime: closesAt}
		var (
			id  int
			err error
		)
		if index == 2 {
			row.ActiveOpensAt, row.ActiveClosesAt = &opensAt, &closesAt
			row.ActiveCapacity, row.ActiveColumns = 30, 5
			id, err = repo.AddTemporarySchedule(ctx, row)
		} else {
			id, err = repo.AddNewScheduleEntry(ctx, row)
			if err == nil {
				_, err = repo.pool.Exec(ctx, `UPDATE schedules SET active_opens_at = $2, active_closes_at = $3,
 active_thread_id = thread_id, active_capacity = 30, active_columns = 5 WHERE id = $1`, id, opensAt, closesAt)
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.SetQueueMessageID(ctx, id, 100+index); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.pool.Exec(ctx, "INSERT INTO queue_entries (schedule_id, user_id, position, completed) VALUES ($1, $2, 1, true)", id, int64(1000+index)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	before := make(map[int]Schedule)
	participants := make(map[int][]QueueEntry)
	for _, id := range ids {
		var err error
		before[id], err = repo.GetScheduleEntry(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		participants[id], err = repo.GetQueue(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertRunsPreserved := func() {
		t.Helper()
		for _, id := range ids {
			after, err := repo.GetScheduleEntry(ctx, id)
			if err != nil || !reflect.DeepEqual(after, before[id]) {
				t.Fatalf("chat metadata changed an existing run: before=%+v after=%+v err=%v", before[id], after, err)
			}
			entries, err := repo.GetQueue(ctx, id)
			if err != nil || !reflect.DeepEqual(entries, participants[id]) {
				t.Fatalf("chat migration changed participants: before=%+v after=%+v err=%v", participants[id], entries, err)
			}
		}
	}
	if err := migration.Migrate(9); err != nil {
		t.Fatal(err)
	}
	assertRunsPreserved()
	service := chats.NewService(repo, nil)
	if err := service.Initialize(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Default(ctx); !errors.Is(err, chats.ErrDefaultNotSet) {
		t.Fatalf("multiple legacy destinations were collapsed into a guessed default: %v", err)
	}
	if unassigned, err := service.Get(ctx, 0); err != nil || unassigned.ID != 0 || unassigned.IsDefault {
		t.Fatalf("unassigned legacy schedules disappeared: %+v err=%v", unassigned, err)
	}
	if err := repo.BackfillChatIDs(ctx, -1001); err != nil {
		t.Fatal(err)
	}
	assigned := before[ids[1]]
	assigned.ChatID = -1001
	before[ids[1]] = assigned
	if err := service.Initialize(ctx, -1001); err != nil {
		t.Fatal(err)
	}
	if selected, err := service.Default(ctx); err != nil || selected.ID != -1001 || selected.Name != "" {
		t.Fatalf("legacy bootstrap did not select the old environment destination: %+v err=%v", selected, err)
	}
	if _, err := service.SetDefault(ctx, -2002); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rename(ctx, -2002, "Local alias"); err != nil {
		t.Fatal(err)
	}
	restarted := chats.NewService(repo, nil)
	if err := restarted.Initialize(ctx, -1001); err != nil {
		t.Fatal(err)
	}
	if selected, err := restarted.Default(ctx); err != nil || selected.ID != -2002 || selected.Name != "Local alias" {
		t.Fatalf("restart restored the obsolete environment default: %+v err=%v", selected, err)
	}
	assertRunsPreserved()
	if err := migration.Steps(-1); err != nil {
		t.Fatal(err)
	}
	assertRunsPreserved()
	if err := migration.Migrate(9); err != nil {
		t.Fatal(err)
	}
	assertRunsPreserved()
}

func TestLegacyEnvironmentDefaultWinsOverOneKnownSchedule(t *testing.T) {
	repo, migration := newChatTestRepository(t, 8)
	ctx := t.Context()
	id, err := repo.AddNewScheduleEntry(ctx, Schedule{ChatID: -1001, ThreadID: 7, DayOfWeek: "monday", WeekType: "odd", StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Migrate(9); err != nil {
		t.Fatal(err)
	}
	service := chats.NewService(repo, nil)
	if err := service.Initialize(ctx, -2002); err != nil {
		t.Fatal(err)
	}
	if selected, err := service.Default(ctx); err != nil || selected.ID != -2002 {
		t.Fatalf("the sole known schedule overrode the old private-command destination: %+v err=%v", selected, err)
	}
	if row, err := repo.GetScheduleEntry(ctx, id); err != nil || row.ChatID != -1001 {
		t.Fatalf("bootstrap moved an existing schedule: %+v err=%v", row, err)
	}
}

func TestChatDirectoryIncludesWritersWithoutMetadataAndUnassignedSchedules(t *testing.T) {
	repo, _ := newChatTestRepository(t, 9)
	ctx := t.Context()
	for _, chatID := range []int64{-1001, 0} {
		if _, err := repo.AddNewScheduleEntry(ctx, Schedule{ChatID: chatID, ThreadID: 7, DayOfWeek: "monday", WeekType: "odd", StartTime: time.Now(), EndTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.AddTemporarySchedule(ctx, Schedule{ChatID: -2002, ThreadID: 7, StartTime: time.Now(), EndTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureChat(ctx, -3003, "Empty group"); err != nil {
		t.Fatal(err)
	}
	known, err := repo.ListChats(ctx)
	if err != nil || len(known) != 4 {
		t.Fatalf("directory hid metadata-free, temporary, unassigned or empty groups: %+v err=%v", known, err)
	}
	for _, id := range []int64{-1001, -2002, -3003, 0} {
		chat, err := repo.GetChat(ctx, id)
		if err != nil || chat.ID != id {
			t.Fatalf("known chat is missing: id=%d chat=%+v err=%v", id, chat, err)
		}
	}
	service := chats.NewService(repo, nil)
	if err := service.Remember(ctx, -1001, "Observed title"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rename(ctx, -1001, "Manual alias"); err != nil {
		t.Fatal(err)
	}
	if err := service.Remember(ctx, -1001, "Changed Telegram title"); err != nil {
		t.Fatal(err)
	}
	if chat, err := service.Get(ctx, -1001); err != nil || chat.Name != "Manual alias" {
		t.Fatalf("automatic metadata replaced a manual alias: %+v err=%v", chat, err)
	}
}

func TestChatMigrationPreservesPositiveLegacyDestination(t *testing.T) {
	repo, migration := newChatTestRepository(t, 8)
	ctx := t.Context()
	id, err := repo.AddNewScheduleEntry(ctx, Schedule{ChatID: 123, ThreadID: 0, DayOfWeek: "monday", WeekType: "odd", StartTime: time.Now(), EndTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Migrate(9); err != nil {
		t.Fatal(err)
	}
	service := chats.NewService(repo, nil)
	if err := service.Initialize(ctx, 123); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rename(ctx, 123, "Legacy private"); err != nil {
		t.Fatal(err)
	}
	if selected, err := service.Default(ctx); err != nil || selected.ID != 123 || selected.Name != "Legacy private" {
		t.Fatalf("positive legacy destination disappeared: %+v err=%v", selected, err)
	}
	if row, err := repo.GetScheduleEntry(ctx, id); err != nil || row.ChatID != 123 {
		t.Fatalf("metadata migration changed the old private destination: %+v err=%v", row, err)
	}
}

func TestDefaultSelectionIsAtomicAndBootstrapCannotReplaceManualChoice(t *testing.T) {
	repo, _ := newChatTestRepository(t, 9)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for _, id := range []int64{-1001, -2002} {
		if err := repo.EnsureChat(ctx, id, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SetDefaultChat(ctx, -1001, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetDefaultChat(ctx, -9999, false); !errors.Is(err, chats.ErrNotFound) {
		t.Fatalf("missing destination did not fail: %v", err)
	}
	service := chats.NewService(repo, nil)
	if selected, err := service.Default(ctx); err != nil || selected.ID != -1001 {
		t.Fatalf("failed default change did not roll back the previous selection: %+v err=%v", selected, err)
	}
	var (
		start   = make(chan struct{})
		results = make(chan error, 33)
		writers sync.WaitGroup
	)
	for range 32 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			results <- repo.SetDefaultChat(ctx, -1001, true)
		}()
	}
	writers.Add(1)
	go func() {
		defer writers.Done()
		<-start
		results <- repo.SetDefaultChat(ctx, -2002, false)
	}()
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	monitorDone := make(chan struct{})
	monitorErrors := make(chan error, 1)
	go func() {
		defer close(monitorDone)
		for monitorCtx.Err() == nil {
			known, err := repo.ListChats(monitorCtx)
			if err != nil {
				if monitorCtx.Err() == nil {
					monitorErrors <- err
				}
				return
			}
			var defaults int
			for _, chat := range known {
				if chat.IsDefault {
					defaults++
				}
			}
			if defaults != 1 {
				monitorErrors <- fmt.Errorf("observed %d defaults during an atomic change", defaults)
				return
			}
		}
	}()
	close(start)
	writers.Wait()
	close(results)
	stopMonitor()
	<-monitorDone
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-monitorErrors:
		t.Fatal(err)
	default:
	}
	if selected, err := service.Default(ctx); err != nil || selected.ID != -2002 {
		t.Fatalf("concurrent bootstrap replaced the manual choice: %+v err=%v", selected, err)
	}
}

func newChatTestRepository(t *testing.T, version int) (*DBRepository, *migrate.Migrate) {
	t.Helper()
	databaseURL := os.Getenv("QUEUEBOT_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("QUEUEBOT_REQUIRE_DATABASE") == "1" {
			t.Fatal("QUEUEBOT_TEST_DATABASE_URL is required in integration CI")
		}
		t.Skip("set QUEUEBOT_TEST_DATABASE_URL to run chat integration tests")
	}
	admin, err := SetUpDBConn(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("queuebot_chats_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	migration, err := migrate.New("file://../migrations", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = migration.Close() })
	if err := migration.Steps(version); err != nil {
		t.Fatal(err)
	}
	pool, err := SetUpDBConn(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewDBRepo(pool), migration
}

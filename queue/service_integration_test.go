package queue_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestServiceConcurrentJoinKeepsOnePlacePerParticipant(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	const clicks = 16
	var workers sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, clicks)
	for position := 1; position <= clicks; position++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := fixture.service.Join(fixture.ctx, ref, participant(101), position)
			failures <- err
		}()
	}
	close(start)
	workers.Wait()
	close(failures)
	var joined, alreadyJoined int
	for err := range failures {
		switch {
		case err == nil:
			joined++
		case errors.Is(err, queue.ErrAlreadyJoined):
			alreadyJoined++
		default:
			t.Fatalf("unexpected concurrent join error: %v", err)
		}
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if joined != 1 || alreadyJoined != clicks-1 || len(entries) != 1 || entries[0].UserID != 101 {
		t.Fatalf("participant reserved multiple places: joined=%d duplicates=%d entries=%+v", joined, alreadyJoined, entries)
	}
}

func TestServiceCompletionChecksOwnershipAndDeduplicatesCallbacks(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	alice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(303), alice.ID, alice.Position, nil, "forbidden"); !errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("another participant could mark Alice: %v", err)
	}
	for range 2 {
		completed, err := fixture.service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "same-callback")
		if err != nil || !completed {
			t.Fatalf("repeated callback undid completion: completed=%t err=%v", completed, err)
		}
	}
	administrator := queue.Actor{ID: 999, Admin: true}
	if completed, err := fixture.service.Complete(fixture.ctx, ref, administrator, bob.ID, bob.Position, nil, "admin-mark"); err != nil || !completed {
		t.Fatalf("administrator could not mark Bob: completed=%t err=%v", completed, err)
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if len(entries) != 2 || !entries[0].Completed || !entries[1].Completed || entries[0].Position != 1 || entries[1].Position != 2 {
		t.Fatalf("completion changed queue ownership or positions: %+v", entries)
	}
}

func TestServiceSwapKeepsCompletionWithPersonAndRejectsOldPlaceButton(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	alice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "alice-done"); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Swap(fixture.ctx, ref, alice.ID, bob.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	administrator := queue.Actor{ID: 999, Admin: true}
	if _, err := fixture.service.Complete(fixture.ctx, ref, administrator, alice.ID, 1, nil, "old-place-button"); !errors.Is(err, queue.ErrChanged) {
		t.Fatalf("old place button was accepted after swap: %v", err)
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if len(entries) != 2 || entries[0].UserID != 202 || entries[0].Completed || entries[1].UserID != 101 || !entries[1].Completed {
		t.Fatalf("swap moved completion to another participant: %+v", entries)
	}
}

func TestServiceLeaveAndRejoinInvalidatesPreviousSwapAndCompletion(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	oldAlice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	if err := fixture.service.Leave(fixture.ctx, ref, participant(101)); err != nil {
		t.Fatal(err)
	}
	newAlice := fixture.join(t, ref, 101, 1)
	if newAlice.ID == oldAlice.ID {
		t.Fatal("rejoin reused the previous queue entry identity")
	}
	if err := fixture.service.Swap(fixture.ctx, ref, oldAlice.ID, bob.ID, 1, 2); !errors.Is(err, db.ErrQueueStateChanged) {
		t.Fatalf("old swap remained valid after rejoin: %v", err)
	}
	administrator := queue.Actor{ID: 999, Admin: true}
	if _, err := fixture.service.Complete(fixture.ctx, ref, administrator, oldAlice.ID, 1, nil, "old-entry"); !errors.Is(err, queue.ErrChanged) {
		t.Fatalf("old completion button remained valid after rejoin: %v", err)
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if len(entries) != 2 || entries[0].ID != newAlice.ID || entries[0].Position != 1 || entries[0].Completed || entries[1].ID != bob.ID || entries[1].Position != 2 {
		t.Fatalf("obsolete actions changed the rejoined participant: %+v", entries)
	}
}

func TestServiceRepostPersistsNewMessageAndRejectsOldButtons(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.join(t, ref, 101, 1)
	if err := fixture.service.Repost(fixture.ctx, ref); err != nil {
		t.Fatal(err)
	}
	retired := fixture.messenger.await(t, func(event delivery) bool { return event.Kind == "retire" })
	if retired.ChatID != ref.ChatID || retired.MessageID != ref.MessageID {
		t.Fatalf("wrong message retired: %+v", retired)
	}
	schedule, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.QueueMesID == nil || *schedule.QueueMesID == ref.MessageID {
		t.Fatal("repost did not persist the replacement message ID")
	}
	fixture.service.Stop()
	restored := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, fixture.messenger)
	t.Cleanup(restored.Stop)
	active, err := restored.ActiveTopic(fixture.ctx, ref.ChatID, 7)
	if err != nil || active.QueueMesID == nil || *active.QueueMesID != *schedule.QueueMesID {
		t.Fatalf("restart did not restore reposted message: schedule=%+v err=%v", active, err)
	}
	if _, err := restored.Join(fixture.ctx, ref, participant(202), 2); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("old message was accepted after repost: %v", err)
	}
	ref.MessageID = *schedule.QueueMesID
	if _, err := restored.Join(fixture.ctx, ref, participant(202), 2); err != nil {
		t.Fatalf("replacement message was rejected: %v", err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 2 {
		t.Fatalf("repost or restart lost participants: %+v", entries)
	}
}

func TestServiceClosePublishesFinalParticipantsBeforeClearingAndCannotReopen(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.join(t, ref, 101, 1)
	gate := fixture.messenger.block(t, ref.ScheduleID, utils.QueueClosed)
	finished := make(chan error, 1)
	go func() { finished <- fixture.service.Close(fixture.ctx, ref.ScheduleID) }()
	final := awaitChannel(t, gate.started, "closed message to begin rendering")
	if len(final.Snapshot.Entries) != 1 || final.Snapshot.Entries[0].UserID != 101 {
		t.Fatalf("closing message lost the last participants: %+v", final.Snapshot)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 {
		t.Fatalf("database was cleared before final message was delivered: %+v", entries)
	}
	gate.unblock()
	if err := awaitChannel(t, finished, "queue close"); err != nil {
		t.Fatal(err)
	}
	fixture.observed.drain()
	fixture.service.Changed(ref.ScheduleID)
	awaitChannel(t, fixture.observed.reads, "late asynchronous refresh")
	if err := fixture.service.Refresh(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 0 {
		t.Fatalf("closed queue retained active participants: %+v", entries)
	}
	schedule, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if schedule.ActiveOpensAt != nil || schedule.ActiveClosesAt != nil || schedule.QueueMesID != nil {
		t.Fatalf("closed queue remained active in database: %+v", schedule)
	}
	seenClosed := false
	for _, event := range fixture.messenger.history() {
		if event.Kind != "send" && event.Kind != "edit" {
			continue
		}
		if event.Snapshot.Status == utils.QueueClosed {
			seenClosed = true
		} else if seenClosed {
			t.Fatalf("late asynchronous update reopened the closed message: %+v", event.Snapshot)
		}
	}
	if !seenClosed {
		t.Fatal("closed message was never delivered")
	}
}

func TestServiceRetriesTransientMessageEditWithoutLosingCommittedJoin(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.messenger.failEdits(ref.ScheduleID, 1)
	fixture.join(t, ref, 101, 1)
	failed := awaitChannel(t, fixture.messenger.failures, "first failed edit")
	if failed.Snapshot.Schedule.ID != ref.ScheduleID {
		t.Fatalf("failure belongs to another queue: %+v", failed)
	}
	updated := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.Snapshot.Schedule.ID == ref.ScheduleID && len(event.Snapshot.Entries) == 1
	})
	if updated.Snapshot.Entries[0].UserID != 101 || updated.Snapshot.Status != utils.QueueOpen {
		t.Fatalf("retry delivered incorrect state: %+v", updated.Snapshot)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 1 || entries[0].UserID != 101 {
		t.Fatalf("transient Telegram failure lost committed join: %+v", entries)
	}
}

func TestServiceBlockedQueueDoesNotStopOtherTopicsOrGroups(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.openQueue(t, -100, 7)
	otherTopic := fixture.openQueue(t, -100, 8)
	otherGroup := fixture.openQueue(t, -200, 7)
	gate := fixture.messenger.block(t, first.ScheduleID, utils.QueueOpen)
	fixture.join(t, first, 101, 1)
	awaitChannel(t, gate.started, "blocked queue update")
	fixture.join(t, otherTopic, 202, 1)
	fixture.join(t, otherGroup, 303, 1)
	seen := make(map[int]bool)
	for len(seen) < 2 {
		event := fixture.messenger.await(t, func(event delivery) bool {
			return event.Kind == "edit" && (event.Snapshot.Schedule.ID == otherTopic.ScheduleID || event.Snapshot.Schedule.ID == otherGroup.ScheduleID)
		})
		if len(event.Snapshot.Entries) != 1 {
			t.Fatalf("independent queue update has no participant: %+v", event.Snapshot)
		}
		seen[event.Snapshot.Schedule.ID] = true
	}
	gate.unblock()
	fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.Snapshot.Schedule.ID == first.ScheduleID
	})
}

func TestServiceSettingsApplyToNewRunsAndTopicCannotOpenTwice(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.openQueue(t, -100, 7)
	if err := fixture.settings.Update(fixture.ctx, func(values *settings.Values) error {
		values.TotalSlotsInQueue = 2
		values.AmountOfButtonsInRow = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := fixture.openQueue(t, -100, 8)
	if _, err := fixture.service.Join(fixture.ctx, first, participant(101), 30); err != nil {
		t.Fatalf("existing run lost its original capacity: %v", err)
	}
	if _, err := fixture.service.Join(fixture.ctx, second, participant(202), 3); err == nil {
		t.Fatal("new run ignored updated capacity")
	}
	active, err := fixture.repository.GetScheduleEntry(fixture.ctx, second.ScheduleID)
	if err != nil || active.ActiveCapacity != 2 || active.ActiveColumns != 1 {
		t.Fatalf("new run did not snapshot updated settings: %+v, err=%v", active, err)
	}
	duplicate := fixture.addSchedule(t, -100, 7)
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, duplicate, now.Add(-time.Minute), now.Add(time.Hour)); !errors.Is(err, db.ErrTopicBusy) {
		t.Fatalf("a second queue was allowed in the same topic: %v", err)
	}
}

func TestServiceDeleteSerializesConcurrentPrepareAndRefresh(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.join(t, ref, 101, 1)
	fixture.service.Stop()
	guarded := &deleteBarrierStore{
		observedStore: fixture.observed,
		deleting:      make(chan struct{}), release: make(chan struct{}), preparing: make(chan struct{}, 1),
	}
	service := queue.NewService(fixture.ctx, guarded, fixture.settings, fixture.messenger)
	t.Cleanup(service.Stop)
	t.Cleanup(guarded.unblock)
	deleted := make(chan error, 1)
	go func() { deleted <- service.Delete(fixture.ctx, ref.ScheduleID) }()
	awaitChannel(t, guarded.deleting, "delete after closing the active run")

	competing := make(chan [2]error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		now := time.Now()
		prepareErr := service.Prepare(fixture.ctx, ref.ScheduleID, now.Add(-time.Minute), now.Add(time.Hour))
		refreshErr := service.Refresh(fixture.ctx, ref.ScheduleID)
		competing <- [2]error{prepareErr, refreshErr}
	}()
	awaitChannel(t, started, "concurrent schedule restart")
	select {
	case <-guarded.preparing:
		t.Error("another run entered storage between close and delete")
	case <-time.After(100 * time.Millisecond):
	}
	guarded.unblock()
	if err := awaitChannel(t, deleted, "atomic queue deletion"); err != nil {
		t.Fatal(err)
	}
	outcomes := awaitChannel(t, competing, "concurrent restart after deletion")
	if outcomes[0] == nil || outcomes[1] == nil {
		t.Fatalf("deleted schedule could be restarted or refreshed: %v", outcomes)
	}
	if _, err := fixture.repository.GetScheduleEntry(fixture.ctx, ref.ScheduleID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted schedule still exists: %v", err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 0 {
		t.Fatalf("deleted schedule retained participants: %+v", entries)
	}
	var sends int
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("delete left a newly published orphan message: %d published messages", sends)
	}
}

func TestServiceRecoversDeletedMessageWithoutLosingEntriesOrCompletion(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	alice := fixture.join(t, ref, 101, 1)
	bob := fixture.join(t, ref, 202, 2)
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "complete-before-message-deletion"); err != nil {
		t.Fatal(err)
	}
	fixture.service.Stop()
	messenger := &missingMessageMessenger{Messenger: fixture.messenger, messageID: ref.MessageID, missing: true}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	if err := service.RecoverTopic(fixture.ctx, ref.ChatID, 7); err != nil {
		t.Fatalf("recover deleted queue message: %v", err)
	}
	replacement := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "send" && event.MessageID != ref.MessageID
	})
	if len(replacement.Snapshot.Entries) != 2 || replacement.Snapshot.Entries[0].ID != alice.ID || !replacement.Snapshot.Entries[0].Completed || replacement.Snapshot.Entries[1].ID != bob.ID || replacement.Snapshot.Entries[1].Completed {
		t.Fatalf("replacement message lost queue or completion: %+v", replacement.Snapshot.Entries)
	}
	active, err := service.ActiveTopic(fixture.ctx, ref.ChatID, 7)
	if err != nil || active.QueueMesID == nil || *active.QueueMesID != replacement.MessageID {
		t.Fatalf("replacement message ID was not persisted: %+v, err=%v", active, err)
	}
	if _, err := service.Join(fixture.ctx, ref, participant(303), 3); !errors.Is(err, queue.ErrStale) {
		t.Fatalf("old reference remained active after recovery: %v", err)
	}
	current := ref
	current.MessageID = replacement.MessageID
	if _, err := service.Join(fixture.ctx, current, participant(303), 3); err != nil {
		t.Fatalf("replacement message cannot accept participants: %v", err)
	}
	if err := service.RecoverTopic(fixture.ctx, ref.ChatID, 7); err != nil {
		t.Fatalf("repeat recovery of an existing message: %v", err)
	}
	entries := fixture.entries(t, ref.ScheduleID)
	if len(entries) != 3 || entries[0].ID != alice.ID || !entries[0].Completed || entries[1].ID != bob.ID {
		t.Fatalf("recovery changed participant identities: %+v", entries)
	}
	var sends int
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("repeat recovery published an unnecessary message: %d sends", sends)
	}
}

func TestServiceClosesDeletedMessageWithFinalListAndReleasesTopic(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	alice := fixture.join(t, ref, 101, 1)
	if _, err := fixture.service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "complete-before-closing-deleted-message"); err != nil {
		t.Fatal(err)
	}
	fixture.service.Stop()
	messenger := &missingMessageMessenger{Messenger: fixture.messenger, messageID: ref.MessageID, missing: true}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	if err := service.Close(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatalf("close deleted queue message: %v", err)
	}
	final := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "send" && event.MessageID != ref.MessageID
	})
	if final.Snapshot.Status != utils.QueueClosed || len(final.Snapshot.Entries) != 1 || final.Snapshot.Entries[0].ID != alice.ID || !final.Snapshot.Entries[0].Completed {
		t.Fatalf("replacement final message lost participants or completion: %+v", final.Snapshot)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 0 {
		t.Fatalf("recovered final message prevented cleanup: %+v", entries)
	}
	if _, err := service.ActiveTopic(fixture.ctx, ref.ChatID, 7); !errors.Is(err, db.ErrActiveQueueNotFound) {
		t.Fatalf("deleted-message closure retained the active topic: %v", err)
	}
	fixture.observed.drain()
	service.Changed(ref.ScheduleID)
	awaitChannel(t, fixture.observed.reads, "late refresh after closing deleted message")
	if err := service.Refresh(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	var sends int
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" && event.Snapshot.Schedule.ID == ref.ScheduleID {
			sends++
		}
	}
	if sends != 2 {
		t.Fatalf("late update republished the closed queue: %d sends", sends)
	}
	next := fixture.addSchedule(t, ref.ChatID, 7)
	now := time.Now()
	if err := service.Prepare(fixture.ctx, next, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatalf("closed deleted message kept the topic busy: %v", err)
	}
}

type serviceFixture struct {
	ctx        context.Context
	repository *db.DBRepository
	observed   *observedStore
	settings   *settings.Manager
	messenger  *httpMessenger
	service    *queue.Service
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	repository := isolatedRepository(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	seed := settings.Defaults()
	seed.DelayUpdateQueue = 50 * time.Millisecond
	manager, err := settings.NewManager(ctx, repository, seed)
	if err != nil {
		t.Fatal(err)
	}
	messenger := newHTTPMessenger(t)
	observed := &observedStore{DBRepository: repository, reads: make(chan int, 64)}
	service := queue.NewService(ctx, observed, manager, messenger)
	t.Cleanup(service.Stop)
	return &serviceFixture{ctx: ctx, repository: repository, observed: observed, settings: manager, messenger: messenger, service: service}
}

func isolatedRepository(t *testing.T) *db.DBRepository {
	t.Helper()
	databaseURL := os.Getenv("QUEUEBOT_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("QUEUEBOT_REQUIRE_DATABASE") == "1" {
			t.Fatal("QUEUEBOT_TEST_DATABASE_URL is required in integration CI")
		}
		t.Skip("set QUEUEBOT_TEST_DATABASE_URL to run queue integration tests")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("invalid QUEUEBOT_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect integration database: %s", redactDatabaseError(err, parsed))
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "queue_service_" + hex.EncodeToString(random[:])
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated integration schema: %s", redactDatabaseError(err, parsed))
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop owned integration schema: %s", redactDatabaseError(err, parsed))
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	migration, err := migrate.New("file://../migrations", parsed.String())
	if err != nil {
		t.Fatalf("initialize isolated migrations: %s", redactDatabaseError(err, parsed))
	}
	err = migration.Up()
	sourceErr, databaseErr := migration.Close()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate isolated schema: %s", redactDatabaseError(err, parsed))
	}
	if sourceErr != nil || databaseErr != nil {
		t.Fatal("close isolated migrations")
	}
	poolConfig, err := pgxpool.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal("parse isolated database configuration")
	}
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect isolated database: %s", redactDatabaseError(err, parsed))
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated database: %s", redactDatabaseError(err, parsed))
	}
	return db.NewDBRepo(pool)
}

func redactDatabaseError(err error, databaseURL *url.URL) string {
	text := strings.ReplaceAll(err.Error(), databaseURL.String(), "<test database>")
	if databaseURL.User != nil {
		if password, exists := databaseURL.User.Password(); exists && password != "" {
			text = strings.ReplaceAll(text, password, "***")
		}
	}
	return text
}

func (fixture *serviceFixture) addSchedule(t *testing.T, chatID int64, threadID int) int {
	t.Helper()
	schedule := db.Schedule{
		ChatID: chatID, ThreadID: threadID, Title: "Integration queue",
		DayOfWeek: "monday", WeekType: "odd",
		StartTime: time.Date(0, 1, 1, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(0, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	id, err := fixture.repository.AddNewScheduleEntry(fixture.ctx, schedule)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (fixture *serviceFixture) openQueue(t *testing.T, chatID int64, threadID int) queue.Reference {
	t.Helper()
	id := fixture.addSchedule(t, chatID, threadID)
	now := time.Now()
	if err := fixture.service.Prepare(fixture.ctx, id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.Refresh(fixture.ctx, id); err != nil {
		t.Fatal(err)
	}
	schedule, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || schedule.QueueMesID == nil {
		t.Fatalf("queue did not publish a message: err=%v", err)
	}
	return queue.Reference{ScheduleID: id, ChatID: chatID, MessageID: *schedule.QueueMesID}
}

func (fixture *serviceFixture) join(t *testing.T, ref queue.Reference, userID int64, position int) db.QueueEntry {
	t.Helper()
	if _, err := fixture.service.Join(fixture.ctx, ref, participant(userID), position); err != nil {
		t.Fatal(err)
	}
	entry, err := fixture.repository.GetQueueEntryByUser(fixture.ctx, userID, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func (fixture *serviceFixture) entries(t *testing.T, scheduleID int) []db.QueueEntry {
	t.Helper()
	entries, err := fixture.repository.GetQueue(fixture.ctx, scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func participant(id int64) queue.Actor {
	return queue.Actor{ID: id, Username: fmt.Sprintf("participant%d", id), DisplayName: fmt.Sprintf("Participant %d", id)}
}

type observedStore struct {
	*db.DBRepository
	reads chan int
}

func (store *observedStore) GetScheduleEntry(ctx context.Context, scheduleID int) (db.Schedule, error) {
	schedule, err := store.DBRepository.GetScheduleEntry(ctx, scheduleID)
	select {
	case store.reads <- scheduleID:
	default:
	}
	return schedule, err
}

func (store *observedStore) drain() {
	for {
		select {
		case <-store.reads:
		default:
			return
		}
	}
}

type delivery struct {
	Kind      string
	Snapshot  queue.Snapshot
	ChatID    int64
	MessageID int
	Text      string
}

type editGate struct {
	scheduleID int
	status     utils.QueueStatus
	started    chan delivery
	release    chan struct{}
	once       sync.Once
}

func (gate *editGate) unblock() {
	gate.once.Do(func() { close(gate.release) })
}

type httpMessenger struct {
	server         *httptest.Server
	mu             sync.Mutex
	nextMessageID  int
	delivered      []delivery
	events         chan delivery
	failures       chan delivery
	failBySchedule map[int]int
	gates          map[int]*editGate
}

var _ queue.Messenger = (*httpMessenger)(nil)

func newHTTPMessenger(t *testing.T) *httpMessenger {
	t.Helper()
	messenger := &httpMessenger{
		nextMessageID: 1000,
		events:        make(chan delivery, 128), failures: make(chan delivery, 128),
		failBySchedule: make(map[int]int), gates: make(map[int]*editGate),
	}
	messenger.server = httptest.NewServer(http.HandlerFunc(messenger.handle))
	t.Cleanup(messenger.server.Close)
	return messenger
}

func (messenger *httpMessenger) handle(writer http.ResponseWriter, request *http.Request) {
	var event delivery
	if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
		http.Error(writer, "invalid test request", http.StatusBadRequest)
		return
	}
	messenger.mu.Lock()
	gate := messenger.gates[event.Snapshot.Schedule.ID]
	failed := event.Kind == "edit" && messenger.failBySchedule[event.Snapshot.Schedule.ID] > 0
	if failed {
		messenger.failBySchedule[event.Snapshot.Schedule.ID]--
	}
	messenger.mu.Unlock()
	if failed {
		messenger.failures <- event
		http.Error(writer, "temporary transport failure", http.StatusServiceUnavailable)
		return
	}
	if event.Kind == "edit" && gate != nil && gate.status == event.Snapshot.Status {
		select {
		case gate.started <- event:
		default:
		}
		select {
		case <-gate.release:
		case <-request.Context().Done():
			return
		}
	}
	messenger.mu.Lock()
	if event.Kind == "send" {
		messenger.nextMessageID++
		event.MessageID = messenger.nextMessageID
	}
	messenger.delivered = append(messenger.delivered, event)
	messenger.mu.Unlock()
	messenger.events <- event
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct{ MessageID int }{MessageID: event.MessageID})
}

func (messenger *httpMessenger) SendQueue(ctx context.Context, snapshot queue.Snapshot) (int, error) {
	return messenger.call(ctx, delivery{Kind: "send", Snapshot: snapshot})
}

func (messenger *httpMessenger) EditQueue(ctx context.Context, snapshot queue.Snapshot, messageID int) error {
	_, err := messenger.call(ctx, delivery{Kind: "edit", Snapshot: snapshot, MessageID: messageID})
	return err
}

func (messenger *httpMessenger) RetireQueue(ctx context.Context, chatID int64, messageID int) error {
	_, err := messenger.call(ctx, delivery{Kind: "retire", ChatID: chatID, MessageID: messageID})
	return err
}

func (messenger *httpMessenger) NotifyQueue(ctx context.Context, schedule db.Schedule, text string) error {
	_, err := messenger.call(ctx, delivery{Kind: "notify", Snapshot: queue.Snapshot{Schedule: schedule}, Text: text})
	return err
}

func (messenger *httpMessenger) call(ctx context.Context, event delivery) (int, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, messenger.server.URL, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := messenger.server.Client().Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, response.Body)
		return 0, fmt.Errorf("message transport returned HTTP %d", response.StatusCode)
	}
	var result struct{ MessageID int }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return 0, err
	}
	return result.MessageID, nil
}

func (messenger *httpMessenger) block(t *testing.T, scheduleID int, status utils.QueueStatus) *editGate {
	t.Helper()
	gate := &editGate{scheduleID: scheduleID, status: status, started: make(chan delivery, 1), release: make(chan struct{})}
	messenger.mu.Lock()
	messenger.gates[scheduleID] = gate
	messenger.mu.Unlock()
	t.Cleanup(gate.unblock)
	return gate
}

func (messenger *httpMessenger) failEdits(scheduleID, count int) {
	messenger.mu.Lock()
	messenger.failBySchedule[scheduleID] = count
	messenger.mu.Unlock()
}

func (messenger *httpMessenger) await(t *testing.T, match func(delivery) bool) delivery {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-messenger.events:
			if match(event) {
				return event
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for message delivery")
		}
	}
}

func (messenger *httpMessenger) history() []delivery {
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	return append([]delivery(nil), messenger.delivered...)
}

func awaitChannel[T any](t *testing.T, channel <-chan T, operation string) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
		var zero T
		return zero
	}
}

type deleteBarrierStore struct {
	*observedStore
	deleting  chan struct{}
	release   chan struct{}
	preparing chan struct{}
	once      sync.Once
}

func (store *deleteBarrierStore) DeleteScheduleEntry(ctx context.Context, scheduleID int) error {
	close(store.deleting)
	select {
	case <-store.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return store.DBRepository.DeleteScheduleEntry(ctx, scheduleID)
}

func (store *deleteBarrierStore) BeginQueueRun(ctx context.Context, scheduleID int, opensAt, closesAt time.Time, capacity, columns int) (db.Schedule, error) {
	select {
	case store.preparing <- struct{}{}:
	default:
	}
	return store.DBRepository.BeginQueueRun(ctx, scheduleID, opensAt, closesAt, capacity, columns)
}

func (store *deleteBarrierStore) unblock() {
	store.once.Do(func() { close(store.release) })
}

type missingMessageMessenger struct {
	queue.Messenger
	mu        sync.Mutex
	messageID int
	missing   bool
}

func (messenger *missingMessageMessenger) EditQueue(ctx context.Context, snapshot queue.Snapshot, messageID int) error {
	messenger.mu.Lock()
	missing := messenger.missing && messageID == messenger.messageID
	if missing {
		messenger.missing = false
	}
	messenger.mu.Unlock()
	if missing {
		return queue.ErrMessageMissing
	}
	return messenger.Messenger.EditQueue(ctx, snapshot, messageID)
}

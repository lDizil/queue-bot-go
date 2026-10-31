package scheduler

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
)

func TestExpiredLegacyQueuesInSameTopicCloseWithTheirOwnParticipants(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "expired queues release the topic"
		if live {
			name = "live conflict preserves both queues"
		}
		t.Run(name, func(t *testing.T) {
			repository := legacyTestRepository(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const chatID = -987654
			const threadID = 902201
			messenger := &legacyTestMessenger{sentAt: moscowDate(7, 23, 30), closed: make(map[int]queue.Snapshot)}
			manager, err := settings.NewManager(ctx, memorySettings{}, settings.Defaults())
			if err != nil {
				t.Fatal(err)
			}
			service := queue.NewService(ctx, repository, manager, messenger)
			t.Cleanup(service.Stop)
			scheduler := NewScheduler(ctx, repository, service, manager)
			now := moscowDate(15, 0, 30)
			if live {
				now = moscowDate(8, 0, 30)
			}
			scheduler.clock = fixedClock{now: now}
			var schedules []db.Schedule
			for index, messageID := range []int{101, 102} {
				row := db.Schedule{ChatID: chatID, ThreadID: threadID, StartTime: makeTimeOnly(23, 0), EndTime: makeTimeOnly(1, 0), IsTemporary: true}
				row.ID, err = repository.AddTemporarySchedule(ctx, row)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = repository.DeleteScheduleEntry(context.Background(), row.ID) })
				row.QueueMesID = &messageID
				if err := repository.SetQueueMessageID(ctx, row.ID, messageID); err != nil {
					t.Fatal(err)
				}
				if err := repository.JoinQueue(ctx, db.QueueEntry{UserID: int64(index + 1), ScheduleID: row.ID, Position: 1}); err != nil {
					t.Fatal(err)
				}
				schedules = append(schedules, row)
			}
			if live {
				for _, row := range schedules {
					if _, err := scheduler.restoreLegacy(ctx, row); !errors.Is(err, db.ErrTopicBusy) {
						t.Fatalf("conflicting live queue was selected or removed: %v", err)
					}
					entries, err := repository.GetQueue(ctx, row.ID)
					if err != nil || len(entries) != 1 {
						t.Fatalf("live legacy participants were removed: %+v err=%v", entries, err)
					}
				}
				if len(messenger.closed) != 0 {
					t.Fatal("a conflicting live legacy message was closed")
				}
				return
			}
			var workers []*scheduleWorker
			for _, row := range schedules {
				workerCtx, workerCancel := context.WithCancel(ctx)
				worker := &scheduleWorker{cancel: workerCancel, done: make(chan struct{}), active: true}
				scheduler.mu.Lock()
				scheduler.workers[row.ID] = worker
				scheduler.mu.Unlock()
				workers = append(workers, worker)
				go scheduler.run(workerCtx, row.ID, worker)
			}
			for _, worker := range workers {
				select {
				case <-worker.done:
				case <-ctx.Done():
					t.Fatal("expired legacy queues blocked each other in the same topic")
				}
			}
			for index, row := range schedules {
				closed := messenger.closed[*row.QueueMesID]
				if closed.Status != utils.QueueClosed || len(closed.Entries) != 1 || closed.Entries[0].UserID != int64(index+1) {
					t.Fatalf("final message lost or mixed legacy participants: %+v", closed)
				}
				if _, err := repository.GetScheduleEntry(ctx, row.ID); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("expired legacy temporary queue remains active: %v", err)
				}
			}
			if _, err := repository.GetActiveScheduleByTopic(ctx, chatID, threadID); !errors.Is(err, db.ErrActiveQueueNotFound) {
				t.Fatalf("expired legacy queues kept their topic busy: %v", err)
			}
		})
	}
}

func legacyTestRepository(t *testing.T) *db.DBRepository {
	t.Helper()
	databaseURL := os.Getenv("QUEUEBOT_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("QUEUEBOT_REQUIRE_DATABASE") == "1" {
			t.Fatal("QUEUEBOT_TEST_DATABASE_URL is required in integration CI")
		}
		t.Skip("set QUEUEBOT_TEST_DATABASE_URL to run legacy integration tests")
	}
	migration, err := migrate.New("file://../migrations", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	err = migration.Up()
	_, _ = migration.Close()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatal(err)
	}
	pool, err := db.SetUpDBConn(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return db.NewDBRepo(pool)
}

type legacyTestMessenger struct {
	mu     sync.Mutex
	sentAt time.Time
	closed map[int]queue.Snapshot
}

var (
	_ queue.Messenger       = (*legacyTestMessenger)(nil)
	_ queue.LegacyMessenger = (*legacyTestMessenger)(nil)
)

func (messenger *legacyTestMessenger) EditQueue(_ context.Context, snapshot queue.Snapshot, messageID int) error {
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	messenger.closed[messageID] = snapshot
	return nil
}

func (*legacyTestMessenger) SendQueue(context.Context, queue.Snapshot) (int, error) {
	return 0, errors.New("legacy recovery must edit the original message")
}

func (*legacyTestMessenger) RetireQueue(context.Context, int64, int) error          { return nil }
func (*legacyTestMessenger) NotifyQueue(context.Context, db.Schedule, string) error { return nil }

func (messenger *legacyTestMessenger) QueueMessageDate(context.Context, db.Schedule, int) (time.Time, error) {
	return messenger.sentAt, nil
}

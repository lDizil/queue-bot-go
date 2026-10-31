package db

import (
	"context"
	"testing"
	"time"
)

func TestSyncQueueStateWaitsForTerminalCommitOrRollback(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			repo := newTestDBRepo(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			id := mustCreateTemporarySchedule(t, repo, 902101)
			if err := repo.SetQueueMessageID(ctx, id, 100); err != nil {
				t.Fatal(err)
			}
			tx, err := repo.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err := lockQueue(ctx, tx, id); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, "UPDATE schedules SET queue_message_id = 200 WHERE id = $1", id); err != nil {
				t.Fatal(err)
			}
			ordinary, err := repo.GetScheduleEntry(ctx, id)
			if err != nil || ordinary.QueueMesID == nil || *ordinary.QueueMesID != 100 {
				t.Fatalf("test did not establish an in-flight invisible write: row=%+v err=%v", ordinary, err)
			}
			var settled Schedule
			result := make(chan error, 1)
			go func() {
				var err error
				settled, err = repo.SyncQueueState(ctx, id)
				result <- err
			}()
			waitForQueueLockWaiter(t, ctx, repo, id, result)
			if commit {
				err = tx.Commit(ctx)
			} else {
				err = tx.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			awaitQueueWrite(t, ctx, result)
			wantMessageID := 100
			if commit {
				wantMessageID = 200
			}
			if settled.QueueMesID == nil || *settled.QueueMesID != wantMessageID {
				t.Fatalf("readback used a snapshot from before the terminal outcome: row=%+v want=%d", settled, wantMessageID)
			}
		})
	}
}

func waitForQueueLockWaiter(t *testing.T, ctx context.Context, repo *DBRepository, id int, result <-chan error) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := repo.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
 AND classid = 0 AND objid = $1 AND objsubid = 1)`, id).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("operation completed before the preceding writer reached a terminal outcome: %v", err)
		case <-ctx.Done():
			t.Fatal("queue readback never waited on the preceding writer")
		case <-ticker.C:
		}
	}
}

func awaitQueueWrite(t *testing.T, ctx context.Context, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("queue operation did not finish after releasing the preceding transaction")
	}
}

func TestSetQueueMessageIDAcquiresQueueLockBeforeWriting(t *testing.T) {
	repo := newTestDBRepo(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	id := mustCreateTemporarySchedule(t, repo, 902102)
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err := lockQueue(ctx, tx, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- repo.SetQueueMessageID(ctx, id, 300) }()
	waitForQueueLockWaiter(t, ctx, repo, id, result)
	pending, err := repo.GetScheduleEntry(ctx, id)
	if err != nil || pending.QueueMesID != nil {
		t.Fatalf("message ID was written before acknowledging the queue lock: row=%+v err=%v", pending, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	awaitQueueWrite(t, ctx, result)
	written, err := repo.GetScheduleEntry(ctx, id)
	if err != nil || written.QueueMesID == nil || *written.QueueMesID != 300 {
		t.Fatalf("message ID was not committed after releasing the queue lock: row=%+v err=%v", written, err)
	}
}

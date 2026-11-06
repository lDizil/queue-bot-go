package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrQueueStateChanged = errors.New("queue state changed")
var ErrSwapStateChanged = ErrQueueStateChanged

const queueColumns = "id, user_id, COALESCE(username, ''), display_name, schedule_id, position, completed"

func scanQueueEntry(row rowScanner) (QueueEntry, error) {
	var entry QueueEntry
	err := row.Scan(&entry.ID, &entry.UserID, &entry.Username, &entry.DisplayName, &entry.ScheduleID, &entry.Position, &entry.Completed)
	return entry, err
}

func (db *DBRepository) GetQueue(ctx context.Context, scheduleID int) ([]QueueEntry, error) {
	rows, err := db.pool.Query(ctx, "SELECT "+queueColumns+" FROM queue_entries WHERE schedule_id = $1 ORDER BY position", scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []QueueEntry
	for rows.Next() {
		entry, err := scanQueueEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (db *DBRepository) JoinQueue(ctx context.Context, entry QueueEntry) error {
	if entry.Position <= 0 {
		return ErrQueueStateChanged
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, entry.ScheduleID); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM queue_entries WHERE schedule_id = $1 AND user_id = $2)", entry.ScheduleID, entry.UserID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return pgx.ErrNoRows
	}
	if err := checkQueueRejoin(ctx, tx, entry.ScheduleID, entry.UserID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO queue_entries (user_id, username, display_name, schedule_id, position, completed)
  VALUES ($1, $2, $3, $4, $5, $6)`, entry.UserID, entry.Username, entry.DisplayName, entry.ScheduleID, entry.Position, entry.Completed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockQueue(ctx context.Context, tx pgx.Tx, scheduleID int) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(scheduleID))
	return err
}

func (db *DBRepository) GetQueueEntryByUser(ctx context.Context, userID int64, scheduleID int) (QueueEntry, error) {
	return scanQueueEntry(db.pool.QueryRow(ctx, "SELECT "+queueColumns+" FROM queue_entries WHERE user_id = $1 AND schedule_id = $2", userID, scheduleID))
}

func (db *DBRepository) GetQueueEntryByPosition(ctx context.Context, position, scheduleID int) (QueueEntry, error) {
	return scanQueueEntry(db.pool.QueryRow(ctx, "SELECT "+queueColumns+" FROM queue_entries WHERE position = $1 AND schedule_id = $2", position, scheduleID))
}

func (db *DBRepository) SetCompleted(ctx context.Context, scheduleID, entryID, expectedPosition int, completed bool) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE queue_entries SET completed = $4 WHERE schedule_id = $1 AND id = $2 AND position = $3", scheduleID, entryID, expectedPosition, completed)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrQueueStateChanged
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) SwapQueueEntries(ctx context.Context, scheduleID, requesterEntryID, targetEntryID, requesterExpectedPosition, targetExpectedPosition int) error {
	if requesterEntryID == targetEntryID || requesterExpectedPosition <= 0 || targetExpectedPosition <= 0 {
		return ErrQueueStateChanged
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	var requesterPosition, targetPosition int
	if err := tx.QueryRow(ctx, "SELECT position FROM queue_entries WHERE schedule_id = $1 AND id = $2 FOR UPDATE", scheduleID, requesterEntryID).Scan(&requesterPosition); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQueueStateChanged
		}
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT position FROM queue_entries WHERE schedule_id = $1 AND id = $2 FOR UPDATE", scheduleID, targetEntryID).Scan(&targetPosition); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQueueStateChanged
		}
		return err
	}
	if requesterPosition != requesterExpectedPosition || targetPosition != targetExpectedPosition {
		return ErrQueueStateChanged
	}
	if _, err := tx.Exec(ctx, "UPDATE queue_entries SET position = 0 WHERE id = $1", requesterEntryID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE queue_entries SET position = $2 WHERE id = $1", targetEntryID, requesterPosition); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE queue_entries SET position = $2 WHERE id = $1", requesterEntryID, targetPosition); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) SwapQueuePositions(ctx context.Context, scheduleID int, requesterUserID, targetUserID int64, requesterExpectedPosition, targetExpectedPosition int) error {
	requester, err := db.GetQueueEntryByUser(ctx, requesterUserID, scheduleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQueueStateChanged
		}
		return err
	}
	target, err := db.GetQueueEntryByUser(ctx, targetUserID, scheduleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrQueueStateChanged
		}
		return err
	}
	return db.SwapQueueEntries(ctx, scheduleID, requester.ID, target.ID, requesterExpectedPosition, targetExpectedPosition)
}

func (db *DBRepository) JoinFirstFreeSlot(ctx context.Context, userID int64, username string, scheduleID, totalSlots int) (int, error) {
	return db.JoinFirstFreeSlotWithName(ctx, userID, username, "", scheduleID, totalSlots)
}

func (db *DBRepository) JoinFirstFreeSlotWithName(ctx context.Context, userID int64, username, displayName string, scheduleID, totalSlots int) (int, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return -1, err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return -1, err
	}
	if err := checkQueueRejoin(ctx, tx, scheduleID, userID); err != nil {
		return -1, err
	}
	var position int
	if err := tx.QueryRow(ctx,
		`INSERT INTO queue_entries (user_id, username, display_name, schedule_id, position)
   SELECT $1, $2, $3, $4, free.pos FROM generate_series(1, $5) AS free(pos)
   WHERE NOT EXISTS (SELECT 1 FROM queue_entries WHERE schedule_id = $4 AND position = free.pos)
   AND NOT EXISTS (SELECT 1 FROM queue_entries WHERE schedule_id = $4 AND user_id = $1)
   ORDER BY free.pos LIMIT 1 RETURNING position`, userID, username, displayName, scheduleID, totalSlots).Scan(&position); err != nil {
		return -1, err
	}
	return position, tx.Commit(ctx)
}

func (db *DBRepository) LeaveFromQueue(ctx context.Context, userID int64, scheduleID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM queue_entries WHERE user_id = $1 AND schedule_id = $2", userID, scheduleID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) IsPositionTaken(ctx context.Context, position, scheduleID int) (bool, error) {
	var taken bool
	err := db.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM queue_entries WHERE position = $1 AND schedule_id = $2)", position, scheduleID).Scan(&taken)
	return taken, err
}

func (db *DBRepository) IsUserInQueue(ctx context.Context, userID int64, scheduleID int) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM queue_entries WHERE user_id = $1 AND schedule_id = $2)", userID, scheduleID).Scan(&exists)
	return exists, err
}

func (db *DBRepository) ClearQueue(ctx context.Context, scheduleID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "DELETE FROM queue_entries WHERE schedule_id = $1", scheduleID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

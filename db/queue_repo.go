package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrSwapStateChanged = errors.New("swap state changed")

func (db *DBRepository) GetQueue(ctx context.Context, scheduleID int) ([]QueueEntry, error) {
	rows, err := db.pool.Query(ctx, "SELECT id, user_id, username, schedule_id, position FROM queue_entries WHERE schedule_id=$1 ORDER BY position", scheduleID)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var queues []QueueEntry

	for rows.Next() {
		var entry QueueEntry

		err := rows.Scan(&entry.ID, &entry.UserID, &entry.Username, &entry.ScheduleID, &entry.Position)
		if err != nil {
			return nil, err
		}

		queues = append(queues, entry)
	}

	return queues, nil
}

func (db *DBRepository) GetScheduleByID(ctx context.Context, scheduleID int) (Schedule, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description, queue_message_id,
		notified_5min, notified_1min, notified_open, is_temporary
		FROM schedules
		WHERE id=$1`,
		scheduleID,
	)

	var schedule Schedule
	err := row.Scan(
		&schedule.ID,
		&schedule.DayOfWeek,
		&schedule.WeekType,
		&schedule.StartTime,
		&schedule.EndTime,
		&schedule.ThreadID,
		&schedule.ThreadDescription,
		&schedule.QueueMesID,
		&schedule.Notified5min,
		&schedule.Notified1min,
		&schedule.NotifiedOpen,
		&schedule.IsTemporary,
	)

	if err != nil {
		return schedule, err
	}

	return schedule, nil
}

func (db *DBRepository) JoinQueue(ctx context.Context, entry QueueEntry) error {
	_, err := db.pool.Exec(ctx,
		"INSERT INTO queue_entries (user_id, username, schedule_id, position) VALUES ($1, $2, $3, $4)",
		entry.UserID, entry.Username, entry.ScheduleID, entry.Position,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) GetQueueEntryByUser(ctx context.Context, userID int64, scheduleID int) (QueueEntry, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT id, user_id, username, schedule_id, position FROM queue_entries WHERE user_id=$1 AND schedule_id=$2",
		userID, scheduleID,
	)

	var entry QueueEntry
	err := row.Scan(&entry.ID, &entry.UserID, &entry.Username, &entry.ScheduleID, &entry.Position)
	if err != nil {
		return QueueEntry{}, err
	}

	return entry, nil
}

func (db *DBRepository) GetQueueEntryByPosition(ctx context.Context, position int, scheduleID int) (QueueEntry, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT id, user_id, username, schedule_id, position FROM queue_entries WHERE position=$1 AND schedule_id=$2",
		position, scheduleID,
	)

	var entry QueueEntry
	err := row.Scan(&entry.ID, &entry.UserID, &entry.Username, &entry.ScheduleID, &entry.Position)
	if err != nil {
		return QueueEntry{}, err
	}

	return entry, nil
}

func (db *DBRepository) SwapQueuePositions(ctx context.Context, scheduleID int, requesterUserID int64, targetUserID int64, requesterExpectedPosition int, targetExpectedPosition int) error {
	if requesterUserID == targetUserID {
		return ErrSwapStateChanged
	}

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(scheduleID))
	if err != nil {
		return err
	}

	var requesterPosition int
	err = tx.QueryRow(ctx,
		"SELECT position FROM queue_entries WHERE schedule_id=$1 AND user_id=$2 FOR UPDATE",
		scheduleID, requesterUserID,
	).Scan(&requesterPosition)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSwapStateChanged
		}
		return err
	}

	var targetPosition int
	err = tx.QueryRow(ctx,
		"SELECT position FROM queue_entries WHERE schedule_id=$1 AND user_id=$2 FOR UPDATE",
		scheduleID, targetUserID,
	).Scan(&targetPosition)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSwapStateChanged
		}
		return err
	}

	if requesterPosition != requesterExpectedPosition || targetPosition != targetExpectedPosition {
		return ErrSwapStateChanged
	}

	const tmpPosition = 0

	_, err = tx.Exec(ctx,
		"UPDATE queue_entries SET position=$1 WHERE schedule_id=$2 AND user_id=$3",
		tmpPosition, scheduleID, requesterUserID,
	)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		"UPDATE queue_entries SET position=$1 WHERE schedule_id=$2 AND user_id=$3",
		requesterExpectedPosition, scheduleID, targetUserID,
	)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx,
		"UPDATE queue_entries SET position=$1 WHERE schedule_id=$2 AND user_id=$3",
		targetExpectedPosition, scheduleID, requesterUserID,
	)
	if err != nil {
		return err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) JoinFirstFreeSlot(ctx context.Context, userID int64, username string, scheduleID int, totalSlots int) (int, error) {
	var position int

	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return -1, err
	}

	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(scheduleID))
	if err != nil {
		return -1, err
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO queue_entries (user_id, username, schedule_id, position)
		SELECT $1, $2, $3, free.pos
		FROM generate_series(1, $4) AS free(pos)
		WHERE free.pos NOT IN (
			SELECT position FROM queue_entries WHERE schedule_id=$3
		)
		AND NOT EXISTS (
			SELECT 1 FROM queue_entries WHERE user_id=$1 AND schedule_id=$3
		)
		ORDER BY free.pos
		LIMIT 1
		RETURNING position`,
		userID, username, scheduleID, totalSlots,
	).Scan(&position)

	if err != nil {
		return -1, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return -1, err
	}

	return position, nil
}

func (db *DBRepository) LeaveFromQueue(ctx context.Context, userID int64, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		"DELETE FROM queue_entries WHERE user_id=$1 AND schedule_id=$2",
		userID, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) IsPositionTaken(ctx context.Context, position int, scheduleID int) (bool, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM queue_entries WHERE position=$1 AND schedule_id=$2)",
		position, scheduleID,
	)

	var taken bool
	err := row.Scan(&taken)

	if err != nil {
		return false, err
	}

	return taken, nil
}

func (db *DBRepository) IsUserInQueue(ctx context.Context, userID int64, scheduleID int) (bool, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM queue_entries WHERE user_id=$1 AND schedule_id=$2)",
		userID, scheduleID,
	)

	var taken bool
	err := row.Scan(&taken)

	if err != nil {
		return false, err
	}

	return taken, nil
}

func (db *DBRepository) ClearQueue(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		"DELETE FROM queue_entries WHERE schedule_id=$1",
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

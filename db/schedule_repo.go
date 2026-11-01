package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrActiveQueueNotFound          = errors.New("active queue not found")
	ErrMultipleActiveQueuesInThread = errors.New("multiple active queues in thread")
	ErrTopicBusy                    = errors.New("topic already has an active queue")
)

const scheduleColumns = `id, chat_id, title, day_of_week, week_type, start_time, end_time,
 thread_id, thread_description, queue_message_id, notified_5min, notified_1min,
 notified_open, is_temporary, active_opens_at, active_closes_at, active_thread_id,
 active_capacity, active_columns`

type rowScanner interface{ Scan(...any) error }

func scanSchedule(row rowScanner) (Schedule, error) {
	var s Schedule
	err := row.Scan(&s.ID, &s.ChatID, &s.Title, &s.DayOfWeek, &s.WeekType, &s.StartTime,
		&s.EndTime, &s.ThreadID, &s.ThreadDescription, &s.QueueMesID, &s.Notified5min,
		&s.Notified1min, &s.NotifiedOpen, &s.IsTemporary, &s.ActiveOpensAt,
		&s.ActiveClosesAt, &s.ActiveThreadID, &s.ActiveCapacity, &s.ActiveColumns)
	return s, err
}

func (db *DBRepository) GetAllSchedules(ctx context.Context) ([]Schedule, error) {
	return db.listSchedules(ctx, " WHERE is_temporary = false ORDER BY id")
}

func (db *DBRepository) listSchedules(ctx context.Context, suffix string, args ...any) ([]Schedule, error) {
	rows, err := db.pool.Query(ctx, "SELECT "+scheduleColumns+" FROM schedules"+suffix, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var schedules []Schedule
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

func (db *DBRepository) GetStartupSchedules(ctx context.Context) ([]Schedule, error) {
	return db.listSchedules(ctx, " ORDER BY id")
}

func (db *DBRepository) GetScheduleEntry(ctx context.Context, scheduleID int) (Schedule, error) {
	return scanSchedule(db.pool.QueryRow(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id = $1", scheduleID))
}

func (db *DBRepository) GetScheduleByID(ctx context.Context, scheduleID int) (Schedule, error) {
	return db.GetScheduleEntry(ctx, scheduleID)
}

func (db *DBRepository) GetActiveScheduleByTopic(ctx context.Context, chatID int64, threadID int) (Schedule, error) {
	schedules, err := db.listSchedules(ctx,
		" WHERE chat_id = $1 AND COALESCE(active_thread_id, thread_id) = $2 AND (active_opens_at IS NOT NULL OR queue_message_id IS NOT NULL) LIMIT 2", chatID, threadID)
	return singleActiveSchedule(schedules, err)
}

func singleActiveSchedule(schedules []Schedule, err error) (Schedule, error) {
	if err != nil {
		return Schedule{}, err
	}
	if len(schedules) == 0 {
		return Schedule{}, ErrActiveQueueNotFound
	}
	if len(schedules) > 1 {
		return Schedule{}, ErrMultipleActiveQueuesInThread
	}
	return schedules[0], nil
}

func (db *DBRepository) GetActiveScheduleByMessage(ctx context.Context, chatID int64, messageID int) (Schedule, error) {
	schedules, err := db.listSchedules(ctx, " WHERE chat_id = $1 AND queue_message_id = $2 LIMIT 2", chatID, messageID)
	return singleActiveSchedule(schedules, err)
}

func (db *DBRepository) GetActiveScheduleIDByThread(ctx context.Context, threadID int) (int, error) {
	schedules, err := db.listSchedules(ctx,
		" WHERE COALESCE(active_thread_id, thread_id) = $1 AND (active_opens_at IS NOT NULL OR queue_message_id IS NOT NULL) LIMIT 2", threadID)
	schedule, err := singleActiveSchedule(schedules, err)
	return schedule.ID, err
}

func (db *DBRepository) AddNewScheduleEntry(ctx context.Context, schedule Schedule) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx,
		`INSERT INTO schedules (chat_id, title, day_of_week, week_type, start_time, end_time, thread_id, thread_description)
  VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		schedule.ChatID, schedule.Title, schedule.DayOfWeek, schedule.WeekType, schedule.StartTime,
		schedule.EndTime, schedule.ThreadID, schedule.ThreadDescription).Scan(&id)
	return id, err
}

func (db *DBRepository) AddTemporarySchedule(ctx context.Context, schedule Schedule) (int, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if schedule.ActiveOpensAt != nil {
		if err := lockTopic(ctx, tx, schedule.ChatID, schedule.ThreadID); err != nil {
			return 0, err
		}
		if err := topicAvailable(ctx, tx, schedule.ChatID, schedule.ThreadID, 0); err != nil {
			return 0, err
		}
	}
	var threadID *int
	if schedule.ActiveOpensAt != nil {
		threadID = &schedule.ThreadID
	}
	var id int
	err = tx.QueryRow(ctx,
		`INSERT INTO schedules (chat_id, title, day_of_week, week_type, start_time, end_time, thread_id,
   thread_description, is_temporary, active_opens_at, active_closes_at, active_thread_id, active_capacity, active_columns)
  VALUES ($1, $2, '', '', $3, $4, $5, $6, true, $7, $8, $9, $10, $11) RETURNING id`,
		schedule.ChatID, schedule.Title, schedule.StartTime, schedule.EndTime, schedule.ThreadID,
		schedule.ThreadDescription, schedule.ActiveOpensAt, schedule.ActiveClosesAt, threadID,
		schedule.ActiveCapacity, schedule.ActiveColumns).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func lockTopic(ctx context.Context, tx pgx.Tx, chatID int64, threadID int) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", fmt.Sprintf("queue-topic:%d:%d", chatID, threadID))
	return err
}

func topicAvailable(ctx context.Context, tx pgx.Tx, chatID int64, threadID, excludeID int) error {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schedules WHERE chat_id = $1
   AND COALESCE(active_thread_id, thread_id) = $2 AND id <> $3
   AND (active_opens_at IS NOT NULL OR queue_message_id IS NOT NULL))`, chatID, threadID, excludeID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return ErrTopicBusy
	}
	return nil
}

func (db *DBRepository) BeginQueueRun(ctx context.Context, scheduleID int, opensAt, closesAt time.Time, capacity, columns int) (Schedule, error) {
	return db.setQueueRun(ctx, scheduleID, opensAt, closesAt, capacity, columns, false)
}

func (db *DBRepository) SetLegacyQueueRunBounds(ctx context.Context, scheduleID int, opensAt, closesAt time.Time, capacity, columns int) (Schedule, error) {
	return db.setQueueRun(ctx, scheduleID, opensAt, closesAt, capacity, columns, true)
}

func (db *DBRepository) setQueueRun(ctx context.Context, scheduleID int, opensAt, closesAt time.Time, capacity, columns int, legacy bool) (Schedule, error) {
	if !closesAt.After(opensAt) || capacity <= 0 || columns <= 0 {
		return Schedule{}, ErrQueueStateChanged
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return Schedule{}, err
	}
	schedule, err := scanSchedule(tx.QueryRow(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id = $1 FOR UPDATE", scheduleID))
	if err != nil {
		return Schedule{}, err
	}
	if schedule.ActiveOpensAt != nil {
		if schedule.ActiveOpensAt.Equal(opensAt) && schedule.ActiveClosesAt != nil && schedule.ActiveClosesAt.Equal(closesAt) {
			return schedule, tx.Commit(ctx)
		}
		return Schedule{}, ErrQueueStateChanged
	}
	if !legacy && schedule.QueueMesID != nil {
		return Schedule{}, ErrQueueStateChanged
	}
	if err := lockTopic(ctx, tx, schedule.ChatID, schedule.ThreadID); err != nil {
		return Schedule{}, err
	}
	if err := topicAvailable(ctx, tx, schedule.ChatID, schedule.ThreadID, scheduleID); err != nil {
		return Schedule{}, err
	}
	if !legacy {
		if _, err := tx.Exec(ctx, "DELETE FROM queue_entries WHERE schedule_id = $1", scheduleID); err != nil {
			return Schedule{}, err
		}
	}
	schedule, err = scanSchedule(tx.QueryRow(ctx,
		`UPDATE schedules SET active_opens_at = $2, active_closes_at = $3, active_thread_id = thread_id,
   active_capacity = $4, active_columns = $5,
   queue_message_id = CASE WHEN $6 THEN queue_message_id ELSE NULL END,
   notified_5min = CASE WHEN $6 THEN notified_5min ELSE false END,
   notified_1min = CASE WHEN $6 THEN notified_1min ELSE false END,
   notified_open = CASE WHEN $6 THEN notified_open ELSE false END
   WHERE id = $1 RETURNING `+scheduleColumns, scheduleID, opensAt, closesAt, capacity, columns, legacy))
	if err != nil {
		return Schedule{}, err
	}
	return schedule, tx.Commit(ctx)
}

func (db *DBRepository) FinishQueueRun(ctx context.Context, scheduleID int) error {
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
	if _, err := tx.Exec(ctx, "DELETE FROM schedules WHERE id = $1 AND is_temporary", scheduleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE schedules SET active_opens_at = NULL, active_closes_at = NULL,
  active_thread_id = NULL, active_capacity = 0, active_columns = 0, queue_message_id = NULL,
  notified_5min = false, notified_1min = false, notified_open = false WHERE id = $1`, scheduleID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) BackfillChatIDs(ctx context.Context, defaultChatID int64) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET chat_id = $1 WHERE chat_id = 0", defaultChatID)
	return err
}

func (db *DBRepository) DeleteScheduleEntry(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "DELETE FROM schedules WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) SyncQueueState(ctx context.Context, scheduleID int) (Schedule, error) {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Schedule{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return Schedule{}, err
	}
	schedule, err := scanSchedule(tx.QueryRow(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE id = $1", scheduleID))
	if err != nil {
		return Schedule{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

func (db *DBRepository) SetQueueMessageID(ctx context.Context, scheduleID, messageID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE schedules SET queue_message_id = $2 WHERE id = $1", scheduleID, messageID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) ClearQueueMessageID(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET queue_message_id = NULL WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) SetTitle(ctx context.Context, scheduleID int, title string) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET title = $2 WHERE id = $1", scheduleID, title)
	return err
}

func (db *DBRepository) ChangeWeekType(ctx context.Context, scheduleID int, weekType string) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET week_type = $2 WHERE id = $1", scheduleID, weekType)
	return err
}

func (db *DBRepository) ChangeWeekDay(ctx context.Context, scheduleID int, weekDay string) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET day_of_week = $2 WHERE id = $1", scheduleID, weekDay)
	return err
}

func (db *DBRepository) ChangeStartTime(ctx context.Context, scheduleID int, startTime time.Time) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET start_time = $2 WHERE id = $1", scheduleID, startTime)
	return err
}

func (db *DBRepository) ChangeEndTime(ctx context.Context, scheduleID int, endTime time.Time) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET end_time = $2 WHERE id = $1", scheduleID, endTime)
	return err
}

func (db *DBRepository) ChangeThreadID(ctx context.Context, scheduleID, threadID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET thread_id = $2 WHERE id = $1", scheduleID, threadID)
	return err
}

func (db *DBRepository) ChangeDescription(ctx context.Context, scheduleID int, description string) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET thread_description = $2 WHERE id = $1", scheduleID, description)
	return err
}

func (db *DBRepository) ResetNotifications(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET notified_5min = false, notified_1min = false, notified_open = false WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) SetNotified5min(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET notified_5min = true WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) SetNotified1min(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET notified_1min = true WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) SetNotifiedOpen(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, "UPDATE schedules SET notified_open = true WHERE id = $1", scheduleID)
	return err
}

func (db *DBRepository) GetAllSchedulesWithNotifications(ctx context.Context) ([]Schedule, error) {
	return db.GetAllSchedules(ctx)
}

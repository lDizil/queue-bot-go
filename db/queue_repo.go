package db

import (
	"context"
)


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
		"SELECT * FROM schedules WHERE id=$1",
		scheduleID,
	)

	var schedule Schedule
	err := row.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.Notified5min, &schedule.Notified1min, &schedule.NotifiedOpen)

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

func (db *DBRepository) JoinFirstFreeSlot(ctx context.Context, userID int64, username string, scheduleID int, totalSlots int) (int, error) {
	var position int

	err := db.pool.QueryRow(ctx,
		`INSERT INTO queue_entries (user_id, username, schedule_id, position)
		SELECT $1, $2, $3, MIN(free.pos)
		FROM generate_series(1, $4) AS free(pos)
		WHERE free.pos NOT IN (
			SELECT position FROM queue_entries WHERE schedule_id=$3
		)
		RETURNING position`,
		userID, username, scheduleID, totalSlots,
	).Scan(&position)

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

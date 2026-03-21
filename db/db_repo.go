package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DBRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepo(pool *pgxpool.Pool) *DBRepository {
	return &DBRepository{pool: pool}
}

func (db *DBRepository) GetQueue(ctx context.Context, scheduleID int) ([]QueueEntry, error) {
	rows, err := db.pool.Query(ctx, "SELECT id, user_id, username, schedule_id, position FROM queue_entries WHERE schedule_id=$1", scheduleID)
		
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var queues []QueueEntry
	
	for rows.Next() {
		var entry QueueEntry
		rows.Scan(&entry.ID, &entry.UserId, &entry.Username, &entry.ScheduleID, &entry.Position)
		queues = append(queues, entry)
	}
	
	return queues, nil
}

func(db *DBRepository) GetScheduleByID(ctx context.Context, scheduleID int) (Schedule, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT * FROM schedules WHERE id=$1",
		scheduleID,
	)

	var schedule Schedule
	err := row.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadId, &schedule.Notified5min, &schedule.Notified1min, &schedule.NotifiedOpen, &schedule.queueMessageID)

	if err != nil {
		return schedule, err
	}

	return schedule, nil
}

func (db *DBRepository) JoinQueue(ctx context.Context, entry QueueEntry) error {
	_, err := db.pool.Exec(ctx, 
		"INSERT INTO queue_entries (user_id, username, schedule_id, position) VALUES ($1, $2, $3, $4)",
		entry.UserId, entry.Username, entry.ScheduleID, entry.Position,
	)
	
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) LeaveFromQueue(ctx context.Context, userID int64, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		"DELETE FROM queue_entries WHERE user_id=$1 AND schedule_id=$2",
		userID,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) IsPositionTaken(ctx context.Context, position int) (bool, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM queue_entries WHERE position=$1)",
		position,
	)

	var taken bool
	err := row.Scan(&taken)

	if err != nil {
		return false, err
	}

	return taken, nil
}

func(db *DBRepository) IsUserInQueue(ctx context.Context, userID int64, scheduleID int) (bool, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM queue_entries WHERE user_id=$1 and schedule_id=$2)",
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

func (db *DBRepository) SaveMessageID(ctx context.Context, scheduleID int, massageID int64) error {
	_, err := db.pool.Exec(ctx,
		"UPDATE schedules SET queue_message_id=$1 WHERE schedule_id=$2",
		massageID, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

// func (db *DBRepository) GetBusySlots(ctx context.Context, scheduleID int) ([]int, error) {
// 	rows, err := db.pool.Query(ctx,
// 		"SELECT position FROM queue_entries WHERE schedule_id=$1",
// 		scheduleID,
// 	)

// 	if err != nil {
// 		return nil, err
// 	}

// 	defer rows.Close()

// 	var busySlots []int

// 	for rows.Next() {
// 		var busySlot int
// 		rows.Scan(&busySlot)
// 		busySlots = append(busySlots, busySlot)
// 	}

// 	return busySlots, nil
// }
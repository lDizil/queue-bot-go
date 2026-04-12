package db

import (
	"context"
	"time"
)

func (db *DBRepository) GetAllSchedules(ctx context.Context) ([]Schedule, error) {
	schedules := []Schedule{}

	rows, err := db.pool.Query(ctx, 
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description, queue_message_id
		FROM schedules
		WHERE is_temporary = false`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var schedule Schedule
		
		err := rows.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.ThreadDescription, &schedule.QueueMesID)
		if err != nil {
			return nil, err
		}

		schedules = append(schedules, schedule)
	}

	return schedules, nil
}

func (db *DBRepository) GetScheduleEntry(ctx context.Context, scheduleID int) (Schedule, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description, queue_message_id
		FROM schedules
		WHERE id = $1`,
		scheduleID,
	)

	schedule := Schedule{}

	err := row.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.ThreadDescription, &schedule.QueueMesID)

	if err != nil {
		return schedule, err
	}

	return schedule, nil
}

func (db *DBRepository) AddNewScheduleEntry(ctx context.Context, schedule Schedule) (int, error) {
	var id int
	err := db.pool.QueryRow(ctx, 
		`INSERT INTO schedules (day_of_week, week_type, start_time, end_time, thread_id, thread_description)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		schedule.DayOfWeek, schedule.WeekType, schedule.StartTime, schedule.EndTime, schedule.ThreadID, schedule.ThreadDescription,
	).Scan(&id)

	if err != nil {
		return 0, err
	}

	return id, nil
}

func (db *DBRepository) DeleteScheduleEntry(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM schedules
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}
 
func (db *DBRepository) SetQueueMessageID(ctx context.Context, scheduleID int, messageID int) error {
	_, err := db.pool.Exec(ctx, 
		`UPDATE schedules
		SET queue_message_id = $1
		WHERE id = $2`,
		messageID, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ClearQueueMessageID(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx, 
		`UPDATE schedules
		SET queue_message_id = NULL
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeWeekType(ctx context.Context, scheduleID int, weekType string) error {
	_, err := db.pool.Exec(ctx, 
		`UPDATE schedules
		SET week_type = $1
		WHERE id = $2`,
		weekType, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeWeekDay(ctx context.Context, scheduleID int, weekDay string) error {
	_, err := db.pool.Exec(ctx, 
		`UPDATE schedules
		SET day_of_week = $1
		WHERE id = $2`,
		weekDay, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeStartTime(ctx context.Context, scheduleID int, startTime time.Time) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET start_time = $1
		WHERE id = $2`,
		startTime, scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeEndTime(ctx context.Context, scheduleID int, endTime time.Time) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET end_time = $1
		WHERE id = $2`,
		endTime, scheduleID,
	)
	
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeThreadID(ctx context.Context, scheduleID int, threadID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET thread_id = $1
		WHERE id = $2`,
		threadID, scheduleID,
	)
	
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ChangeDescription(ctx context.Context, scheduleID int, description string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET thread_description = $1
		WHERE id = $2`,
		description, scheduleID,
	)
	
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) ResetNotifications(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET notified_5min = false,  notified_1min = false, notified_open = false
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) SetNotified5min(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET notified_5min = true
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) SetNotified1min(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET notified_1min = true
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) SetNotifiedOpen(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE schedules
		SET notified_open = true
		WHERE id = $1`,
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) GetAllSchedulesWithNotifications(ctx context.Context) ([]Schedule, error) {
	schedules := []Schedule{}

	rows, err := db.pool.Query(ctx, 
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description, queue_message_id, notified_5min, notified_1min, notified_open
		FROM schedules
		WHERE is_temporary = false`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var schedule Schedule
		
		err := rows.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.ThreadDescription, &schedule.QueueMesID, &schedule.Notified5min, &schedule.Notified1min, &schedule.NotifiedOpen)
		if err != nil {
			return nil, err
		}

		schedules = append(schedules, schedule)
	}

	return schedules, nil
}

func (db *DBRepository) AddTemporarySchedule(ctx context.Context, schedule Schedule) (int, error) {
    var id int

    err := db.pool.QueryRow(ctx,
        `INSERT INTO schedules (day_of_week, week_type, start_time, end_time, thread_id, is_temporary)
        VALUES ('', '', $1, $2, $3, true)
        RETURNING id`,
        schedule.StartTime, schedule.EndTime, schedule.ThreadID,
    ).Scan(&id)

    if err != nil {
        return 0, err
    }

    return id, nil
}
package db

import (
	"context"
	"time"
)

func (db *DBRepository) GetAllSchedules(ctx context.Context) ([]Schedule, error) {
	schedules := []Schedule{}

	rows, err := db.pool.Query(ctx, 
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description
		FROM schedules`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var schedule Schedule
		
		err := rows.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.ThreadDescription)
		if err != nil {
			return nil, err
		}

		schedules = append(schedules, schedule)
	}

	return schedules, nil
}

func (db *DBRepository) GetScheduleEntry(ctx context.Context, scheduleID int) (Schedule, error) {
	row := db.pool.QueryRow(ctx,
		`SELECT id, day_of_week, week_type, start_time, end_time, thread_id, thread_description
		FROM schedules
		WHERE id = $1`,
		scheduleID,
	)

	schedule := Schedule{}

	err := row.Scan(&schedule.ID, &schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadID, &schedule.ThreadDescription)

	if err != nil {
		return schedule, err
	}

	return schedule, nil
}

func (db *DBRepository) AddNewScheduleEntry(ctx context.Context, schedule Schedule) error {
	_, err := db.pool.Exec(ctx, 
		`INSERT INTO schedules (day_of_week, week_type, start_time, end_time, thread_id, thread_description)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		schedule.DayOfWeek, schedule.WeekType, schedule.StartTime, schedule.EndTime, schedule.ThreadID, schedule.ThreadDescription,
	)

	if err != nil {
		return err
	}

	return nil
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
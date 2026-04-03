package db

import (
	"context"
)

func (db *DBRepository) GetAllSchedules(ctx context.Context) ([]Schedule, error) {
	schedules := []Schedule{}

	rows, err := db.pool.Query(ctx, 
		`SELECT day_of_week, week_type, start_time, end_time, thread_id, thread_description
		FROM schedules`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	for rows.Next() {
		var schedule Schedule
		
		err := rows.Scan(&schedule.DayOfWeek, &schedule.WeekType, &schedule.StartTime, &schedule.EndTime, &schedule.ThreadId, &schedule.ThreadDescription)
		if err != nil {
			return nil, err
		}

		schedules = append(schedules, schedule)
	}

	return schedules, nil
}

func (db *DBRepository) AddNewScheduleEntry(ctx context.Context, schedule Schedule) error {
	_, err := db.pool.Exec(ctx, 
		`INSERT INTO schedules (day_of_week, week_type, start_time, end_time, thread_id, thread_description)
		VALUES $1, $2, $3, $4, $5, $6`,
		schedule.DayOfWeek, schedule.WeekType, schedule.StartTime, schedule.EndTime, schedule.ThreadId, schedule.ThreadDescription,
	)

	if err != nil {
		return err
	}

	return nil
}

package db

import "time"

type Schedule struct {
	ID        int
	DayOfWeek string
	WeekType  string
	StartTime time.Time
	EndTime   time.Time
	ThreadId  int
}

type QueueEntry struct {
	ID         int
	UserId     int64
	Username   string
	ScheduleID int
	Position   int
}

package db

import "time"

type Schedule struct {
	ID        int
	DayOfWeek string
	WeekType  string
	StartTime time.Time
	EndTime   time.Time
	Notified5min bool
    Notified1min bool
    NotifiedOpen bool
	ThreadId  int
}

type QueueEntry struct {
	ID         int
	UserID     int64
	Username   string
	ScheduleID int
	Position   int
}

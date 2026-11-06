package db

import "time"

type Schedule struct {
	ID                int
	ChatID            int64
	Title             string
	DayOfWeek         string
	WeekType          string
	StartTime         time.Time
	EndTime           time.Time
	Notified5min      bool
	Notified1min      bool
	NotifiedOpen      bool
	ThreadID          int
	ThreadDescription *string
	QueueMesID        *int
	IsTemporary       bool
	ActiveOpensAt     *time.Time
	ActiveClosesAt    *time.Time
	ActiveThreadID    *int
	ActiveCapacity    int
	ActiveColumns     int
}

type QueueEntry struct {
	ID          int
	UserID      int64
	Username    string
	DisplayName string
	ScheduleID  int
	Position    int
	Completed   bool
}

type QueueBlock struct {
	ID           int
	UserID       int64
	Username     string
	DisplayName  string
	BlockedUntil time.Time
}

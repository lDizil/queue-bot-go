package utils

import (
	"time"
	_ "time/tzdata"
)

var moscow = func() *time.Location {
	location, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		panic(err)
	}
	return location
}()

func Moscow() *time.Location {
	return moscow
}

func WeekTypeForDate(date time.Time, week1Date time.Time, week1Type string) string {
	dateMonday := civilMonday(date)
	anchorMonday := civilMonday(week1Date)
	weeksSince := (dateMonday.Unix() - anchorMonday.Unix()) / (7 * 24 * 60 * 60)
	if weeksSince%2 == 0 {
		return week1Type
	}
	return SwitchWeekType[week1Type]
}

func civilMonday(date time.Time) time.Time {
	date = date.In(moscow)
	midnight := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(midnight.Weekday()) + 6) % 7
	return midnight.AddDate(0, 0, -offset)
}

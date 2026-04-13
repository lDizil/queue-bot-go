package utils

import "time"

func WeekTypeForDate(date time.Time, week1Date time.Time, week1Type string) string {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	w := time.Date(week1Date.Year(), week1Date.Month(), week1Date.Day(), 0, 0, 0, 0, time.UTC)

	days := int(d.Sub(w).Hours() / 24)
	weeksSince := days / 7

	if weeksSince%2 == 0 {
		return week1Type
	} else {
		return SwitchWeekType[week1Type]
	}
}

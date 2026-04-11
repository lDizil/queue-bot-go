package utils

import "time"

func WeekTypeForDate(date time.Time, week1Date time.Time, week1Type string) string {
	days := int(date.Sub(week1Date).Hours() / 24)
	weeksSince := days / 7

	if weeksSince%2 == 0 {
		return week1Type
	} else {
		return SwitchWeekType[week1Type]
	}
}

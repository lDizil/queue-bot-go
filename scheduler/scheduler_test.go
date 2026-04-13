package scheduler

import (
	"testing"
	"time"

	"queuebot/db"
)

// dayOfWeekToWeekDay
func TestDayOfWeekToWeekDay_AllDays(t *testing.T) {
	cases := []struct {
		input string
		want  time.Weekday
	}{
		{"monday", time.Monday},
		{"tuesday", time.Tuesday},
		{"wednesday", time.Wednesday},
		{"thursday", time.Thursday},
		{"friday", time.Friday},
		{"saturday", time.Saturday},
		{"sunday", time.Sunday},
	}

	for _, c := range cases {
		got, err := dayOfWeekToWeekDay(c.input)
		if err != nil {
			t.Errorf("dayOfWeekToWeekDay(%q): unexpected error: %v", c.input, err)
		}
		if got != c.want {
			t.Errorf("dayOfWeekToWeekDay(%q): got %v, want %v", c.input, got, c.want)
		}
	}
}

func TestDayOfWeekToWeekDay_Invalid(t *testing.T) {
	_, err := dayOfWeekToWeekDay("суббота")
	if err == nil {
		t.Error("expected error for unknown day, got nil")
	}
}

// nextOccurence
func makeTimeOnly(h, m int) time.Time {
	return time.Date(0, 1, 1, h, m, 0, 0, time.UTC)
}

func TestNextOccurence_CorrectWeekday(t *testing.T) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	targetDay := now.AddDate(0, 0, 2).Weekday()
	dayStr := weekdayToStr(targetDay)
	if dayStr == "" {
		t.Skip("не удалось вычислить тестовый день")
	}

	week1Date := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)

	sch := db.Schedule{
		DayOfWeek: dayStr,
		StartTime: makeTimeOnly(10, 0),
		EndTime:   makeTimeOnly(12, 0),
	}
	sch.WeekType = weekTypeForDate(now.AddDate(0, 0, 2), week1Date, "odd")

	result, err := nextOccurence(sch, week1Date, "odd")
	if err != nil {
		t.Fatalf("nextOccurence вернул ошибку: %v", err)
	}
	if result.Weekday() != targetDay {
		t.Errorf("got weekday %v, want %v", result.Weekday(), targetDay)
	}
}

func TestNextOccurence_TodayAlreadyEnded(t *testing.T) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	todayStr := weekdayToStr(now.Weekday())
	if todayStr == "" {
		t.Skip("нет маппинга для сегодняшнего дня")
	}

	week1Date := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)
	endHour := now.Add(-1 * time.Minute).Hour()
	endMin := now.Add(-1 * time.Minute).Minute()

	sch := db.Schedule{
		DayOfWeek: todayStr,
		WeekType:  weekTypeForDate(now, week1Date, "odd"),
		StartTime: makeTimeOnly(0, 0),
		EndTime:   makeTimeOnly(endHour, endMin),
	}

	result, _ := nextOccurence(sch, week1Date, "odd")

	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, moscow)
	if result.Equal(todayMidnight) || result.Before(todayMidnight) {
		t.Errorf("ожидался будущий день, получили %v", result)
	}
}

func TestNextOccurence_NoMatch_ReturnsError(t *testing.T) {
	week1Date := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)

	sch := db.Schedule{
		DayOfWeek: "invalid_day",
		WeekType:  "odd",
		StartTime: makeTimeOnly(10, 0),
		EndTime:   makeTimeOnly(12, 0),
	}

	_, err := nextOccurence(sch, week1Date, "odd")
	if err == nil {
		t.Error("ожидалась ошибка для неверного дня недели")
	}
}

func weekdayToStr(d time.Weekday) string {
	m := map[time.Weekday]string{
		time.Monday:    "monday",
		time.Tuesday:   "tuesday",
		time.Wednesday: "wednesday",
		time.Thursday:  "thursday",
		time.Friday:    "friday",
		time.Saturday:  "saturday",
		time.Sunday:    "sunday",
	}
	return m[d]
}

// weekTypeForDate (дублирует логику utils.WeekTypeForDate чтобы не делать import)
func weekTypeForDate(date time.Time, week1Date time.Time, week1Type string) string {
	d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	w := time.Date(week1Date.Year(), week1Date.Month(), week1Date.Day(), 0, 0, 0, 0, time.UTC)
	days := int(d.Sub(w).Hours() / 24)
	if (days/7)%2 == 0 {
		return week1Type
	}
	if week1Type == "odd" {
		return "even"
	}
	return "odd"
}

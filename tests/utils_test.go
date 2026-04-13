package tests

import (
	"testing"
	"time"

	"queuebot/utils"
)

var (
	week1Date = time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)
	week1Type = "odd"
)

func TestWeekTypeForDate_SameWeek(t *testing.T) {
	date := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)
	got := utils.WeekTypeForDate(date, week1Date, week1Type)
	if got != "odd" {
		t.Errorf("тот же день: got %q, want %q", got, "odd")
	}

	date = time.Date(2025, 2, 14, 0, 0, 0, 0, time.UTC)
	got = utils.WeekTypeForDate(date, week1Date, week1Type)
	if got != "odd" {
		t.Errorf("пятница той же недели: got %q, want %q", got, "odd")
	}
}

func TestWeekTypeForDate_NextWeek(t *testing.T) {
	date := time.Date(2025, 2, 17, 0, 0, 0, 0, time.UTC)
	got := utils.WeekTypeForDate(date, week1Date, week1Type)
	if got != "even" {
		t.Errorf("+7 дней: got %q, want %q", got, "even")
	}
}

func TestWeekTypeForDate_TwoWeeksLater(t *testing.T) {
	date := time.Date(2025, 2, 24, 0, 0, 0, 0, time.UTC)
	got := utils.WeekTypeForDate(date, week1Date, week1Type)
	if got != "odd" {
		t.Errorf("+14 дней: got %q, want %q", got, "odd")
	}
}

func TestWeekTypeForDate_BeforeWeek1(t *testing.T) {
	date := time.Date(2025, 2, 3, 0, 0, 0, 0, time.UTC)
	got := utils.WeekTypeForDate(date, week1Date, week1Type)
	if got != "even" {
		t.Errorf("-7 дней: got %q, want %q", got, "even")
	}
}

func TestWeekTypeForDate_TimezoneNormalization(t *testing.T) {
	moscow, _ := time.LoadLocation("Europe/Moscow")

	dateMSK := time.Date(2026, 4, 13, 0, 0, 0, 0, moscow)
	dateUTC := time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC)

	gotMSK := utils.WeekTypeForDate(dateMSK, week1Date, week1Type)
	gotUTC := utils.WeekTypeForDate(dateUTC, week1Date, week1Type)

	if gotMSK != gotUTC {
		t.Errorf("нормализация timezone: MSK=%q UTC=%q (должны совпадать)", gotMSK, gotUTC)
	}
}

func TestWeekTypeForDate_Week1TypeEven(t *testing.T) {
	date := time.Date(2025, 2, 10, 0, 0, 0, 0, time.UTC)
	got := utils.WeekTypeForDate(date, week1Date, "even")
	if got != "even" {
		t.Errorf("week1=even, тот же день: got %q, want %q", got, "even")
	}

	date = time.Date(2025, 2, 17, 0, 0, 0, 0, time.UTC)
	got = utils.WeekTypeForDate(date, week1Date, "even")
	if got != "odd" {
		t.Errorf("week1=even, +7 дней: got %q, want %q", got, "odd")
	}
}

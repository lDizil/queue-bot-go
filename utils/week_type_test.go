package utils

import (
	"testing"
	"time"
)

func TestWeekTypeUsesMondayBoundariesAndMoscowDates(t *testing.T) {
	anchor := time.Date(2026, time.April, 10, 0, 0, 0, 0, Moscow())
	tests := []struct {
		name string
		date string
		want string
	}{
		{"monday before friday anchor", "2026-04-06T00:00:00+03:00", "odd"},
		{"sunday in anchor week", "2026-04-12T23:59:59+03:00", "odd"},
		{"next monday", "2026-04-13T00:00:00+03:00", "even"},
		{"previous sunday", "2026-04-05T23:59:59+03:00", "even"},
		{"previous monday", "2026-03-30T00:00:00+03:00", "even"},
		{"two weeks before", "2026-03-23T00:00:00+03:00", "odd"},
		{"utc sunday before moscow monday", "2026-04-12T20:59:59Z", "odd"},
		{"utc sunday at moscow monday", "2026-04-12T21:00:00Z", "even"},
		{"calendar year boundary", "2025-12-29T00:00:00+03:00", "odd"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			date, err := time.Parse(time.RFC3339, test.date)
			if err != nil {
				t.Fatal(err)
			}
			if got := WeekTypeForDate(date, anchor, "odd"); got != test.want {
				t.Fatalf("WeekTypeForDate(%s) = %s, want %s", test.date, got, test.want)
			}
		})
	}
}

func TestWeekTypeIsIndependentOfTimezoneRepresentation(t *testing.T) {
	var (
		anchor   = time.Date(2026, time.April, 10, 0, 0, 0, 0, Moscow())
		nextWeek = time.Date(2026, time.April, 13, 0, 0, 0, 0, Moscow())
	)
	for _, date := range []time.Time{nextWeek, nextWeek.UTC(), nextWeek.In(time.FixedZone("west", -8*60*60))} {
		if got := WeekTypeForDate(date, anchor.UTC(), "even"); got != "odd" {
			t.Fatalf("same instant %s has week type %s, want odd", date, got)
		}
	}
}

func TestWeekTypeDoesNotCountHistoricalDaylightSavingHoursAsDays(t *testing.T) {
	var (
		anchor   = time.Date(2010, time.March, 22, 0, 0, 0, 0, Moscow())
		nextWeek = time.Date(2010, time.March, 29, 0, 0, 0, 0, Moscow())
	)
	if got := WeekTypeForDate(nextWeek, anchor, "odd"); got != "even" {
		t.Fatalf("week following timezone transition = %s, want even", got)
	}
}

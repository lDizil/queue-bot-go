package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"queuebot/db"
)

type mockScheduleStore struct {
	scheduleByID map[int]db.Schedule
	getErr       error

	resetCalls  int
	getCalls    int
	deleteCalls int
	clearCalls  int
}

func (m *mockScheduleStore) SetNotified5min(ctx context.Context, scheduleID int) error {
	return nil
}

func (m *mockScheduleStore) SetNotified1min(ctx context.Context, scheduleID int) error {
	return nil
}

func (m *mockScheduleStore) SetNotifiedOpen(ctx context.Context, scheduleID int) error {
	return nil
}

func (m *mockScheduleStore) DeleteScheduleEntry(ctx context.Context, scheduleID int) error {
	m.deleteCalls++
	return nil
}

func (m *mockScheduleStore) ClearQueueMessageID(ctx context.Context, scheduleID int) error {
	m.clearCalls++
	return nil
}

func (m *mockScheduleStore) ResetNotifications(ctx context.Context, scheduleID int) error {
	m.resetCalls++
	return nil
}

func (m *mockScheduleStore) GetScheduleEntry(ctx context.Context, scheduleID int) (db.Schedule, error) {
	m.getCalls++
	if m.getErr != nil {
		return db.Schedule{}, m.getErr
	}

	schedule, ok := m.scheduleByID[scheduleID]
	if !ok {
		return db.Schedule{}, errors.New("schedule not found")
	}

	return schedule, nil
}

func (m *mockScheduleStore) AddTemporarySchedule(ctx context.Context, schedule db.Schedule) (int, error) {
	return 0, nil
}

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

func TestPrepareNextScheduleAfterClose_UsesFreshScheduleFromDB(t *testing.T) {
	store := &mockScheduleStore{
		scheduleByID: map[int]db.Schedule{
			42: {
				ID:           42,
				ThreadID:     25111,
				Notified5min: false,
				Notified1min: false,
				NotifiedOpen: false,
			},
		},
	}

	s := NewScheduler(context.Background(), store, nil, nil, 0, time.Now(), "odd", time.Second)

	stale := db.Schedule{
		ID:           42,
		ThreadID:     27543,
		Notified5min: true,
		Notified1min: true,
		NotifiedOpen: true,
	}

	got, shouldSchedule := s.prepareNextScheduleAfterClose(context.Background(), stale)

	if !shouldSchedule {
		t.Fatal("ожидалось перепланирование, получено shouldSchedule=false")
	}

	if got.ThreadID != 25111 {
		t.Fatalf("ожидался thread_id из БД 25111, получено %d", got.ThreadID)
	}

	if store.resetCalls != 1 {
		t.Fatalf("ResetNotifications вызван %d раз(а), ожидалось 1", store.resetCalls)
	}

	if store.getCalls != 1 {
		t.Fatalf("GetScheduleEntry вызван %d раз(а), ожидалось 1", store.getCalls)
	}
}

func TestPrepareNextScheduleAfterClose_FallbackOnGetError(t *testing.T) {
	store := &mockScheduleStore{getErr: errors.New("db unavailable")}
	s := NewScheduler(context.Background(), store, nil, nil, 0, time.Now(), "odd", time.Second)

	stale := db.Schedule{
		ID:           7,
		ThreadID:     27543,
		Notified5min: true,
		Notified1min: true,
		NotifiedOpen: true,
	}

	got, shouldSchedule := s.prepareNextScheduleAfterClose(context.Background(), stale)

	if !shouldSchedule {
		t.Fatal("ожидалось перепланирование, получено shouldSchedule=false")
	}

	if got.ThreadID != stale.ThreadID {
		t.Fatalf("ожидался fallback на старый thread_id %d, получено %d", stale.ThreadID, got.ThreadID)
	}

	if got.Notified5min || got.Notified1min || got.NotifiedOpen {
		t.Fatal("в fallback-расписании флаги уведомлений должны быть сброшены")
	}

	if store.resetCalls != 1 {
		t.Fatalf("ResetNotifications вызван %d раз(а), ожидалось 1", store.resetCalls)
	}
}

func TestPrepareNextScheduleAfterClose_TemporaryDoesNotReschedule(t *testing.T) {
	store := &mockScheduleStore{}
	s := NewScheduler(context.Background(), store, nil, nil, 0, time.Now(), "odd", time.Second)

	temporary := db.Schedule{ID: 55, IsTemporary: true}

	_, shouldSchedule := s.prepareNextScheduleAfterClose(context.Background(), temporary)

	if shouldSchedule {
		t.Fatal("временная запись не должна перепланироваться")
	}

	if store.deleteCalls != 1 {
		t.Fatalf("DeleteScheduleEntry вызван %d раз(а), ожидалось 1", store.deleteCalls)
	}

	if store.clearCalls != 1 {
		t.Fatalf("ClearQueueMessageID вызван %d раз(а), ожидалось 1", store.clearCalls)
	}

	if store.resetCalls != 0 {
		t.Fatalf("ResetNotifications не должен вызываться для временной записи, сейчас: %d", store.resetCalls)
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

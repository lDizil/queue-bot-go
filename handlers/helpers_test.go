package handlers

import (
	"testing"
	"time"

	"queuebot/db"
)

// isValidData
func TestIsValidData_Valid(t *testing.T) {
	h := &BotHandler{}

	cases := [][]string{
		{"понедельник", "чётная", "10:00", "12:00", "123"},
		{"monday", "even", "09:30", "11:30", "0"},
		{"пятница", "нечётная", "08:00:00", "10:00:00", "456"},
		{"friday", "odd", "18:00", "20:00", "1"},
		{"среда", "четная", "14:00", "16:00", "42", "описание"},
	}

	for _, data := range cases {
		if !h.isValidData(data) {
			t.Errorf("isValidData(%v) = false, want true", data)
		}
	}
}

func TestIsValidData_Invalid(t *testing.T) {
	h := &BotHandler{}

	cases := []struct {
		name string
		data []string
	}{
		{"пустой слайс", []string{}},
		{"слишком мало полей", []string{"понедельник", "чётная", "10:00"}},
		{"слишком много полей", []string{"понедельник", "чётная", "10:00", "12:00", "1", "desc", "extra"}},
		{"неверный день", []string{"воскресение_опечатка", "чётная", "10:00", "12:00", "1"}},
		{"неверный тип недели", []string{"понедельник", "какаяТо", "10:00", "12:00", "1"}},
		{"неверный формат времени начала", []string{"понедельник", "чётная", "25:00", "12:00", "1"}},
		{"неверный формат времени конца", []string{"понедельник", "чётная", "10:00", "abc", "1"}},
		{"thread_id не число", []string{"понедельник", "чётная", "10:00", "12:00", "abc"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if h.isValidData(c.data) {
				t.Errorf("isValidData(%v) = true, want false", c.data)
			}
		})
	}
}

// isQueueOpen
func makeScheduleWithTimes(startH, startM, endH, endM int) db.Schedule {
	return db.Schedule{
		StartTime: time.Date(0, 1, 1, startH, startM, 0, 0, time.UTC),
		EndTime:   time.Date(0, 1, 1, endH, endM, 0, 0, time.UTC),
	}
}

func TestIsQueueOpen_WideWindow_AlwaysOpen(t *testing.T) {
	h := &BotHandler{}
	sch := makeScheduleWithTimes(0, 1, 23, 58)
	if !h.isQueueOpen(sch) {
		t.Error("ожидалась открытая очередь для окна 00:01–23:58")
	}
}

func TestIsQueueOpen_ZeroWindow_AlwaysClosed(t *testing.T) {
	h := &BotHandler{}
	sch := makeScheduleWithTimes(0, 0, 0, 0)
	if h.isQueueOpen(sch) {
		t.Error("ожидалась закрытая очередь при start == end")
	}
}

func TestIsQueueOpen_MidnightCrossing_DoesNotPanic(t *testing.T) {
	h := &BotHandler{}
	sch := makeScheduleWithTimes(22, 0, 2, 0)
	_ = h.isQueueOpen(sch)
}

package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"queuebot/db"

	"github.com/go-telegram/bot"
)

var switchWeekType = map[string]string{
	"even": "odd",
	"odd":  "even",
}

type QueueSender interface {
	SendScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) (int, error)
	EditScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, messageID int, scheduleID int, isOpen bool) error
	ClearScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, messageID int, scheduleID int) error
}

func dayOfWeekToWeekDay(day string) (time.Weekday, error) {
	switch day {
	case "monday":
		return time.Monday, nil
	case "tuesday":
		return time.Tuesday, nil
	case "wednesday":
		return time.Wednesday, nil
	case "thursday":
		return time.Thursday, nil
	case "friday":
		return time.Friday, nil
	case "saturday":
		return time.Saturday, nil
	case "sunday":
		return time.Sunday, nil
	default:
		return time.Sunday, fmt.Errorf("Неизвестный день недели: %q", day)
	}
}

func weekTypeForDate(date time.Time, week1Date time.Time, week1Type string) string {
	days := int(date.Sub(week1Date).Hours() / 24)
	weeksSince := days / 7

	if weeksSince%2 == 0 {
		return week1Type
	} else {
		return switchWeekType[week1Type]
	}
}

func nextOccurence(schedule db.Schedule, week1Date time.Time, week1Type string) (time.Time, error) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, moscow)

	schDay, err := dayOfWeekToWeekDay(schedule.DayOfWeek)
	if err != nil {
		return time.Time{}, err
	}

	for i := 0; i < 14; i++ {
		candidate := today.AddDate(0, 0, i)

		if candidate.Weekday() != schDay {
			continue
		}

		curWeekType := weekTypeForDate(candidate, week1Date, week1Type)

		if curWeekType != schedule.WeekType {
			continue
		}

		return candidate, nil
	}

	return today.AddDate(0, 0, 14), fmt.Errorf("Не ближайшего начала для записи с днём %q и типом недели %q", schedule.DayOfWeek, schedule.WeekType)
}

type scheduleTimer struct {
	timer  *time.Timer
	cancel context.CancelFunc
}

type Scheduler struct {
	db     *db.DBRepository
	b      *bot.Bot
	sender QueueSender
	chatID int64

	week1Date time.Time
	week1Type string

	timers map[int]*scheduleTimer
	mu     sync.Mutex

	tickInterval time.Duration
}

func NewScheduler(db *db.DBRepository, b *bot.Bot, sender QueueSender, chatID int64, week1Date time.Time, week1Type string, tickInterval time.Duration) *Scheduler {
	return &Scheduler{
		db:        db,
		b:         b,
		sender:    sender,
		chatID:    chatID,
		week1Date: week1Date,
		week1Type: week1Type,
		timers:    make(map[int]*scheduleTimer),
		tickInterval: tickInterval,
	}
}

func (s *Scheduler) RemoveSchedule(scheduleID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.timers[scheduleID]
	if !ok {
		return
	}

	t.timer.Stop()
	t.cancel()
	delete(s.timers, scheduleID)
}

func (s *Scheduler) scheduleNext(ctx context.Context, schedule db.Schedule) {
	nextDate, err := nextOccurence(schedule, s.week1Date, s.week1Type)
	if err != nil {
		log.Printf("scheduleNext: ошибка для расписания %d: %v", schedule.ID, err)
		return
	}

	duration := time.Until(nextDate)

	workerCtx, cancel := context.WithCancel(ctx)

	timer := time.AfterFunc(duration, func() {
		s.runDayWorker(workerCtx, schedule, nextDate)
	})

	s.mu.Lock()

	if old, ok := s.timers[schedule.ID]; ok {
		old.timer.Stop()
		old.cancel()
	}

	s.timers[schedule.ID] = &scheduleTimer{
		timer:  timer,
		cancel: cancel,
	}

	s.mu.Unlock()
}

func (s *Scheduler) runDayWorker(ctx context.Context, schedule db.Schedule, date time.Time) {
	moscow, _ := time.LoadLocation("Europe/Moscow")

	start := time.Date(date.Year(), date.Month(), date.Day(),
		schedule.StartTime.Hour(), schedule.StartTime.Minute(), 0, 0, moscow)
	
	end := time.Date(date.Year(), date.Month(), date.Day(),
		schedule.EndTime.Hour(), schedule.EndTime.Minute(), 0, 0, moscow)

    t5min  := start.Add(-5 * time.Minute)
    t1min  := start.Add(-1 * time.Minute)
    tOpen  := start
    tClose := end

    fired := map[string]bool{
        "5min":  schedule.Notified5min,
        "1min":  schedule.Notified1min,
        "open":  schedule.NotifiedOpen,
        "close": false,
    }

	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <- ctx.Done():
			return
		case now := <-ticker.C:
			now = now.In(moscow)

			if !fired["5min"] && !now.Before(t5min) {
				fired["5min"] = true
			}

			if !fired["1min"] && !now.Before(t1min) {
				fired["1min"] = true
			}

			if !fired["open"] && !now.Before(tOpen) {
				fired["open"] = true
			}

			if !fired["close"] && !now.Before(tClose) {
				fired["close"] = true

				s.scheduleNext(ctx, schedule)
                return
			}
		}
	}
}
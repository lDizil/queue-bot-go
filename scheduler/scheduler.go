package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"queuebot/db"
	u "queuebot/utils"

	"github.com/go-telegram/bot"
)

type QueueSender interface {
	SendNotification5min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error
    SendNotification1min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error
    SendScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int, statusQueue u.QueueStatus) (int, error)
    EditScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error
    ClearScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error
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
		return u.SwitchWeekType[week1Type]
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

	for i := range 14 {
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
		db:           db,
		b:            b,
		sender:       sender,
		chatID:       chatID,
		week1Date:    week1Date,
		week1Type:    week1Type,
		timers:       make(map[int]*scheduleTimer),
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

func (s *Scheduler) ScheduleNext(ctx context.Context, schedule db.Schedule) {
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

	t5min := start.Add(-5 * time.Minute)
	t1min := start.Add(-1 * time.Minute)
	tOpen := start
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
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			now = now.In(moscow)

			if !fired["5min"] && !now.Before(t5min) {
				err := s.sender.SendNotification5min(ctx, s.b, s.chatID, schedule.ThreadID, schedule.ID)
				if err != nil {
					log.Printf("ошибка runDayWorker: %v", err)
				} else {
					fired["5min"] = true
					s.db.SetNotified5min(ctx, schedule.ID)
				}
			}

			if !fired["1min"] && !now.Before(t1min) {
				err := s.sender.SendNotification1min(ctx, s.b, s.chatID, schedule.ThreadID, schedule.ID)
				if err != nil {
					log.Printf("ошибка runDayWorker: %v", err)
				} else {
					_, err := s.sender.SendScheduledQueue(ctx, s.b, s.chatID, schedule.ThreadID, schedule.ID, u.QueuePending) 
					if err != nil {
						log.Printf("ошибка runDayWorker: %v", err)
					} else {
						fired["1min"] = true
            			s.db.SetNotified1min(ctx, schedule.ID)
					}
				}
			}

			if !fired["open"] && !now.Before(tOpen) {
				err := s.sender.EditScheduledQueue(ctx, s.b, s.chatID, schedule.ID, u.QueueOpen) 
				if err != nil {
					log.Printf("ошибка runDayWorker: %v", err)
				} else {
					fired["open"] = true
				}
			}

			if !fired["close"] && !now.Before(tClose) {
				err := s.sender.ClearScheduledQueue(ctx, s.b, s.chatID, schedule.ID, u.QueueClosed) 
				if err != nil {
					log.Printf("ошибка runDayWorker: %v", err)
				} else {
					fired["close"] = true
					s.db.ResetNotifications(ctx, schedule.ID)
					schedule.Notified5min = false
					schedule.Notified1min = false
					schedule.NotifiedOpen = false
					s.ScheduleNext(ctx, schedule)
					return
				}
			}
		}
	}
}

package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"queuebot/db"
	u "queuebot/utils"
	_ "time/tzdata"

	"github.com/go-telegram/bot"
)

type QueueSender interface {
	SendNotification5min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error
	SendNotification1min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error
	SendScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, threadID int, statusQueue u.QueueStatus) (int, error)
	EditScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error
	ClearScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error
}

type ScheduleStore interface {
	SetNotified5min(ctx context.Context, scheduleID int) error
	SetNotified1min(ctx context.Context, scheduleID int) error
	SetNotifiedOpen(ctx context.Context, scheduleID int) error
	DeleteScheduleEntry(ctx context.Context, scheduleID int) error
	ClearQueueMessageID(ctx context.Context, scheduleID int) error
	ResetNotifications(ctx context.Context, scheduleID int) error
	GetScheduleEntry(ctx context.Context, scheduleID int) (db.Schedule, error)
	AddTemporarySchedule(ctx context.Context, schedule db.Schedule) (int, error)
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

func nextOccurence(schedule db.Schedule, week1Date time.Time, week1Type string) (time.Time, error) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, moscow)

	schDay, err := dayOfWeekToWeekDay(schedule.DayOfWeek)
	if err != nil {
		return time.Time{}, err
	}

	for i := range 15 {
		candidate := today.AddDate(0, 0, i)

		if candidate.Weekday() != schDay {
			continue
		}

		curWeekType := u.WeekTypeForDate(candidate, week1Date, week1Type)

		if curWeekType != schedule.WeekType {
			continue
		}

		if i == 0 {
			endTime := time.Date(today.Year(), today.Month(), today.Day(),
				schedule.EndTime.Hour(), schedule.EndTime.Minute(), 0, 0, moscow)
			if now.After(endTime) {
				continue
			}
		}

		return candidate, nil
	}

	return today.AddDate(0, 0, 15), fmt.Errorf("Нет ближайшего начала для записи с днём %q и типом недели %q", schedule.DayOfWeek, schedule.WeekType)
}

type scheduleTimer struct {
	timer  *time.Timer
	cancel context.CancelFunc
}

type Scheduler struct {
	db     ScheduleStore
	b      *bot.Bot
	sender QueueSender
	chatID int64

	ctx context.Context

	week1Date time.Time
	week1Type string

	timers map[int]*scheduleTimer
	mu     sync.Mutex

	tickInterval time.Duration
}

func NewScheduler(ctx context.Context, db ScheduleStore, b *bot.Bot, sender QueueSender, chatID int64, week1Date time.Time, week1Type string, tickInterval time.Duration) *Scheduler {
	return &Scheduler{
		db:           db,
		b:            b,
		sender:       sender,
		chatID:       chatID,
		ctx:          ctx,
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

func (s *Scheduler) ScheduleNext(schedule db.Schedule) {
	nextDate, err := nextOccurence(schedule, s.week1Date, s.week1Type)
	if err != nil {
		log.Printf("scheduleNext: ошибка для расписания %d: %v", schedule.ID, err)
		return
	}

	duration := time.Until(nextDate)
	log.Printf("[sched] запись %d запланирована на %s", schedule.ID, nextDate.Format("2006-01-02"))

	workerCtx, cancel := context.WithCancel(s.ctx)

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

	if end.Before(start) {
		end = end.AddDate(0, 0, 1)
	}

	t5min := start.Add(-5 * time.Minute)
	t1min := start.Add(-1 * time.Minute)
	tOpen := start
	tClose := end

	now := time.Now().In(moscow)

	fired := map[string]bool{
		"5min":       schedule.Notified5min || now.After(t5min),
		"1min_notif": schedule.Notified1min || now.After(t1min),
		"1min":       schedule.Notified1min || now.After(t1min),
		"open":       schedule.NotifiedOpen || now.After(tOpen),
		"close":      now.After(tClose),
	}

	log.Printf("[sched] воркер запущен: запись %d, старт %s, конец %s",
		schedule.ID, start.Format("15:04"), end.Format("15:04"))

	ticker := time.NewTicker(s.tickInterval)
	defer ticker.Stop()

	retries := map[string]int{}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[sched] воркер остановлен: запись %d", schedule.ID)
			return
		case now := <-ticker.C:
			now = now.In(moscow)

			if !fired["5min"] && !now.Before(t5min) {
				err := s.sender.SendNotification5min(ctx, s.b, s.chatID, schedule.ThreadID, schedule.ID)
				if err != nil {
					retries["5min"]++
					if retries["5min"] >= 3 {
						fired["5min"] = true
					}
					log.Printf("[sched] ошибка уведомления за 5 мин (запись %d): %v", schedule.ID, err)
				} else {
					fired["5min"] = true
					log.Printf("[sched] уведомление за 5 мин отправлено (запись %d)", schedule.ID)
					s.db.SetNotified5min(ctx, schedule.ID)
				}
			}

			if !fired["1min_notif"] && !now.Before(t1min) {
				err := s.sender.SendNotification1min(ctx, s.b, s.chatID, schedule.ThreadID, schedule.ID)
				if err != nil {
					log.Printf("[sched] ошибка уведомления за 1 мин (запись %d): %v", schedule.ID, err)
					retries["1min_notif"]++
					if retries["1min_notif"] >= 3 {
						fired["1min_notif"] = true
					}
				} else {
					fired["1min_notif"] = true
					log.Printf("[sched] уведомление за 1 мин отправлено (запись %d)", schedule.ID)
					s.db.SetNotified1min(ctx, schedule.ID)
				}
			}

			if !fired["1min"] && fired["1min_notif"] && !now.Before(t1min) {
				_, err := s.sender.SendScheduledQueue(ctx, s.b, s.chatID, schedule.ID, schedule.ThreadID, u.QueuePending)
				if err != nil {
					log.Printf("[sched] ошибка отправки очереди (запись %d): %v", schedule.ID, err)
					retries["1min"]++
					if retries["1min"] >= 3 {
						fired["1min"] = true
					}
				} else {
					fired["1min"] = true
					log.Printf("[sched] очередь отправлена (запись %d)", schedule.ID)
					s.db.SetNotified1min(ctx, schedule.ID)
				}
			}

			if !fired["open"] && !now.Before(tOpen) {
				err := s.sender.EditScheduledQueue(ctx, s.b, s.chatID, schedule.ID, u.QueueOpen)
				if err != nil {
					log.Printf("[sched] ошибка открытия очереди (запись %d): %v", schedule.ID, err)
					retries["open"]++
					if retries["open"] >= 3 {
						fired["open"] = true
					}
				} else {
					fired["open"] = true
					log.Printf("[sched] очередь открыта (запись %d)", schedule.ID)
					s.db.SetNotifiedOpen(ctx, schedule.ID)
				}
			}

			if !fired["close"] && !now.Before(tClose) {
				err := s.sender.ClearScheduledQueue(ctx, s.b, s.chatID, schedule.ID, u.QueueClosed)
				if err != nil {
					log.Printf("[sched] ошибка закрытия очереди (запись %d): %v", schedule.ID, err)
					retries["close"]++
					if retries["close"] >= 3 {
						fired["close"] = true
					}
				} else {
					fired["close"] = true
					log.Printf("[sched] очередь закрыта (запись %d)", schedule.ID)

					nextSchedule, shouldSchedule := s.prepareNextScheduleAfterClose(ctx, schedule)
					if shouldSchedule {
						s.ScheduleNext(nextSchedule)
					}
					return
				}
			}
		}
	}
}

func (s *Scheduler) prepareNextScheduleAfterClose(ctx context.Context, schedule db.Schedule) (db.Schedule, bool) {
	if schedule.IsTemporary {
		if err := s.db.DeleteScheduleEntry(ctx, schedule.ID); err != nil {
			log.Printf("[sched] ошибка удаления временной записи (запись %d): %v", schedule.ID, err)
		}

		if err := s.db.ClearQueueMessageID(ctx, schedule.ID); err != nil {
			log.Printf("[sched] ошибка очистки queue_message_id (запись %d): %v", schedule.ID, err)
		}

		return db.Schedule{}, false
	}

	if err := s.db.ResetNotifications(ctx, schedule.ID); err != nil {
		log.Printf("[sched] ошибка сброса флагов уведомлений (запись %d): %v", schedule.ID, err)
	}

	nextSchedule, err := s.db.GetScheduleEntry(ctx, schedule.ID)
	if err != nil {
		log.Printf("[sched] ошибка получения актуальной записи для перепланирования (запись %d): %v", schedule.ID, err)
		nextSchedule = schedule
		nextSchedule.Notified5min = false
		nextSchedule.Notified1min = false
		nextSchedule.NotifiedOpen = false
	}

	return nextSchedule, true
}

func (s *Scheduler) RunInstant(ctx context.Context, threadID int) {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	startMsk := now.Add(6*time.Minute)
	endMsk := now.Add(6*time.Minute + 90*time.Minute)

	startTimeOnly := time.Date(0, 1, 1, startMsk.Hour(), startMsk.Minute(), 0, 0, time.UTC)
	endTimeOnly := time.Date(0, 1, 1, endMsk.Hour(), endMsk.Minute(), 0, 0, time.UTC)

	schedule := db.Schedule{
		ThreadID:    threadID,
		StartTime:   startTimeOnly,
		EndTime:     endTimeOnly,
		IsTemporary: true,
	}

	id, err := s.db.AddTemporarySchedule(ctx, schedule)
	if err != nil {
		log.Printf("RunInstant: ошибка создания временной записи: %v", err)
		return
	}

	schedule.ID = id

	workerCtx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.timers[id] = &scheduleTimer{
		timer:  time.AfterFunc(0, func() {}),
		cancel: cancel,
	}
	s.mu.Unlock()

	go s.runDayWorker(workerCtx, schedule, now)
}

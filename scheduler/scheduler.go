package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"queuebot/db"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/jackc/pgx/v5"
)

type QueueController interface {
	Prepare(context.Context, int, time.Time, time.Time) error
	CancelPending(context.Context, int, time.Time, time.Time) error
	LegacyMessageDate(context.Context, int) (time.Time, error)
	Refresh(context.Context, int) error
	Close(context.Context, int) error
	Notify(context.Context, db.Schedule, string) error
}

type ScheduleStore interface {
	GetStartupSchedules(context.Context) ([]db.Schedule, error)
	GetScheduleEntry(context.Context, int) (db.Schedule, error)
	SyncQueueState(context.Context, int) (db.Schedule, error)
	AddTemporarySchedule(context.Context, db.Schedule) (int, error)
	SetLegacyQueueRunBounds(context.Context, int, time.Time, time.Time, int, int) (db.Schedule, error)
	SetNotified5min(context.Context, int) error
	SetNotified1min(context.Context, int) error
	SetNotifiedOpen(context.Context, int) error
}

type schedulerClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type scheduleWorker struct {
	cancel     context.CancelFunc
	done       chan struct{}
	active     bool
	runKey     string
	reconciled bool
	delivered5 bool
	delivered1 bool
}

type Scheduler struct {
	db          ScheduleStore
	controller  QueueController
	cfg         *settings.Manager
	ctx         context.Context
	clock       schedulerClock
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	workers     map[int]*scheduleWorker
}

func NewScheduler(ctx context.Context, store ScheduleStore, controller QueueController, cfg *settings.Manager) *Scheduler {
	return &Scheduler{db: store, controller: controller, cfg: cfg, ctx: ctx, clock: realClock{}, workers: make(map[int]*scheduleWorker)}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	schedules, err := s.db.GetStartupSchedules(ctx)
	if err != nil {
		return err
	}
	warnConflictingRuns(schedules)
	for _, schedule := range schedules {
		s.scheduleNext(schedule)
	}
	return nil
}

func (s *Scheduler) ScheduleNext(schedule db.Schedule) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.scheduleNext(schedule)
}

func (s *Scheduler) scheduleNext(schedule db.Schedule) {
	s.mu.Lock()
	if existing := s.workers[schedule.ID]; existing != nil && (existing.active || schedule.ActiveOpensAt != nil) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.stopWorker(schedule.ID)
	if s.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	worker := &scheduleWorker{cancel: cancel, done: make(chan struct{}), active: schedule.ActiveOpensAt != nil || schedule.QueueMesID != nil}
	s.mu.Lock()
	s.workers[schedule.ID] = worker
	s.mu.Unlock()
	go s.run(ctx, schedule.ID, worker)
}

func (s *Scheduler) stopWorker(scheduleID int) {
	s.mu.Lock()
	worker := s.workers[scheduleID]
	if worker != nil {
		worker.cancel()
		delete(s.workers, scheduleID)
	}
	s.mu.Unlock()
	if worker != nil && worker.done != nil {
		<-worker.done
	}
}

func (s *Scheduler) RemoveSchedule(scheduleID int) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopWorker(scheduleID)
}

func (s *Scheduler) Reconfigure() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	schedules, err := s.db.GetStartupSchedules(s.ctx)
	if err != nil {
		return err
	}
	var (
		errs    []error
		restart []db.Schedule
	)
	present := make(map[int]bool, len(schedules))
	for _, schedule := range schedules {
		present[schedule.ID] = true
		keep, err := s.keepRun(schedule)
		if err != nil {
			errs = append(errs, err)
		}
		if keep {
			s.scheduleNext(schedule)
			continue
		}
		s.stopWorker(schedule.ID)
		fresh, err := s.reconfigurePending(schedule.ID)
		if err != nil {
			errs = append(errs, err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if fresh.ID == 0 {
			fresh = schedule
		}
		restart = append(restart, fresh)
	}

	s.mu.Lock()
	var removed []int
	for id := range s.workers {
		if !present[id] {
			removed = append(removed, id)
		}
	}
	s.mu.Unlock()
	for _, id := range removed {
		s.stopWorker(id)
	}
	for _, schedule := range restart {
		s.scheduleNext(schedule)
	}
	return errors.Join(errs...)
}

func (s *Scheduler) keepRun(schedule db.Schedule) (bool, error) {
	if schedule.IsTemporary {
		return true, nil
	}
	if schedule.ActiveOpensAt == nil || schedule.ActiveClosesAt == nil {
		return false, nil
	}
	now := s.clock.Now()
	if !now.Before(*schedule.ActiveOpensAt) {
		return true, nil
	}
	opensAt, closesAt, err := nextRun(schedule, s.cfg.Values(), now)
	return err == nil && opensAt.Equal(*schedule.ActiveOpensAt) && closesAt.Equal(*schedule.ActiveClosesAt), err
}

func (s *Scheduler) reconfigurePending(id int) (db.Schedule, error) {
	schedule, err := s.db.SyncQueueState(s.ctx, id)
	if err != nil {
		return db.Schedule{}, err
	}
	keep, err := s.keepRun(schedule)
	if err != nil || keep || schedule.ActiveOpensAt == nil || schedule.ActiveClosesAt == nil {
		return schedule, err
	}
	cancelErr := s.controller.CancelPending(s.ctx, id, *schedule.ActiveOpensAt, *schedule.ActiveClosesAt)
	fresh, err := s.db.GetScheduleEntry(s.ctx, id)
	return fresh, errors.Join(cancelErr, err)
}

func (s *Scheduler) RunInstant(ctx context.Context, chatID int64, threadID int, title string) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	values := s.cfg.Values()
	opensAt := s.clock.Now().Add(values.OneShotDelay)
	closesAt := opensAt.Add(values.OneShotDuration)
	schedule := db.Schedule{
		ChatID: chatID, ThreadID: threadID, Title: title, IsTemporary: true,
		StartTime: opensAt.In(utils.Moscow()), EndTime: closesAt.In(utils.Moscow()),
		ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt,
		ActiveCapacity: values.TotalSlotsInQueue, ActiveColumns: values.AmountOfButtonsInRow,
	}
	id, err := s.db.AddTemporarySchedule(ctx, schedule)
	if err != nil {
		return err
	}
	schedule.ID = id
	s.scheduleNext(schedule)
	return nil
}

func (s *Scheduler) run(ctx context.Context, scheduleID int, worker *scheduleWorker) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.workers[scheduleID] == worker {
			delete(s.workers, scheduleID)
		}
		if worker.done != nil {
			close(worker.done)
		}
	}()
	failures := 0
	initialRead := true
	for ctx.Err() == nil {
		var (
			schedule db.Schedule
			err      error
		)
		if initialRead {
			schedule, err = s.db.SyncQueueState(ctx, scheduleID)
		} else {
			schedule, err = s.db.GetScheduleEntry(ctx, scheduleID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			initialRead = true
			failures++
			s.logFailure(scheduleID, err)
			if !s.wait(ctx, retryDelay(failures, err)) {
				return
			}
			continue
		}
		initialRead = false
		now := s.clock.Now()
		if schedule.ActiveOpensAt == nil && schedule.QueueMesID != nil {
			schedule, err = s.restoreLegacy(ctx, schedule)
			if errors.Is(err, pgx.ErrNoRows) {
				return
			}
			if err != nil {
				initialRead = true
				failures++
				s.logFailure(scheduleID, err)
				if !s.wait(ctx, retryDelay(failures, err)) {
					return
				}
				continue
			}
			now = s.clock.Now()
		}
		if schedule.ActiveOpensAt == nil {
			s.setActive(scheduleID, worker, false)
			if schedule.IsTemporary {
				if schedule.QueueMesID == nil {
					log.Printf("[sched] temporary schedule %d has no absolute run dates; preserved for manual recovery", scheduleID)
				}
				return
			}
			opensAt, closesAt, nextErr := nextRun(schedule, s.cfg.Values(), now)
			if nextErr != nil {
				s.logFailure(scheduleID, nextErr)
				return
			}
			due := opensAt.Add(-5 * time.Minute)
			if now.Before(due) {
				if !s.wait(ctx, due.Sub(now)) {
					return
				}
				continue
			}
			if !s.claimActive(ctx, scheduleID, worker) {
				return
			}
			if err := s.controller.Prepare(ctx, scheduleID, opensAt, closesAt); err != nil {
				initialRead = true
				s.setActive(scheduleID, worker, false)
				failures++
				s.logFailure(scheduleID, err)
				if !s.wait(ctx, retryDelay(failures, err)) {
					return
				}
				continue
			}
			failures = 0
			continue
		}
		if !s.claimActive(ctx, scheduleID, worker) {
			return
		}
		done, err := s.processActive(ctx, schedule, now, worker)
		if done {
			return
		}
		if err != nil {
			initialRead = true
			failures++
			s.logFailure(scheduleID, err)
			if !s.wait(ctx, retryDelay(failures, err)) {
				return
			}
			continue
		}
		failures = 0
		if !s.wait(ctx, s.cfg.Values().SchedulerTickInterval) {
			return
		}
	}
}

func (s *Scheduler) restoreLegacy(ctx context.Context, schedule db.Schedule) (db.Schedule, error) {
	sentAt, err := s.controller.LegacyMessageDate(ctx, schedule.ID)
	if err != nil {
		return schedule, fmt.Errorf("legacy message date unavailable; preserving schedule %d and participants: %w", schedule.ID, err)
	}
	opensAt, closesAt, err := legacyRunBounds(schedule, sentAt)
	if err != nil {
		return schedule, fmt.Errorf("cannot recover legacy schedule %d from its message date; preserving participants: %w", schedule.ID, err)
	}
	if !s.clock.Now().Before(closesAt) {
		if err := s.controller.Close(ctx, schedule.ID); err != nil {
			return schedule, err
		}
		return s.db.GetScheduleEntry(ctx, schedule.ID)
	}
	values := s.cfg.Values()
	return s.db.SetLegacyQueueRunBounds(ctx, schedule.ID, opensAt, closesAt, values.TotalSlotsInQueue, values.AmountOfButtonsInRow)
}

func (s *Scheduler) setActive(id int, worker *scheduleWorker, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workers[id] == worker {
		worker.active = active
	}
}

func (s *Scheduler) claimActive(ctx context.Context, id int, worker *scheduleWorker) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.workers[id] != worker {
		return false
	}
	worker.active = true
	return true
}

func warnConflictingRuns(schedules []db.Schedule) {
	type topic struct {
		chatID   int64
		threadID int
	}
	runs := make(map[topic][]int)
	for _, schedule := range schedules {
		if schedule.ActiveOpensAt == nil && schedule.QueueMesID == nil {
			continue
		}
		threadID := schedule.ThreadID
		if schedule.ActiveThreadID != nil {
			threadID = *schedule.ActiveThreadID
		}
		key := topic{chatID: schedule.ChatID, threadID: threadID}
		runs[key] = append(runs[key], schedule.ID)
	}
	for _, ids := range runs {
		if len(ids) > 1 {
			log.Printf("[sched] conflicting active schedule IDs %v; manual recovery required", ids)
		}
	}
}

func (s *Scheduler) processActive(ctx context.Context, schedule db.Schedule, now time.Time, worker *scheduleWorker) (bool, error) {
	if schedule.ActiveClosesAt == nil {
		return false, fmt.Errorf("schedule %d has no closing time", schedule.ID)
	}
	if !now.Before(*schedule.ActiveClosesAt) {
		if err := s.controller.Close(ctx, schedule.ID); err != nil {
			return false, err
		}
		return schedule.IsTemporary, nil
	}
	opensAt := *schedule.ActiveOpensAt
	key := opensAt.UTC().Format(time.RFC3339Nano)
	if worker.runKey != key {
		worker.runKey, worker.delivered5, worker.delivered1, worker.reconciled = key, false, false, false
	}
	values := s.cfg.Values()
	grace := max(2*values.SchedulerTickInterval, 5*time.Second)
	var errs []error
	if !schedule.Notified5min && !now.Before(opensAt.Add(-5*time.Minute)) {
		if !worker.delivered5 && now.Before(opensAt.Add(-time.Minute)) && now.Sub(opensAt.Add(-5*time.Minute)) <= grace {
			if err := s.controller.Notify(ctx, schedule, "Через 5 минут откроется запись в очередь."); err != nil {
				errs = append(errs, err)
			} else {
				worker.delivered5 = true
			}
		} else {
			worker.delivered5 = true
		}
		if worker.delivered5 {
			if err := s.db.SetNotified5min(ctx, schedule.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if !schedule.Notified1min && !now.Before(opensAt.Add(-time.Minute)) {
		if !worker.delivered1 && now.Before(opensAt) && now.Sub(opensAt.Add(-time.Minute)) <= grace {
			if err := s.controller.Notify(ctx, schedule, "Через 1 минуту откроется запись в очередь."); err != nil {
				errs = append(errs, err)
			} else {
				worker.delivered1 = true
			}
		} else {
			worker.delivered1 = true
		}
		if worker.delivered1 {
			if err := s.db.SetNotified1min(ctx, schedule.ID); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if !now.Before(opensAt.Add(-time.Minute)) && (!worker.reconciled || schedule.QueueMesID == nil || (!now.Before(opensAt) && !schedule.NotifiedOpen)) {
		if err := s.controller.Refresh(ctx, schedule.ID); err != nil {
			errs = append(errs, err)
		} else {
			worker.reconciled = true
			if !now.Before(opensAt) && !schedule.NotifiedOpen {
				if err := s.db.SetNotifiedOpen(ctx, schedule.ID); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return false, errors.Join(errs...)
}

func (s *Scheduler) wait(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.clock.After(duration):
		return ctx.Err() == nil
	}
}

func retryDelay(failures int, err error) time.Duration {
	delay := time.Second * time.Duration(1<<min(max(failures-1, 0), 5))
	var limited interface{ RetryAfter() time.Duration }
	if errors.As(err, &limited) {
		return max(delay, limited.RetryAfter())
	}
	return delay
}

func (s *Scheduler) logFailure(id int, err error) {
	log.Printf("[sched] schedule %d: %v", id, err)
}

func nextRun(schedule db.Schedule, values settings.Values, now time.Time) (time.Time, time.Time, error) {
	if schedule.StartTime.Hour() == schedule.EndTime.Hour() && schedule.StartTime.Minute() == schedule.EndTime.Minute() && schedule.StartTime.Second() == schedule.EndTime.Second() {
		return time.Time{}, time.Time{}, fmt.Errorf("schedule %d has identical opening and closing times", schedule.ID)
	}
	day, err := dayOfWeekToWeekDay(schedule.DayOfWeek)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	anchor, err := time.ParseInLocation("2006-01-02", values.Week1Date, utils.Moscow())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	now = now.In(utils.Moscow())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, utils.Moscow())
	for offset := -1; offset <= 14; offset++ {
		date := today.AddDate(0, 0, offset)
		if date.Weekday() != day || utils.WeekTypeForDate(date, anchor, values.Week1Type) != schedule.WeekType {
			continue
		}
		opensAt, closesAt := runBounds(schedule, date)
		if now.Before(closesAt) {
			return opensAt, closesAt, nil
		}
	}
	return time.Time{}, time.Time{}, fmt.Errorf("no upcoming run for schedule %d", schedule.ID)
}

func legacyRunBounds(schedule db.Schedule, sentAt time.Time) (time.Time, time.Time, error) {
	if sentAt.IsZero() {
		return time.Time{}, time.Time{}, errors.New("message has no original send date")
	}
	if schedule.StartTime.Hour() == schedule.EndTime.Hour() && schedule.StartTime.Minute() == schedule.EndTime.Minute() && schedule.StartTime.Second() == schedule.EndTime.Second() {
		return time.Time{}, time.Time{}, errors.New("legacy schedule has identical opening and closing times")
	}
	sentAt = sentAt.In(utils.Moscow())
	date := time.Date(sentAt.Year(), sentAt.Month(), sentAt.Day(), 0, 0, 0, 0, utils.Moscow())
	var weekday time.Weekday
	if !schedule.IsTemporary {
		var err error
		weekday, err = dayOfWeekToWeekDay(schedule.DayOfWeek)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	for offset := -1; offset <= 1; offset++ {
		candidate := date.AddDate(0, 0, offset)
		if !schedule.IsTemporary && candidate.Weekday() != weekday {
			continue
		}
		opensAt, closesAt := runBounds(schedule, candidate)
		if !sentAt.Before(opensAt.Add(-time.Minute)) && sentAt.Before(closesAt) {
			return opensAt, closesAt, nil
		}
	}
	return time.Time{}, time.Time{}, db.ErrActiveQueueNotFound
}

func runBounds(schedule db.Schedule, date time.Time) (time.Time, time.Time) {
	opensAt := time.Date(date.Year(), date.Month(), date.Day(), schedule.StartTime.Hour(), schedule.StartTime.Minute(), schedule.StartTime.Second(), 0, utils.Moscow())
	closesAt := time.Date(date.Year(), date.Month(), date.Day(), schedule.EndTime.Hour(), schedule.EndTime.Minute(), schedule.EndTime.Second(), 0, utils.Moscow())
	if !closesAt.After(opensAt) {
		closesAt = closesAt.AddDate(0, 0, 1)
	}
	return opensAt, closesAt
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
		return 0, fmt.Errorf("unknown weekday %q", day)
	}
}

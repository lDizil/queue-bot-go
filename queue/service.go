package queue

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"queuebot/db"
	"queuebot/settings"
	u "queuebot/utils"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrClosed                = errors.New("очередь сейчас закрыта")
	ErrStale                 = errors.New("это сообщение очереди уже устарело")
	ErrNotJoined             = errors.New("вы не стоите в этой очереди")
	ErrAlreadyJoined         = errors.New("вы уже заняли место в очереди")
	ErrOccupied              = errors.New("место уже занято")
	ErrForbidden             = errors.New("отмечать другого участника может только администратор")
	ErrChanged               = errors.New("участник уже сменил место; используйте обновлённые кнопки")
	ErrFull                  = errors.New("все места в очереди заняты")
	ErrPosition              = errors.New("место вне диапазона очереди")
	ErrMessageMissing        = errors.New("queue message was deleted")
	ErrLegacyDateUnavailable = errors.New("queue message has no trustworthy original date")
)

type Store interface {
	GetScheduleEntry(context.Context, int) (db.Schedule, error)
	SyncQueueState(context.Context, int) (db.Schedule, error)
	GetActiveScheduleByTopic(context.Context, int64, int) (db.Schedule, error)
	GetQueue(context.Context, int) ([]db.QueueEntry, error)
	BeginQueueRun(context.Context, int, time.Time, time.Time, int, int) (db.Schedule, error)
	FinishQueueRun(context.Context, int) error
	SetQueueMessageID(context.Context, int, int) error
	DeleteScheduleEntry(context.Context, int) error
	JoinQueue(context.Context, db.QueueEntry) error
	JoinFirstFreeSlotWithName(context.Context, int64, string, string, int, int) (int, error)
	GetQueueEntryByUser(context.Context, int64, int) (db.QueueEntry, error)
	GetQueueEntryByPosition(context.Context, int, int) (db.QueueEntry, error)
	LeaveFromQueue(context.Context, int64, int) error
	SetCompleted(context.Context, int, int, int, bool) error
	SwapQueueEntries(context.Context, int, int, int, int, int) error
}

type Snapshot struct {
	Schedule db.Schedule
	Entries  []db.QueueEntry
	Status   u.QueueStatus
}

type Messenger interface {
	SendQueue(context.Context, Snapshot) (int, error)
	EditQueue(context.Context, Snapshot, int) error
	RetireQueue(context.Context, int64, int) error
	NotifyQueue(context.Context, db.Schedule, string) error
}

type LegacyMessenger interface {
	QueueMessageDate(context.Context, db.Schedule, int) (time.Time, error)
}

type Actor struct {
	ID          int64
	Username    string
	DisplayName string
	Admin       bool
}

type Reference struct {
	ScheduleID int
	ChatID     int64
	MessageID  int
}

type queueState struct {
	mu           sync.Mutex
	pending      bool
	uncertain    bool
	seen         map[string]time.Time
	publication  *publication
	cancellation *Snapshot
	cleanup      map[int]messageCleanup
}

type publication struct {
	snapshot          Snapshot
	messageID         int
	previousMessageID int
}

type messageCleanup struct {
	chatID   int64
	snapshot *Snapshot
}

type Service struct {
	store     Store
	settings  *settings.Manager
	messenger Messenger
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	states    map[int]*queueState
	jobs      chan int
	wg        sync.WaitGroup
	now       func() time.Time
}

func NewService(ctx context.Context, store Store, cfg *settings.Manager, messenger Messenger) *Service {
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{store: store, settings: cfg, messenger: messenger, ctx: ctx, cancel: cancel,
		states: make(map[int]*queueState), jobs: make(chan int, 128), now: time.Now}
	for range cfg.Values().UpdateWorkers {
		s.wg.Add(1)
		go s.worker()
	}
	return s
}

func (s *Service) Stop() {
	s.cancel()
	s.wg.Wait()
}

func (s *Service) state(id int) *queueState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[id]
	if state == nil {
		state = &queueState{seen: make(map[string]time.Time), cleanup: make(map[int]messageCleanup)}
		s.states[id] = state
	}
	return state
}

func (s *Service) Prepare(ctx context.Context, id int, opensAt, closesAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.uncertain {
		if _, err := s.readSchedule(ctx, id, state); err != nil {
			return err
		}
	}
	values := s.settings.Values()
	_, err := s.store.BeginQueueRun(ctx, id, opensAt, closesAt, values.TotalSlotsInQueue, values.AmountOfButtonsInRow)
	if err != nil {
		state.uncertain = true
	}
	return err
}

func (s *Service) Refresh(ctx context.Context, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.refresh(ctx, id, state)
}

func (s *Service) refresh(ctx context.Context, id int, state *queueState) error {
	schedule, err := s.readSchedule(ctx, id, state)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	s.resolveWrites(state, schedule, errors.Is(err, pgx.ErrNoRows))
	if err != nil {
		if cleanupErr := s.cleanupMessages(ctx, state, schedule); cleanupErr != nil {
			return cleanupErr
		}
		return err
	}
	refreshErr := s.refreshCurrent(ctx, schedule, state)
	if len(state.cleanup) == 0 {
		return refreshErr
	}
	current, readErr := s.readSchedule(ctx, id, state)
	if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
		return errors.Join(refreshErr, readErr)
	}
	cleanupErr := s.cleanupMessages(ctx, state, current)
	return errors.Join(refreshErr, cleanupErr)
}

func (s *Service) refreshCurrent(ctx context.Context, schedule db.Schedule, state *queueState) error {
	if schedule.ActiveOpensAt == nil || schedule.ActiveClosesAt == nil || !s.now().Before(*schedule.ActiveClosesAt) {
		return nil
	}
	snapshot, err := s.snapshot(ctx, schedule)
	if err != nil {
		return err
	}
	if schedule.QueueMesID != nil {
		if err := s.messenger.EditQueue(ctx, snapshot, *schedule.QueueMesID); !errors.Is(err, ErrMessageMissing) {
			return err
		}
	}
	messageID, err := s.messenger.SendQueue(ctx, snapshot)
	if err != nil {
		return err
	}
	return s.persistPublication(ctx, state, publication{snapshot: snapshot, messageID: messageID})
}

func (s *Service) persistPublication(ctx context.Context, state *queueState, published publication) error {
	err := s.store.SetQueueMessageID(ctx, published.snapshot.Schedule.ID, published.messageID)
	if err == nil {
		if published.previousMessageID != 0 {
			state.cleanup[published.previousMessageID] = messageCleanup{chatID: published.snapshot.Schedule.ChatID}
		}
		return nil
	}
	state.publication = &published
	state.uncertain = true
	s.reconcileWrites(ctx, state, published.snapshot.Schedule.ID)
	s.scheduleUpdate(state, published.snapshot.Schedule.ID, 2*time.Second)
	return err
}

func (s *Service) readSchedule(ctx context.Context, id int, state *queueState) (db.Schedule, error) {
	if !state.uncertain {
		return s.store.GetScheduleEntry(ctx, id)
	}
	schedule, err := s.store.SyncQueueState(ctx, id)
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		state.uncertain = false
	}
	return schedule, err
}

func (s *Service) reconcileWrites(ctx context.Context, state *queueState, id int) {
	schedule, err := s.readSchedule(ctx, id, state)
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		s.resolveWrites(state, schedule, errors.Is(err, pgx.ErrNoRows))
	}
}

func (s *Service) resolveWrites(state *queueState, schedule db.Schedule, missing bool) {
	if published := state.publication; published != nil {
		if !missing && schedule.QueueMesID != nil && *schedule.QueueMesID == published.messageID {
			if published.previousMessageID != 0 {
				state.cleanup[published.previousMessageID] = messageCleanup{chatID: published.snapshot.Schedule.ChatID}
			}
		} else {
			closed := published.snapshot
			closed.Status = u.QueueClosed
			state.cleanup[published.messageID] = messageCleanup{snapshot: &closed}
		}
		state.publication = nil
	}
	if canceled := state.cancellation; canceled != nil {
		if missing || !sameRun(schedule, canceled.Schedule.ActiveOpensAt, canceled.Schedule.ActiveClosesAt) {
			if canceled.Schedule.QueueMesID != nil {
				state.cleanup[*canceled.Schedule.QueueMesID] = messageCleanup{snapshot: canceled}
			}
		}
		state.cancellation = nil
	}
}

func sameRun(schedule db.Schedule, opensAt, closesAt *time.Time) bool {
	return schedule.ActiveOpensAt != nil && schedule.ActiveClosesAt != nil && opensAt != nil && closesAt != nil &&
		schedule.ActiveOpensAt.Equal(*opensAt) && schedule.ActiveClosesAt.Equal(*closesAt)
}

func (s *Service) cleanupMessages(ctx context.Context, state *queueState, current db.Schedule) error {
	var errs []error
	for messageID, cleanup := range state.cleanup {
		if current.QueueMesID != nil && *current.QueueMesID == messageID {
			delete(state.cleanup, messageID)
			continue
		}
		var err error
		if cleanup.snapshot != nil {
			err = s.messenger.EditQueue(ctx, *cleanup.snapshot, messageID)
		} else {
			err = s.messenger.RetireQueue(ctx, cleanup.chatID, messageID)
		}
		if err == nil || errors.Is(err, ErrMessageMissing) {
			delete(state.cleanup, messageID)
			continue
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Service) snapshot(ctx context.Context, schedule db.Schedule) (Snapshot, error) {
	entries, err := s.store.GetQueue(ctx, schedule.ID)
	if err != nil {
		return Snapshot{}, err
	}
	status := u.QueueClosed
	if schedule.ActiveOpensAt != nil && schedule.ActiveClosesAt != nil && s.now().Before(*schedule.ActiveClosesAt) {
		status = u.QueuePending
		if !s.now().Before(*schedule.ActiveOpensAt) {
			status = u.QueueOpen
		}
	}
	return Snapshot{Schedule: schedule, Entries: entries, Status: status}, nil
}

func (s *Service) Close(ctx context.Context, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.close(ctx, id, state)
}

func (s *Service) Delete(ctx context.Context, id int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := s.close(ctx, id, state); err != nil {
		return err
	}
	return s.store.DeleteScheduleEntry(ctx, id)
}

func (s *Service) close(ctx context.Context, id int, state *queueState) error {
	schedule, err := s.readSchedule(ctx, id, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.resolveWrites(state, schedule, false)
	if schedule.QueueMesID != nil {
		snapshot, err := s.snapshot(ctx, schedule)
		if err != nil {
			return err
		}
		snapshot.Status = u.QueueClosed
		if err := s.messenger.EditQueue(ctx, snapshot, *schedule.QueueMesID); err != nil {
			if !errors.Is(err, ErrMessageMissing) {
				return err
			}
			messageID, sendErr := s.messenger.SendQueue(ctx, snapshot)
			if sendErr != nil {
				return sendErr
			}
			if err := s.persistPublication(ctx, state, publication{snapshot: snapshot, messageID: messageID}); err != nil {
				return err
			}
		}
	}
	if err := s.store.FinishQueueRun(ctx, id); err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, id, 2*time.Second)
		return err
	}
	clear(state.seen)
	return nil
}

func (s *Service) Notify(ctx context.Context, schedule db.Schedule, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.messenger.NotifyQueue(ctx, schedule, text)
}

func (s *Service) CancelPending(ctx context.Context, id int, expectedOpensAt, expectedClosesAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	schedule, err := s.readSchedule(ctx, id, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.resolveWrites(state, schedule, false)
	if schedule.IsTemporary || schedule.ActiveOpensAt == nil || !s.now().Before(*schedule.ActiveOpensAt) {
		return nil
	}
	if !sameRun(schedule, &expectedOpensAt, &expectedClosesAt) {
		return db.ErrQueueStateChanged
	}
	closed, err := s.snapshot(ctx, schedule)
	if err != nil {
		return err
	}
	closed.Status = u.QueueClosed
	state.cancellation = &closed
	if err := s.store.FinishQueueRun(ctx, id); err != nil {
		state.uncertain = true
		s.reconcileWrites(ctx, state, id)
		s.scheduleUpdate(state, id, 2*time.Second)
		return err
	}
	clear(state.seen)
	cleared := schedule
	cleared.ActiveOpensAt, cleared.ActiveClosesAt, cleared.QueueMesID = nil, nil, nil
	s.resolveWrites(state, cleared, false)
	if err := s.cleanupMessages(ctx, state, cleared); err != nil {
		log.Printf("[queue] close canceled preview: schedule=%d: %v", id, err)
		s.scheduleUpdate(state, id, 2*time.Second)
	}
	return nil
}

func (s *Service) LegacyMessageDate(ctx context.Context, id int) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	schedule, err := s.readSchedule(ctx, id, state)
	if err != nil {
		return time.Time{}, err
	}
	if schedule.QueueMesID == nil {
		return time.Time{}, ErrMessageMissing
	}
	messenger, ok := s.messenger.(LegacyMessenger)
	if !ok {
		return time.Time{}, ErrLegacyDateUnavailable
	}
	date, err := messenger.QueueMessageDate(ctx, schedule, *schedule.QueueMesID)
	if err != nil {
		return time.Time{}, err
	}
	if date.IsZero() || date.Unix() <= 0 {
		return time.Time{}, fmt.Errorf("%w: message %d", ErrLegacyDateUnavailable, *schedule.QueueMesID)
	}
	return date, nil
}

func (s *Service) RecoverTopic(ctx context.Context, chatID int64, threadID int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	schedule, err := s.store.GetActiveScheduleByTopic(ctx, chatID, threadID)
	if err != nil {
		return err
	}
	if schedule.ActiveClosesAt == nil || !s.now().Before(*schedule.ActiveClosesAt) {
		return ErrClosed
	}
	return s.Refresh(ctx, schedule.ID)
}

func (s *Service) Active(ctx context.Context, ref Reference) (db.Schedule, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	return s.active(ctx, ref, state)
}

func (s *Service) active(ctx context.Context, ref Reference, state *queueState) (db.Schedule, error) {
	schedule, err := s.readSchedule(ctx, ref.ScheduleID, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Schedule{}, ErrClosed
	}
	if err != nil {
		return db.Schedule{}, err
	}
	if schedule.ChatID != ref.ChatID || schedule.QueueMesID == nil || *schedule.QueueMesID != ref.MessageID {
		return db.Schedule{}, ErrStale
	}
	if schedule.ActiveOpensAt == nil || schedule.ActiveClosesAt == nil || s.now().Before(*schedule.ActiveOpensAt) || !s.now().Before(*schedule.ActiveClosesAt) {
		return db.Schedule{}, ErrClosed
	}
	return schedule, nil
}

func (s *Service) ActiveTopic(ctx context.Context, chatID int64, threadID int) (db.Schedule, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	schedule, err := s.store.GetActiveScheduleByTopic(ctx, chatID, threadID)
	if err != nil {
		return db.Schedule{}, err
	}
	if schedule.QueueMesID == nil {
		return db.Schedule{}, ErrClosed
	}
	return s.Active(ctx, Reference{ScheduleID: schedule.ID, ChatID: chatID, MessageID: *schedule.QueueMesID})
}

func (s *Service) Join(ctx context.Context, ref Reference, actor Actor, position int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	schedule, err := s.active(ctx, ref, state)
	if err != nil {
		return 0, err
	}
	if position < 0 || position > schedule.ActiveCapacity {
		return 0, ErrPosition
	}
	_, err = s.store.GetQueueEntryByUser(ctx, actor.ID, schedule.ID)
	if err == nil {
		return 0, ErrAlreadyJoined
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if position == 0 {
		position, err = s.store.JoinFirstFreeSlotWithName(ctx, actor.ID, actor.Username, actor.DisplayName, schedule.ID, schedule.ActiveCapacity)
	} else {
		err = s.store.JoinQueue(ctx, db.QueueEntry{UserID: actor.ID, Username: actor.Username, DisplayName: actor.DisplayName, ScheduleID: schedule.ID, Position: position})
	}
	if err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, schedule.ID, s.settings.Values().DelayUpdateQueue)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrFull
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return 0, ErrOccupied
		}
		return 0, err
	}
	s.scheduleUpdate(state, schedule.ID, s.settings.Values().DelayUpdateQueue)
	return position, nil
}

func (s *Service) Leave(ctx context.Context, ref Reference, actor Actor) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, err := s.active(ctx, ref, state); err != nil {
		return err
	}
	if _, err := s.store.GetQueueEntryByUser(ctx, actor.ID, ref.ScheduleID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotJoined
	} else if err != nil {
		return err
	}
	if err := s.store.LeaveFromQueue(ctx, actor.ID, ref.ScheduleID); err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
		return err
	}
	s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
	return nil
}

func (s *Service) Complete(ctx context.Context, ref Reference, actor Actor, entryID, position int, completed *bool, callbackID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, err := s.active(ctx, ref, state); err != nil {
		return false, err
	}
	var entry db.QueueEntry
	var err error
	if entryID == 0 {
		entry, err = s.store.GetQueueEntryByUser(ctx, actor.ID, ref.ScheduleID)
	} else {
		entry, err = s.store.GetQueueEntryByPosition(ctx, position, ref.ScheduleID)
		if err == nil && entry.ID != entryID {
			return false, ErrChanged
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotJoined
	}
	if err != nil {
		return false, err
	}
	if entry.UserID != actor.ID && !actor.Admin {
		return false, ErrForbidden
	}
	if until, seen := state.seen[callbackID]; callbackID != "" && seen && until.After(s.now()) {
		return entry.Completed, nil
	}
	value := !entry.Completed
	if completed != nil {
		value = *completed
	}
	for key, until := range state.seen {
		if !until.After(s.now()) {
			delete(state.seen, key)
		}
	}
	if callbackID != "" {
		state.seen[callbackID] = s.now().Add(10 * time.Minute)
	}
	if err := s.store.SetCompleted(ctx, ref.ScheduleID, entry.ID, entry.Position, value); err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
		if errors.Is(err, db.ErrQueueStateChanged) {
			return false, ErrChanged
		}
		return false, err
	}
	s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
	return value, nil
}

func (s *Service) Swap(ctx context.Context, ref Reference, requesterID, targetID, requesterPosition, targetPosition int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, err := s.active(ctx, ref, state); err != nil {
		return err
	}
	if err := s.store.SwapQueueEntries(ctx, ref.ScheduleID, requesterID, targetID, requesterPosition, targetPosition); err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
		return err
	}
	s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
	return nil
}

func (s *Service) Repost(ctx context.Context, ref Reference) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	schedule, err := s.active(ctx, ref, state)
	if err != nil {
		return err
	}
	s.resolveWrites(state, schedule, false)
	snapshot, err := s.snapshot(ctx, schedule)
	if err != nil {
		return err
	}
	messageID, err := s.messenger.SendQueue(ctx, snapshot)
	if err != nil {
		return err
	}
	if err := s.persistPublication(ctx, state, publication{snapshot: snapshot, messageID: messageID, previousMessageID: ref.MessageID}); err != nil {
		return err
	}
	current := schedule
	current.QueueMesID = &messageID
	if err := s.cleanupMessages(ctx, state, current); err != nil {
		s.scheduleUpdate(state, schedule.ID, 2*time.Second)
		return err
	}
	return nil
}

func (s *Service) Changed(id int) {
	state := s.state(id)
	state.mu.Lock()
	defer state.mu.Unlock()
	s.scheduleUpdate(state, id, s.settings.Values().DelayUpdateQueue)
}

func (s *Service) scheduleUpdate(state *queueState, id int, delay time.Duration) {
	if state.pending || s.ctx.Err() != nil {
		return
	}
	state.pending = true
	time.AfterFunc(delay, func() {
		select {
		case s.jobs <- id:
		case <-s.ctx.Done():
		}
	})
}

func (s *Service) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case id := <-s.jobs:
			state := s.state(id)
			state.mu.Lock()
			state.pending = false
			ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			err := s.refresh(ctx, id, state)
			cancel()
			if err != nil && !errors.Is(err, pgx.ErrNoRows) && s.ctx.Err() == nil {
				log.Printf("[queue] refresh schedule=%d: %v", id, err)
				s.scheduleUpdate(state, id, 2*time.Second)
			}
			state.mu.Unlock()
		}
	}
}

func ErrorText(err error) string {
	if errors.Is(err, db.ErrTopicBusy) {
		return "В этой теме уже есть действующая очередь"
	}
	if errors.Is(err, db.ErrQueueStateChanged) {
		return ErrChanged.Error()
	}
	for _, known := range []error{ErrClosed, ErrStale, ErrNotJoined, ErrAlreadyJoined, ErrOccupied, ErrForbidden, ErrChanged, ErrFull, ErrPosition} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "Не удалось выполнить действие. Попробуйте ещё раз"
}

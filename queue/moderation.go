package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"queuebot/access"
	"queuebot/db"
	u "queuebot/utils"

	"github.com/jackc/pgx/v5"
)

type Authorizer interface {
	Allowed(context.Context, int64, access.Permission) (bool, error)
}

type RunReference struct {
	ScheduleID int
	ChatID     int64
	OpensAt    time.Time
}

func (s *Service) SetAuthorizer(authorizer Authorizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizer = authorizer
}

func (s *Service) authorize(ctx context.Context, actorID int64, permission access.Permission) error {
	s.mu.Lock()
	authorizer := s.authorizer
	s.mu.Unlock()
	if authorizer == nil {
		return ErrForbidden
	}
	allowed, err := authorizer.Allowed(ctx, actorID, permission)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Service) OpenQueues(ctx context.Context) ([]Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	schedules, err := s.store.GetStartupSchedules(ctx)
	if err != nil {
		return nil, err
	}
	var snapshots []Snapshot
	for _, candidate := range schedules {
		if candidate.ActiveOpensAt == nil {
			continue
		}
		snapshot, err := s.InspectRun(ctx, RunReference{ScheduleID: candidate.ID, ChatID: candidate.ChatID, OpensAt: *candidate.ActiveOpensAt})
		if errors.Is(err, ErrClosed) || errors.Is(err, ErrStale) {
			continue
		}
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (s *Service) InspectRun(ctx context.Context, ref RunReference) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	schedule, err := s.activeRun(ctx, ref, state)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := s.snapshot(ctx, schedule)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Status != u.QueueOpen {
		return Snapshot{}, ErrClosed
	}
	return snapshot, nil
}

func (s *Service) activeRun(ctx context.Context, ref RunReference, state *queueState) (db.Schedule, error) {
	if state.closing || state.deleting {
		return db.Schedule{}, ErrClosed
	}
	schedule, err := s.readSchedule(ctx, ref.ScheduleID, state)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Schedule{}, ErrClosed
	}
	if err != nil {
		return db.Schedule{}, err
	}
	if schedule.ActiveOpensAt == nil || schedule.ActiveClosesAt == nil {
		return db.Schedule{}, ErrClosed
	}
	if schedule.ChatID != ref.ChatID || !schedule.ActiveOpensAt.Equal(ref.OpensAt) {
		return db.Schedule{}, ErrStale
	}
	now := s.now()
	if now.Before(*schedule.ActiveOpensAt) || !now.Before(*schedule.ActiveClosesAt) {
		return db.Schedule{}, ErrClosed
	}
	return schedule, nil
}

func (s *Service) Remove(ctx context.Context, ref RunReference, actorID int64, entryID int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := s.state(ref.ScheduleID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, err := s.activeRun(ctx, ref, state); err != nil {
		return err
	}
	if err := s.authorize(ctx, actorID, access.RemoveParticipants); err != nil {
		return err
	}
	if entryID <= 0 {
		return ErrChanged
	}
	if err := s.store.RemoveQueueEntry(ctx, ref.ScheduleID, ref.OpensAt, entryID); err != nil {
		state.uncertain = true
		s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
		if errors.Is(err, db.ErrQueueStateChanged) {
			return ErrChanged
		}
		return err
	}
	s.scheduleUpdate(state, ref.ScheduleID, s.settings.Values().DelayUpdateQueue)
	return nil
}

package queue

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Service) startHTTP(ctx context.Context, state *queueState, capture bool) error {
	select {
	case state.render <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	state.mu.Lock()
	state.inflight = true
	if capture {
		state.dirty = false
	}
	state.mu.Unlock()
	return nil
}

func (s *Service) finishHTTP(state *queueState, id int, err error, retry bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	<-state.render
	state.inflight = false
	if retry && err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.scheduleRetry(state, id, 2*time.Second)
		return
	}
	if state.dirty {
		s.scheduleUpdate(state, id, 0)
	}
}

func (s *Service) scheduleRetry(state *queueState, id int, delay time.Duration) {
	if readyAt := s.now().Add(delay); readyAt.After(state.readyAt) {
		state.readyAt = readyAt
	}
	state.dirty = true
	s.scheduleUpdate(state, id, 0)
}

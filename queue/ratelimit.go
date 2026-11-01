package queue

import (
	"context"
	"errors"
	"time"
)

type RetryAfterError interface {
	error
	RetryAfter() time.Duration
}

type cooldown struct {
	until time.Time
	cause error
}

type cooldownError struct {
	after time.Duration
	cause error
}

var _ RetryAfterError = (*cooldownError)(nil)

func (e *cooldownError) Error() string             { return e.cause.Error() }
func (e *cooldownError) Unwrap() error             { return e.cause }
func (e *cooldownError) RetryAfter() time.Duration { return e.after }

func (s *Service) cooldownRemaining(chatID int64) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return max(0, s.cooldowns[chatID].until.Sub(s.now()))
}

func (s *Service) callHTTP(ctx context.Context, chatID int64, call func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	limited := s.cooldowns[chatID]
	s.mu.Unlock()
	if remaining := limited.until.Sub(s.now()); remaining > 0 {
		return &cooldownError{after: remaining, cause: limited.cause}
	}
	err := call()
	var rateLimit RetryAfterError
	if errors.As(err, &rateLimit) {
		after := rateLimit.RetryAfter()
		if after <= 0 {
			after = 2 * time.Second
		}
		until := s.now().Add(after)
		s.mu.Lock()
		if until.After(s.cooldowns[chatID].until) {
			s.cooldowns[chatID] = cooldown{until: until, cause: err}
		}
		s.mu.Unlock()
	}
	return err
}

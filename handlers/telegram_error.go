package handlers

import (
	"errors"
	"time"

	"queuebot/queue"

	"github.com/go-telegram/bot"
)

func queueDeliveryError(err error) error {
	var limited *bot.TooManyRequestsError
	if !errors.As(err, &limited) {
		return err
	}
	return &telegramRateLimitError{cause: err, after: time.Duration(max(limited.RetryAfter, 0)) * time.Second}
}

type telegramRateLimitError struct {
	cause error
	after time.Duration
}

var _ queue.RetryAfterError = (*telegramRateLimitError)(nil)

func (err *telegramRateLimitError) Error() string             { return err.cause.Error() }
func (err *telegramRateLimitError) Unwrap() error             { return err.cause }
func (err *telegramRateLimitError) RetryAfter() time.Duration { return err.after }

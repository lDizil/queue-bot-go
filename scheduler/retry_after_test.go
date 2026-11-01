package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"queuebot/db"
)

type retryAfterTestError struct{ after time.Duration }

func (*retryAfterTestError) Error() string                 { return "Telegram cooldown" }
func (err *retryAfterTestError) RetryAfter() time.Duration { return err.after }

type retryAfterClock struct {
	now    time.Time
	waits  []time.Duration
	cancel context.CancelFunc
}

func (clock *retryAfterClock) Now() time.Time { return clock.now }
func (clock *retryAfterClock) After(delay time.Duration) <-chan time.Time {
	clock.waits = append(clock.waits, delay)
	clock.cancel()
	return make(chan time.Time)
}

func TestSchedulerWaitsForRemainingCooldownAcrossOutboundOperations(t *testing.T) {
	for _, operation := range []string{"refresh", "notify", "legacy date", "close"} {
		t.Run(operation, func(t *testing.T) {
			opensAt, closesAt := moscowDate(7, 10, 0), moscowDate(7, 11, 0)
			messageID := 123
			row := db.Schedule{ID: 1, QueueMesID: &messageID, ActiveOpensAt: &opensAt, ActiveClosesAt: &closesAt, Notified5min: true, Notified1min: true, NotifiedOpen: true}
			now := moscowDate(7, 10, 30)
			switch operation {
			case "notify":
				now = moscowDate(7, 9, 55)
				row.Notified5min = false
			case "legacy date":
				row.ActiveOpensAt, row.ActiveClosesAt = nil, nil
			case "close":
				now = moscowDate(7, 11, 1)
			}
			store := &mockScheduleStore{rows: map[int]db.Schedule{1: row}}
			cause := fmt.Errorf("outbound request: %w", &retryAfterTestError{after: 38 * time.Second})
			controller := &retryAfterController{mockController: &mockController{store: store}, operation: operation, err: cause}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			clock := &retryAfterClock{now: now, cancel: cancel}
			s := NewScheduler(ctx, store, controller, testManager(t))
			s.clock = clock
			worker := &scheduleWorker{}
			s.workers[1] = worker
			s.run(ctx, 1, worker)
			if len(clock.waits) != 1 || clock.waits[0] != 38*time.Second || controller.calls != 1 {
				t.Fatalf("scheduler ignored remaining Telegram cooldown: waits=%v calls=%d", clock.waits, controller.calls)
			}
		})
	}
}

type retryAfterController struct {
	*mockController
	operation string
	err       error
	calls     int
}

func (controller *retryAfterController) Refresh(ctx context.Context, id int) error {
	if controller.operation == "refresh" {
		controller.calls++
		return controller.err
	}
	return controller.mockController.Refresh(ctx, id)
}

func (controller *retryAfterController) Notify(ctx context.Context, schedule db.Schedule, text string) error {
	if controller.operation == "notify" {
		controller.calls++
		return controller.err
	}
	return controller.mockController.Notify(ctx, schedule, text)
}

func (controller *retryAfterController) LegacyMessageDate(ctx context.Context, id int) (time.Time, error) {
	if controller.operation == "legacy date" {
		controller.calls++
		return time.Time{}, controller.err
	}
	return controller.mockController.LegacyMessageDate(ctx, id)
}

func (controller *retryAfterController) Close(ctx context.Context, id int) error {
	if controller.operation == "close" {
		controller.calls++
		return controller.err
	}
	return controller.mockController.Close(ctx, id)
}

func TestShorterCooldownDoesNotReplaceLongerFailureBackoff(t *testing.T) {
	cause := &retryAfterTestError{after: time.Second}
	if delay := retryDelay(6, cause); delay != 32*time.Second {
		t.Fatalf("failure backoff was reduced by a shorter server cooldown: %s", delay)
	}
}

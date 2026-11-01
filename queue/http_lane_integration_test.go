package queue_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/settings"
	"queuebot/utils"
)

func TestSlowQueueHTTPDoesNotBlockMutationsAndPublishesLatestState(t *testing.T) {
	for _, phase := range []string{"edit", "send", "metadata"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			fixture := newServiceFixture(t)
			ref := fixture.openQueue(t, -100, 7)
			alice := fixture.join(t, ref, 101, 1)
			bob := fixture.join(t, ref, 202, 2)
			fixture.service.Stop()
			messenger := newPhaseGate(fixture.messenger, phase)
			defer messenger.unblock()
			service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
			t.Cleanup(service.Stop)
			finished := make(chan error, 1)
			go func() {
				switch phase {
				case "edit":
					finished <- service.Refresh(fixture.ctx, ref.ScheduleID)
				case "send":
					finished <- service.Repost(fixture.ctx, ref)
				case "metadata":
					_, err := service.LegacyMessageDate(fixture.ctx, ref.ScheduleID)
					finished <- err
				}
			}()
			awaitChannel(t, messenger.started, "blocked outbound queue request")
			mutated := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(fixture.ctx, time.Second)
				defer cancel()
				if _, err := service.Join(ctx, ref, participant(303), 3); err != nil {
					mutated <- err
					return
				}
				if _, err := service.Complete(ctx, ref, participant(101), 0, 0, nil, "during-http"); err != nil {
					mutated <- err
					return
				}
				if err := service.Swap(ctx, ref, alice.ID, bob.ID, 1, 2); err != nil {
					mutated <- err
					return
				}
				mutated <- service.Leave(ctx, ref, participant(202))
			}()
			select {
			case err := <-mutated:
				if err != nil {
					t.Fatalf("HTTP blocked a database mutation: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("database mutations waited for the outbound HTTP request")
			}
			entries := fixture.entries(t, ref.ScheduleID)
			if len(entries) != 2 || entries[0].UserID != 101 || entries[0].Position != 2 || !entries[0].Completed || entries[1].UserID != 303 {
				t.Fatalf("concurrent database mutations were lost: %+v", entries)
			}
			for range 64 {
				service.Changed(ref.ScheduleID)
			}
			messenger.unblock()
			if err := awaitChannel(t, finished, "outbound request to finish"); err != nil {
				t.Fatal(err)
			}
			latest := fixture.messenger.await(t, func(event delivery) bool {
				entries := event.Snapshot.Entries
				return event.Kind == "edit" && event.Snapshot.Schedule.ID == ref.ScheduleID && len(entries) == 2 &&
					entries[0].UserID == 101 && entries[0].Position == 2 && entries[0].Completed && entries[1].UserID == 303
			})
			if latest.Snapshot.Status != utils.QueueOpen {
				t.Fatalf("latest database state was rendered with a wrong lifecycle: %+v", latest.Snapshot)
			}
		})
	}
}

func TestBusyQueueDoesNotFillWorkerPoolWithWaitingRenders(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.openQueue(t, -100, 7)
	others := []queue.Reference{fixture.openQueue(t, -100, 8), fixture.openQueue(t, -100, 9), fixture.openQueue(t, -200, 7)}
	gate := fixture.messenger.block(t, first.ScheduleID, utils.QueueOpen)
	fixture.join(t, first, 101, 1)
	awaitChannel(t, gate.started, "busy queue renderer")
	for range 100 {
		fixture.service.Changed(first.ScheduleID)
	}
	for i, ref := range others {
		fixture.join(t, ref, int64(202+i), 1)
	}
	seen := make(map[int]bool)
	for len(seen) != len(others) {
		event := fixture.messenger.await(t, func(event delivery) bool {
			return event.Kind == "edit" && event.Snapshot.Schedule.ID != first.ScheduleID && len(event.Snapshot.Entries) == 1
		})
		seen[event.Snapshot.Schedule.ID] = true
	}
	gate.unblock()
}

func TestMutationDuringHTTPKeepsTheConfiguredCoalescingWindow(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.join(t, ref, 101, 1)
	fixture.service.Stop()
	if err := fixture.settings.Update(fixture.ctx, func(values *settings.Values) error {
		values.DelayUpdateQueue = 250 * time.Millisecond
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	messenger := &timedMessenger{Messenger: fixture.messenger, attempts: make(chan time.Time, 8)}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	gate := fixture.messenger.block(t, ref.ScheduleID, utils.QueueOpen)
	service.Changed(ref.ScheduleID)
	awaitChannel(t, gate.started, "first renderer to block")
	awaitChannel(t, messenger.attempts, "first HTTP start")
	changedAt := time.Now()
	if _, err := service.Complete(fixture.ctx, ref, participant(101), 0, 0, nil, "new-window"); err != nil {
		t.Fatal(err)
	}
	gate.unblock()
	second := awaitChannel(t, messenger.attempts, "next coalesced HTTP start")
	if second.Sub(changedAt) < 250*time.Millisecond {
		t.Fatalf("in-flight completion bypassed the configured window: elapsed=%s", second.Sub(changedAt))
	}
}

func TestStopCancelsAnInflightWorkerWithoutWaitingForItsHTTPTimeout(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	gate := fixture.messenger.block(t, ref.ScheduleID, utils.QueueOpen)
	fixture.join(t, ref, 101, 1)
	awaitChannel(t, gate.started, "worker HTTP request")
	stopped := make(chan struct{})
	go func() { fixture.service.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for the ten-second HTTP timeout")
	}
	gate.unblock()
}

func TestCloseIntentRejectsMutationsBeforeAnOlderRenderFinishes(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	gate := fixture.messenger.block(t, ref.ScheduleID, utils.QueueOpen)
	fixture.join(t, ref, 101, 1)
	awaitChannel(t, gate.started, "old open render")
	closed := make(chan error, 1)
	go func() { closed <- fixture.service.Close(fixture.ctx, ref.ScheduleID) }()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := fixture.service.Active(fixture.ctx, ref)
		if errors.Is(err, queue.ErrClosed) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("closing intent was not visible independently of HTTP: %v", err)
		}
	}
	if _, err := fixture.service.Join(fixture.ctx, ref, participant(202), 2); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("closing queue accepted a new participant: %v", err)
	}
	gate.unblock()
	if err := awaitChannel(t, closed, "serialized final close"); err != nil {
		t.Fatal(err)
	}
	seenClosed := false
	for _, event := range fixture.messenger.history() {
		if event.Snapshot.Schedule.ID != ref.ScheduleID || (event.Kind != "send" && event.Kind != "edit") {
			continue
		}
		if seenClosed && event.Snapshot.Status != utils.QueueClosed {
			t.Fatalf("an older render arrived after the final close: %+v", event)
		}
		seenClosed = seenClosed || event.Snapshot.Status == utils.QueueClosed
	}
	if !seenClosed {
		t.Fatal("closing did not publish the final message")
	}
}

func TestRateLimitPausesOneChatWhileMutationsAndOtherGroupsContinue(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	first := fixture.openQueue(t, -100, 7)
	sameChat := fixture.openQueue(t, -100, 8)
	otherChat := fixture.openQueue(t, -200, 7)
	fixture.service.Stop()
	messenger := &limitedMessenger{Messenger: fixture.messenger, target: first.ScheduleID,
		after: time.Second, failed: make(chan time.Time, 1)}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	if _, err := service.Join(fixture.ctx, first, participant(101), 1); err != nil {
		t.Fatal(err)
	}
	limitedAt := awaitChannel(t, messenger.failed, "rate-limited queue update")
	ctx, cancel := context.WithTimeout(fixture.ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := service.Complete(ctx, first, participant(101), 0, 0, nil, "during-cooldown"); err != nil {
		t.Fatalf("rate limiting blocked completion in the database: %v", err)
	}
	if _, err := service.Join(ctx, sameChat, participant(202), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Join(ctx, otherChat, participant(303), 1); err != nil {
		t.Fatal(err)
	}
	err := service.Refresh(ctx, sameChat.ScheduleID)
	var limited queue.RetryAfterError
	if !errors.As(err, &limited) || limited.RetryAfter() <= 0 || limited.RetryAfter() > messenger.after {
		t.Fatalf("active cooldown lost the remaining retry interval: %v", err)
	}
	fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.Snapshot.Schedule.ID == otherChat.ScheduleID && len(event.Snapshot.Entries) == 1
	})
	for _, attempt := range messenger.history() {
		if attempt.id == sameChat.ScheduleID {
			t.Fatal("another topic in the rate-limited chat made an HTTP request during cooldown")
		}
		if attempt.id == otherChat.ScheduleID && !attempt.at.Before(limitedAt.Add(messenger.after)) {
			t.Fatal("another group waited for the first chat's cooldown")
		}
	}
	seen := make(map[int]bool)
	for len(seen) != 2 {
		event := fixture.messenger.await(t, func(event delivery) bool {
			if event.Kind != "edit" || len(event.Snapshot.Entries) != 1 {
				return false
			}
			return event.Snapshot.Schedule.ID == sameChat.ScheduleID ||
				(event.Snapshot.Schedule.ID == first.ScheduleID && event.Snapshot.Entries[0].Completed)
		})
		seen[event.Snapshot.Schedule.ID] = true
	}
	for i, attempt := range messenger.history() {
		if i == 0 || attempt.id == otherChat.ScheduleID {
			continue
		}
		if attempt.at.Before(limitedAt.Add(messenger.after)) {
			t.Fatalf("HTTP retried before the server's retry_after: %+v", attempt)
		}
	}
}

func TestFailedCloseKeepsIntentAndAutomaticallyFinishesFinalSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	ref := fixture.openQueue(t, -100, 7)
	fixture.join(t, ref, 101, 1)
	fixture.service.Stop()
	messenger := &closedFailureMessenger{Messenger: fixture.messenger}
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	if err := service.Close(fixture.ctx, ref.ScheduleID); !errors.Is(err, errClosedDelivery) {
		t.Fatalf("close did not expose its delivery failure: %v", err)
	}
	if _, err := service.Join(fixture.ctx, ref, participant(202), 2); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("failed close reopened business operations: %v", err)
	}
	final := fixture.messenger.await(t, func(event delivery) bool {
		return event.Kind == "edit" && event.Snapshot.Schedule.ID == ref.ScheduleID && event.Snapshot.Status == utils.QueueClosed
	})
	if len(final.Snapshot.Entries) != 1 || final.Snapshot.Entries[0].UserID != 101 {
		t.Fatalf("automatic close lost the final participants: %+v", final.Snapshot)
	}
	if err := service.Refresh(fixture.ctx, ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if entries := fixture.entries(t, ref.ScheduleID); len(entries) != 0 {
		t.Fatalf("automatic close did not finish database cleanup: %+v", entries)
	}
}

func TestCancelPendingWaitsForUntrackedSendBeforePreparingAnotherRun(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	id := fixture.addSchedule(t, -100, 7)
	opensAt, closesAt := time.Now().Add(time.Minute), time.Now().Add(time.Hour)
	if err := fixture.service.Prepare(fixture.ctx, id, opensAt, closesAt); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service.Stop()
	messenger := newPhaseGate(fixture.messenger, "send")
	defer messenger.unblock()
	service := queue.NewService(fixture.ctx, fixture.observed, fixture.settings, messenger)
	t.Cleanup(service.Stop)
	published := make(chan error, 1)
	go func() { published <- service.Refresh(fixture.ctx, id) }()
	awaitChannel(t, messenger.started, "untracked initial send")
	waitCtx, cancelWait := context.WithTimeout(fixture.ctx, 100*time.Millisecond)
	defer cancelWait()
	if err := service.CancelPending(waitCtx, id, *stored.ActiveOpensAt, *stored.ActiveClosesAt); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation overtook the untracked HTTP send: %v", err)
	}
	stillPending, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || stillPending.ActiveOpensAt == nil || stillPending.QueueMesID != nil {
		t.Fatalf("cancellation changed the run before the send finished: %+v, err=%v", stillPending, err)
	}
	canceled := make(chan error, 1)
	go func() {
		canceled <- service.CancelPending(fixture.ctx, id, *stored.ActiveOpensAt, *stored.ActiveClosesAt)
	}()
	messenger.unblock()
	if err := awaitChannel(t, published, "initial publication before cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := awaitChannel(t, canceled, "cancellation after the initial send"); err != nil {
		t.Fatal(err)
	}
	if err := service.Prepare(fixture.ctx, id, opensAt.Add(time.Hour), closesAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(fixture.ctx, id); err != nil {
		t.Fatal(err)
	}
	var sends []delivery
	closed := make(map[int]bool)
	for _, event := range fixture.messenger.history() {
		if event.Kind == "send" {
			sends = append(sends, event)
		}
		if event.Kind == "edit" && event.Snapshot.Status == utils.QueueClosed {
			closed[event.MessageID] = true
		}
	}
	current, err := fixture.repository.GetScheduleEntry(fixture.ctx, id)
	if err != nil || len(sends) != 2 || !closed[sends[0].MessageID] || closed[sends[1].MessageID] ||
		current.QueueMesID == nil || *current.QueueMesID != sends[1].MessageID {
		t.Fatalf("old publication was attached to the replacement run: sends=%+v closed=%v current=%+v err=%v", sends, closed, current, err)
	}
}

type httpAttempt struct {
	id int
	at time.Time
}

type timedMessenger struct {
	queue.Messenger
	attempts chan time.Time
}

func (messenger *timedMessenger) EditQueue(ctx context.Context, snapshot queue.Snapshot, id int) error {
	messenger.attempts <- time.Now()
	return messenger.Messenger.EditQueue(ctx, snapshot, id)
}

type limitedMessenger struct {
	queue.Messenger
	mu       sync.Mutex
	target   int
	after    time.Duration
	failed   chan time.Time
	attempts []httpAttempt
	limited  bool
}

func (messenger *limitedMessenger) EditQueue(ctx context.Context, snapshot queue.Snapshot, id int) error {
	at := time.Now()
	messenger.mu.Lock()
	messenger.attempts = append(messenger.attempts, httpAttempt{id: snapshot.Schedule.ID, at: at})
	fail := snapshot.Schedule.ID == messenger.target && !messenger.limited
	if fail {
		messenger.limited = true
	}
	messenger.mu.Unlock()
	if fail {
		messenger.failed <- at
		return &testRateLimit{after: messenger.after}
	}
	return messenger.Messenger.EditQueue(ctx, snapshot, id)
}

func (messenger *limitedMessenger) history() []httpAttempt {
	messenger.mu.Lock()
	defer messenger.mu.Unlock()
	return append([]httpAttempt(nil), messenger.attempts...)
}

type testRateLimit struct{ after time.Duration }

var _ queue.RetryAfterError = (*testRateLimit)(nil)

func (err *testRateLimit) Error() string             { return "rate limited" }
func (err *testRateLimit) RetryAfter() time.Duration { return err.after }

var errClosedDelivery = errors.New("final message delivery failed")

type closedFailureMessenger struct {
	queue.Messenger
	once sync.Once
}

func (messenger *closedFailureMessenger) EditQueue(ctx context.Context, snapshot queue.Snapshot, id int) error {
	if snapshot.Status == utils.QueueClosed {
		var fail bool
		messenger.once.Do(func() { fail = true })
		if fail {
			return errClosedDelivery
		}
	}
	return messenger.Messenger.EditQueue(ctx, snapshot, id)
}

type phaseGate struct {
	queue.Messenger
	phase   string
	started chan struct{}
	release chan struct{}
	once    sync.Once
	freed   sync.Once
}

var _ queue.Messenger = (*phaseGate)(nil)
var _ queue.LegacyMessenger = (*phaseGate)(nil)

func newPhaseGate(messenger queue.Messenger, phase string) *phaseGate {
	return &phaseGate{Messenger: messenger, phase: phase, started: make(chan struct{}), release: make(chan struct{})}
}

func (gate *phaseGate) unblock() { gate.freed.Do(func() { close(gate.release) }) }

func (gate *phaseGate) wait(ctx context.Context, phase string) error {
	if phase != gate.phase {
		return nil
	}
	var block bool
	gate.once.Do(func() { block = true; close(gate.started) })
	if !block {
		return nil
	}
	select {
	case <-gate.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *phaseGate) SendQueue(ctx context.Context, snapshot queue.Snapshot) (int, error) {
	if err := gate.wait(ctx, "send"); err != nil {
		return 0, err
	}
	return gate.Messenger.SendQueue(ctx, snapshot)
}

func (gate *phaseGate) EditQueue(ctx context.Context, snapshot queue.Snapshot, id int) error {
	if err := gate.wait(ctx, "edit"); err != nil {
		return err
	}
	return gate.Messenger.EditQueue(ctx, snapshot, id)
}

func (gate *phaseGate) QueueMessageDate(ctx context.Context, _ db.Schedule, _ int) (time.Time, error) {
	if err := gate.wait(ctx, "metadata"); err != nil {
		return time.Time{}, err
	}
	return time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), nil
}

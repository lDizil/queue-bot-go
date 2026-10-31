package settings

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestValidateRejectsUnsafeSettings(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Values)
	}{
		{"no slots", func(v *Values) { v.TotalSlotsInQueue = 0 }},
		{"too many buttons", func(v *Values) { v.TotalSlotsInQueue = 91 }},
		{"no columns", func(v *Values) { v.AmountOfButtonsInRow = 0 }},
		{"too many columns", func(v *Values) { v.AmountOfButtonsInRow = 9 }},
		{"invalid date", func(v *Values) { v.Week1Date = "2026-02-29" }},
		{"noncanonical date", func(v *Values) { v.Week1Date = "2026-04-1" }},
		{"unknown parity", func(v *Values) { v.Week1Type = "both" }},
		{"no debounce", func(v *Values) { v.DelayUpdateQueue = 0 }},
		{"excessive debounce", func(v *Values) { v.DelayUpdateQueue = time.Minute }},
		{"no session expiry", func(v *Values) { v.TimeForExpiredEditSes = 0 }},
		{"negative ticker", func(v *Values) { v.SchedulerTickInterval = -time.Second }},
		{"busy ticker", func(v *Values) { v.SchedulerTickInterval = time.Nanosecond }},
		{"no opening delay", func(v *Values) { v.OneShotDelay = 0 }},
		{"no queue duration", func(v *Values) { v.OneShotDuration = 0 }},
		{"no workers", func(v *Values) { v.UpdateWorkers = 0 }},
		{"too many workers", func(v *Values) { v.UpdateWorkers = 33 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := Defaults()
			test.change(&values)
			if err := values.Validate(); err == nil {
				t.Fatal("unsafe settings accepted")
			}
		})
	}
}

func TestManagerPersistsInitialSettingsAndRestoresThem(t *testing.T) {
	var (
		ctx   = context.Background()
		store memoryStore
	)
	seed := Defaults()
	seed.TotalSlotsInQueue = 24
	manager, err := NewManager(ctx, &store, seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Update(ctx, func(values *Values) error {
		values.Week1Type = "even"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := NewManager(ctx, &store, Values{UpdateWorkers: Defaults().UpdateWorkers})
	if err != nil {
		t.Fatal(err)
	}
	expected := seed
	expected.Week1Type = "even"
	if got := restored.Values(); got != expected {
		t.Fatalf("restored settings = %+v, want %+v", got, expected)
	}
	if store.saves != 2 {
		t.Fatalf("saves = %d, want initial save and update", store.saves)
	}
}

func TestManagerUsesStartupWorkersWithoutOverwritingRuntimeConfiguration(t *testing.T) {
	var (
		ctx       = context.Background()
		persisted = Defaults()
		seed      = Defaults()
	)
	persisted.TotalSlotsInQueue = 24
	persisted.Week1Type = "even"
	seed.TotalSlotsInQueue = 60
	seed.UpdateWorkers = 8
	store := memoryStore{values: persisted, found: true}
	manager, err := NewManager(ctx, &store, seed)
	if err != nil {
		t.Fatal(err)
	}
	expected := persisted
	expected.UpdateWorkers = 8
	if got := manager.Values(); got != expected {
		t.Fatalf("startup configuration overwrote runtime settings: %+v, want %+v", got, expected)
	}
	if store.values != expected {
		t.Fatal("stored settings do not reflect the current startup worker count")
	}
	if err := manager.Update(ctx, func(values *Values) error {
		values.UpdateWorkers = 16
		return nil
	}); err == nil {
		t.Fatal("worker count was accepted as a runtime update")
	}
	if manager.Values() != expected || store.values != expected {
		t.Fatal("rejected worker update changed active or persisted configuration")
	}
}

func TestManagerKeepsPreviousSettingsOnFailedUpdate(t *testing.T) {
	var (
		ctx         = context.Background()
		store       = memoryStore{values: Defaults(), found: true}
		saveError   = errors.New("database unavailable")
		changeError = errors.New("cannot apply change")
	)
	manager, err := NewManager(ctx, &store, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	original := manager.Values()
	if err := manager.Update(ctx, func(values *Values) error {
		values.Week1Type = "even"
		return changeError
	}); !errors.Is(err, changeError) {
		t.Fatalf("change error = %v, want %v", err, changeError)
	}
	if err := manager.Update(ctx, func(values *Values) error {
		values.TotalSlotsInQueue = 0
		return nil
	}); err == nil {
		t.Fatal("invalid update accepted")
	}
	if store.saves != 0 {
		t.Fatalf("unvalidated settings reached storage: %d writes", store.saves)
	}
	store.saveError = saveError
	if err := manager.Update(ctx, func(values *Values) error {
		values.Week1Type = "even"
		return nil
	}); !errors.Is(err, saveError) {
		t.Fatalf("save error = %v, want %v", err, saveError)
	}
	if got := manager.Values(); got != original {
		t.Fatalf("failed update changed published settings: %+v", got)
	}
	if store.values != original {
		t.Fatalf("failed update changed stored settings: %+v", store.values)
	}
}

func TestManagerConcurrentUpdatesDoNotLoseChanges(t *testing.T) {
	var (
		ctx     = context.Background()
		store   = memoryStore{values: Defaults(), found: true}
		workers sync.WaitGroup
	)
	manager, err := NewManager(ctx, &store, Defaults())
	if err != nil {
		t.Fatal(err)
	}
	const updates = 32
	failures := make(chan error, updates)
	start := make(chan struct{})
	for range updates {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			failures <- manager.Update(ctx, func(values *Values) error {
				current := values.TotalSlotsInQueue
				runtime.Gosched()
				values.TotalSlotsInQueue = current + 1
				return nil
			})
		}()
	}
	close(start)
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := Defaults().TotalSlotsInQueue + updates
	if got := manager.Values().TotalSlotsInQueue; got != expected {
		t.Fatalf("slots = %d, want %d; concurrent update lost", got, expected)
	}
	if manager.Values() != store.values {
		t.Fatal("published settings differ from stored settings")
	}
}

func TestManagerReadersSeeCommittedValues(t *testing.T) {
	var (
		ctx     = context.Background()
		initial = Defaults()
		store   = blockingStore{
			memoryStore: memoryStore{values: initial, found: true},
			started:     make(chan struct{}),
			release:     make(chan struct{}),
		}
	)
	manager, err := NewManager(ctx, &store, initial)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- manager.Update(ctx, func(values *Values) error {
			values.Week1Type = "even"
			return nil
		})
	}()
	<-store.started
	if got := manager.Values(); got != initial {
		close(store.release)
		t.Fatalf("uncommitted settings published: %+v", got)
	}
	close(store.release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := manager.Values().Week1Type; got != "even" {
		t.Fatalf("committed week type = %s, want even", got)
	}
}

type memoryStore struct {
	mu        sync.Mutex
	values    Values
	found     bool
	saves     int
	saveError error
}

func (s *memoryStore) LoadSettings(context.Context) (Values, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values, s.found, nil
}

func (s *memoryStore) SaveSettings(_ context.Context, values Values) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.saveError != nil {
		return s.saveError
	}
	s.values = values
	s.found = true
	return nil
}

type blockingStore struct {
	memoryStore
	started chan struct{}
	release chan struct{}
}

func (s *blockingStore) SaveSettings(ctx context.Context, values Values) error {
	close(s.started)
	<-s.release
	return s.memoryStore.SaveSettings(ctx, values)
}

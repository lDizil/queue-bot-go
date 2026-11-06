package settings

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Values struct {
	TotalSlotsInQueue       int           `json:"total_slots_in_queue" mapstructure:"total_slots_in_queue"`
	AmountOfButtonsInRow    int           `json:"amount_of_buttons_in_row" mapstructure:"amount_of_buttons_in_row"`
	DelayUpdateQueue        time.Duration `json:"delay_update_queue" mapstructure:"delay_update_queue"`
	TimeForExpiredEditSes   time.Duration `json:"edit_session_timeout" mapstructure:"edit_session_timeout"`
	Week1Date               string        `json:"week1_date" mapstructure:"week1_date"`
	Week1Type               string        `json:"week1_type" mapstructure:"week1_type"`
	SchedulerTickInterval   time.Duration `json:"scheduler_tick_interval" mapstructure:"scheduler_tick_interval"`
	OneShotDelay            time.Duration `json:"one_shot_delay" mapstructure:"one_shot_delay"`
	OneShotDuration         time.Duration `json:"one_shot_duration" mapstructure:"one_shot_duration"`
	UpdateWorkers           int           `json:"update_workers" mapstructure:"update_workers"`
	ModerationBlockDuration time.Duration `json:"moderation_block_duration" mapstructure:"moderation_block_duration"`
}

func Defaults() Values {
	return Values{
		TotalSlotsInQueue:       30,
		AmountOfButtonsInRow:    5,
		DelayUpdateQueue:        400 * time.Millisecond,
		TimeForExpiredEditSes:   4 * time.Minute,
		Week1Date:               "2026-04-10",
		Week1Type:               "odd",
		SchedulerTickInterval:   time.Second,
		OneShotDelay:            6 * time.Minute,
		OneShotDuration:         90 * time.Minute,
		UpdateWorkers:           4,
		ModerationBlockDuration: 3 * time.Minute,
	}
}

func (v Values) Validate() error {
	if v.TotalSlotsInQueue < 1 || v.TotalSlotsInQueue > 90 {
		return errors.New("total_slots_in_queue must be between 1 and 90")
	}
	if v.AmountOfButtonsInRow < 1 || v.AmountOfButtonsInRow > 8 {
		return errors.New("amount_of_buttons_in_row must be between 1 and 8")
	}
	if v.DelayUpdateQueue < 50*time.Millisecond || v.DelayUpdateQueue > 30*time.Second {
		return errors.New("delay_update_queue must be between 50ms and 30s")
	}
	if v.TimeForExpiredEditSes < 30*time.Second || v.TimeForExpiredEditSes > 24*time.Hour {
		return errors.New("edit_session_timeout must be between 30s and 24h")
	}
	anchor, err := time.Parse("2006-01-02", v.Week1Date)
	if err != nil || anchor.Format("2006-01-02") != v.Week1Date {
		return errors.New("week1_date must be a valid date in YYYY-MM-DD format")
	}
	if v.Week1Type != "odd" && v.Week1Type != "even" {
		return errors.New("week1_type must be odd or even")
	}
	if v.SchedulerTickInterval < 100*time.Millisecond || v.SchedulerTickInterval > time.Minute {
		return errors.New("scheduler_tick_interval must be between 100ms and 1m")
	}
	if v.OneShotDelay < time.Minute || v.OneShotDelay > 7*24*time.Hour {
		return errors.New("one_shot_delay must be between 1m and 168h")
	}
	if v.OneShotDuration < time.Minute || v.OneShotDuration > 7*24*time.Hour {
		return errors.New("one_shot_duration must be between 1m and 168h")
	}
	if v.ModerationBlockDuration < time.Second || v.ModerationBlockDuration > 24*time.Hour {
		return errors.New("moderation_block_duration must be between 1s and 24h")
	}
	if v.UpdateWorkers < 1 || v.UpdateWorkers > 32 {
		return errors.New("update_workers must be between 1 and 32")
	}
	return nil
}

type Store interface {
	LoadSettings(context.Context) (Values, bool, error)
	SaveSettings(context.Context, Values) error
}

type Manager struct {
	store    Store
	updateMu sync.Mutex
	mu       sync.RWMutex
	values   Values
}

func NewManager(ctx context.Context, store Store, seed Values) (*Manager, error) {
	if store == nil {
		return nil, errors.New("settings store is required")
	}
	values, found, err := store.LoadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	needsSave := !found
	if !found {
		values = seed
	} else if values.UpdateWorkers != seed.UpdateWorkers {
		values.UpdateWorkers = seed.UpdateWorkers
		needsSave = true
	}
	if err := values.Validate(); err != nil {
		return nil, fmt.Errorf("invalid settings: %w", err)
	}
	if needsSave {
		if err := store.SaveSettings(ctx, values); err != nil {
			return nil, fmt.Errorf("initialize settings: %w", err)
		}
	}
	return &Manager{store: store, values: values}, nil
}

func (m *Manager) Values() Values {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.values
}

func (m *Manager) Update(ctx context.Context, change func(*Values) error) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	current := m.Values()
	values := current
	if err := change(&values); err != nil {
		return err
	}
	if values.UpdateWorkers != current.UpdateWorkers {
		return errors.New("update_workers is a startup-only setting")
	}
	if err := values.Validate(); err != nil {
		return err
	}
	if err := m.store.SaveSettings(ctx, values); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	m.mu.Lock()
	m.values = values
	m.mu.Unlock()
	return nil
}

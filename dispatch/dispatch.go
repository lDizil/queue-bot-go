package dispatch

import (
	"context"
	"errors"
	"sync"
)

var ErrClosed = errors.New("dispatcher is closed")

type action func(context.Context)

type lane struct {
	actions []action
	running bool
}

type Dispatcher struct {
	mu       sync.Mutex
	lanes    map[string]*lane
	ready    []string
	queued   int
	capacity int
	closed   bool
	changed  chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(workers, capacity int) *Dispatcher {
	if workers < 1 || capacity < 1 {
		panic("dispatcher workers and capacity must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		lanes: make(map[string]*lane), capacity: capacity,
		changed: make(chan struct{}), ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	var workersDone sync.WaitGroup
	for range workers {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			d.worker()
		}()
	}
	go func() {
		workersDone.Wait()
		cancel()
		close(d.done)
	}()
	return d
}

func (d *Dispatcher) Submit(ctx context.Context, key string, run func(context.Context)) error {
	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return ErrClosed
		}
		if err := ctx.Err(); err != nil {
			d.mu.Unlock()
			return err
		}
		if d.queued < d.capacity {
			current := d.lanes[key]
			if current == nil {
				current = &lane{}
				d.lanes[key] = current
			}
			if !current.running && len(current.actions) == 0 {
				d.ready = append(d.ready, key)
			}
			current.actions = append(current.actions, run)
			d.queued++
			d.notify()
			d.mu.Unlock()
			return nil
		}
		changed := d.changed
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	d.closed = true
	d.notify()
	d.mu.Unlock()
	select {
	case <-d.done:
		return nil
	default:
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		d.cancel()
		return ctx.Err()
	}
}

func (d *Dispatcher) worker() {
	for {
		d.mu.Lock()
		for len(d.ready) == 0 {
			if d.closed {
				d.mu.Unlock()
				return
			}
			changed := d.changed
			d.mu.Unlock()
			<-changed
			d.mu.Lock()
		}
		key := d.ready[0]
		d.ready[0] = ""
		d.ready = d.ready[1:]
		current := d.lanes[key]
		run := current.actions[0]
		current.actions[0] = nil
		current.actions = current.actions[1:]
		current.running = true
		d.queued--
		d.notify()
		d.mu.Unlock()

		run(d.ctx)

		d.mu.Lock()
		current.running = false
		if len(current.actions) == 0 {
			delete(d.lanes, key)
		} else {
			d.ready = append(d.ready, key)
		}
		d.notify()
		d.mu.Unlock()
	}
}

func (d *Dispatcher) notify() {
	close(d.changed)
	d.changed = make(chan struct{})
}

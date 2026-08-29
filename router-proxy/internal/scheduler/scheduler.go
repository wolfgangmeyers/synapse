// Package scheduler coordinates durable sweeps without owning transport or
// persistence details.
package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Clock interface{ Now() time.Time }

type SweepFunc func(context.Context, time.Time) error

type Scheduler struct {
	clock   Clock
	sweep   SweepFunc
	mu      sync.Mutex
	running bool
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func New(clock Clock, sweep SweepFunc) (*Scheduler, error) {
	if clock == nil {
		return nil, errors.New("scheduler clock is required")
	}
	if sweep == nil {
		return nil, errors.New("scheduler sweep is required")
	}
	return &Scheduler{clock: clock, sweep: sweep}, nil
}

// Run invokes one sweep. A concurrent invocation is skipped and reports false.
// The callback receives one captured time, making ordering tests independent of
// wall-clock changes during a sweep.
func (s *Scheduler) Run(ctx context.Context) (ran bool, err error) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return false, nil
	}
	s.running = true
	now := s.clock.Now()
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.running = false; s.mu.Unlock() }()
	return true, s.sweep(ctx, now)
}

func (s *Scheduler) Running() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.running }

// Start performs the startup sweep synchronously, then runs one sweep every
// interval. The default interval is one second, matching the Node router.
func (s *Scheduler) Start(ctx context.Context, intervals ...time.Duration) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if len(intervals) > 1 {
		return errors.New("scheduler accepts at most one interval")
	}
	interval := time.Second
	if len(intervals) == 1 && intervals[0] != 0 {
		interval = intervals[0]
	}
	if interval <= 0 {
		return errors.New("scheduler interval must be positive")
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("scheduler is already started")
	}
	s.mu.Unlock()
	if _, err := s.Run(ctx); err != nil {
		return err
	}
	child, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		cancel()
		return errors.New("scheduler is already started")
	}
	s.started, s.cancel, s.done = true, cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go s.loop(child, interval, done)
	return nil
}

func (s *Scheduler) loop(ctx context.Context, interval time.Duration, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.Run(ctx)
		}
	}
}

func (s *Scheduler) Stop() error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel, done := s.cancel, s.done
	s.started, s.cancel, s.done = false, nil, nil
	s.mu.Unlock()
	cancel()
	<-done
	return nil
}

func (s *Scheduler) Started() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.started }

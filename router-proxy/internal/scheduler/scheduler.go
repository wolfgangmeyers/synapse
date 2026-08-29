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

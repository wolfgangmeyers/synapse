package attachments

import (
	"context"
	"errors"
	"sync"
	"time"
)

const DefaultSweepInterval = time.Second

// Coordinator runs attachment cleanup independently of HTTP and router
// handlers. Startup and shutdown sweeps are synchronous; periodic work is
// serialized by Store.Sweep.
type Coordinator struct {
	store      *Store
	referenced func(string) bool
	interval   time.Duration
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
	started    bool
	lastErr    error
	sweepCount int
}

func NewCoordinator(store *Store, referenced func(string) bool, interval time.Duration) (*Coordinator, error) {
	if store == nil {
		return nil, errors.New("attachment store is required")
	}
	if interval == 0 {
		interval = DefaultSweepInterval
	}
	if interval <= 0 {
		return nil, errors.New("attachment sweep interval must be positive")
	}
	if referenced == nil {
		referenced = store.referenced
	}
	return &Coordinator{store: store, referenced: referenced, interval: interval}, nil
}

func (c *Coordinator) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("attachment coordinator is already started")
	}
	c.mu.Unlock()
	if _, err := c.store.Sweep(c.referenced); err != nil {
		return err
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("attachment coordinator is already started")
	}
	child, cancel := context.WithCancel(ctx)
	c.cancel, c.done, c.started = cancel, make(chan struct{}), true
	c.recordSweepLocked()
	done := c.done
	c.mu.Unlock()
	go c.loop(child, done)
	return nil
}

func (c *Coordinator) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := c.store.Sweep(c.referenced)
			c.mu.Lock()
			c.lastErr = err
			if err == nil {
				c.recordSweepLocked()
			}
			c.mu.Unlock()
		}
	}
}

func (c *Coordinator) Stop() error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return nil
	}
	cancel, done := c.cancel, c.done
	c.started, c.cancel, c.done = false, nil, nil
	c.mu.Unlock()
	cancel()
	<-done
	_, err := c.store.Sweep(c.referenced)
	c.mu.Lock()
	c.lastErr = err
	if err == nil {
		c.recordSweepLocked()
	}
	c.mu.Unlock()
	return err
}

func (c *Coordinator) Running() bool      { c.mu.Lock(); defer c.mu.Unlock(); return c.started }
func (c *Coordinator) SweepCount() int    { c.mu.Lock(); defer c.mu.Unlock(); return c.sweepCount }
func (c *Coordinator) LastError() error   { c.mu.Lock(); defer c.mu.Unlock(); return c.lastErr }
func (c *Coordinator) recordSweepLocked() { c.sweepCount++ }

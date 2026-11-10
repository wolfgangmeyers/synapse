package managed

import (
	"context"
	"errors"
	"sync"
)

// The mutex protects only admission state, never process operations. A revoked
// capability remains fenced even when waiting for an admitted launch times out.
type backgroundBashOwnerCapability struct {
	mu              sync.Mutex
	revoked         bool
	active          chan struct{}
	fenced          chan struct{}
	deferredCleanup bool
	afterLaunch     func()
}

var errBackgroundBashOwnerRetired = errors.New("background bash owner retired")

func (c *backgroundBashOwnerCapability) launch(start func() (BackgroundBashJob, error)) (BackgroundBashJob, error) {
	return c.launchContext(context.Background(), start)
}

func (c *backgroundBashOwnerCapability) launchContext(ctx context.Context, start func() (BackgroundBashJob, error)) (BackgroundBashJob, error) {
	if c == nil {
		return BackgroundBashJob{}, errBackgroundBashOwnerRetired
	}
	for {
		c.mu.Lock()
		if c.revoked {
			c.mu.Unlock()
			return BackgroundBashJob{}, errBackgroundBashOwnerRetired
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return BackgroundBashJob{}, err
		}
		if c.active == nil {
			done := make(chan struct{})
			c.active = done
			c.mu.Unlock()
			defer func() {
				c.mu.Lock()
				c.active = nil
				close(done)
				after := c.afterLaunch
				c.afterLaunch = nil
				c.mu.Unlock()
				if after != nil {
					go after()
				}
			}()
			return start()
		}
		done := c.active
		if c.fenced == nil {
			c.fenced = make(chan struct{})
		}
		fenced := c.fenced
		c.mu.Unlock()
		select {
		case <-done:
		case <-fenced:
		case <-ctx.Done():
			return BackgroundBashJob{}, ctx.Err()
		}
	}
}

// deferCleanupAfterLaunch schedules at most one cleanup for this retired
// capability, without creating a waiter for a launch that never settles.
func (c *backgroundBashOwnerCapability) deferCleanupAfterLaunch(cleanup func()) {
	c.mu.Lock()
	if c.deferredCleanup {
		c.mu.Unlock()
		return
	}
	c.deferredCleanup = true
	if c.active != nil {
		c.afterLaunch = cleanup
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	go cleanup()
}

func (c *backgroundBashOwnerCapability) revoke(cleanup func() error) error {
	return c.revokeContext(context.Background(), cleanup)
}

// Fence first, then wait for the admitted launch before inspecting its jobs.
// A timeout is not successful cleanup; callers may retry with a fresh context.
func (c *backgroundBashOwnerCapability) revokeContext(ctx context.Context, cleanup func() error) error {
	if c == nil {
		return errBackgroundBashOwnerRetired
	}
	c.mu.Lock()
	if !c.revoked {
		c.revoked = true
		if c.fenced != nil {
			close(c.fenced)
		}
	}
	done := c.active
	c.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return cleanup()
}

package managed

import (
	"context"
	"errors"
	"log"
	"time"
)

// retireBackgroundBashOwner fences launches before waiting for owned jobs.
func (m *Manager) retireBackgroundBashOwner(owner embeddedOwner) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.retireBackgroundBashOwnerContext(ctx, owner)
}

func (m *Manager) retireBackgroundBashOwnerContext(ctx context.Context, owner embeddedOwner) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m.mu.Lock()
	if m.backgroundBash == nil && !m.closed {
		m.backgroundBash = NewBackgroundBashManager(m.backgroundBashCompleted)
		m.backgroundBash.SetChangeCallbackContext(m.backgroundBashChanged)
	}
	jobs := m.backgroundBash
	m.mu.Unlock()
	if owner.backgroundBash == nil {
		// Legacy test fixtures may have no launch capability; no stale launch
		// can be admitted from such an owner.
		if jobs != nil {
			return jobs.StopOwnerContext(ctx, backgroundBashOwnerKey(owner))
		}
		return nil
	}
	err := owner.backgroundBash.revokeContext(ctx, func() error {
		if jobs == nil {
			return nil
		}
		return jobs.StopOwnerContext(ctx, backgroundBashOwnerKey(owner))
	})
	if jobs != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		owner.backgroundBash.deferCleanupAfterLaunch(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if cleanupErr := jobs.StopOwnerContext(cleanupCtx, backgroundBashOwnerKey(owner)); cleanupErr != nil {
				log.Printf("[embedded:%s] deferred background bash retirement cleanup failed: %v", owner.binding.AgentID, cleanupErr)
			}
		})
	}
	return err
}

func (m *Manager) retireBackgroundBashAgent(ctx context.Context, agentID string) error {
	m.mu.Lock()
	owner, ok := m.embeddedOwners[agentID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return m.retireBackgroundBashOwnerContext(ctx, owner)
}

package managed

import (
	"context"
	"errors"
	"time"
)

// Wait observes the entry's completion channel while holding the same lock
// used to publish its final snapshot. It does not spend polling credits.
func (m *BackgroundBashManager) Wait(ctx context.Context, owner, id string) (BackgroundBashJob, error) {
	m.mu.Lock()
	e := m.jobs[id]
	if e == nil || e.job.Owner != owner || backgroundBashExpired(e, time.Now()) {
		m.mu.Unlock()
		return BackgroundBashJob{}, errors.New("background bash job not found")
	}
	done := e.done
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return BackgroundBashJob{}, ctx.Err()
	case <-done:
		m.mu.Lock()
		job := m.snapshot(e)
		m.mu.Unlock()
		return job, nil
	}
}

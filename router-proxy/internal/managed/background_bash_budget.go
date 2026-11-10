package managed

import (
	"errors"
	"time"
)

const backgroundBashReadExhausted = "Background bash read budget exhausted: wait for the completion notification or continue other work. Credits replenish at one per minute up to three."

// consumeBackgroundBashReadCredit uses the same lane and lock as the separate
// subagent pool, but never spends subagent credits.
func (l *managedAgentLane) consumeBackgroundBashReadCredit(now time.Time) error {
	if l == nil {
		return errors.New("background bash lane unavailable")
	}
	l.activeMu.Lock()
	defer l.activeMu.Unlock()
	refillReadPool(&l.backgroundBashReadPoolRemainingMs, &l.backgroundBashReadPoolUpdatedAt, now, true)
	if l.backgroundBashReadPoolRemainingMs < SubagentReadReplenishmentIntervalMS {
		return errors.New(backgroundBashReadExhausted)
	}
	l.backgroundBashReadPoolRemainingMs -= SubagentReadReplenishmentIntervalMS
	return nil
}

// Package managed provides lifecycle orchestration, session isolation, and
// runtime tool governance for managed agents.
//
// Subagent Read Budget Design:
// Embedded subagent read tools (get_subagent_result on active subagents and list_subagents)
// enforce a token-bucket replenishment rate limit on managedAgentLane. This budget
// replaces noisy prompt deterrent warnings and complex per-child throttle maps.
// When agents launch background subagents, repeated polling consumes credit and rapidly
// triggers an explicit redirection error, cleanly steering polling agents toward resting,
// progressing unblocked work, or awaiting asynchronous push notifications upon completion.
// Finished or terminal subagents bypass the budget so deliverable inspection is never blocked.
package managed

import (
	"errors"
	"time"
)

const (
	// SubagentReadPoolCapacity is the maximum number of credits in the subagent read pool.
	SubagentReadPoolCapacity = 3

	// SubagentReadReplenishmentIntervalMS is the duration in milliseconds required to refill 1 credit (1 minute).
	SubagentReadReplenishmentIntervalMS = int64(60_000)

	// SubagentReadPoolMaxMs is the maximum capacity of the subagent read pool in milliseconds.
	SubagentReadPoolMaxMs = SubagentReadPoolCapacity * SubagentReadReplenishmentIntervalMS

	// SubagentReadExhaustionError is the exact redirection error returned when an agent exhausts its subagent read budget.
	SubagentReadExhaustionError = "Subagent read budget exhausted: you have used all available subagent read credits. Subagent read tools (get_subagent_result and list_subagents) are intended only for occasional checks; repeated polling of subagent status is disallowed. You must rest, continue unblocked work, or wait for the automatic completion notification instead of polling. (Credits replenish at 1 credit per minute up to a maximum of 3.)"
)

// replenishSubagentReadPool refills the subagent read credit pool up to capacity based on elapsed time.
// Must be called with l.activeMu locked.
func (l *managedAgentLane) replenishSubagentReadPool(now time.Time) {
	refillReadPool(&l.subagentReadPoolRemainingMs, &l.subagentReadPoolUpdatedAt, now, false)
}

// consumeSubagentReadCredit attempts to deduct 1 credit from the lane's subagent read pool.
// Returns SubagentReadExhaustionError if insufficient credits remain.
func (l *managedAgentLane) consumeSubagentReadCredit(now time.Time) error {
	if l == nil {
		return nil
	}
	l.activeMu.Lock()
	defer l.activeMu.Unlock()

	l.replenishSubagentReadPool(now)
	if l.subagentReadPoolRemainingMs < SubagentReadReplenishmentIntervalMS {
		return errors.New(SubagentReadExhaustionError)
	}

	l.subagentReadPoolRemainingMs -= SubagentReadReplenishmentIntervalMS
	return nil
}

// subagentReadPoolRemaining returns the fractional pool credits and whole available credits.
func (l *managedAgentLane) subagentReadPoolRemaining(now time.Time) (float64, int) {
	if l == nil {
		return 0, 0
	}
	l.activeMu.Lock()
	defer l.activeMu.Unlock()

	l.replenishSubagentReadPool(now)
	availableWhole := int(l.subagentReadPoolRemainingMs / SubagentReadReplenishmentIntervalMS)
	fractional := float64(l.subagentReadPoolRemainingMs) / float64(SubagentReadReplenishmentIntervalMS)
	return fractional, availableWhole
}

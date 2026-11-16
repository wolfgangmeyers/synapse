package managed

import (
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type claudeRetryDecision int

const (
	claudeRetryBlocked claudeRetryDecision = iota
	claudeRetryWait
	claudeRetryExecute
	claudeRetrySettle
)

// An admitted execution whose result is unknown must never be replayed or settled.
func decideClaudeRetry(state store.ClaudeStartRetryState, now time.Time) claudeRetryDecision {
	if state.InFlight {
		return claudeRetryBlocked
	}
	if state.PromptSettlement || !now.Before(state.Deadline) {
		return claudeRetrySettle
	}
	if state.Executions >= 5 || now.Before(state.NextEligible) {
		return claudeRetryWait
	}
	return claudeRetryExecute
}

// Only the explicit, fenced absence result authorizes first execution.
func claudeRetryAbsent(err error) bool { return errors.Is(err, store.ErrClaudeRetryAbsent) }

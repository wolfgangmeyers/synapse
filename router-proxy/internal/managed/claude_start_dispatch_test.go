package managed

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeRetryDecision(t *testing.T) {
	now := time.Unix(1000, 0)
	state := store.ClaudeStartRetryState{Deadline: now.Add(time.Minute), NextEligible: now.Add(5 * time.Second), Executions: 1}
	require.Equal(t, claudeRetryWait, decideClaudeRetry(state, now))
	require.Equal(t, claudeRetryExecute, decideClaudeRetry(state, now.Add(5*time.Second)))
	require.Equal(t, claudeRetrySettle, decideClaudeRetry(state, state.Deadline))
	state.InFlight = true
	require.Equal(t, claudeRetryBlocked, decideClaudeRetry(state, state.Deadline))
	state.InFlight = false
	state.Executions = 5
	require.Equal(t, claudeRetryWait, decideClaudeRetry(state, now.Add(10*time.Second)))
}

func TestClaudeRetryLookupMustDistinguishAbsentFromErrors(t *testing.T) {
	require.True(t, claudeRetryAbsent(store.ErrClaudeRetryAbsent))
	require.False(t, claudeRetryAbsent(nil))
	require.False(t, claudeRetryAbsent(sql.ErrNoRows))
	require.False(t, claudeRetryAbsent(errors.New("database unavailable")))
}

func TestClaudeRetryPromptSettlementRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	state := store.ClaudeStartRetryState{Deadline: now.Add(time.Minute), NextEligible: now.Add(5 * time.Second), Executions: 1, PromptSettlement: true}
	require.Equal(t, claudeRetrySettle, decideClaudeRetry(state, now))
	state.InFlight = true
	require.Equal(t, claudeRetryBlocked, decideClaudeRetry(state, now))
}

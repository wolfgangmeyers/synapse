package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestWorkflowJournalReceiptReplayConflictAndAgentDeletion(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	calls := 0
	apply := func() (string, error) { calls++; return `{"run_id":"run"}`, nil }
	first, replay, err := s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "op", "payload", apply)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, `{"run_id":"run"}`, first)
	again, replay, err := s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "op", "payload", apply)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	_, _, err = s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "op", "different", apply)
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	require.Equal(t, 1, calls)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_readiness_sources WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	var receiptCount int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_receipts WHERE agent_id=?`, agent.AgentID).Scan(&receiptCount))
	require.Equal(t, 1, receiptCount)
	// A receipt replays after agent deletion, but does not restore journal access.
	deletedReplay, replay, err := s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "op", "payload", apply)
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, deletedReplay)
	_, replay, err = s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "op", "changed", apply)
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	require.False(t, replay)
	// A fresh operation is not admitted for the deleted agent.
	_, replay, err = s.WorkflowJournalOperation(ctx, agent.AgentID, "transition", "new-op", "payload", apply)
	var missing *ManagedAgentNotFoundError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, agent.AgentID, missing.AgentID)
	require.False(t, replay)
	require.Equal(t, 1, calls)
}

func TestWorkflowJournalReceiptExpiryBoundary(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	calls := 0
	apply := func() (string, error) { calls++; return "ok", nil }
	acceptedAt := s.clock.Now()
	_, _, err := s.WorkflowJournalOperation(ctx, agent.AgentID, "note", "id", "x", apply)
	require.NoError(t, err)
	// A forward persisted cutoff may expire receipts early; rollback cannot restore them.
	jump := acceptedAt.Add(24 * time.Hour)
	s.clock = deterministic.NewFixedClock(jump)
	_, _, err = s.WorkflowJournalOperation(ctx, agent.AgentID, "note", "advance-cutoff", "x", apply)
	require.NoError(t, err)
	// Roll back to before the receipt's original expiry. Persisted cutoff still
	// expires it early and it must not replay.
	s.clock = deterministic.NewFixedClock(acceptedAt.Add(1 * time.Minute))
	_, replayed, err := s.WorkflowJournalOperation(ctx, agent.AgentID, "note", "id", "x", apply)
	require.NoError(t, err)
	require.False(t, replayed)
	// Calls: initial receipt, cutoff-advance operation, and the non-replay.
	require.Equal(t, 3, calls)
	// Forward persisted cutoff expired the original receipt; rollback cannot replay it.
	require.Equal(t, 3, calls)
}

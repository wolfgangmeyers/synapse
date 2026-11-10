package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUILifecycleHistoryCannotResurrectAfterTermination(t *testing.T) {
	s, _, agent, _ := restartFixture(t)
	ctx := context.Background()
	var incarnation string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT incarnation_id FROM managed_history_boundaries WHERE agent_id=?`, agent.AgentID).Scan(&incarnation))
	presentation := &ManagedHistoryPresentation{Kind: "error", Summary: "failure"}
	write := func(id string) ManagedHistoryEntry {
		entry, err := s.RecordManagedLifecycleHistoryForIncarnation(ctx, agent.AgentID, incarnation, "Agent failed", id, presentation)
		require.NoError(t, err)
		return entry
	}
	first := write("old-before")
	require.NotEmpty(t, first.ID)
	require.Equal(t, first.ID, write("old-before").ID)
	fence := restartFenceFromAgent(&agent)
	admission := terminateArchiveTestAction(t, s, agent, lifecycleUUID(998), fence)
	advanceArchiveActionToDurableWrite(t, s, admission.Action.ActionID, fence)
	require.NoError(t, s.CompleteManagedTermination(ctx, admission.Action.ActionID))
	require.Empty(t, write("old-after").ID)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=?`, agent.AgentID).Scan(&count))
	require.Zero(t, count)
}

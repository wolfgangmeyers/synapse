package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func readyParentCompactionFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "parent-compaction", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"parent-compaction"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	return durable, fence
}

func TestReconcileManagedParentCompactionNeverCallsMissingEndSuccess(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)
	updated, err := durable.ReconcileManagedParentCompaction(ctx, fence, true)
	require.NoError(t, err)
	require.True(t, updated, "get_state=true repairs a missing start")
	updated, err = durable.ReconcileManagedParentCompaction(ctx, fence, false)
	require.NoError(t, err)
	require.True(t, updated, "get_state=false clears only as an unknown reconciliation")
	var outcome string
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT outcome FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY complete_sequence DESC LIMIT 1`, fence.AgentID).Scan(&outcome))
	require.Equal(t, "reconciled_unknown", outcome)
}

func TestManagedParentActivityTracksNativeTurnsWithoutOverridingCompaction(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)

	updated, err := durable.ApplyManagedParentActivity(ctx, fence, ManagedParentActivityEvent{Sequence: 1, State: "active"})
	require.NoError(t, err)
	require.True(t, updated)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	updated, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 2, State: "compacting"})
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = durable.ApplyManagedParentActivity(ctx, fence, ManagedParentActivityEvent{Sequence: 3, State: "idle"})
	require.NoError(t, err)
	require.True(t, updated)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "compacting", agent.ActivityState, "a settled turn cannot hide active compaction")

	updated, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 4, State: "idle", Outcome: "completed"})
	require.NoError(t, err)
	require.True(t, updated)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	updated, err = durable.ApplyManagedParentActivity(ctx, fence, ManagedParentActivityEvent{Sequence: 3, State: "active"})
	require.NoError(t, err)
	require.False(t, updated, "a delayed start cannot reactivate a settled later stream state")
}

func TestManagedParentCompactionPreservesExactOutcomesAndFencesDelayedEvents(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyParentCompactionFixture(t)

	updated, err := durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 1, State: "compacting"})
	require.NoError(t, err)
	require.True(t, updated)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "compacting", agent.ActivityState)

	failedTransition := ManagedParentCompactionEvent{Sequence: 2, State: "idle", Outcome: "failed"}
	updated, err = durable.ApplyManagedParentCompaction(ctx, fence, failedTransition)
	require.NoError(t, err)
	require.True(t, updated)
	liveEntry, err := durable.ManagedParentCompactionHistoryEntry(ctx, fence, failedTransition)
	require.NoError(t, err)
	require.Equal(t, "Context compaction failed", liveEntry.Message)
	require.Equal(t, &ManagedHistoryPresentation{Kind: "compaction", Outcome: "failed"}, liveEntry.Presentation)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	updated, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 2, State: "idle", Outcome: "failed"})
	require.NoError(t, err)
	require.False(t, updated, "duplicate source event cannot add another complete-history row")
	updated, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 1, State: "idle", Outcome: "completed"})
	require.NoError(t, err)
	require.False(t, updated, "an old terminal event cannot clear a later state")

	var transitions, complete int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_parent_compaction_transitions WHERE agent_id=?`, fence.AgentID).Scan(&transitions))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_parent_compaction'`, fence.AgentID).Scan(&complete))
	require.Equal(t, 2, transitions)
	require.Equal(t, transitions, complete, "the transition transaction is the sole parent complete-history writer")
	var outcome string
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT outcome FROM managed_parent_compaction_transitions WHERE agent_id=? AND source_sequence=2`, fence.AgentID).Scan(&outcome))
	require.Equal(t, "failed", outcome)
	completePage, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, completePage.Entries, 2)
	require.Equal(t, "Context compaction failed", completePage.Entries[0].Message)
	require.Equal(t, &ManagedHistoryPresentation{Kind: "compaction", Outcome: "failed"}, completePage.Entries[0].Presentation)

	stale := fence
	stale.RuntimeGeneration = "stale-generation"
	updated, err = durable.ApplyManagedParentCompaction(ctx, stale, ManagedParentCompactionEvent{Sequence: 3, State: "compacting"})
	var staleErr *ManagedReadinessSourceStaleError
	require.ErrorAs(t, err, &staleErr)
	require.False(t, updated)
}

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func builtinToolFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "builtin-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"builtin-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	return durable, fence
}

func TestManagedBuiltinToolLifecycleMutatesOneCompleteEntry(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)

	active, created, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "call-1", "bash")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "Running bash", active.Message)
	require.Equal(t, "active", active.Presentation.Outcome)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	terminal, changed, err := durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", "succeeded")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, active.ID, terminal.ID)
	require.Equal(t, "Ran bash", terminal.Message)
	require.Equal(t, "success", terminal.Presentation.Outcome)
	agent, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	repeated, changed, err := durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", "succeeded")
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, terminal, repeated)
	_, _, err = durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", "failed")
	require.Error(t, err)

	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, terminal, page.Entries[0])
}

func TestManagedOrdinaryToolCompletionIsMetadataOnlyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	entry, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "ordinary-call", "web_search", false)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "managed_tool", mustHistoryCategory(t, durable, entry))
	require.Equal(t, "Ran web_search", entry.Message)
	require.Equal(t, "success", entry.Presentation.Outcome)
	require.NotContains(t, fmt.Sprintf("%+v", entry), "private")

	replayed, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "ordinary-call", "web_search", false)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, entry, replayed)

	failed, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "failed-call", "web_search", true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "error", failed.Level)
	require.Equal(t, "TOOL_FAILED", failed.Presentation.Error.Code)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)
}

func mustHistoryCategory(t *testing.T, durable *Store, entry ManagedHistoryEntry) string {
	t.Helper()
	var category string
	require.NoError(t, durable.db.QueryRowContext(context.Background(), `SELECT category FROM managed_complete_history WHERE sequence=?`, entry.ID[len("complete:"):]).Scan(&category))
	return category
}

func TestManagedOrdinaryToolCompletionFailsClosedOnKeyConflict(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	entry, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "conflict-call", "web_search", false)
	require.NoError(t, err)
	require.True(t, created)
	key := managedOrdinaryToolCompletionKey(fence, "conflict-call")
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_complete_history SET message=? WHERE event_key=?`, "tampered", key)
	require.NoError(t, err)

	replayed, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "conflict-call", "web_search", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "conflicts with canonical metadata")
	require.False(t, created)
	require.Empty(t, replayed.ID)
	require.Len(t, builtinHistoryRowsFor(t, durable, fence.AgentID), 1)
	require.Equal(t, entry.ID, builtinHistoryRowsFor(t, durable, fence.AgentID)[0].ID)
}

func TestManagedOrdinaryToolCompletionFencesAndSeparatesIncarnations(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	stale := fence
	stale.ProcessIncarnation = "stale-incarnation"
	_, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, stale, "same-call", "web_search", false)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationFenceInactive)
	require.False(t, created)

	first, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "same-call", "web_search", false)
	require.NoError(t, err)
	require.True(t, created)
	secondFence := fence
	secondFence.PiSessionID = "replacement-session"
	secondFence.SessionEpoch = 2
	secondFence.ProcessIncarnation = "replacement-incarnation"
	secondFence.RuntimeGeneration = "replacement-generation"
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_incarnation=?,runtime_generation=?,lifecycle_state='starting',activity_state='starting',accepting_prompts=0 WHERE agent_id=?`, secondFence.PiSessionID, secondFence.SessionEpoch, secondFence.ProcessIncarnation, secondFence.RuntimeGeneration, fence.AgentID)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET pi_session_id=?,session_epoch=?,process_incarnation=?,runtime_generation=?,applied_policy_canonical=NULL,applied_policy_sha256=NULL,policy_confirmed_at=NULL,snapshot_id=NULL,snapshot_owner_session_id=NULL,snapshot_sequence=NULL,snapshot_canonical=NULL,snapshot_sha256=NULL,snapshot_confirmed_at=NULL WHERE agent_id=?`, secondFence.PiSessionID, secondFence.SessionEpoch, secondFence.ProcessIncarnation, secondFence.RuntimeGeneration, fence.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, secondFence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "replacement-snapshot", OwnerSessionID: secondFence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"replacement-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"replacement-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, secondFence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, secondFence))
	second, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, secondFence, "same-call", "web_search", false)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, first.ID, second.ID)
	require.Len(t, builtinHistoryRowsFor(t, durable, fence.AgentID), 2)
}

func TestManagedOrdinaryToolCompletionPreservesBuiltinOwnershipAndMismatchedCallIDs(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	builtin, created, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "builtin-call", "bash")
	require.NoError(t, err)
	require.True(t, created)

	ordinary, created, err := durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "builtin-call", "web_search", false)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, builtin.ID, ordinary.ID)

	_, created, err = durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "builtin-call", "bash", false)
	require.Error(t, err)
	require.False(t, created)
	require.Len(t, builtinHistoryRowsFor(t, durable, fence.AgentID), 2)
}

func builtinHistoryRowsFor(t *testing.T, durable *Store, agentID string) []ManagedHistoryEntry {
	t.Helper()
	page, err := durable.ManagedHistoryPage(context.Background(), agentID, "complete", 0, maxManagedHistoryEntries)
	require.NoError(t, err)
	return page.Entries
}

func TestManagedBuiltinToolLifecycleRejectsTerminalAfterDurableFailure(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	_, _, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "call-1", "bash")
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedFailed(ctx, fence.AgentID, "PROCESS_EXITED", "process exited"))

	_, changed, err := durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", ManagedBuiltinToolSucceeded)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationFenceInactive)
	require.False(t, changed)

	entries, err := durable.InterruptActiveManagedBuiltinInvocations(ctx, fence)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	_, changed, err = durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", ManagedBuiltinToolSucceeded)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationFenceInactive)
	require.False(t, changed)
}

func TestManagedBuiltinToolLifecycleRejectsStaleFenceAndEvictsLinkedInvocation(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	stale := fence
	stale.ProcessIncarnation = "other-incarnation"
	_, _, err := durable.BeginManagedBuiltinInvocation(ctx, stale, "call-1", "read")
	require.Error(t, err)

	first, _, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "call-1", "read")
	require.NoError(t, err)
	for index := 0; index < maxManagedHistoryEntries; index++ {
		_, err = durable.AppendManagedCompleteHistory(ctx, fence.AgentID, "test", "info", fmt.Sprintf("event-%03d", index))
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_builtin_tool_invocations WHERE complete_sequence=?`, first.ID[len("complete:"):]).Scan(&count))
	require.Zero(t, count, "retention must remove the invocation with its Complete row")
}

func TestManagedBuiltinToolLifecycleInterruptsOnlyExactFence(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	_, _, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "call-1", "write")
	require.NoError(t, err)
	entries, err := durable.InterruptActiveManagedBuiltinInvocations(ctx, fence)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Write interrupted", entries[0].Message)
	require.Equal(t, "interrupted", entries[0].Presentation.Outcome)
	entries, err = durable.InterruptActiveManagedBuiltinInvocations(ctx, fence)
	require.NoError(t, err)
	require.Empty(t, entries)
}

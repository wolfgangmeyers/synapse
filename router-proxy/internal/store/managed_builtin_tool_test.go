package store

import (
	"context"
	"fmt"
	"path/filepath"
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

func TestManagedToolInvocationPersistsInputAndCorrelatesReplays(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	input := map[string]any{"query": "status", "api_token": "private-token"}

	active, created, err := durable.BeginManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "web_search", input)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "active", active.Presentation.Outcome)
	require.JSONEq(t, `{"api_token":"[REDACTED]","query":"status"}`, string(active.ToolInput))

	replayed, created, err := durable.BeginManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "web_search", input)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, active, replayed)

	_, created, err = durable.BeginManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "other_tool", input)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationConflict)
	require.False(t, created)
	_, created, err = durable.BeginManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "web_search", map[string]any{"query": "different"})
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationConflict)
	require.False(t, created)

	_, changed, err := durable.FinishManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "other_tool", ManagedBuiltinToolSucceeded)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationConflict)
	require.False(t, changed)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, "active", page.Entries[0].Presentation.Outcome)

	terminal, changed, err := durable.FinishManagedToolInvocation(ctx, fence, "ordinary-lifecycle", "web_search", ManagedBuiltinToolSucceeded)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, active.ID, terminal.ID)
	require.Equal(t, "success", terminal.Presentation.Outcome)
	require.JSONEq(t, string(active.ToolInput), string(terminal.ToolInput))
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

func TestManagedOrdinaryToolCompletionRejectsOwnedCallIDMismatch(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	_, created, err := durable.BeginManagedBuiltinInvocation(ctx, fence, "builtin-call", "bash")
	require.NoError(t, err)
	require.True(t, created)

	_, created, err = durable.RecordManagedOrdinaryToolCompletion(ctx, fence, "builtin-call", "web_search", false)
	require.ErrorIs(t, err, ErrManagedBuiltinInvocationConflict)
	require.False(t, created)
	require.Len(t, builtinHistoryRowsFor(t, durable, fence.AgentID), 1)
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

func TestManagedToolInvocationMigrationPreservesBuiltinRowsAndAllowsOrdinaryNames(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "053_managed_tool_invocation_names")]})
	require.NoError(t, err)

	agent := ManagedAgentRecord{AgentID: "migration-tool", Name: "migration-tool", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "migration-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "migration-tool-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-tool-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 101, StartMarker: "migration-marker", Incarnation: "migration-incarnation"}, "migration-generation"))
	stored, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	fence := restartFenceFromAgent(stored)
	require.NoError(t, before.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "migration-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"migration-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"migration-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, before.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, before.MarkManagedReadyFromSources(ctx, fence))

	builtin, created, err := before.BeginManagedBuiltinInvocation(ctx, fence, "legacy-builtin", "read")
	require.NoError(t, err)
	require.True(t, created)
	_, _, err = before.BeginManagedToolInvocation(ctx, fence, "pre-upgrade-ordinary", "web_search", map[string]any{"query": "before"})
	require.Error(t, err, "the historical builtin-only constraint must require the upgrade")
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer func() { require.NoError(t, after.Close()) }()
	var toolName, state string
	var sequence int64
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT tool_name,state,complete_sequence FROM managed_builtin_tool_invocations WHERE invocation_key=?`, managedBuiltinInvocationKey(fence, "legacy-builtin")).Scan(&toolName, &state, &sequence))
	require.Equal(t, "read", toolName)
	require.Equal(t, "active", state)
	require.Equal(t, builtin.ID[len("complete:"):], fmt.Sprint(sequence))

	ordinary, created, err := after.BeginManagedToolInvocation(ctx, fence, "post-upgrade-ordinary", "web_search", map[string]any{"query": "after"})
	require.NoError(t, err)
	require.True(t, created)
	require.JSONEq(t, `{"query":"after"}`, string(ordinary.ToolInput))
	var foreignKeyFailures int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyFailures))
	require.Zero(t, foreignKeyFailures)
}

func TestInterruptAllManagedToolInvocationsUsesGenericPresentation(t *testing.T) {
	ctx := context.Background()
	durable, fence := builtinToolFixture(t)
	active, created, err := durable.BeginManagedToolInvocation(ctx, fence, "ordinary-active", "web_search", map[string]any{"query": "retained"})
	require.NoError(t, err)
	require.True(t, created)

	require.NoError(t, durable.InterruptAllManagedBuiltinInvocations(ctx))
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, active.ID, page.Entries[0].ID)
	require.Equal(t, "Web_search interrupted", page.Entries[0].Message)
	require.Equal(t, "web_search", page.Entries[0].Presentation.ToolName)
	require.Equal(t, "interrupted", page.Entries[0].Presentation.Outcome)
	require.JSONEq(t, `{"query":"retained"}`, string(page.Entries[0].ToolInput))
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

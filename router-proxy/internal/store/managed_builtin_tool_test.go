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

	terminal, changed, err := durable.FinishManagedBuiltinInvocation(ctx, fence, "call-1", "succeeded")
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, active.ID, terminal.ID)
	require.Equal(t, "Ran bash", terminal.Message)
	require.Equal(t, "success", terminal.Presentation.Outcome)

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

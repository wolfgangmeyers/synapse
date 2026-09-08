package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func readinessSourceFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	agent := ManagedAgentRecord{AgentID: "source-agent", Name: "source-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "source-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "source-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 99, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	return durable, restartFenceFromAgent(stored)
}

func TestForwardMigration023To024BackfillsManagedReadinessSources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "legacy-source", Name: "legacy-source", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "legacy-op", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "legacy-op", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.Close())
	// This historical migration proof stops before 027: its deliberately
	// unmatched starting row is now a migration-027 rejection fixture.
	after, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	defer after.Close()
	source, err := after.ManagedReadinessSource(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, DefaultManagedReadinessPolicy(), source.DesiredPolicy)
	require.Nil(t, source.Fence)
	require.Nil(t, source.AppliedPolicy)
	require.Nil(t, source.Snapshot)
	var foreignKeySQL string
	require.NoError(t, after.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_readiness_sources'`).Scan(&foreignKeySQL))
	require.NotContains(t, foreignKeySQL, "ON DELETE CASCADE")
}

func TestManagedReadinessSourceTransactions(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, DefaultManagedReadinessPolicy(), source.DesiredPolicy)
	require.Equal(t, &fence, source.Fence)
	require.Nil(t, source.AppliedPolicy)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	afterPolicy, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, afterPolicy, "policy source confirmation does not bump projection")
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	afterSnapshot, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, afterSnapshot, "snapshot source confirmation does not bump projection")
	stale := fence
	stale.ManifestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var staleErr *ManagedReadinessSourceStaleError
	require.ErrorAs(t, durable.ConfirmManagedPolicyApplied(ctx, stale, policy), &staleErr)
	result, err := durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	source, err = durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, source.Snapshot, "generic operation retention never deletes current source rows")
}

func TestManagedReadyRequiresSourceConfirmation(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	var stale *ManagedReadinessSourceStaleError
	require.ErrorAs(t, durable.MarkManagedReadyFromSources(ctx, fence), &stale)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	require.ErrorAs(t, durable.MarkManagedReadyFromSources(ctx, fence), &stale)
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", agent.LifecycleState)
	require.True(t, agent.AcceptingPrompts)
}

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedGoalPolicyCommitsDesiredRevisionAndRejectsStaleAcknowledgement(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "goal-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"goal-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	first, err := durable.UpdateManagedGoalPolicy(ctx, fence, ManagedGoalConfig{
		Enabled:  true,
		Goal:     "finish the migration",
		Settings: ManagedGoalSettings{SnoozeEnabled: true, SnoozeDurationMS: 60_000, SnoozeRefillYieldMS: 30_000, SnoozeRefillElapsedMS: 60_000},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, first.DesiredRevision)
	require.Zero(t, first.AppliedRevision)
	require.Equal(t, "pending", first.ProjectionState)

	second, err := durable.UpdateManagedGoalPolicy(ctx, fence, ManagedGoalConfig{
		Enabled:  true,
		Goal:     "finish the tests",
		Settings: ManagedGoalSettings{SnoozeEnabled: true, SnoozeDurationMS: 60_000, SnoozeRefillYieldMS: 30_000, SnoozeRefillElapsedMS: 60_000},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, second.DesiredRevision)

	applied, err := durable.ConfirmManagedGoalPolicyApplied(ctx, fence, first.DesiredRevision)
	require.NoError(t, err)
	require.False(t, applied, "an old fenced acknowledgement must not apply a newer desired policy")

	applied, err = durable.ConfirmManagedGoalPolicyApplied(ctx, fence, second.DesiredRevision)
	require.NoError(t, err)
	require.True(t, applied)
	stored, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, second.DesiredRevision, stored.AppliedRevision)
	require.Equal(t, "applied", stored.ProjectionState)
}

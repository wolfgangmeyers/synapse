package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGoalSnoozeWakeupIsFencedAcrossReplacement(t *testing.T) {
	ctx := context.Background()
	durable, oldFence, clock := readyManagedGoalEvaluatorFixture(t)

	oldPolicy, snoozed, err := durable.SnoozeManagedGoalPolicy(ctx, oldFence)
	require.NoError(t, err)
	require.True(t, snoozed)
	require.NotEmpty(t, oldPolicy.WakeupMessageID)
	oldWakeupID, oldDeadline := oldPolicy.WakeupMessageID, oldPolicy.SnoozedUntil

	replacement := oldFence
	replacement.PiSessionID = "replacement-session"
	replacement.SessionEpoch++
	replacement.ProcessPID++
	replacement.ProcessStartMarker = "replacement-marker"
	replacement.ProcessIncarnation = "replacement-incarnation"
	replacement.RuntimeGeneration = "replacement-generation"
	require.NoError(t, durable.db.QueryRow(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=? RETURNING 1`, replacement.PiSessionID, replacement.SessionEpoch, replacement.ProcessPID, replacement.ProcessStartMarker, replacement.ProcessIncarnation, replacement.RuntimeGeneration, replacement.AgentID).Scan(new(int)))

	// The old timer is due, but the live managed-agent row has the replacement
	// fence. It cannot consume the old snooze or reserve its wakeup.
	require.NoError(t, clock.Advance(time.Minute))
	_, woke, err := durable.WakeSnoozedManagedGoalPolicy(ctx, oldFence)
	require.NoError(t, err)
	require.False(t, woke)
	current, err := durable.ManagedGoalPolicyFor(ctx, oldFence.AgentID)
	require.NoError(t, err)
	require.Equal(t, oldWakeupID, current.WakeupMessageID)
	require.Equal(t, oldDeadline, current.SnoozedUntil)

	// Recovery clears the old timer. Re-register the replacement readiness
	// sources, then prove its own snooze wakes exactly once.
	changed, err := durable.RecoverManagedGoalEvaluationForFence(ctx, replacement)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, durable.db.QueryRow(`UPDATE managed_agents SET lifecycle_state='starting',activity_state='idle',accepting_prompts=0 WHERE agent_id=? RETURNING 1`, replacement.AgentID).Scan(new(int)))
	require.NoError(t, durable.RollManagedReadinessSourceFence(ctx, replacement))
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, replacement, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "replacement-snapshot", OwnerSessionID: replacement.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"replacement-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"replacement-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, replacement, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, replacement))

	replacementPolicy, snoozed, err := durable.SnoozeManagedGoalPolicy(ctx, replacement)
	require.NoError(t, err)
	require.True(t, snoozed)
	require.NotEqual(t, oldWakeupID, replacementPolicy.WakeupMessageID)
	require.NoError(t, clock.Advance(time.Minute))
	_, woke, err = durable.WakeSnoozedManagedGoalPolicy(ctx, replacement)
	require.NoError(t, err)
	require.True(t, woke)
	_, woke, err = durable.WakeSnoozedManagedGoalPolicy(ctx, replacement)
	require.NoError(t, err)
	require.False(t, woke)
}

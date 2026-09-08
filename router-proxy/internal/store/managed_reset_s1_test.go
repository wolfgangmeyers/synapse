package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func resetReservationForTest(agent ManagedAgentRecord, id string) ManagedResetReservation {
	fence := restartFenceFromAgent(&agent)
	return ManagedResetReservation{
		AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agent.AgentID + "/reset", RequestID: id,
		CanonicalBodyHash: testManifestHash, SourceFence: fence, LiveFence: fence, PlannedTargetRuntimeGeneration: lifecycleUUID(900),
		Operation: ManagedOperation{OperationID: id, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agent.AgentID + "/reset", RequestID: id, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"X-Request-ID": id}, ResponseBody: []byte(`{"operation_id":"` + id + `"}`)},
	}
}

func TestReserveManagedResetReplaysAndCancelsAnUnstartedRestart(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	resetID := lifecycleUUID(901)
	first, err := durable.ReserveManagedReset(ctx, resetReservationForTest(agent, resetID))
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Equal(t, "admitted", first.Action.State)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "resetting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)
	replayed, err := durable.ReserveManagedReset(ctx, resetReservationForTest(agent, resetID))
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replayed.Operation.ResponseBody)

	// A separate ready agent exercises reset's precedence exception. The lower
	// legacy restart has not made an external call, so reset cancels it instead
	// of rejecting the closed prompt/restarting projection.
	durable2, ctx2, agent2, launch2 := restartFixture(t)
	_, replayedRestart, err := durable2.ReserveManagedRestart(ctx2, restartReservation(agent2, launch2))
	require.NoError(t, err)
	require.False(t, replayedRestart)
	resetID2 := lifecycleUUID(902)
	admission, err := durable2.ReserveManagedReset(ctx2, resetReservationForTest(agent2, resetID2))
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	lower, err := durable2.ManagedLifecycleAction(ctx2, "restart-operation")
	require.NoError(t, err)
	require.Equal(t, "cancelled", lower.State)
	require.Equal(t, "CANCELLED_BY_HIGHER_ACTION", lower.TerminalCode)
	_, err = durable2.ManagedRestartTransition(ctx2, agent2.AgentID)
	require.Error(t, err)
	stored2, err := durable2.ManagedAgent(ctx2, agent2.AgentID)
	require.NoError(t, err)
	require.Equal(t, "resetting", stored2.LifecycleState)
	require.False(t, stored2.AcceptingPrompts)
}

func TestFailedBoundResetClearsTargetFenceAfterVerifiedAbsenceForResumeRetry(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	target := source
	target.PiSessionID, target.SessionEpoch = "failed-target", source.SessionEpoch+1
	target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation = 73, "target-marker", "target-incarnation"
	target.RuntimeGeneration = lifecycleUUID(903)
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(904), AgentID: agent.AgentID, SessionEpoch: target.SessionEpoch, PiSessionID: target.PiSessionID, RootRelativePath: "sessions/failed-target.json", PathSHA256: testManifestHash, DiscoveryState: "materialized", FileDevice: "1", FileInode: "2", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, durable.RecordManagedSessionArtifact(ctx, artifact))
	action := lifecycleActionInput(lifecycleUUID(905), agent.AgentID, "reset", "direct", source)
	action.State, action.Stage, action.TargetFence, action.TargetArtifactID, action.PlannedTargetRuntimeGeneration = "awaiting_target_ready", "target_ready", &target, artifact.ArtifactID, target.RuntimeGeneration
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, action))
	_, err := durable.db.Exec(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, agent.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.FailManagedReset(ctx, action.ActionID, "RESET_TARGET_START_FAILED", true, target))
	failed, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, target.PiSessionID, failed.PiSessionID, "failure keeps the target fence until an external absence probe is complete")
	require.NoError(t, durable.ClearResetTargetAfterVerifiedAbsence(ctx, action.ActionID, source, target))
	restored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, source.PiSessionID, restored.PiSessionID)
	require.Equal(t, source.RuntimeGeneration, restored.RuntimeGeneration)
	cleared, err := durable.ManagedLifecycleAction(ctx, action.ActionID)
	require.NoError(t, err)
	require.Nil(t, cleared.TargetFence)
	require.Equal(t, "failed", cleared.State)
	require.Equal(t, "target_start", cleared.Stage)
	reservation := resetReservationForTest(*restored, lifecycleUUID(906))
	reservation.SourceFence, reservation.LiveFence = source, source
	reservation.RetryOf, reservation.TargetArtifactID = action.ActionID, artifact.ArtifactID
	reservation.InitialState, reservation.InitialStage = "starting_target", "target_start"
	reservation.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	admission, err := durable.ReserveManagedReset(ctx, reservation)
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, action.ActionID, admission.Action.RetryOf)
	require.Equal(t, "starting_target", admission.Action.State)
	require.Equal(t, "target_start", admission.Action.Stage)
	require.Nil(t, admission.Action.TargetFence)
	require.Equal(t, artifact.ArtifactID, admission.Action.TargetArtifactID)
}

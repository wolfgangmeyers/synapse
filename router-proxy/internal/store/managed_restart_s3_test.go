package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func lifecycleRestartReservation(t *testing.T, agent ManagedAgentRecord, current ManagedOperation, id string) ManagedLifecycleRestartReservation {
	t.Helper()
	fence := restartFenceFromAgent(&agent)
	operation := ManagedOperation{
		OperationID: id, Method: "POST", CanonicalPath: "/v1/managed-agents/restartable/restart", RequestID: id,
		CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202,
		ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": id},
		ResponseBody:    []byte(`{"operation_id":"` + id + `"}`),
	}
	return ManagedLifecycleRestartReservation{AgentID: agent.AgentID, Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: id, CanonicalBodyHash: operation.CanonicalBodyHash, Source: "direct", CurrentOperationID: current.OperationID, SourceFence: fence, PlannedTargetRuntimeGeneration: lifecycleUUID(701), Operation: operation}
}

func recordLifecycleRestartSourceArtifact(t *testing.T, durable *Store, ctx context.Context, agent ManagedAgentRecord) ManagedSessionArtifact {
	t.Helper()
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(702), AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, RootRelativePath: "sessions/source.json", PathSHA256: testManifestHash, DiscoveryState: "materialized", FileDevice: "1", FileInode: "2", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, durable.RecordManagedSessionArtifact(ctx, artifact))
	return artifact
}

func TestLifecycleRestartReservationOwnsNewDirectRestartWithout025(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(700))
	admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, agent.AgentID, admission.Action.AgentID)
	require.Equal(t, "restart", admission.Action.Kind)
	require.Equal(t, "lifecycle", admission.Action.ExecutionOwner)
	require.Equal(t, "admitted", admission.Action.State)
	require.Equal(t, "admission", admission.Action.Stage)
	require.Equal(t, reservation.PlannedTargetRuntimeGeneration, admission.Action.PlannedTargetRuntimeGeneration)
	require.Equal(t, reservation.SourceFence, *admission.Action.SourceFence)
	require.Nil(t, admission.Action.TargetFence)
	_, err = durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.Error(t, err)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)

	replay, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, admission.Operation.ResponseBody, replay.Operation.ResponseBody)
	conflict := reservation
	conflict.CanonicalPath += "/other"
	conflict.Operation.CanonicalPath = conflict.CanonicalPath
	_, err = durable.ReserveManagedLifecycleRestart(ctx, conflict)
	var idempotency *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &idempotency)
}

func TestLifecycleRestartSettleTransitionUsesExactAdmittedSourceFence(t *testing.T) {
	// Exercise the stale-fence rejection while the action is still eligible for
	// settlement. A failure after settlement would only prove the state check.
	durable, ctx, agent, launch := restartFixture(t)
	reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(705))
	admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.Equal(t, "admitted", admission.Action.State)
	require.Equal(t, "admission", admission.Action.Stage)
	require.Equal(t, "restart", admission.Action.Kind)
	require.Equal(t, "lifecycle", admission.Action.ExecutionOwner)
	require.Equal(t, reservation.SourceFence, *admission.Action.SourceFence)

	stale := reservation.SourceFence
	stale.ProcessIncarnation = "mutated-source-incarnation"
	before := *admission.Action
	var staleErr *ManagedRestartStaleError
	require.ErrorAs(t, durable.AdvanceManagedLifecycleRestartToSettle(ctx, admission.Action.ActionID, stale), &staleErr)
	after, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, before.State, after.State)
	require.Equal(t, before.Stage, after.Stage)
	require.Equal(t, before.SourceFence, after.SourceFence)

	// Use a fresh action for the matching-fence transition so the success path
	// is independently shown to work from the admitted/admission state.
	durable, ctx, agent, launch = restartFixture(t)
	reservation = lifecycleRestartReservation(t, agent, launch, lifecycleUUID(706))
	admission, err = durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedLifecycleRestartToSettle(ctx, admission.Action.ActionID, reservation.SourceFence))
	settled, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_settle", settled.State)
	require.Equal(t, "source_settle", settled.Stage)
}

func TestLifecycleRestartArtifactAttachFollowsIdleOrSettledSourceBoundary(t *testing.T) {
	t.Run("active source must settle first", func(t *testing.T) {
		durable, ctx, agent, launch := restartFixture(t)
		_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agent.AgentID)
		require.NoError(t, err)
		reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(707))
		admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
		require.NoError(t, err)
		artifact := recordLifecycleRestartSourceArtifact(t, durable, ctx, agent)
		source := reservation.SourceFence

		var staleErr *ManagedRestartStaleError
		require.ErrorAs(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, source, artifact.ArtifactID), &staleErr)
		before, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
		require.NoError(t, err)
		require.Empty(t, before.TargetArtifactID)
		require.Equal(t, "admitted", before.State)
		require.Equal(t, "admission", before.Stage)

		require.NoError(t, durable.AdvanceManagedLifecycleRestartToSettle(ctx, admission.Action.ActionID, source))
		stale := source
		stale.ProcessIncarnation = "stale-source-incarnation"
		require.ErrorAs(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, stale, artifact.ArtifactID), &staleErr)
		require.Error(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, source, lifecycleUUID(708)))
		require.NoError(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, source, artifact.ArtifactID))
		attached, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
		require.NoError(t, err)
		require.Equal(t, artifact.ArtifactID, attached.TargetArtifactID)
		require.Equal(t, "awaiting_settle", attached.State)
		require.Equal(t, "source_settle", attached.Stage)
	})

	t.Run("idle source attaches from admission", func(t *testing.T) {
		durable, ctx, agent, launch := restartFixture(t)
		reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(709))
		admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
		require.NoError(t, err)
		artifact := recordLifecycleRestartSourceArtifact(t, durable, ctx, agent)
		require.NoError(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, reservation.SourceFence, artifact.ArtifactID))
		attached, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
		require.NoError(t, err)
		require.Equal(t, artifact.ArtifactID, attached.TargetArtifactID)
		require.Equal(t, "admitted", attached.State)
		require.Equal(t, "admission", attached.Stage)
	})
}

func TestLifecycleRestartArtifactMarkerAndTargetFenceAreFenced(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(710))
	admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	artifact := recordLifecycleRestartSourceArtifact(t, durable, ctx, agent)
	source := reservation.SourceFence
	require.NoError(t, durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, source, artifact.ArtifactID))
	require.NoError(t, durable.AdvanceManagedLifecycleRestartToSourceStop(ctx, admission.Action.ActionID, source))
	require.NoError(t, durable.MarkManagedLifecycleRestartSourceStopped(ctx, admission.Action.ActionID, source))

	attempt := lifecycleUUID(711)
	require.NoError(t, durable.ClaimManagedLifecycleRestartTargetStart(ctx, admission.Action.ActionID, attempt, source, reservation.PlannedTargetRuntimeGeneration))
	require.NoError(t, durable.ClearManagedLifecycleRecoveryClaims(ctx))
	marked, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, RestartStartAttemptPrefix+attempt, marked.RecoveryOwner)
	require.Error(t, durable.ClaimManagedLifecycleRestartTargetStart(ctx, admission.Action.ActionID, lifecycleUUID(712), source, reservation.PlannedTargetRuntimeGeneration))
	require.NoError(t, durable.ReleaseManagedLifecycleRestartTargetStartAttemptWithoutChild(ctx, admission.Action.ActionID, attempt, source))

	require.NoError(t, durable.ClaimManagedLifecycleRestartTargetStart(ctx, admission.Action.ActionID, attempt, source, reservation.PlannedTargetRuntimeGeneration))
	target := source
	target.ProcessPID = 84
	target.ProcessStartMarker = "target-marker"
	target.ProcessIncarnation = "target-incarnation"
	target.RuntimeGeneration = reservation.PlannedTargetRuntimeGeneration
	require.NoError(t, durable.InstallManagedLifecycleRestartTarget(ctx, admission.Action.ActionID, attempt, source, target))
	installed, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_target_ready", installed.State)
	require.Equal(t, "target_ready", installed.Stage)
	require.Equal(t, target, *installed.TargetFence)
	require.Empty(t, installed.RecoveryOwner)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, target, restartFenceFromAgent(stored))

	wrong := target
	wrong.ProcessIncarnation = "wrong"
	require.Error(t, durable.ResetManagedLifecycleRestartTargetToStopped(ctx, admission.Action.ActionID, source, wrong))
	require.NoError(t, durable.ResetManagedLifecycleRestartTargetToStopped(ctx, admission.Action.ActionID, source, target))
	stopped, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "starting_target", stopped.State)
	require.Equal(t, "target_start", stopped.Stage)
	require.Nil(t, stopped.TargetFence)
	require.Empty(t, stopped.RecoveryOwner)
}

func TestLifecycleRestartAdmissionClearFailureRollsBackForReplay(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(725))
	_, err := durable.db.Exec(`UPDATE managed_crash_recovery SET retry_at='2026-09-13T12:00:01.000Z',crash_history='["2026-09-13T12:00:00.000Z"]' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = durable.db.Exec(`CREATE TRIGGER fail_restart_crash_clear BEFORE UPDATE OF retry_at ON managed_crash_recovery BEGIN SELECT RAISE(ABORT, 'injected restart crash clear failure'); END`)
	require.NoError(t, err)
	_, err = durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.ErrorContains(t, err, "injected restart crash clear failure")
	_, err = durable.ManagedOperation(ctx, reservation.Operation.OperationID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.True(t, stored.AcceptingPrompts)
	_, err = durable.db.Exec(`DROP TRIGGER fail_restart_crash_clear`)
	require.NoError(t, err)
	admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	recovery, err := durable.ManagedCrashRecovery(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Empty(t, recovery.RetryAt)
	require.Empty(t, recovery.CrashTimes)
}

func TestLifecycleRestartPreparationFailureRestoresSourceProjection(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(720))
	admission, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.RecordManagedLifecycleRestartFailure(context.Background(), admission.Action.ActionID, "RESTART_PREPARATION_FAILED", reservation.SourceFence))
	action, err := durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "failed", action.State)
	require.Equal(t, "admission", action.Stage)
	require.Equal(t, "RESTART_PREPARATION_FAILED", action.TerminalCode)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.Equal(t, "idle", stored.ActivityState)
	require.True(t, stored.AcceptingPrompts)
	op, err := durable.ManagedOperation(ctx, admission.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
}

func TestLifecycleRestartReservationRejectsDirectWithParent(t *testing.T) {
	for _, tc := range []struct{ name, source, parent string }{
		{"direct with parent", "direct", lifecycleUUID(800)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			durable, ctx, agent, launch := restartFixture(t)
			reservation := lifecycleRestartReservation(t, agent, launch, lifecycleUUID(801))
			reservation.Source, reservation.ParentActionID = tc.source, tc.parent
			before := captureNamedTableRows(t, durable.db, []string{"managed_lifecycle_actions", "managed_http_operations", "managed_agents"})
			_, err := durable.ReserveManagedLifecycleRestart(ctx, reservation)
			require.ErrorContains(t, err, "reservation is invalid")
			require.Equal(t, before, captureNamedTableRows(t, durable.db, []string{"managed_lifecycle_actions", "managed_http_operations", "managed_agents"}))
		})
	}
}

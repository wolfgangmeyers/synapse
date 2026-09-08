package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReserveManagedLifecycleValidatesAdmissionShapeAndRetryLineage(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	target := source
	target.PiSessionID, target.ProcessPID = "retry-target", 91
	target.ProcessStartMarker, target.ProcessIncarnation = "retry-marker", "retry-incarnation"
	target.RuntimeGeneration = lifecycleUUID(700)
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(701), AgentID: agent.AgentID, SessionEpoch: target.SessionEpoch, PiSessionID: target.PiSessionID, RootRelativePath: "sessions/retry.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, store.RecordManagedSessionArtifact(ctx, artifact))

	predecessor := lifecycleActionInput(lifecycleUUID(702), agent.AgentID, "reset", "direct", source)
	predecessor.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	predecessor.TargetFence, predecessor.TargetArtifactID = &target, artifact.ArtifactID
	predecessor.State, predecessor.Stage = "failed", "target_start"
	predecessor.TerminalCode, predecessor.TerminalMessage, predecessor.Retryable = "RESET_TARGET_START_FAILED", "reset replacement did not become ready", true
	predecessor.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, predecessor))

	retry := lifecycleReservation(lifecycleUUID(703), agent.AgentID, "reset", source)
	retry.RetryOf = predecessor.ActionID
	retry.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	retry.TargetFence, retry.TargetArtifactID = &target, artifact.ArtifactID
	admission, err := store.ReserveManagedLifecycle(ctx, retry)
	require.NoError(t, err)
	require.Equal(t, predecessor.ActionID, admission.Action.RetryOf)

	invalidParent := lifecycleReservation(lifecycleUUID(704), agent.AgentID, "restart", source)
	invalidParent.ParentActionID = lifecycleUUID(705)
	_, err = store.ReserveManagedLifecycle(ctx, invalidParent)
	require.Error(t, err)

	invalidRetry := lifecycleReservation(lifecycleUUID(706), agent.AgentID, "restart", source)
	invalidRetry.RetryOf = predecessor.ActionID
	_, err = store.ReserveManagedLifecycle(ctx, invalidRetry)
	require.Error(t, err)

	invalidOperation := lifecycleReservation(lifecycleUUID(707), agent.AgentID, "restart", source)
	invalidOperation.Operation.State = "terminal"
	_, err = store.ReserveManagedLifecycle(ctx, invalidOperation)
	require.Error(t, err)
}

func TestLifecycleTerminalizationRequiresCurrentFenceAndSafeOutcome(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	action := lifecycleActionInput(lifecycleUUID(710), agent.AgentID, "restart", "direct", source)
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, action))

	wrong := source
	wrong.ProcessIncarnation = "wrong-incarnation"
	err := store.CompleteManagedLifecycleAction(ctx, ManagedLifecycleTerminalization{ActionID: action.ActionID, ExpectedState: "admitted", ExpectedStage: "admission", TerminalState: "succeeded", Code: "RESTARTED", Message: "managed agent restarted", Fence: &wrong})
	require.Error(t, err)
	err = store.CompleteManagedLifecycleAction(ctx, ManagedLifecycleTerminalization{ActionID: action.ActionID, ExpectedState: "admitted", ExpectedStage: "admission", TerminalState: "succeeded", Code: "RAW_STDERR", Message: "raw stderr token=secret", Fence: &source})
	require.Error(t, err)
	require.NoError(t, store.CompleteManagedLifecycleAction(ctx, ManagedLifecycleTerminalization{ActionID: action.ActionID, ExpectedState: "admitted", ExpectedStage: "admission", TerminalState: "succeeded", Code: "RESTARTED", Message: "managed agent restarted", Fence: &source}))

	stored, err := store.ManagedLifecycleAction(ctx, action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "succeeded", stored.State)
	require.Equal(t, "complete", stored.Stage)
	op, err := store.ManagedOperation(ctx, action.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
}

func TestLifecycleTerminalStagesKeepOnlyFailureStage(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)

	succeededAtFailureStage := lifecycleActionInput(lifecycleUUID(720), agent.AgentID, "restart", "direct", source)
	succeededAtFailureStage.State, succeededAtFailureStage.Stage = "succeeded", "source_stop"
	succeededAtFailureStage.TerminalCode, succeededAtFailureStage.TerminalMessage = "RESTARTED", "managed agent restarted"
	succeededAtFailureStage.Operation.State = "terminal"
	require.Error(t, store.CreateManagedLifecycleAction(ctx, succeededAtFailureStage))

	failedAtFailureStage := lifecycleActionInput(lifecycleUUID(721), agent.AgentID, "restart", "direct", source)
	failedAtFailureStage.State, failedAtFailureStage.Stage = "failed", "source_stop"
	failedAtFailureStage.TerminalCode, failedAtFailureStage.TerminalMessage = "RESTART_STOP_FAILED", "restart process stop did not complete"
	failedAtFailureStage.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, failedAtFailureStage))
}

func TestLaunchCallbacksStayClosedOnBothSidesOfTerminateTombstone(t *testing.T) {
	ctx := context.Background()
	for _, callbackFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("callback_first=%t", callbackFirst), func(t *testing.T) {
			store := openTestStore(t)
			agent := ManagedAgentRecord{AgentID: "launch-race", Name: "launch-race", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "launch-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
			launch := ManagedOperation{OperationID: "launch-race-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-race-request", CanonicalBodyHash: "launch-race-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
			require.NoError(t, store.ReserveManagedLaunch(ctx, agent, launch))

			if callbackFirst {
				require.NoError(t, store.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 88, StartMarker: "launch-marker", Incarnation: "launch-incarnation"}, "launch-generation"))
			}
			terminate := lifecycleReservation(lifecycleUUID(730), agent.AgentID, "terminate", ManagedRuntimeFence{})
			_, err := store.ReserveManagedLifecycle(ctx, terminate)
			require.NoError(t, err)
			if !callbackFirst {
				err = store.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 89, StartMarker: "late-marker", Incarnation: "late-incarnation"}, "late-generation")
				require.Error(t, err)
			}
			err = store.MarkManagedReady(ctx, agent.AgentID)
			require.Error(t, err)

			stored, err := store.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, "terminated", stored.DesiredState)
			require.Equal(t, "terminating", stored.LifecycleState)
			op, err := store.ManagedOperation(ctx, launch.OperationID)
			require.NoError(t, err)
			require.Equal(t, "terminal", op.State)
		})
	}
}

func TestMigration027UpgradeProvesIndexesAndRowLocalActionShapes(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/router.db"
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	seedMigration027Accepted026Fixture(t, before)
	require.NoError(t, before.Close())
	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()

	for _, index := range []string{
		"managed_session_artifacts_cleanup_idx", "managed_lifecycle_recovery_idx", "managed_lifecycle_agent_terminal_idx", "managed_lifecycle_one_active_lane", "managed_lifecycle_batch_parent_idx",
		"managed_control_request_dedup_idx", "managed_control_nonterminal_agent_idx", "managed_control_nonterminal_global_idx", "managed_control_append_due_idx", "managed_control_terminal_retention_idx",
	} {
		var count int
		require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count))
		require.Equalf(t, 1, count, "missing 027 index %s", index)
	}
	migration027ShapeRows(t, after)
}

func TestMigration027RollbackRestoresAllAccepted026RowsAndSchema(t *testing.T) {
	ctx := context.Background()
	for _, checkpoint := range []string{"after_parent_copy", "after_parent_drop", "pre_commit_foreign_key_check"} {
		t.Run(checkpoint, func(t *testing.T) {
			path := t.TempDir() + "/router.db"
			migrations := DefaultMigrations()
			store, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
			require.NoError(t, err)
			seedMigration027Accepted026Fixture(t, store)
			before := captureMigration027RollbackState(t, store.db)
			store.managed027Fault = func(stage string) error {
				if stage == checkpoint {
					return fmt.Errorf("injected %s", checkpoint)
				}
				return nil
			}
			err = store.applyManagedControlMigration027(ctx, migrationByID(t, migrations, "027_managed_control_requests"))
			require.ErrorContains(t, err, checkpoint)
			require.Equal(t, before, captureMigration027RollbackState(t, store.db))
			var foreignKeys, migrationRows int
			require.NoError(t, store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='027_managed_control_requests'`).Scan(&migrationRows))
			require.Equal(t, 1, foreignKeys)
			require.Zero(t, migrationRows)
		})
	}
}

func TestForeignKeysCannotBeDisabledAfterImmediateTransactionBegins(t *testing.T) {
	store := openTestStore(t)
	require.NoError(t, store.db.QueryRow(`PRAGMA foreign_keys`).Scan(new(int)))
	_, err := store.db.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	_, err = store.db.Exec(`PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	var foreignKeys int
	require.NoError(t, store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
	_, err = store.db.Exec(`ROLLBACK`)
	require.NoError(t, err)
}

func TestLifecycleGraphRetainsExpiredChildWhenBatchParentIsUnexpired(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	parentID, childID := lifecycleUUID(740), lifecycleUUID(741)
	parent := lifecycleActionInput(parentID, "", "restart_all", "direct", ManagedRuntimeFence{})
	parent.Operation = lifecycleOperation(parentID, "2999-01-01T00:00:00.000Z")
	parent.State, parent.Stage = "succeeded", "complete"
	parent.TerminalCode, parent.TerminalMessage = "BATCH_ADMITTED", "managed restart batch admitted"
	parent.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, parent))
	child := lifecycleActionInput(childID, agent.AgentID, "restart", "direct", source)
	child.Operation = lifecycleOperation(childID, "2000-01-01T00:00:00.000Z")
	child.State, child.Stage = "succeeded", "complete"
	child.TerminalCode, child.TerminalMessage = "RESTARTED", "managed agent restarted"
	child.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, child))
	require.NoError(t, store.WriteManagedLifecycleBatchItems(ctx, parentID, []ManagedLifecycleBatchItem{{SnapshotPosition: 0, AgentID: agent.AgentID, Status: "coalesced", ChildActionID: childID, ChildOperationID: childID, CreatedAt: "2026-09-08T00:00:00.000Z"}}))

	result, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	assertRowsRemain(t, store.db, "managed_lifecycle_actions", parentID, childID)
}

func TestLifecycleGraphRetrySuccessorBlocksExpiredPredecessor(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	target := source
	target.PiSessionID, target.ProcessPID = "graph-target", 93
	target.ProcessStartMarker, target.ProcessIncarnation = "graph-marker", "graph-incarnation"
	target.RuntimeGeneration = lifecycleUUID(750)
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(751), AgentID: agent.AgentID, SessionEpoch: target.SessionEpoch, PiSessionID: target.PiSessionID, RootRelativePath: "sessions/graph.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, store.RecordManagedSessionArtifact(ctx, artifact))
	predecessor := lifecycleActionInput(lifecycleUUID(752), agent.AgentID, "reset", "direct", source)
	predecessor.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	predecessor.TargetFence, predecessor.TargetArtifactID = &target, artifact.ArtifactID
	predecessor.State, predecessor.Stage = "failed", "target_start"
	predecessor.TerminalCode, predecessor.TerminalMessage, predecessor.Retryable = "RESET_TARGET_START_FAILED", "reset replacement did not become ready", true
	predecessor.Operation = lifecycleOperation(predecessor.ActionID, "2000-01-01T00:00:00.000Z")
	predecessor.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, predecessor))
	successor := lifecycleActionInput(lifecycleUUID(753), agent.AgentID, "reset", "direct", source)
	successor.RetryOf = predecessor.ActionID
	successor.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	successor.TargetFence, successor.TargetArtifactID = &target, artifact.ArtifactID
	successor.Operation = lifecycleOperation(successor.ActionID, "2999-01-01T00:00:00.000Z")
	successor.Operation.State = "pending"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, successor))

	result, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	assertRowsRemain(t, store.db, "managed_lifecycle_actions", predecessor.ActionID, successor.ActionID)
}

func TestLifecycleGraphRefusesTerminalOperationWithRetained025Transition(t *testing.T) {
	store, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := store.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE managed_http_operations SET state='terminal',terminal_at='2000-01-01T00:00:00.000Z',expires_at='2000-01-02T00:00:00.000Z' WHERE operation_id=?`, reservation.Operation.OperationID)
	require.NoError(t, err)

	_, err = store.Sweep(ctx, DefaultRetentionPolicy)
	require.ErrorContains(t, err, "retains a 025 transition")
	assertRowsRemain(t, store.db, "managed_restart_transitions", agent.AgentID)
	assertRowsRemain(t, store.db, "managed_lifecycle_actions", reservation.Operation.OperationID)
	assertRowsRemain(t, store.db, "managed_http_operations", reservation.Operation.OperationID)
}

func TestLifecycleGraphInjectedItemDeleteRestoresEveryRow(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	parentID, childID := lifecycleUUID(760), lifecycleUUID(761)
	parent := lifecycleActionInput(parentID, "", "restart_all", "direct", ManagedRuntimeFence{})
	parent.Operation = lifecycleOperation(parentID, "2000-01-01T00:00:00.000Z")
	parent.State, parent.Stage = "succeeded", "complete"
	parent.TerminalCode, parent.TerminalMessage = "BATCH_ADMITTED", "managed restart batch admitted"
	parent.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, parent))
	child := lifecycleActionInput(childID, agent.AgentID, "restart", "restart_all_child", source)
	child.ParentActionID = parentID
	child.Operation = lifecycleOperation(childID, "2000-01-01T00:00:00.000Z")
	child.State, child.Stage = "succeeded", "complete"
	child.TerminalCode, child.TerminalMessage = "RESTARTED", "managed agent restarted"
	child.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, child))
	require.NoError(t, store.WriteManagedLifecycleBatchItems(ctx, parentID, []ManagedLifecycleBatchItem{{SnapshotPosition: 0, AgentID: agent.AgentID, Status: "accepted", ChildActionID: childID, ChildOperationID: childID, CreatedAt: "2026-09-08T00:00:00.000Z"}}))
	before := captureNamedTableRows(t, store.db, []string{"managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_http_operations"})
	store.managedGraphFault = func(stage string) error {
		if stage == "after_first_batch_item_delete" {
			return fmt.Errorf("injected graph failure")
		}
		return nil
	}
	_, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.ErrorContains(t, err, "injected graph failure")
	require.Equal(t, before, captureNamedTableRows(t, store.db, []string{"managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_http_operations"}))
	var foreignProblems int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Zero(t, foreignProblems)
}

func TestManagedControlReservationsRejectRouterWide513thRequest(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	for i := 1; i <= 512; i++ {
		fence := restartFenceFromAgent(&agent)
		fence.AgentID = fmt.Sprintf("global-agent-%03d", i)
		_, err := store.ReserveManagedControlRequest(ctx, lifecycleControlInput(800+i, fence.AgentID, fence))
		require.NoError(t, err)
	}
	fence := restartFenceFromAgent(&agent)
	fence.AgentID = "global-agent-513"
	_, err := store.ReserveManagedControlRequest(ctx, lifecycleControlInput(1313, fence.AgentID, fence))
	var conflict *ManagedControlConflictError
	require.ErrorAs(t, err, &conflict)
}

func lifecycleReservation(id, agentID, kind string, fence ManagedRuntimeFence) ManagedLifecycleReservation {
	plannedTarget := lifecycleUUID(990)
	if kind == "terminate" {
		plannedTarget = ""
	}
	return ManagedLifecycleReservation{
		AgentID: agentID, Kind: kind, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agentID + "/" + kind,
		RequestID: id, CanonicalBodyHash: testManifestHash, Fence: fence, PlannedTargetRuntimeGeneration: plannedTarget,
		Operation: ManagedOperation{OperationID: id, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agentID + "/" + kind, RequestID: id, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)},
	}
}

func migration027ShapeRows(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	resetAgent := migration027ReadyAgent(t, store, "shape-reset", 1)
	terminateAgent := migration027ReadyAgent(t, store, "shape-terminate", 2)
	restartAgent := migration027ReadyAgent(t, store, "shape-restart", 3)
	childAgent := migration027ReadyAgent(t, store, "shape-child", 4)
	retryResetAgent := migration027ReadyAgent(t, store, "shape-retry-reset", 5)
	retryTerminateAgent := migration027ReadyAgent(t, store, "shape-retry-terminate", 6)

	reset := lifecycleActionInput(lifecycleUUID(810), resetAgent.AgentID, "reset", "direct", restartFenceFromAgent(resetAgent))
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, reset))
	termination := lifecycleActionInput(lifecycleUUID(811), terminateAgent.AgentID, "terminate", "direct", restartFenceFromAgent(terminateAgent))
	termination.SourceFence = nil
	termination.PlannedTargetRuntimeGeneration = ""
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, termination))
	directRestart := lifecycleActionInput(lifecycleUUID(812), restartAgent.AgentID, "restart", "direct", restartFenceFromAgent(restartAgent))
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, directRestart))
	parent := lifecycleActionInput(lifecycleUUID(813), "", "restart_all", "direct", ManagedRuntimeFence{})
	parent.State, parent.Stage = "succeeded", "complete"
	parent.TerminalCode, parent.TerminalMessage = "BATCH_ADMITTED", "managed restart batch admitted"
	parent.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, parent))
	child := lifecycleActionInput(lifecycleUUID(814), childAgent.AgentID, "restart", "restart_all_child", restartFenceFromAgent(childAgent))
	child.ParentActionID = parent.ActionID
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, child))

	resetSource := restartFenceFromAgent(retryResetAgent)
	resetTarget := resetSource
	resetTarget.PiSessionID, resetTarget.ProcessPID = "shape-reset-target", 102
	resetTarget.ProcessStartMarker, resetTarget.ProcessIncarnation = "shape-reset-marker", "shape-reset-incarnation"
	resetTarget.RuntimeGeneration = lifecycleUUID(815)
	resetArtifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(816), AgentID: retryResetAgent.AgentID, SessionEpoch: resetTarget.SessionEpoch, PiSessionID: resetTarget.PiSessionID, RootRelativePath: "sessions/shape-reset.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, store.RecordManagedSessionArtifact(ctx, resetArtifact))
	resetPredecessor := lifecycleActionInput(lifecycleUUID(817), retryResetAgent.AgentID, "reset", "direct", resetSource)
	resetPredecessor.PlannedTargetRuntimeGeneration, resetPredecessor.TargetFence, resetPredecessor.TargetArtifactID = resetTarget.RuntimeGeneration, &resetTarget, resetArtifact.ArtifactID
	resetPredecessor.State, resetPredecessor.Stage = "failed", "target_start"
	resetPredecessor.TerminalCode, resetPredecessor.TerminalMessage, resetPredecessor.Retryable = "RESET_TARGET_START_FAILED", "reset replacement did not become ready", true
	resetPredecessor.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, resetPredecessor))
	resetRetry := lifecycleActionInput(lifecycleUUID(818), retryResetAgent.AgentID, "reset", "direct", resetSource)
	resetRetry.RetryOf, resetRetry.PlannedTargetRuntimeGeneration, resetRetry.TargetFence, resetRetry.TargetArtifactID = resetPredecessor.ActionID, resetTarget.RuntimeGeneration, &resetTarget, resetArtifact.ArtifactID
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, resetRetry))

	terminateSource := restartFenceFromAgent(retryTerminateAgent)
	terminatePredecessor := lifecycleActionInput(lifecycleUUID(819), retryTerminateAgent.AgentID, "terminate", "direct", terminateSource)
	terminatePredecessor.PlannedTargetRuntimeGeneration = ""
	terminatePredecessor.State, terminatePredecessor.Stage = "failed", "terminate_reap"
	terminatePredecessor.TerminalCode, terminatePredecessor.TerminalMessage, terminatePredecessor.Retryable = "TERMINATION_CLEANUP_FAILED", "managed agent termination cleanup failed", true
	terminatePredecessor.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, terminatePredecessor))
	terminateRetry := lifecycleActionInput(lifecycleUUID(820), retryTerminateAgent.AgentID, "terminate", "direct", terminateSource)
	terminateRetry.PlannedTargetRuntimeGeneration, terminateRetry.RetryOf = "", terminatePredecessor.ActionID
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, terminateRetry))

	for _, actionID := range []string{directRestart.ActionID, parent.ActionID, child.ActionID} {
		_, err := store.db.Exec(`UPDATE managed_lifecycle_actions SET retry_of=? WHERE action_id=?`, resetPredecessor.ActionID, actionID)
		require.Error(t, err)
	}
}

func migration027ReadyAgent(t *testing.T, store *Store, id string, n int) *ManagedAgentRecord {
	t.Helper()
	ctx := context.Background()
	agent := ManagedAgentRecord{AgentID: id, Name: id, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "shape-session", SessionEpoch: int64(n), ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "shape-launch-" + id, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "shape-launch-request-" + id, CanonicalBodyHash: "shape-launch-body-" + id, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, store.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, store.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 200 + n, StartMarker: fmt.Sprintf("shape-marker-%d", n), Incarnation: fmt.Sprintf("shape-incarnation-%d", n)}, fmt.Sprintf("shape-generation-%d", n)))
	require.NoError(t, store.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, store.MarkManagedOperationTerminal(ctx, op.OperationID))
	stored, err := store.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	return stored
}

type migration027RollbackState map[string]migration027TableState

type migration027TableState struct {
	DDL, Indexes, Foreigns, Rows string
}

func captureMigration027RollbackState(t *testing.T, db *sql.DB) migration027RollbackState {
	t.Helper()
	return migration027RollbackState(captureNamedTableRows(t, db, []string{"schema_migrations", "managed_http_operations", "managed_agents", "managed_readiness_sources", "managed_restart_transitions", "managed_direct_delivery_attempts", "managed_direct_delivery_diagnostics"}))
}

func captureNamedTableRows(t *testing.T, db *sql.DB, tables []string) map[string]migration027TableState {
	t.Helper()
	result := make(map[string]migration027TableState, len(tables))
	for _, table := range tables {
		result[table] = captureMigration027Table(t, db, table)
	}
	return result
}

func captureMigration027Table(t *testing.T, db *sql.DB, table string) migration027TableState {
	t.Helper()
	var snapshot migration027TableState
	require.NoError(t, db.QueryRow(`SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&snapshot.DDL))
	indexes, err := db.Query(`SELECT name,COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND tbl_name=? ORDER BY name`, table)
	require.NoError(t, err)
	for indexes.Next() {
		var name, ddl string
		require.NoError(t, indexes.Scan(&name, &ddl))
		snapshot.Indexes += name + "\x00" + ddl + "\x00"
	}
	require.NoError(t, indexes.Close())
	foreigns, err := db.Query(`PRAGMA foreign_key_list(` + table + `)`)
	require.NoError(t, err)
	for foreigns.Next() {
		var id, seq int
		var target, from, to, update, deleteRule, match string
		require.NoError(t, foreigns.Scan(&id, &seq, &target, &from, &to, &update, &deleteRule, &match))
		snapshot.Foreigns += fmt.Sprintf("%d:%d:%s:%s:%s:%s:%s:%s\x00", id, seq, target, from, to, update, deleteRule, match)
	}
	require.NoError(t, foreigns.Close())
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
	require.NoError(t, err)
	columns, err := rows.Columns()
	require.NoError(t, err)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		require.NoError(t, rows.Scan(destinations...))
		for _, value := range values {
			switch typed := value.(type) {
			case nil:
				snapshot.Rows += "NULL\x00"
			case []byte:
				snapshot.Rows += "BLOB:" + string(typed) + "\x00"
			default:
				snapshot.Rows += fmt.Sprintf("%T:%v\x00", typed, typed)
			}
		}
		snapshot.Rows += "\n"
	}
	require.NoError(t, rows.Close())
	return snapshot
}

func assertRowsRemain(t *testing.T, db *sql.DB, table string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		var count int
		query := `SELECT COUNT(*) FROM ` + table + ` WHERE `
		if table == "managed_restart_transitions" {
			query += `agent_id=?`
		} else if table == "managed_http_operations" {
			query += `operation_id=?`
		} else {
			query += `action_id=?`
		}
		require.NoError(t, db.QueryRow(query, id).Scan(&count))
		require.Equalf(t, 1, count, "%s row %s was deleted", table, id)
	}
}

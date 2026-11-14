package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func lifecycleUUID(n int) string {
	return fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", n)
}

func lifecycleOperation(id string, expiresAt string) ManagedOperation {
	return ManagedOperation{
		OperationID: id, Method: "POST", CanonicalPath: "/v1/managed-agents/test/reset",
		RequestID: id, CanonicalBodyHash: testManifestHash, State: "terminal",
		ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
		CreatedAt: "2026-09-08T00:00:00.000Z", TerminalAt: "2026-09-08T00:00:00.000Z", ExpiresAt: expiresAt,
	}
}

func TestMigration027Accepted026FixturePreservesChildrenAndBackfillsEveryStage(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	seedMigration027Accepted026Fixture(t, before)
	children := []string{"managed_readiness_sources", "managed_restart_transitions", "managed_direct_delivery_attempts", "managed_direct_delivery_diagnostics"}
	snapshots := make(map[string]managed027ChildSnapshot, len(children))
	for _, child := range children {
		snapshot, snapshotErr := captureManaged027Child(ctx, before.db, child)
		require.NoError(t, snapshotErr)
		snapshots[child] = snapshot
	}
	var parentRows int
	require.NoError(t, before.db.QueryRow(`SELECT COUNT(*) FROM managed_agents`).Scan(&parentRows))
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")+1]})
	require.NoError(t, err)
	defer after.Close()
	for _, child := range children {
		afterSnapshot, snapshotErr := captureManaged027Child(ctx, after.db, child)
		require.NoError(t, snapshotErr)
		require.Truef(t, snapshots[child].same(afterSnapshot), "child %s changed during parent rebuild", child)
	}
	var copiedRows, links, foreignProblems int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_agents`).Scan(&copiedRows))
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_agents WHERE launch_operation_id IS NOT NULL`).Scan(&links))
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, parentRows, copiedRows)
	require.Zero(t, links)
	require.Zero(t, foreignProblems)
	for _, stage := range []string{"reserved", "prepared", "settling", "stopping", "stopped", "starting"} {
		var owner, state, mappedStage string
		require.NoError(t, after.db.QueryRow(`SELECT execution_owner,state,stage FROM managed_lifecycle_actions WHERE action_id=?`, "legacy-"+stage).Scan(&owner, &state, &mappedStage))
		require.Equal(t, "legacy025", owner)
		require.Equal(t, legacy027StateForStage(stage), state)
		require.Equal(t, legacy027ActionStageForStage(stage), mappedStage)
		action, actionErr := after.ManagedLifecycleAction(ctx, "legacy-"+stage)
		require.NoError(t, actionErr)
		require.NotNil(t, action.SourceFence)
		require.Equal(t, "replacement-generation", action.PlannedTargetRuntimeGeneration)
		if stage == "starting" {
			require.NotNil(t, action.TargetFence)
		} else {
			require.Nil(t, action.TargetFence)
		}
		require.Empty(t, action.RetryOf)
	}
	_, err = after.db.Exec(`UPDATE managed_agents SET desired_state='terminated' WHERE agent_id='legacy-reserved'`)
	require.NoError(t, err)
	_, err = after.db.Exec(`UPDATE managed_agents SET desired_state='not-a-state' WHERE agent_id='legacy-reserved'`)
	require.Error(t, err)
	var foreignKeys int
	require.NoError(t, after.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Equal(t, 1, foreignKeys)
}

func TestMigration027FailureRollsBackAndRestoresForeignKeys(t *testing.T) {
	for _, checkpoint := range []string{"after_foreign_keys_off", "after_parent_copy", "after_parent_drop", "pre_commit_foreign_key_check", "after_migration_record"} {
		t.Run(checkpoint, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "router.db")
			migrations := DefaultMigrations()
			store, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
			require.NoError(t, err)
			seedMigration027Accepted026Fixture(t, store)
			store.managed027Fault = func(stage string) error {
				if stage == checkpoint {
					return errors.New("injected " + stage)
				}
				return nil
			}
			err = store.applyManagedControlMigration027(ctx, migrationByID(t, migrations, "027_managed_control_requests"))
			require.ErrorContains(t, err, "injected "+checkpoint)
			var foreignKeys, migrationRows, launchColumn int
			require.NoError(t, store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='027_managed_control_requests'`).Scan(&migrationRows))
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('managed_agents') WHERE name='launch_operation_id'`).Scan(&launchColumn))
			require.Equal(t, 1, foreignKeys)
			require.Zero(t, migrationRows)
			require.Zero(t, launchColumn)
			require.NoError(t, store.Close())
		})
	}
}

func TestMigration027RejectsUnmatchedStartingWithoutMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	store, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "unmatched", Name: "unmatched", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "unmatched-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "unmatched-request", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, store.ReserveManagedLaunch(ctx, agent, op))
	err = store.applyManagedControlMigration027(ctx, migrationByID(t, migrations, "027_managed_control_requests"))
	require.ErrorContains(t, err, "unmatched pre-027 starting")
	var migrationRows, launchColumn, foreignKeys int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='027_managed_control_requests'`).Scan(&migrationRows))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('managed_agents') WHERE name='launch_operation_id'`).Scan(&launchColumn))
	require.NoError(t, store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Zero(t, migrationRows)
	require.Zero(t, launchColumn)
	require.Equal(t, 1, foreignKeys)
	require.NoError(t, store.Close())
}

func TestLifecycleActionShapesRetriesAndFencedRecoveryClaims(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	target := source
	target.PiSessionID = "target-session"
	target.ProcessPID = 84
	target.ProcessStartMarker = "target-marker"
	target.ProcessIncarnation = "target-incarnation"
	target.RuntimeGeneration = lifecycleUUID(2)

	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(3), AgentID: agent.AgentID, SessionEpoch: target.SessionEpoch, PiSessionID: target.PiSessionID, RootRelativePath: "sessions/target.json", PathSHA256: testManifestHash, DiscoveryState: "materialized", FileDevice: "1", FileInode: "2", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, store.RecordManagedSessionArtifact(ctx, artifact))

	directRestart := lifecycleActionInput(lifecycleUUID(4), agent.AgentID, "restart", "direct", source)
	directRestart.State, directRestart.Stage = "succeeded", "complete"
	directRestart.TerminalCode, directRestart.TerminalMessage, directRestart.Retryable = "RESTARTED", "managed agent restarted", false
	directRestart.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, directRestart))
	termination := lifecycleActionInput(lifecycleUUID(5), agent.AgentID, "terminate", "direct", source)
	termination.SourceFence = nil
	termination.State, termination.Stage = "succeeded", "complete"
	termination.TerminalCode, termination.TerminalMessage, termination.Retryable = "TERMINATED", "managed agent terminated", false
	termination.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, termination))
	// Restart All is retired: neither its parent kind nor its child source
	// can be admitted, and nothing is written for the rejected attempts.
	retiredParent := lifecycleActionInput(lifecycleUUID(6), "", "restart_all", "direct", ManagedRuntimeFence{})
	require.ErrorContains(t, store.CreateManagedLifecycleAction(ctx, retiredParent), "kind is invalid")
	retiredChild := lifecycleActionInput(lifecycleUUID(7), agent.AgentID, "restart", "restart_all_child", source)
	retiredChild.ParentActionID = directRestart.ActionID
	require.ErrorContains(t, store.CreateManagedLifecycleAction(ctx, retiredChild), "source is invalid")
	directWithParent := lifecycleActionInput(lifecycleUUID(8), agent.AgentID, "restart", "direct", source)
	directWithParent.ParentActionID = directRestart.ActionID
	require.Error(t, store.CreateManagedLifecycleAction(ctx, directWithParent))
	var retiredRows int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE kind='restart_all' OR source='restart_all_child' OR action_id IN (?,?,?)`, retiredParent.ActionID, retiredChild.ActionID, directWithParent.ActionID).Scan(&retiredRows))
	require.Zero(t, retiredRows)

	invalid := lifecycleActionInput(lifecycleUUID(10), agent.AgentID, "restart", "direct", source)
	invalid.SourceFence = nil
	require.Error(t, store.CreateManagedLifecycleAction(ctx, invalid))

	predecessor := lifecycleActionInput(lifecycleUUID(11), agent.AgentID, "reset", "direct", source)
	predecessor.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	predecessor.TargetFence = &target
	predecessor.TargetArtifactID = artifact.ArtifactID
	predecessor.State, predecessor.Stage = "failed", "target_start"
	predecessor.TerminalCode, predecessor.TerminalMessage, predecessor.Retryable = "RESET_TARGET_START_FAILED", "reset replacement did not become ready", true
	predecessor.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, predecessor))
	newer := lifecycleActionInput(lifecycleUUID(15), agent.AgentID, "reset", "direct", source)
	newer.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	newer.TargetFence, newer.TargetArtifactID = &target, artifact.ArtifactID
	newer.State, newer.Stage = "failed", "target_start"
	newer.TerminalCode, newer.TerminalMessage, newer.Retryable = "RESET_TARGET_START_FAILED", "reset replacement did not become ready", true
	newer.Operation.State = "terminal"
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, newer))
	staleRetry := lifecycleActionInput(lifecycleUUID(16), agent.AgentID, "reset", "direct", source)
	staleRetry.RetryOf = predecessor.ActionID
	staleRetry.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	staleRetry.TargetFence, staleRetry.TargetArtifactID = &target, artifact.ArtifactID
	require.Error(t, store.CreateManagedLifecycleAction(ctx, staleRetry))

	retry := lifecycleActionInput(lifecycleUUID(12), agent.AgentID, "reset", "direct", source)
	retry.RetryOf = newer.ActionID
	retry.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	retry.TargetFence = &target
	retry.TargetArtifactID = artifact.ArtifactID
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, retry))
	storedRetry, err := store.ManagedLifecycleAction(ctx, retry.ActionID)
	require.NoError(t, err)
	require.Equal(t, newer.ActionID, storedRetry.RetryOf)
	require.Equal(t, source, *storedRetry.SourceFence)
	require.Equal(t, target, *storedRetry.TargetFence)

	fork := lifecycleActionInput(lifecycleUUID(13), agent.AgentID, "reset", "direct", source)
	fork.RetryOf = newer.ActionID
	fork.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	fork.TargetFence, fork.TargetArtifactID = &target, artifact.ArtifactID
	require.Error(t, store.CreateManagedLifecycleAction(ctx, fork))

	restartRetry := lifecycleActionInput(lifecycleUUID(14), agent.AgentID, "restart", "direct", source)
	restartRetry.RetryOf = predecessor.ActionID
	require.Error(t, store.CreateManagedLifecycleAction(ctx, restartRetry))

	claim, err := store.ClaimManagedLifecycleRecovery(ctx, retry.ActionID, "startup-owner")
	require.NoError(t, err)
	require.Equal(t, retry.ActionID, claim.ActionID)
	_, err = store.ClaimManagedLifecycleRecovery(ctx, retry.ActionID, "second-owner")
	require.Error(t, err)
	wrong := source
	wrong.ProcessIncarnation = "different"
	require.Error(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: retry.ActionID, FromState: "admitted", FromStage: "admission", ToState: "starting_target", ToStage: "target_start", Fence: wrong}))
	require.NoError(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: retry.ActionID, FromState: "admitted", FromStage: "admission", ToState: "starting_target", ToStage: "target_start", Fence: source}))
	updated, err := store.ManagedLifecycleAction(ctx, retry.ActionID)
	require.NoError(t, err)
	require.Equal(t, "starting_target", updated.State)
	require.Equal(t, "target_start", updated.Stage)
	require.Empty(t, updated.RecoveryOwner)
	require.NoError(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: retry.ActionID, FromState: "starting_target", FromStage: "target_start", ToState: "awaiting_target_ready", ToStage: "target_ready", Fence: source}))
	require.Error(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: retry.ActionID, FromState: "awaiting_target_ready", FromStage: "target_ready", ToState: "old_artifact_cleanup", ToStage: "old_artifact_cleanup", Fence: source}))
	require.NoError(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: retry.ActionID, FromState: "awaiting_target_ready", FromStage: "target_ready", ToState: "old_artifact_cleanup", ToStage: "old_artifact_cleanup", Fence: target}))
	updated, err = store.ManagedLifecycleAction(ctx, retry.ActionID)
	require.NoError(t, err)
	require.Equal(t, target, *updated.TargetFence)
	require.Equal(t, "old_artifact_cleanup", updated.State)
}

func TestLifecycleTargetBindingAndRecoveryClaimClear(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	target := source
	target.PiSessionID, target.ProcessPID = "bound-target", 99
	target.ProcessStartMarker, target.ProcessIncarnation = "bound-marker", "bound-incarnation"
	target.RuntimeGeneration = lifecycleUUID(40)
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(41), AgentID: agent.AgentID, SessionEpoch: target.SessionEpoch, PiSessionID: target.PiSessionID, RootRelativePath: "sessions/bound.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, store.RecordManagedSessionArtifact(ctx, artifact))
	action := lifecycleActionInput(lifecycleUUID(42), agent.AgentID, "reset", "direct", source)
	action.PlannedTargetRuntimeGeneration = target.RuntimeGeneration
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, action))
	require.NoError(t, store.TransitionManagedLifecycleAction(ctx, ManagedLifecycleTransition{ActionID: action.ActionID, FromState: "admitted", FromStage: "admission", ToState: "starting_target", ToStage: "target_start", Fence: source}))
	wrong := source
	wrong.ProcessPID++
	require.Error(t, store.BindManagedLifecycleTarget(ctx, action.ActionID, wrong, target, artifact.ArtifactID))
	require.NoError(t, store.BindManagedLifecycleTarget(ctx, action.ActionID, source, target, artifact.ArtifactID))
	claim, err := store.ClaimManagedLifecycleRecovery(ctx, action.ActionID, "startup-owner")
	require.NoError(t, err)
	require.Equal(t, target, claim.Fence)
	require.NoError(t, store.ClearManagedLifecycleRecoveryClaims(ctx))
	stored, err := store.ManagedLifecycleAction(ctx, action.ActionID)
	require.NoError(t, err)
	require.Empty(t, stored.RecoveryOwner)
	require.Empty(t, stored.RecoveryClaimedAt)
}

func TestLegacyBatchRowsAndGraphRetentionAreAtomic(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	parentID, childID := lifecycleUUID(20), lifecycleUUID(21)
	child := lifecycleActionInput(childID, agent.AgentID, "restart", "direct", source)
	child.Operation = lifecycleOperation(childID, "2026-09-07T00:00:00.000Z")
	child.State, child.Stage = "succeeded", "complete"
	child.TerminalCode, child.TerminalMessage, child.Retryable = "RESTARTED", "managed agent restarted", false
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, child))
	seedLegacyRestartAllBatch(t, store, parentID, "2026-09-07T00:00:00.000Z")
	seedLegacyAcceptedChild(t, store, parentID, childID, agent.AgentID)
	_, err := store.db.Exec(`INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,error_code,error_message,created_at) VALUES (?,1,'rejected-agent','rejected','NOT_READY','legacy rejection','2026-09-08T00:00:00.000Z')`, parentID)
	require.NoError(t, err)
	store.managedGraphFault = func(stage string) error {
		if stage == "after_first_batch_item_delete" {
			return errors.New("injected graph retention failure")
		}
		return nil
	}
	_, err = store.Sweep(ctx, DefaultRetentionPolicy)
	require.ErrorContains(t, err, "injected graph retention failure")
	var retainedItems int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&retainedItems))
	require.Equal(t, 2, retainedItems)
	store.managedGraphFault = nil

	result, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 2, result.ManagedOperations)
	var actions, items, operations int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id IN (?,?)`, parentID, childID).Scan(&actions))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&items))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations WHERE operation_id IN (?,?)`, parentID, childID).Scan(&operations))
	require.Zero(t, actions)
	require.Zero(t, items)
	require.Zero(t, operations)
}

func TestLifecycleGraphRetentionKeepsAnExpiredParentForAnUnexpiredCoalescedChild(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	// This retention boundary is about the relative age of the two operations,
	// not the wall clock when the test happens to run.
	store.clock = deterministic.NewFixedClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	source := restartFenceFromAgent(&agent)
	parentID, childID := lifecycleUUID(30), lifecycleUUID(31)
	child := lifecycleActionInput(childID, agent.AgentID, "restart", "direct", source)
	child.Operation = lifecycleOperation(childID, "2026-09-10T00:00:00.000Z")
	child.State, child.Stage = "succeeded", "complete"
	child.TerminalCode, child.TerminalMessage, child.Retryable = "RESTARTED", "managed agent restarted", false
	require.NoError(t, store.CreateManagedLifecycleAction(ctx, child))
	seedLegacyRestartAllBatch(t, store, parentID, "2026-09-07T00:00:00.000Z", []string{"0", agent.AgentID, "coalesced", childID})
	result, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	var parents, children, items int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, parentID).Scan(&parents))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, childID).Scan(&children))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&items))
	require.Equal(t, 1, parents)
	require.Equal(t, 1, children)
	require.Equal(t, 1, items)
}

func TestLaunchOperationLinkPreventsUnsafeSweep(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	agent := ManagedAgentRecord{AgentID: "protected", Name: "protected", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "protected-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "protected-request", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, store.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, store.TerminalizeRecoveredManagedLaunch(ctx, agent.AgentID, op.OperationID, "LAUNCH_RECOVERY_IDENTITY_UNSAFE", "managed launch identity could not be verified"))
	_, err := store.db.Exec(`UPDATE managed_http_operations SET expires_at='2026-09-07T00:00:00.000Z' WHERE operation_id=?`, op.OperationID)
	require.NoError(t, err)
	result, err := store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	var agents, operations, foreignProblems int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&agents))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations WHERE operation_id=?`, op.OperationID).Scan(&operations))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, 1, agents)
	require.Equal(t, 1, operations)
	require.Zero(t, foreignProblems)
}

func TestManagedControlReservationsAreFencedBoundedAndCancelable(t *testing.T) {
	store, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	request := lifecycleControlInput(1, agent.AgentID, fence)
	first, err := store.ReserveManagedControlRequest(ctx, request)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	replay, err := store.ReserveManagedControlRequest(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	changed := request
	changed.ToolName = "different"
	_, err = store.ReserveManagedControlRequest(ctx, changed)
	var conflict *ManagedControlConflictError
	require.ErrorAs(t, err, &conflict)
	for i := 2; i <= 64; i++ {
		_, reserveErr := store.ReserveManagedControlRequest(ctx, lifecycleControlInput(i, agent.AgentID, fence))
		require.NoError(t, reserveErr)
	}
	_, err = store.ReserveManagedControlRequest(ctx, lifecycleControlInput(65, agent.AgentID, fence))
	require.ErrorAs(t, err, &conflict)
	require.NoError(t, store.CancelManagedControlRequests(ctx, agent.AgentID, "CANCELLED_BY_RESET"))
	var nonterminal, cancelled int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=? AND state NOT IN ('delivered','failed','cancelled')`, agent.AgentID).Scan(&nonterminal))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=? AND state='cancelled'`, agent.AgentID).Scan(&cancelled))
	require.Zero(t, nonterminal)
	require.Equal(t, 64, cancelled)
}

func lifecycleActionInput(id, agentID, kind, source string, fence ManagedRuntimeFence) ManagedLifecycleActionInput {
	input := ManagedLifecycleActionInput{ActionID: id, AgentID: agentID, Kind: kind, Source: source, ExecutionOwner: "lifecycle", State: "admitted", Stage: "admission", PlannedTargetRuntimeGeneration: lifecycleUUID(99), CreatedAt: "2026-09-08T00:00:00.000Z", UpdatedAt: "2026-09-08T00:00:00.000Z", Operation: lifecycleOperation(id, "2026-09-09T00:00:00.000Z")}
	input.Operation.State = "pending"
	if agentID != "" {
		input.SourceFence = &fence
	}
	if kind == "terminate" || kind == "restart_all" {
		input.PlannedTargetRuntimeGeneration = ""
	}
	return input
}

func lifecycleControlInput(n int, agentID string, fence ManagedRuntimeFence) ManagedControlRequestInput {
	return ManagedControlRequestInput{RequestID: lifecycleUUID(1000 + n), ResultID: lifecycleUUID(2000 + n), AgentID: agentID, ToolCallID: "tool-call", CapabilityID: "capability", ToolName: "control", DefinitionID: "definition", OperationID: "operation", CanonicalArgumentsSHA256: testManifestHash, RequestFingerprint: testManifestHash, Fence: fence, ReceivedAt: "2026-09-08T00:00:00.000Z"}
}

func legacy027StateForStage(stage string) string {
	return map[string]string{"reserved": "admitted", "prepared": "admitted", "settling": "awaiting_settle", "stopping": "stopping_source", "stopped": "starting_target", "starting": "awaiting_target_ready"}[stage]
}

func legacy027ActionStageForStage(stage string) string {
	return map[string]string{"reserved": "admission", "prepared": "admission", "settling": "source_settle", "stopping": "source_stop", "stopped": "target_start", "starting": "target_ready"}[stage]
}

func seedMigration027Accepted026Fixture(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	for index, stage := range []string{"reserved", "prepared", "settling", "stopping", "stopped", "starting"} {
		agentID := "legacy-" + stage
		agent := ManagedAgentRecord{AgentID: agentID, Name: agentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session-" + stage, SessionEpoch: int64(index + 1), ManifestRevision: index + 1, ManifestHash: testManifestHash}
		launch := ManagedOperation{OperationID: "launch-" + stage, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-request-" + stage, CanonicalBodyHash: "body-" + stage, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
		require.NoError(t, store.ReserveManagedLaunch(ctx, agent, launch))
		require.NoError(t, store.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 100 + index, StartMarker: "marker-" + stage, Incarnation: "incarnation-" + stage}, "generation-"+stage))
		require.NoError(t, store.MarkManagedReady(ctx, agent.AgentID))
		require.NoError(t, store.MarkManagedOperationTerminal(ctx, launch.OperationID))
		ready, err := store.ManagedAgent(ctx, agent.AgentID)
		require.NoError(t, err)
		reservation := restartReservation(*ready, launch)
		reservation.Operation.OperationID, reservation.Operation.RequestID = agentID, agentID
		reservation.Operation.CanonicalPath = "/v1/managed-agents/" + agentID + "/restart"
		require.NoError(t, store.PutManagedOperation(ctx, ManagedOperation{OperationID: "current-" + stage, Method: "POST", CanonicalPath: "/v1/current", RequestID: "current-" + stage, CanonicalBodyHash: "current-" + stage, State: "terminal", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`), CreatedAt: "2026-09-08T00:00:00.000Z", TerminalAt: "2026-09-08T00:00:00.000Z", ExpiresAt: "2026-09-09T00:00:00.000Z"}))
		reservation.CurrentOperationID = launch.OperationID
		_, _, err = store.ReserveManagedRestart(ctx, reservation)
		require.NoError(t, err)
		if stage != "reserved" {
			setTransitionStageForMigration027Fixture(t, store, agentID, stage)
		}
	}
	// Populate both direct-delivery children without changing their schema.
	now := "2026-09-08T00:00:00.000Z"
	require.NoError(t, execTestData(store, `INSERT INTO agents (agent_id,first_seen_at) VALUES ('sender',?)`, now))
	require.NoError(t, execTestData(store, `INSERT INTO agents (agent_id,first_seen_at) VALUES ('legacy-reserved',?)`, now))
	require.NoError(t, execTestData(store, `INSERT INTO messages (id,sender,recipient,body,created_at,expires_at,state) VALUES ('migration-message','sender','legacy-reserved','body',?,?,'queued')`, now, "2026-09-09T00:00:00.000Z"))
	require.NoError(t, execTestData(store, `INSERT INTO message_target_snapshots (snapshot_id,message_id,message_incarnation_id,recipient_name,source_kind,owner_identity,created_at) VALUES ('migration-snapshot','migration-message','incarnation','legacy-reserved','managed','legacy-reserved',?)`, now))
	require.NoError(t, execTestData(store, `INSERT INTO managed_direct_delivery_attempts (message_id,message_incarnation_id,attempt_number,snapshot_id,agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,state,created_at,expires_at) VALUES ('migration-message','incarnation',1,'migration-snapshot','legacy-reserved','session-reserved',1,100,'marker-reserved','incarnation-reserved','generation-reserved',1,?,'direct_pending',?,?)`, testManifestHash, now, "2026-09-09T00:00:00.000Z"))
	require.NoError(t, execTestData(store, `INSERT INTO managed_direct_delivery_diagnostics (agent_id,code,observed_at) VALUES ('legacy-reserved','MIGRATION_FIXTURE',?)`, now))
}

func setTransitionStageForMigration027Fixture(t *testing.T, store *Store, agentID, stage string) {
	t.Helper()
	query := `UPDATE managed_restart_transitions SET stage=?, reservation_activity_state='idle', planned_stop=0, turn_settled_at=NULL WHERE agent_id=?`
	args := []any{stage, agentID}
	if stage == "stopping" || stage == "stopped" || stage == "starting" {
		query = `UPDATE managed_restart_transitions SET stage=?, reservation_activity_state='active', planned_stop=1, turn_settled_at='2026-09-08T00:00:00.000Z' WHERE agent_id=?`
	}
	if stage == "starting" {
		query = `UPDATE managed_restart_transitions SET stage=?, reservation_activity_state='active', planned_stop=1, turn_settled_at='2026-09-08T00:00:00.000Z', target_pi_session_id='target-session', target_session_epoch=2, target_process_pid=200, target_process_start_marker='target-marker', target_process_incarnation='target-incarnation', target_manifest_revision=2, target_manifest_hash=? WHERE agent_id=?`
		args = []any{stage, testManifestHash, agentID}
	}
	_, err := store.db.Exec(query, args...)
	require.NoError(t, err)
}

var _ = sql.ErrNoRows
var _ = time.Second

// seedLegacyRestartAllBatch writes the rows an existing database may still
// hold from before Restart All was retired. The retired write API no longer
// exists, so the fixture goes straight to SQL. Production code never reads
// these rows to recover or settle anything; they only matter for retention
// and foreign-key integrity.
func seedLegacyRestartAllBatch(t *testing.T, store *Store, parentID, parentExpiresAt string, items ...[]string) {
	t.Helper()
	require.NoError(t, store.runImmediate(context.Background(), func() error {
		if err := insertManagedOperation(context.Background(), store.db, lifecycleOperation(parentID, parentExpiresAt)); err != nil {
			return err
		}
		if _, err := store.db.Exec(`INSERT INTO managed_lifecycle_actions (action_id,operation_id,agent_id,kind,source,execution_owner,state,stage,terminal_code,terminal_message,retryable,completed_at,created_at,updated_at) VALUES (?,?,NULL,'restart_all','direct','lifecycle','succeeded','complete','BATCH_ADMITTED','managed restart batch admitted',0,'2026-09-08T00:00:00.000Z','2026-09-08T00:00:00.000Z','2026-09-08T00:00:00.000Z')`, parentID, parentID); err != nil {
			return err
		}
		// Each item is {position, agentID, status, childID-or-error-code}.
		for _, item := range items {
			if item[2] == "rejected" {
				if _, err := store.db.Exec(`INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,error_code,error_message,created_at) VALUES (?,?,?,?,?,?,'2026-09-08T00:00:00.000Z')`, parentID, item[0], item[1], item[2], item[3], "legacy rejection"); err != nil {
					return err
				}
				continue
			}
			if _, err := store.db.Exec(`INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,created_at) VALUES (?,?,?,?,?,?,'2026-09-08T00:00:00.000Z')`, parentID, item[0], item[1], item[2], item[3], item[3]); err != nil {
				return err
			}
		}
		return nil
	}))
}

// Convert a terminal direct fixture into the accepted child shape persisted by old releases.
func seedLegacyAcceptedChild(t *testing.T, store *Store, parentID, childID, agentID string) {
	t.Helper()
	_, err := store.db.Exec(`UPDATE managed_lifecycle_actions SET source='restart_all_child', parent_action_id=? WHERE action_id=?`, parentID, childID)
	require.NoError(t, err)
	_, err = store.db.Exec(`INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,created_at) VALUES (?,0,?,'accepted',?,?,'2026-09-08T00:00:00.000Z')`, parentID, agentID, childID, childID)
	require.NoError(t, err)
}

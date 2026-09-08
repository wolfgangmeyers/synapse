package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func restartAllReservation(parentID string, allocate func() (ManagedRestartAllChildIdentity, error)) ManagedRestartAllReservation {
	return ManagedRestartAllReservation{
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents/restart-all",
		RequestID:         parentID,
		CanonicalBodyHash: testManifestHash,
		AllocateChild:     allocate,
	}
}

func TestReserveManagedRestartAllAtomicallyAdmitsChildrenAndBuildsStoredResponse(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	parentID, childID, generation := lifecycleUUID(400), lifecycleUUID(401), lifecycleUUID(402)
	calls := 0
	admission, err := durable.ReserveManagedRestartAll(ctx, restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
		calls++
		return ManagedRestartAllChildIdentity{ActionID: childID, PlannedTargetRuntimeGeneration: generation}, nil
	}))
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, []string{childID}, admission.AcceptedChildActionIDs)
	require.Equal(t, 1, calls)
	require.NotNil(t, admission.Operation)
	require.Equal(t, "terminal", admission.Operation.State)
	require.Equal(t, 202, admission.Operation.ResponseStatus)
	require.Equal(t, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": parentID}, admission.Operation.ResponseHeaders)

	var response struct {
		OperationID string `json:"operation_id"`
		Agents      []struct {
			AgentID     string  `json:"agent_id"`
			OperationID *string `json:"operation_id"`
			Status      string  `json:"status"`
		} `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(admission.Operation.ResponseBody, &response))
	require.Equal(t, parentID, response.OperationID)
	require.Len(t, response.Agents, 1)
	require.Equal(t, agent.AgentID, response.Agents[0].AgentID)
	require.Equal(t, childID, *response.Agents[0].OperationID)
	require.Equal(t, "accepted", response.Agents[0].Status)

	parent, err := durable.ManagedLifecycleAction(ctx, parentID)
	require.NoError(t, err)
	require.Equal(t, "restart_all", parent.Kind)
	require.Equal(t, "succeeded", parent.State)
	require.Equal(t, "complete", parent.Stage)
	require.Nil(t, parent.SourceFence)
	require.Nil(t, parent.TargetFence)
	child, err := durable.ManagedLifecycleAction(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, "restart_all_child", child.Source)
	require.Equal(t, parentID, child.ParentActionID)
	require.Equal(t, "admitted", child.State)
	require.Equal(t, "admission", child.Stage)
	require.Equal(t, generation, child.PlannedTargetRuntimeGeneration)
	require.Equal(t, restartFenceFromAgent(&agent), *child.SourceFence)
	childOperation, err := durable.ManagedOperation(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, "pending", childOperation.State)
	require.Equal(t, "/internal/managed-lifecycle/restart-all/"+parentID+"/children/"+agent.AgentID, childOperation.CanonicalPath)
	require.Equal(t, restartAllEmptyBodyHash(), childOperation.CanonicalBodyHash)

	updated, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "restarting", updated.LifecycleState)
	require.False(t, updated.AcceptingPrompts)
	var items int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=? AND status='accepted'`, parentID).Scan(&items))
	require.Equal(t, 1, items)
	var transitions int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_restart_transitions WHERE agent_id=?`, agent.AgentID).Scan(&transitions))
	require.Zero(t, transitions)
}

func TestReserveManagedRestartAllRollsBackEveryAdmissionCheckpoint(t *testing.T) {
	for index, checkpoint := range []string{"after_parent_operation", "after_parent_action", "after_child_operation", "after_child_action", "after_batch_item", "after_agent_updates", "after_parent_operation_terminal", "after_parent_action_terminal"} {
		t.Run(checkpoint, func(t *testing.T) {
			durable, ctx, agent, _ := restartFixture(t)
			beforeVersion, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			parentID, childID, generation := lifecycleUUID(500+index*3), lifecycleUUID(501+index*3), lifecycleUUID(502+index*3)
			durable.managedRestartAllFault = func(stage string) error {
				if stage == checkpoint {
					return errors.New("injected " + checkpoint)
				}
				return nil
			}
			_, err = durable.ReserveManagedRestartAll(ctx, restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
				return ManagedRestartAllChildIdentity{ActionID: childID, PlannedTargetRuntimeGeneration: generation}, nil
			}))
			require.ErrorContains(t, err, checkpoint)
			var actions, operations, items, foreignProblems int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id IN (?,?)`, parentID, childID).Scan(&actions))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations WHERE operation_id IN (?,?)`, parentID, childID).Scan(&operations))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&items))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
			require.Zero(t, actions)
			require.Zero(t, operations)
			require.Zero(t, items)
			require.Zero(t, foreignProblems)
			stored, readErr := durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, readErr)
			require.Equal(t, "running", stored.LifecycleState)
			require.True(t, stored.AcceptingPrompts)
			afterVersion, readErr := durable.GetProjectionVersion(ctx)
			require.NoError(t, readErr)
			require.Equal(t, beforeVersion, afterVersion)
		})
	}
}

func TestReserveManagedRestartAllCapacityRejectsThirtyThreeRowsBeforeAllocation(t *testing.T) {
	durable, ctx, _, _ := restartFixture(t)
	for index := 0; index < 32; index++ {
		agentID := lifecycleUUID(600 + index)
		_, err := durable.db.ExecContext(ctx, `INSERT INTO managed_agents (
			agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
			desired_state,lifecycle_state,activity_state,accepting_prompts,session_epoch,
			manifest_revision,manifest_hash,created_at,updated_at
		) VALUES (?,?,0,'/tmp','openai-codex','gpt-5.6-terra','running','starting','starting',0,1,1,?,'2026-09-08T00:00:00.000Z','2026-09-08T00:00:00.000Z')`, agentID, agentID, testManifestHash)
		require.NoError(t, err)
	}
	beforeVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	parentID := lifecycleUUID(800)
	allocations := 0
	_, err = durable.ReserveManagedRestartAll(ctx, restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	}))
	require.ErrorContains(t, err, "snapshot exceeds capacity")
	require.Zero(t, allocations)
	var operations, actions, items int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations WHERE operation_id=?`, parentID).Scan(&operations))
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, parentID).Scan(&actions))
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&items))
	require.Zero(t, operations)
	require.Zero(t, actions)
	require.Zero(t, items)
	afterVersion, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeVersion, afterVersion)
}

func TestReserveManagedRestartAllReplaysBeforeAllocationAndCoalescesSharedDirectRestart(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	directID := lifecycleUUID(410)
	directOperation := lifecycleOperation(directID, "2026-09-09T00:00:00.000Z")
	directOperation.State = "pending"
	directOperation.TerminalAt, directOperation.ExpiresAt = "", ""
	direct := ManagedLifecycleActionInput{ActionID: directID, AgentID: agent.AgentID, Kind: "restart", Source: "direct", ExecutionOwner: "lifecycle", State: "admitted", Stage: "admission", SourceFence: &fence, PlannedTargetRuntimeGeneration: lifecycleUUID(411), CreatedAt: "2026-09-08T00:00:00.000Z", UpdatedAt: "2026-09-08T00:00:00.000Z", Operation: directOperation}
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, direct))
	_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting',accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)

	var allocations int
	firstID := lifecycleUUID(412)
	first, err := durable.ReserveManagedRestartAll(ctx, restartAllReservation(firstID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	}))
	require.NoError(t, err)
	require.Empty(t, first.AcceptedChildActionIDs)
	secondID := lifecycleUUID(413)
	second, err := durable.ReserveManagedRestartAll(ctx, restartAllReservation(secondID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	}))
	require.NoError(t, err)
	require.Empty(t, second.AcceptedChildActionIDs)
	require.Zero(t, allocations)

	for _, parentID := range []string{firstID, secondID} {
		var actionID, operationID, status string
		require.NoError(t, durable.db.QueryRow(`SELECT child_action_id,child_operation_id,status FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&actionID, &operationID, &status))
		require.Equal(t, directID, actionID)
		require.Equal(t, directID, operationID)
		require.Equal(t, "coalesced", status)
	}

	replay, err := durable.ReserveManagedRestartAll(ctx, restartAllReservation(firstID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	}))
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replay.Operation.ResponseBody)
	require.Zero(t, allocations)
	changedPath := restartAllReservation(firstID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	})
	changedPath.CanonicalPath = "/v1/managed-agents/restart-all/other"
	_, err = durable.ReserveManagedRestartAll(ctx, changedPath)
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
	changedBody := restartAllReservation(firstID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	})
	changedBody.CanonicalBodyHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = durable.ReserveManagedRestartAll(ctx, changedBody)
	require.ErrorAs(t, err, &conflict)
	require.Zero(t, allocations)
}

func TestRestartAllBatchReferenceMigrationPreservesRowsAndAllowsSharedCoalescing(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/router.db"
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "028_managed_restart_all_batch_references")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "migration-restart", Name: "migration-restart", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "migration-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "migration-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-launch-request", CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 811, StartMarker: "migration-start", Incarnation: "migration-incarnation"}, "migration-generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, launch.OperationID))
	stored, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	source := restartFenceFromAgent(stored)
	directID := lifecycleUUID(430)
	direct := lifecycleActionInput(directID, agent.AgentID, "restart", "direct", source)
	direct.Operation.State = "pending"
	direct.Operation.TerminalAt, direct.Operation.ExpiresAt = "", ""
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, direct))
	parentID := lifecycleUUID(431)
	parent := lifecycleActionInput(parentID, "", "restart_all", "direct", ManagedRuntimeFence{})
	parent.State, parent.Stage = "succeeded", "complete"
	parent.TerminalCode, parent.TerminalMessage, parent.Retryable = "BATCH_ADMITTED", "managed restart batch admitted", false
	parent.Operation.State = "terminal"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, parent))
	require.NoError(t, before.WriteManagedLifecycleBatchItems(ctx, parentID, []ManagedLifecycleBatchItem{{SnapshotPosition: 0, AgentID: agent.AgentID, Status: "coalesced", ChildActionID: directID, ChildOperationID: directID, CreatedAt: "2026-09-08T00:00:00.000Z"}}))
	var originalTableSQL string
	require.NoError(t, before.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_batch_items'`).Scan(&originalTableSQL))
	before.managedRestartAllFault = func(stage string) error {
		if stage == "migration_028_before_record" {
			return errors.New("injected migration 028 failure")
		}
		return nil
	}
	err = before.applyManagedRestartAllBatchReferencesMigration028(ctx, migrationByID(t, migrations, "028_managed_restart_all_batch_references"))
	require.ErrorContains(t, err, "injected migration 028 failure")
	var restoredItems, migrationRows int
	var restoredTableSQL string
	require.NoError(t, before.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items`).Scan(&restoredItems))
	require.NoError(t, before.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='028_managed_restart_all_batch_references'`).Scan(&migrationRows))
	require.NoError(t, before.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_batch_items'`).Scan(&restoredTableSQL))
	require.Equal(t, 1, restoredItems)
	require.Zero(t, migrationRows)
	require.Equal(t, originalTableSQL, restoredTableSQL)
	before.managedRestartAllFault = nil
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	parentTwoID := lifecycleUUID(432)
	parentTwo := lifecycleActionInput(parentTwoID, "", "restart_all", "direct", ManagedRuntimeFence{})
	parentTwo.State, parentTwo.Stage = "succeeded", "complete"
	parentTwo.TerminalCode, parentTwo.TerminalMessage, parentTwo.Retryable = "BATCH_ADMITTED", "managed restart batch admitted", false
	parentTwo.Operation.State = "terminal"
	require.NoError(t, after.CreateManagedLifecycleAction(ctx, parentTwo))
	require.NoError(t, after.WriteManagedLifecycleBatchItems(ctx, parentTwoID, []ManagedLifecycleBatchItem{{SnapshotPosition: 0, AgentID: agent.AgentID, Status: "coalesced", ChildActionID: directID, ChildOperationID: directID, CreatedAt: "2026-09-08T00:00:00.000Z"}}))
	var rows, foreignProblems int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE child_action_id=?`, directID).Scan(&rows))
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, 2, rows)
	require.Zero(t, foreignProblems)
}

func TestReserveManagedRestartAllRejectsMalformedCoalescedIdentityAsFixedItem(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	directID, malformedActionID := lifecycleUUID(440), lifecycleUUID(441)
	direct := lifecycleActionInput(directID, agent.AgentID, "restart", "direct", fence)
	direct.Operation.State, direct.Operation.TerminalAt, direct.Operation.ExpiresAt = "pending", "", ""
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, direct))
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET action_id=? WHERE action_id=?`, malformedActionID, directID)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)

	allocations := 0
	parentID := lifecycleUUID(442)
	admission, err := durable.ReserveManagedRestartAll(ctx, restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{}, nil
	}))
	require.NoError(t, err)
	require.Empty(t, admission.AcceptedChildActionIDs)
	require.Zero(t, allocations)
	var status, code, actionID, operationID string
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT status,error_code,COALESCE(child_action_id,''),COALESCE(child_operation_id,'') FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&status, &code, &actionID, &operationID))
	require.Equal(t, "rejected", status)
	require.Equal(t, restartAllConflictCode, code)
	require.Empty(t, actionID)
	require.Empty(t, operationID)
}

func TestReserveManagedRestartAllConcurrentParentsCoalesceOneDirectRestart(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	fence := restartFenceFromAgent(&agent)
	directID := lifecycleUUID(450)
	direct := lifecycleActionInput(directID, agent.AgentID, "restart", "direct", fence)
	direct.Operation.State, direct.Operation.TerminalAt, direct.Operation.ExpiresAt = "pending", "", ""
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, direct))
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)

	parentIDs := []string{lifecycleUUID(451), lifecycleUUID(452)}
	start := make(chan struct{})
	var wait sync.WaitGroup
	admissions := make([]ManagedRestartAllAdmission, len(parentIDs))
	errs := make([]error, len(parentIDs))
	for index, parentID := range parentIDs {
		wait.Add(1)
		go func(index int, parentID string) {
			defer wait.Done()
			<-start
			admissions[index], errs[index] = durable.ReserveManagedRestartAll(ctx, restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
				return ManagedRestartAllChildIdentity{}, errors.New("coalesced restart must not allocate a child")
			}))
		}(index, parentID)
	}
	close(start)
	wait.Wait()
	for index, parentID := range parentIDs {
		require.NoError(t, errs[index])
		require.Empty(t, admissions[index].AcceptedChildActionIDs)
		var actionID, operationID, status string
		require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT child_action_id,child_operation_id,status FROM managed_lifecycle_batch_items WHERE parent_action_id=?`, parentID).Scan(&actionID, &operationID, &status))
		require.Equal(t, directID, actionID)
		require.Equal(t, directID, operationID)
		require.Equal(t, "coalesced", status)
	}
}

func TestRestartAllSharedCoalescedReferencesRetainEveryParentUntilTheChildExpires(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	source := restartFenceFromAgent(&agent)
	childID := lifecycleUUID(460)
	child := lifecycleActionInput(childID, agent.AgentID, "restart", "direct", source)
	child.State, child.Stage = "succeeded", "complete"
	child.TerminalCode, child.TerminalMessage = "RESTARTED", "managed agent restarted"
	child.Operation = lifecycleOperation(childID, "2999-01-01T00:00:00.000Z")
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, child))

	parentIDs := []string{lifecycleUUID(461), lifecycleUUID(462)}
	for _, parentID := range parentIDs {
		parent := lifecycleActionInput(parentID, "", "restart_all", "direct", ManagedRuntimeFence{})
		parent.State, parent.Stage = "succeeded", "complete"
		parent.TerminalCode, parent.TerminalMessage = "BATCH_ADMITTED", "managed restart batch admitted"
		parent.Operation = lifecycleOperation(parentID, "2000-01-01T00:00:00.000Z")
		require.NoError(t, durable.CreateManagedLifecycleAction(ctx, parent))
		require.NoError(t, durable.WriteManagedLifecycleBatchItems(ctx, parentID, []ManagedLifecycleBatchItem{{SnapshotPosition: 0, AgentID: agent.AgentID, Status: "coalesced", ChildActionID: childID, ChildOperationID: childID, CreatedAt: "2026-09-08T00:00:00.000Z"}}))
	}

	result, err := durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	for _, actionID := range append(parentIDs, childID) {
		var count int
		require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, actionID).Scan(&count))
		require.Equal(t, 1, count)
	}

	_, err = durable.db.ExecContext(ctx, `UPDATE managed_http_operations SET expires_at='2000-01-01T00:00:00.000Z' WHERE operation_id=?`, childID)
	require.NoError(t, err)
	result, err = durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Equal(t, 3, result.ManagedOperations)
	for _, actionID := range append(parentIDs, childID) {
		var count int
		require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, actionID).Scan(&count))
		require.Zero(t, count)
	}
	var items, foreignProblems int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_batch_items WHERE child_action_id=?`, childID).Scan(&items))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Zero(t, items)
	require.Zero(t, foreignProblems)
}

func TestReserveManagedRestartAllRejectsNotReadyRowsWithoutPartialAdmission(t *testing.T) {
	durable, ctx, _, _ := restartFixture(t)
	notReady := ManagedAgentRecord{AgentID: "not-ready", Name: "not-ready", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "not-ready-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "not-ready-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "not-ready-request", CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, notReady, launch))

	parentID, childID, generation := lifecycleUUID(420), lifecycleUUID(421), lifecycleUUID(422)
	allocations := 0
	admission, err := durable.ReserveManagedRestartAll(context.Background(), restartAllReservation(parentID, func() (ManagedRestartAllChildIdentity, error) {
		allocations++
		return ManagedRestartAllChildIdentity{ActionID: childID, PlannedTargetRuntimeGeneration: generation}, nil
	}))
	require.NoError(t, err)
	require.Equal(t, 1, allocations)
	require.Equal(t, []string{childID}, admission.AcceptedChildActionIDs)
	var status, code, message string
	require.NoError(t, durable.db.QueryRow(`SELECT status,error_code,error_message FROM managed_lifecycle_batch_items WHERE parent_action_id=? AND agent_id='not-ready'`, parentID).Scan(&status, &code, &message))
	require.Equal(t, "rejected", status)
	require.Equal(t, "AGENT_NOT_READY", code)
	require.Equal(t, "managed agent is not ready to restart", message)
	stored, err := durable.ManagedAgent(ctx, "not-ready")
	require.NoError(t, err)
	require.Equal(t, "starting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testManifestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestManagedRecipientConflictResolution(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyManagedDirectFixture(t)
	enqueue := func(id, body string) (EnqueueResult, error) {
		envelope := testEnvelope(id, body)
		envelope.To = fence.AgentID
		return durable.Enqueue(ctx, envelope)
	}
	first, err := enqueue("conflict-first", "first")
	require.NoError(t, err)
	firstTarget, err := durable.ResolveMessageTarget(ctx, first.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "managed", firstTarget.SourceKind)

	// Startup reconciliation keeps both concrete contenders. No new snapshot is
	// selected while the recipient is conflicted, so lookup order cannot fall
	// back to tmux or managed delivery.
	_, err = durable.db.Exec(`UPDATE recipient_sources SET state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-source',updated_at=? WHERE recipient_name=?`, formatTimestamp(time.Now()), fence.AgentID)
	require.NoError(t, err)
	blocked, err := enqueue("conflict-blocked", "blocked")
	require.NoError(t, err)
	blockedTarget, err := durable.ResolveMessageTarget(ctx, blocked.Message.ID)
	require.NoError(t, err)
	require.Nil(t, blockedTarget)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.Nil(t, attempt, "a conflicted source must not fall back to managed delivery")

	// Retiring the secondary resumes the primary without rewriting its snapshot.
	require.NoError(t, durable.RetireTmuxRecipientSource(ctx, fence.AgentID, "tmux-source"))
	source, err := durable.RecipientSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "managed", source.SourceKind)
	require.Equal(t, "active", source.State)
	blockedTarget, err = durable.ResolveMessageTarget(ctx, blocked.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "managed", blockedTarget.SourceKind)

	// A second conflict makes the managed owner the retired primary. Its old
	// snapshots become terminal and the recorded tmux survivor is promoted.
	_, err = durable.db.Exec(`UPDATE recipient_sources SET state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-source',updated_at=? WHERE recipient_name=?`, formatTimestamp(time.Now()), fence.AgentID)
	require.NoError(t, err)
	survivor, err := enqueue("conflict-survivor", "survivor")
	require.NoError(t, err)
	require.NoError(t, durable.RetireManagedRecipientSource(ctx, fence.AgentID, fence.AgentID))
	source, err = durable.RecipientSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "tmux", source.SourceKind)
	require.Equal(t, "tmux-source", source.OwnerIdentity)
	require.Equal(t, "active", source.State)
	for _, id := range []string{first.Message.ID, blocked.Message.ID} {
		message, err := durable.MessageFor(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "dead_letter", message.State, "retired snapshots are terminalized")
	}
	survivorTarget, err := durable.ResolveMessageTarget(ctx, survivor.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "tmux", survivorTarget.SourceKind)
	require.Equal(t, "tmux-source", survivorTarget.OwnerIdentity)
	attempt, err = durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.Nil(t, attempt, "the promoted tmux survivor must not fall back to managed direct mail")
}

func TestManagedReadyReconcilesReservedQueuedMessages(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	envelope := testEnvelope("reserved-startup", "queued before ready")
	envelope.To = fence.AgentID
	_, err := durable.Enqueue(ctx, envelope)
	require.NoError(t, err)
	target, err := durable.ResolveMessageTarget(ctx, "reserved-startup")
	require.NoError(t, err)
	require.Nil(t, target, "a reserved managed source must not select a transport")

	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	target, err = durable.MessageTargetSnapshot(ctx, "reserved-startup")
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)
	require.Equal(t, fence.AgentID, target.OwnerIdentity)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.Equal(t, "reserved-startup", attempt.MessageID)
}

func TestDefaultModelSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.db")
	ctx := context.Background()
	first, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	require.NoError(t, first.SetDefaultModel(ctx, &ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}))
	require.NoError(t, first.Close())
	second, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer second.Close()
	model, err := second.GetDefaultModel(ctx)
	require.NoError(t, err)
	require.Equal(t, &ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, model)
}

func TestFailedManagedRowsRemainDurableWhileTheirSlotIsReleased(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "failed-agent", Name: "reserved-name", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "failed-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "failed-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.MarkManagedFailed(ctx, agent.AgentID, "READINESS_FAILED", "failed startup"))
	result, err := durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedAgents)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.LifecycleState)
	require.False(t, stored.SlotHeld)
	conflict := agent
	conflict.AgentID = "replacement"
	conflictOp := op
	conflictOp.OperationID, conflictOp.RequestID = "replacement-operation", "replacement-request"
	var nameConflict *ManagedNameConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, conflict, conflictOp), &nameConflict)
}

func TestMarkManagedStoppedClearsLiveProjectionAndSlot(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "stoppable", Name: "stoppable", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "stoppable-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "stoppable-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedStopped(ctx, agent.AgentID))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.Equal(t, "stopped", stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.False(t, stored.SlotHeld)
}

func restartFixture(t *testing.T) (*Store, context.Context, ManagedAgentRecord, ManagedOperation) {
	t.Helper()
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	agent := ManagedAgentRecord{AgentID: "restartable", Name: "restartable", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "launch-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-request", CanonicalBodyHash: "launch-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"}, ResponseBody: []byte(`{"operation_id":"launch-operation"}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 42, StartMarker: "start", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, launch.OperationID))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	return durable, ctx, *stored, launch
}

func restartReservation(agent ManagedAgentRecord, current ManagedOperation) ManagedRestartReservation {
	operation := ManagedOperation{OperationID: "restart-operation", Method: "POST", CanonicalPath: "/v1/managed-agents/restartable/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": "restart-operation"}, ResponseBody: []byte(`{"operation_id":"restart-operation","agent":{"agent_id":"restartable"}}`)}
	return ManagedRestartReservation{Fence: ManagedRestartFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}, CurrentOperationID: current.OperationID, Operation: operation, Transition: ManagedRestartTransition{TargetRuntimeGeneration: "replacement-generation"}}
}

func TestForwardMigration023To025PreservesManagedRowsAndRestartTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	require.Greater(t, len(migrations), 22)
	require.Equal(t, "026_managed_direct_delivery", migrations[len(migrations)-1].ID)
	before, err := Open(ctx, path, Options{Migrations: migrations[:len(migrations)-2]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "migration-agent", Name: "migration-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 2, ManifestRevision: 3, ManifestHash: testManifestHash, PendingInitialInstruction: "hello"}
	op := ManagedOperation{OperationID: "migration-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 77, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, op.OperationID))
	beforeRestart, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	// The pre-025 store API now writes the widened transition columns. Seed one
	// real 023-shaped retained row directly so this migration proves its copy
	// preserves an in-flight restart as well as ordinary managed rows.
	restartOperation := ManagedOperation{OperationID: "migration-restart", Method: "POST", CanonicalPath: "/v1/managed-agents/migration-agent/restart", RequestID: "migration-restart", CanonicalBodyHash: "restart-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.PutManagedOperation(ctx, restartOperation))
	_, err = before.db.Exec(`INSERT INTO managed_restart_transitions (
		agent_id,operation_id,old_pi_session_id,old_session_epoch,old_process_pid,
		old_process_start_marker,old_process_incarnation,old_runtime_generation,target_runtime_generation,
		old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,
		reservation_activity_state,planned_stop,stage,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		beforeRestart.AgentID, restartOperation.OperationID, beforeRestart.PiSessionID, beforeRestart.SessionEpoch, beforeRestart.ProcessPID,
		beforeRestart.ProcessStartMarker, beforeRestart.ProcessIncarnation, beforeRestart.RuntimeGeneration, "target-generation",
		"running", "running", beforeRestart.ActivityState, 1, 1, beforeRestart.ActivityState, 0, "reserved", beforeRestart.UpdatedAt, beforeRestart.UpdatedAt)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting', accepting_prompts=0 WHERE agent_id=?`, beforeRestart.AgentID)
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	stored, err := after.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "session", stored.PiSessionID)
	require.Equal(t, int64(2), stored.SessionEpoch)
	require.Equal(t, 77, stored.ProcessPID)
	require.Equal(t, "marker", stored.ProcessStartMarker)
	require.Equal(t, "incarnation", stored.ProcessIncarnation)
	require.Equal(t, "generation", stored.RuntimeGeneration)
	transition, err := after.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "migration-restart", transition.OperationID)
	require.Equal(t, "reserved", transition.Stage)
	require.Equal(t, 3, transition.Fence.ManifestRevision)
	require.Equal(t, testManifestHash, transition.Fence.ManifestHash)
	require.Nil(t, transition.TargetFence)
	var indexSQL string
	require.NoError(t, after.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='managed_agents_slot_idx'`).Scan(&indexSQL))
	require.Contains(t, indexSQL, "slot_held")
	_, err = after.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting', activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = after.db.Exec(`UPDATE managed_agents SET desired_state='stopped' WHERE agent_id=?`, agent.AgentID)
	require.Error(t, err)
	var applied int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='023_managed_restart'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='025_managed_restart_execution'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='026_managed_direct_delivery'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='023_managed_restart'`).Scan(&applied))
	require.Equal(t, 1, applied)
}

func TestReserveManagedRestartAtomicallyStoresTransitionAndOperation(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	reservation := restartReservation(agent, launch)
	operation, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, reservation.Operation.OperationID, operation.OperationID)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.Equal(t, agent.ActivityState, stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.True(t, stored.SlotHeld)
	transition, err := durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, reservation.Operation.OperationID, transition.OperationID)
	require.Equal(t, "reserved", transition.Stage)
	require.Equal(t, agent.RuntimeGeneration, transition.Fence.RuntimeGeneration)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	require.Equal(t, 202, operation.ResponseStatus)
	require.Equal(t, reservation.Operation.RequestID, operation.OperationID)
	require.Equal(t, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": reservation.Operation.RequestID}, operation.ResponseHeaders)
}

func TestRestartIdempotencyUsesWorkerScopeThenPathBodyComparison(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	stored, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, reservation.Operation.ResponseBody, stored.ResponseBody)
	changedPath := reservation
	changedPath.Operation.CanonicalPath += "/other"
	var conflict *ManagedIdempotencyConflictError
	_, _, err = durable.ReserveManagedRestart(ctx, changedPath)
	require.ErrorAs(t, err, &conflict)
	changedBody := reservation
	changedBody.Operation.CanonicalBodyHash = "different"
	_, _, err = durable.ReserveManagedRestart(ctx, changedBody)
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestManagedOperationStorageUsesTheInternalWorkerScope(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	operation, err := durable.FindManagedOperation(ctx, reservation.Operation.Method, reservation.Operation.RequestID)
	require.NoError(t, err)
	require.Equal(t, reservation.Operation.OperationID, operation.OperationID)
	var scope string
	require.NoError(t, durable.db.QueryRow(`SELECT session_identity_hash FROM managed_http_operations WHERE operation_id = ?`, reservation.Operation.OperationID).Scan(&scope))
	require.Equal(t, managedWorkerOperationScope, scope)
}

func TestReserveManagedRestartRejectsIneligibleRowsWithoutEffects(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(t *testing.T, durable *Store, agent ManagedAgentRecord, reservation *ManagedRestartReservation)
		conflict bool
	}{
		{name: "non-running lifecycle", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='starting' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "admission closed", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "slot released", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET slot_held=0 WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "waiting activity", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='waiting' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "failed activity", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='failed' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "mismatched fence", mutate: func(_ *testing.T, _ *Store, _ ManagedAgentRecord, reservation *ManagedRestartReservation) {
			reservation.Fence.ProcessIncarnation = "other-incarnation"
		}},
		{name: "existing transition", conflict: true, mutate: func(t *testing.T, durable *Store, agent ManagedAgentRecord, reservation *ManagedRestartReservation) {
			first := *reservation
			first.Operation = reservation.Operation
			first.Operation.OperationID, first.Operation.RequestID = "existing-restart", "existing-restart"
			first.Operation.ResponseBody = []byte(`{}`)
			_, _, err := durable.ReserveManagedRestart(context.Background(), first)
			require.NoError(t, err)
			agentAfter, err := durable.ManagedAgent(context.Background(), agent.AgentID)
			require.NoError(t, err)
			reservation.Fence = ManagedRestartFence{AgentID: agentAfter.AgentID, PiSessionID: agentAfter.PiSessionID, SessionEpoch: agentAfter.SessionEpoch, ProcessPID: agentAfter.ProcessPID, ProcessStartMarker: agentAfter.ProcessStartMarker, ProcessIncarnation: agentAfter.ProcessIncarnation, RuntimeGeneration: agentAfter.RuntimeGeneration, ManifestRevision: agentAfter.ManifestRevision, ManifestHash: agentAfter.ManifestHash}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			durable, ctx, agent, launch := restartFixture(t)
			reservation := restartReservation(agent, launch)
			test.mutate(t, durable, agent, &reservation)
			beforeVersion, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			var operations, transitions int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations`).Scan(&operations))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_restart_transitions`).Scan(&transitions))
			_, _, err = durable.ReserveManagedRestart(ctx, reservation)
			if test.conflict {
				var conflict *ManagedRestartConflictError
				require.ErrorAs(t, err, &conflict)
			} else {
				var notReady *ManagedAgentNotReadyError
				require.ErrorAs(t, err, &notReady)
			}
			afterVersion, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, beforeVersion, afterVersion)
			var afterOperations, afterTransitions int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations`).Scan(&afterOperations))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_restart_transitions`).Scan(&afterTransitions))
			require.Equal(t, operations, afterOperations)
			require.Equal(t, transitions, afterTransitions)
		})
	}
}

func TestReserveManagedRestartRejectsCurrentPendingOperationWithoutEffects(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, err := durable.db.Exec(`UPDATE managed_http_operations SET state='pending', terminal_at=NULL, expires_at=NULL WHERE operation_id=?`, launch.OperationID)
	require.NoError(t, err)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	var conflict *ManagedRestartConflictError
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = durable.db.Exec(`UPDATE managed_http_operations SET state='terminal', terminal_at='2026-01-01T00:00:00.000Z', expires_at='2026-01-02T00:00:00.000Z' WHERE operation_id=?`, launch.OperationID)
	require.NoError(t, err)
	reservation.CurrentLifecycleAction = "terminating"
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.ErrorAs(t, err, &conflict)
	reservation.CurrentLifecycleAction = ""
	_, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	_ = agent
}

func TestRollbackManagedRestartPreparationRestoresReservationAndTerminalizesOperation(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.RollbackManagedRestartPreparation(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_PREPARATION_FAILED", "restart preparation failed"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.LifecycleState, stored.LifecycleState)
	require.Equal(t, agent.ActivityState, stored.ActivityState)
	require.Equal(t, agent.AcceptingPrompts, stored.AcceptingPrompts)
	require.Equal(t, "RESTART_PREPARATION_FAILED", stored.LastErrorCode)
	require.Equal(t, "restart preparation failed", stored.LastErrorMessage)
	op, err := durable.ManagedOperation(ctx, reservation.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	require.NotEmpty(t, op.ExpiresAt)
	_, err = time.Parse("2006-01-02T15:04:05.000Z", op.ExpiresAt)
	require.NoError(t, err)
	_, err = durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.Error(t, err)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	var stale *ManagedRestartStaleError
	require.ErrorAs(t, durable.RollbackManagedRestartPreparation(ctx, reservation.Fence, reservation.Operation.OperationID, "different", "different"), &stale)
	afterStale, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, after, afterStale)
}

func TestMarkManagedRestartTurnSettledRequiresExactOldFence(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	activeFence := reservation.Fence
	// The fixture starts idle. The active-only settlement writer must reject it
	// without changing the transition or public projection.
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.ErrorAs(t, durable.MarkManagedRestartTurnSettled(ctx, activeFence, reservation.Operation.OperationID), new(*ManagedRestartStaleError))
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_ = agent
}

func TestManagedRestartExecutionTransactions(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	reservation := restartReservation(agent, launch)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartSettling(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartTurnSettled(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartStopping(ctx, reservation.Fence, reservation.Operation.OperationID))
	transition, err := durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "stopping", transition.Stage)
	require.True(t, transition.PlannedStop)
	require.NotEmpty(t, transition.TurnSettledAt)
	require.NoError(t, durable.MarkManagedRestartOldStopped(ctx, reservation.Fence, reservation.Operation.OperationID))
	target := reservation.Fence
	target.ProcessPID = 88
	target.ProcessStartMarker = "target-marker"
	target.ProcessIncarnation = "target-incarnation"
	target.RuntimeGeneration = "replacement-generation"
	require.NoError(t, durable.InstallManagedRestartTarget(ctx, reservation.Fence, target, reservation.Operation.OperationID))
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, target, policy))
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, target, ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: target.PiSessionID, Sequence: 0, Canonical: []byte(`{}`), SHA256: testManifestHash}))
	require.NoError(t, durable.CompleteManagedRestartReady(ctx, target, reservation.Operation.OperationID))
	ready, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", ready.LifecycleState)
	require.True(t, ready.AcceptingPrompts)
	require.Equal(t, target.RuntimeGeneration, ready.RuntimeGeneration)
	_, err = durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.Error(t, err)
	op, err := durable.ManagedOperation(ctx, reservation.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
}

func TestRestartAdmissionAndFailureStayDurableAndRedacted(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	updated, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	agent = *updated
	reservation := restartReservation(agent, launch)
	admission, err := durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, reservation.Operation.RequestID, reservation.Operation.CanonicalPath, reservation.Operation.CanonicalBodyHash, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.AgentID, admission.Agent.AgentID)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	_, err = durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, "second", reservation.Operation.CanonicalPath, reservation.Operation.CanonicalBodyHash, agent.AgentID)
	var conflict *ManagedRestartConflictError
	require.ErrorAs(t, err, &conflict)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartSettling(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.RecordManagedRestartFailure(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_SETTLE_FAILED", "restart turn did not settle"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "RESTART_SETTLE_FAILED", stored.LastErrorCode)
	require.Equal(t, "restart turn did not settle", stored.LastErrorMessage)
	require.Error(t, durable.RecordManagedRestartFailure(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_SETTLE_FAILED", "raw stderr token=secret"))
	_, err = durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, reservation.Operation.RequestID, reservation.Operation.CanonicalPath+"/other", reservation.Operation.CanonicalBodyHash, agent.AgentID)
	var idempotency *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &idempotency)
}

func TestManagedLaunchReservesNameSlotAndOperationAtomically(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "123", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, agent, op), &conflict)
	stored, err := durable.ManagedOperation(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, "hash", stored.CanonicalBodyHash)

	second := agent
	second.AgentID, second.Name = "beta", "beta"
	secondOp := op
	secondOp.OperationID, secondOp.RequestID = "456", "456"
	var slot *ManagedSlotLimitError
	require.ErrorAs(t, durable.ReserveManagedLaunchWithLimit(ctx, second, secondOp, 1), &slot)
}

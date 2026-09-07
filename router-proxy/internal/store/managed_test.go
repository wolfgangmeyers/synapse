package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testManifestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

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

func TestForwardMigration022To023PreservesManagedRowsAndIndexes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	require.Greater(t, len(migrations), 22)
	require.Equal(t, "024_managed_readiness_sources", migrations[len(migrations)-1].ID)
	before, err := Open(ctx, path, Options{Migrations: migrations[:len(migrations)-1]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "migration-agent", Name: "migration-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 2, ManifestRevision: 3, ManifestHash: testManifestHash, PendingInitialInstruction: "hello"}
	op := ManagedOperation{OperationID: "migration-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 77, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
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

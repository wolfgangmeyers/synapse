package managed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func prepareFailedRestartTargetRetry(t *testing.T, fixture *restartExecutionFixture, actionID string, targetState ProcessProbeState) (*store.ManagedLifecycleAction, store.ManagedRuntimeFence, store.ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	agent, err := fixture.durable.ManagedAgent(ctx, "restart-agent")
	require.NoError(t, err)
	source := storedAgentFence(agent)
	operation := store.ManagedOperation{OperationID: actionID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: actionID, CanonicalBodyHash: fixture.manifest.CanonicalSHA256, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": actionID}, ResponseBody: []byte(`{"operation_id":"` + actionID + `"}`), CreatedAt: formatManagedTime(time.Now())}
	admission, err := fixture.durable.ReserveManagedLifecycleRestart(ctx, store.ManagedLifecycleRestartReservation{AgentID: source.AgentID, Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: actionID, CanonicalBodyHash: operation.CanonicalBodyHash, Source: "direct", CurrentOperationID: "launch", SourceFence: source, PlannedTargetRuntimeGeneration: "target-generation", Operation: operation})
	require.NoError(t, err)
	artifactID := "123e4567-e89b-42d3-a456-000000000097"
	retainRestartSourceArtifact(t, fixture, artifactID)
	require.NoError(t, fixture.durable.AttachManagedLifecycleRestartResumeArtifact(ctx, admission.Action.ActionID, source, artifactID))
	require.NoError(t, fixture.durable.AdvanceManagedLifecycleRestartToSourceStop(ctx, actionID, source))
	require.NoError(t, fixture.durable.MarkManagedLifecycleRestartSourceStopped(ctx, actionID, source))
	target := fixture.target.identity
	targetFence := source
	targetFence.ProcessPID = target.PID
	targetFence.ProcessStartMarker = target.StartMarker
	targetFence.ProcessIncarnation = target.Incarnation
	targetFence.RuntimeGeneration = "target-generation"
	require.NoError(t, fixture.durable.ClaimManagedLifecycleRestartTargetStart(ctx, actionID, actionID, source, "target-generation"))
	require.NoError(t, fixture.durable.InstallManagedLifecycleRestartTarget(ctx, actionID, actionID, source, targetFence))
	require.NoError(t, fixture.durable.RecordManagedLifecycleRestartFailure(ctx, actionID, "RESTART_TARGET_REAP_FAILED", targetFence))
	fixture.ops.mu.Lock()
	fixture.ops.states[target.PID] = targetState
	fixture.ops.mu.Unlock()
	binding, found := fixture.manager.bindingFor(source.AgentID)
	if found {
		fixture.manager.clearBinding(binding)
	}
	return mustManagedLifecycleAction(t, fixture.durable, actionID), source, targetFence
}

func mustManagedLifecycleAction(t *testing.T, durable *store.Store, actionID string) *store.ManagedLifecycleAction {
	t.Helper()
	action, err := durable.ManagedLifecycleAction(context.Background(), actionID)
	require.NoError(t, err)
	return action
}

func TestRestartRetriesFailedUnboundTargetAfterExactAbsence(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	failed, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000091", ProcessAbsent)
	require.True(t, failed.Retryable)
	retryProcess := &restartExecutionProcess{identity: ProcessIdentity{PID: 7003, StartMarker: "retry-target", Incarnation: "retry-target-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	fixture.factory.mu.Lock()
	fixture.factory.processes[1] = retryProcess
	fixture.factory.rpcs[1] = &restartExecutionRPC{active: expectedManagedActiveTools(fixture.manifest), sessionFile: fixture.root + "/session.json"}
	fixture.ops.process[retryProcess.identity.PID] = retryProcess
	fixture.ops.states[retryProcess.identity.PID] = ProcessOwned
	fixture.factory.mu.Unlock()
	request := RestartRequest{AgentID: source.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "123e4567-e89b-42d3-a456-000000000094", CanonicalBodyHash: fixture.manifest.CanonicalSHA256}
	result, err := fixture.manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Eventually(t, func() bool {
		action, readErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
		return readErr == nil && action.State == "succeeded"
	}, time.Second, time.Millisecond)
	retry := mustManagedLifecycleAction(t, fixture.durable, request.RequestID)
	require.Equal(t, failed.ActionID, retry.RetryOf)
	agent, err := fixture.durable.ManagedAgent(context.Background(), source.AgentID)
	require.NoError(t, err)
	require.Equal(t, source.PiSessionID, agent.PiSessionID)
	require.Equal(t, source.SessionEpoch, agent.SessionEpoch)
	require.Equal(t, "target-generation", agent.RuntimeGeneration)
	fixture.factory.mu.Lock()
	require.Len(t, fixture.factory.specs, 2)
	require.Equal(t, ResumeExisting, fixture.factory.specs[1].LaunchMode)
	require.Equal(t, source.PiSessionID, fixture.factory.specs[1].SessionID)
	fixture.factory.mu.Unlock()
	require.Equal(t, ProcessAbsent, fixture.ops.Probe(ProcessIdentity{PID: target.ProcessPID, StartMarker: target.ProcessStartMarker, Incarnation: target.ProcessIncarnation}).State)
	replayed, err := fixture.manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	fixture.factory.mu.Lock()
	require.Len(t, fixture.factory.specs, 2)
	fixture.factory.mu.Unlock()
}

func TestRestartRetriesFailedUnboundTargetReapsOnlyExactOwnedGroup(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	_, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000092", ProcessOwned)
	retryProcess := &restartExecutionProcess{identity: ProcessIdentity{PID: 7003, StartMarker: "retry-target", Incarnation: "retry-target-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	fixture.factory.mu.Lock()
	fixture.factory.processes[1] = retryProcess
	fixture.factory.rpcs[1] = &restartExecutionRPC{active: expectedManagedActiveTools(fixture.manifest), sessionFile: fixture.root + "/session.json"}
	fixture.ops.process[retryProcess.identity.PID] = retryProcess
	fixture.ops.states[retryProcess.identity.PID] = ProcessOwned
	fixture.factory.mu.Unlock()
	_, err := fixture.manager.ReserveRestart(context.Background(), RestartRequest{AgentID: source.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "123e4567-e89b-42d3-a456-000000000095", CanonicalBodyHash: fixture.manifest.CanonicalSHA256})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, readErr := fixture.durable.ManagedLifecycleAction(context.Background(), "123e4567-e89b-42d3-a456-000000000095")
		return readErr == nil && action.State == "succeeded"
	}, time.Second, time.Millisecond)
	fixture.ops.mu.Lock()
	require.Contains(t, fixture.ops.signalIdentities, ProcessIdentity{PID: target.ProcessPID, StartMarker: target.ProcessStartMarker, Incarnation: target.ProcessIncarnation})
	fixture.ops.mu.Unlock()
}

func TestTerminateEscapesFailedRestartTargetOnlyAfterSafeAbsence(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessAbsent, ProcessOwned, ProcessPresent, ProcessMismatch, ProcessUnknown} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newRestartExecutionFixture(t, time.Millisecond)
			_, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000097", state)
			request := TerminateRequest{AgentID: source.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/terminate", RequestID: "123e4567-e89b-42d3-a456-000000000098", CanonicalBodyHash: fixture.manifest.CanonicalSHA256}
			result, err := fixture.manager.Terminate(context.Background(), request)
			if state == ProcessPresent || state == ProcessMismatch || state == ProcessUnknown {
				var conflict *store.ManagedLifecycleConflictError
				require.ErrorAs(t, err, &conflict)
				fixture.factory.mu.Lock()
				require.Len(t, fixture.factory.specs, 1)
				fixture.factory.mu.Unlock()
				fixture.ops.mu.Lock()
				require.NotContains(t, fixture.ops.signalIdentities, target)
				fixture.ops.mu.Unlock()
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result.Operation)
			require.Eventually(t, func() bool {
				action, readErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
				return readErr == nil && action.State == "succeeded"
			}, time.Second, time.Millisecond)
			action := mustManagedLifecycleAction(t, fixture.durable, request.RequestID)
			require.Nil(t, action.SourceFence, "termination after failed target cleanup must not carry the old source")
		})
	}
}

func prepareFailedRootRecoveryOperatorTest(t *testing.T, state ProcessProbeState) (*Manager, *store.Store, store.ManagedAgentRecord, *coldRecoveryOps) {
	t.Helper()
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: state, 902: ProcessOwned}}
	manager, durable, agent, tracked, _ := setupColdRecovery(t, nil, nil, ops, nil, nil)
	stored, err := durable.ManagedAgent(context.Background(), agent.AgentID)
	require.NoError(t, err)
	require.NotNil(t, stored.LaunchRecipe)
	action, _, err := durable.ClaimManagedRootRecovery(context.Background(), agent.AgentID, storedAgentFence(stored), stored.LaunchRecipe.SHA256, "failed-target-generation")
	require.NoError(t, err)
	require.NoError(t, durable.FailManagedRootRecovery(context.Background(), action.ActionID, "ORPHAN_IDENTITY_UNSAFE", "retained process identity was unsafe"))
	require.NoError(t, durable.MarkManagedFailedForFence(context.Background(), storedAgentFence(stored), "ORPHAN_IDENTITY_UNSAFE", "retained process identity was unsafe"))
	return manager, durable, agent, tracked
}

func TestFailedRootRecoveryRetryReservesExactVersionAndCanonicalRequestIdempotently(t *testing.T) {
	manager, durable, agent, _ := prepareFailedRootRecoveryOperatorTest(t, ProcessAbsent)
	request := RestartRequest{AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agent.AgentID + "/restart", RequestID: "123e4567-e89b-42d3-a456-000000000101", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	first, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	require.Eventually(t, func() bool {
		op, readErr := durable.FindManagedOperation(context.Background(), request.Method, request.RequestID)
		current, agentErr := durable.ManagedAgent(context.Background(), agent.AgentID)
		return readErr == nil && op.State == "terminal" && agentErr == nil && current.LifecycleState == "running" && current.AcceptingPrompts
	}, time.Second, time.Millisecond)

	replayed, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	conflict := request
	conflict.CanonicalBodyHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = manager.ReserveRestart(context.Background(), conflict)
	var idempotency *store.ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &idempotency)
}

func TestFailedRootRecoveryRetryAdoptsCurrentRecipeOnlyAfterAbsenceOrExactOwnership(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessAbsent, ProcessOwned, ProcessMismatch, ProcessUnknown, ProcessPresent} {
		t.Run(string(state), func(t *testing.T) {
			manager, durable, agent, tracked := prepareFailedRootRecoveryOperatorTest(t, state)
			manager.config.TestOnlyExtensions = []string{"/current/operator-retry.ts"}
			request := RestartRequest{AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agent.AgentID + "/restart", RequestID: "123e4567-e89b-42d3-a456-000000000102", CanonicalBodyHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
			_, err := manager.ReserveRestart(context.Background(), request)
			if state == ProcessMismatch || state == ProcessUnknown || state == ProcessPresent {
				require.Error(t, err)
				tracked.mu.Lock()
				require.Empty(t, tracked.signals)
				tracked.mu.Unlock()
				stored, readErr := durable.ManagedAgent(context.Background(), agent.AgentID)
				require.NoError(t, readErr)
				require.Equal(t, "failed", stored.LifecycleState)
				return
			}
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				stored, readErr := durable.ManagedAgent(context.Background(), agent.AgentID)
				return readErr == nil && stored.LifecycleState == "running"
			}, time.Second, time.Millisecond)
			recipe, readErr := durable.ManagedLaunchRecipe(context.Background(), agent.AgentID)
			require.NoError(t, readErr)
			require.Equal(t, []string{"/current/operator-retry.ts"}, recipe.TestOnlyExtensions)
		})
	}
}

func TestFailedRootRecoveryReservationResumesAfterOwnerLoss(t *testing.T) {
	manager, durable, agent, _ := prepareFailedRootRecoveryOperatorTest(t, ProcessAbsent)
	failed, err := durable.LatestRetryableManagedRootRecovery(context.Background(), agent.AgentID)
	require.NoError(t, err)
	requestID := "123e4567-e89b-42d3-a456-000000000104"
	operation := store.ManagedOperation{OperationID: requestID, Method: "POST", CanonicalPath: "/v1/managed-agents/" + agent.AgentID + "/restart", RequestID: requestID, CanonicalBodyHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, err = durable.ReserveManagedRootRecoveryOperator(context.Background(), store.ManagedRootRecoveryOperatorReservation{ActionID: failed.ActionID, ExpectedVersion: failed.Version, Kind: "retry", Owner: "lost-router-owner", Operation: operation})
	require.NoError(t, err)

	require.NoError(t, manager.RecoverManagedRootRecoveryOperators(context.Background()))
	require.Eventually(t, func() bool {
		op, operationErr := durable.FindManagedOperation(context.Background(), operation.Method, requestID)
		current, agentErr := durable.ManagedAgent(context.Background(), agent.AgentID)
		return operationErr == nil && op.State == "terminal" && agentErr == nil && current.LifecycleState == "running"
	}, time.Second, time.Millisecond)
	resumed, err := durable.ManagedRootRecoveryActionByID(context.Background(), failed.ActionID)
	require.NoError(t, err)
	require.Equal(t, "terminal", resumed.OperatorStage)
	require.Greater(t, resumed.OperatorOwnerEpoch, 1)
}

func TestFailedRootRecoveryTerminationGatesAliasUntilCleanupCompletes(t *testing.T) {
	manager, durable, agent, _ := prepareFailedRootRecoveryOperatorTest(t, ProcessMismatch)
	failed, err := durable.LatestRetryableManagedRootRecovery(context.Background(), agent.AgentID)
	require.NoError(t, err)
	requestID := "123e4567-e89b-42d3-a456-000000000105"
	operation := store.ManagedOperation{OperationID: requestID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + agent.AgentID, RequestID: requestID, CanonicalBodyHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, err = durable.ReserveManagedRootRecoveryOperator(context.Background(), store.ManagedRootRecoveryOperatorReservation{ActionID: failed.ActionID, ExpectedVersion: failed.Version, Kind: "terminate", Owner: "lost-router-owner", Operation: operation})
	require.NoError(t, err)

	_, err = manager.Launch(context.Background(), LaunchRequest{Name: agent.Name, Cwd: agent.Cwd, RequestID: "123e4567-e89b-42d3-a456-000000000106"})
	var nameConflict *store.ManagedNameConflictError
	require.ErrorAs(t, err, &nameConflict)
	require.NoError(t, manager.RecoverManagedRootRecoveryOperators(context.Background()))
	_, err = durable.ManagedAgent(context.Background(), agent.AgentID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = manager.Launch(context.Background(), LaunchRequest{Name: agent.Name, Cwd: agent.Cwd, RequestID: "123e4567-e89b-42d3-a456-000000000107"})
	require.NoError(t, err)
}

func TestRestartFailedUnboundRetryFailsClosedForMismatchAndUnknown(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessMismatch, ProcessUnknown} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newRestartExecutionFixture(t, time.Millisecond)
			_, source, _ := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000093", state)
			beforeSignals := len(fixture.ops.signals)
			_, err := fixture.manager.ReserveRestart(context.Background(), RestartRequest{AgentID: source.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "123e4567-e89b-42d3-a456-000000000096", CanonicalBodyHash: fixture.manifest.CanonicalSHA256})
			require.Error(t, err)
			fixture.factory.mu.Lock()
			require.Len(t, fixture.factory.specs, 1)
			fixture.factory.mu.Unlock()
			fixture.ops.mu.Lock()
			require.Len(t, fixture.ops.signals, beforeSignals)
			fixture.ops.mu.Unlock()
		})
	}
}

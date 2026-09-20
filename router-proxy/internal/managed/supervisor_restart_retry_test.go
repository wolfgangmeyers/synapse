package managed

import (
	"context"
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

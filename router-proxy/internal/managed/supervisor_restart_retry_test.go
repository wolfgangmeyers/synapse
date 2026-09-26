package managed

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
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

func TestTerminateFailedRestartSignalsOnlyExactOwnedTargetAndAlwaysDeletes(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessAbsent, ProcessOwned, ProcessPresent, ProcessMismatch, ProcessUnknown} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newFailedRestartTerminationFixture(t)
			_, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000097", state)
			request := TerminateRequest{AgentID: source.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restart-agent", RequestID: "123e4567-e89b-42d3-a456-000000000098", CanonicalBodyHash: fixture.manifest.CanonicalSHA256}
			result, err := fixture.manager.Terminate(context.Background(), request)
			require.NoError(t, err)
			require.NotNil(t, result.Operation)
			require.Eventually(t, func() bool {
				action, readErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
				_, agentErr := fixture.durable.ManagedAgent(context.Background(), request.AgentID)
				return readErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
			}, 10*time.Second, time.Millisecond)
			fixture.ops.mu.Lock()
			signals := 0
			for _, identity := range fixture.ops.signalIdentities {
				if identity == oldFenceIdentity(target) {
					signals++
				}
			}
			fixture.ops.mu.Unlock()
			if state == ProcessOwned {
				require.Equal(t, 1, signals)
			} else {
				require.Zero(t, signals)
			}
		})
	}
}

func newFailedRestartTerminationFixture(t *testing.T) *restartExecutionFixture {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveTools(manifest)
	old := &restartExecutionProcess{identity: ProcessIdentity{PID: 7001, StartMarker: "old", Incarnation: "old-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	target := &restartExecutionProcess{identity: ProcessIdentity{PID: 7002, StartMarker: "target", Incarnation: "target-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	oldRPC := &restartExecutionRPC{active: active, settled: make(chan struct{})}
	targetRPC := &restartExecutionRPC{active: active}
	factory := &restartExecutionFactory{processes: []*restartExecutionProcess{old, target}, rpcs: []*restartExecutionRPC{oldRPC, targetRPC}}
	ops := &restartExecutionOps{states: map[int]ProcessProbeState{7001: ProcessOwned, 7002: ProcessOwned}, process: map[int]*restartExecutionProcess{7001: old, 7002: target}}
	root := t.TempDir()
	sessionFile := filepath.Join(root, hashString("launch")+".jsonl")
	require.NoError(t, os.WriteFile(filepath.Join(root, "session.json"), []byte("{}"), 0o600))
	oldRPC.sessionFile, targetRPC.sessionFile = sessionFile, sessionFile
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{SessionRoot: root, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: &restartExecutionRPCFactory{factory: factory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), IDSource: deterministic.NewSequenceIDSource("session", "old-generation", "target-generation", "123e4567-e89b-42d3-a456-000000000004"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Close(closeCtx)
	})
	_, err = manager.Launch(ctx, LaunchRequest{Name: "restart-agent", Cwd: root, InitialInstruction: "one-time", RequestID: "launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, agentErr := durable.ManagedAgent(ctx, "restart-agent")
		operation, operationErr := durable.ManagedOperation(ctx, "launch")
		return agentErr == nil && operationErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && operation.State == "terminal"
	}, time.Second, time.Millisecond)
	return &restartExecutionFixture{manager: manager, durable: durable, old: old, target: target, oldRPC: oldRPC, targetRPC: targetRPC, ops: ops, factory: factory, manifest: manifest, root: root, database: databasePath}
}

func TestTerminateOwnsExactFailedRestartBindingAtMostOnceWithoutRewritingEvidence(t *testing.T) {
	fixture := newFailedRestartTerminationFixture(t)
	failed, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000109", ProcessOwned)
	stale := &managedBinding{
		agentID: source.AgentID, lane: fixture.manager.laneFor(source.AgentID), process: fixture.target, rpc: fixture.targetRPC,
		operationID: failed.ActionID, lifecycleAction: "restart", identity: oldFenceIdentity(target), sessionID: target.PiSessionID,
		sessionEpoch: target.SessionEpoch, runtimeGeneration: target.RuntimeGeneration, manifestRevision: target.ManifestRevision, manifestHash: target.ManifestHash,
	}
	require.True(t, fixture.manager.installBinding(stale))

	beforeAction := *failed
	beforeOperation, err := fixture.durable.ManagedOperation(context.Background(), failed.OperationID)
	require.NoError(t, err)
	beforeArtifacts, err := fixture.durable.ManagedSessionArtifacts(context.Background(), source.AgentID)
	require.NoError(t, err)
	cleanupEntered := make(chan struct{})
	cleanupRelease := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-cleanupRelease:
		default:
			close(cleanupRelease)
		}
	})
	fixture.manager.beforeTerminationArtifactCleanup = func(context.Context, string) error {
		close(cleanupEntered)
		<-cleanupRelease
		return nil
	}

	request := TerminateRequest{AgentID: source.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restart-agent", RequestID: "123e4567-e89b-42d3-a456-000000000110", CanonicalBodyHash: fixture.manifest.CanonicalSHA256}
	result, err := fixture.manager.Terminate(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	select {
	case <-cleanupEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("termination did not reach artifact cleanup")
	}

	_, bound := fixture.manager.bindingFor(source.AgentID)
	require.False(t, bound, "the exact reaped failed-restart binding is cleared")
	replay, err := fixture.manager.Terminate(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, result.Operation.ResponseBody, replay.Operation.ResponseBody)
	fixture.targetRPC.mu.Lock()
	require.Equal(t, 1, fixture.targetRPC.abortCalls)
	fixture.targetRPC.mu.Unlock()
	fixture.ops.mu.Lock()
	targetSignals := 0
	for _, identity := range fixture.ops.signalIdentities {
		if identity == oldFenceIdentity(target) {
			targetSignals++
		}
	}
	fixture.ops.mu.Unlock()
	require.Equal(t, 1, targetSignals)
	termination := mustManagedLifecycleAction(t, fixture.durable, request.RequestID)
	require.NotNil(t, termination.SourceFence)
	require.Equal(t, target, *termination.SourceFence)
	preservedAction := mustManagedLifecycleAction(t, fixture.durable, failed.ActionID)
	require.Equal(t, beforeAction, *preservedAction)
	preservedOperation, err := fixture.durable.ManagedOperation(context.Background(), failed.OperationID)
	require.NoError(t, err)
	require.Equal(t, beforeOperation, preservedOperation)
	preservedArtifacts, err := fixture.durable.ManagedSessionArtifacts(context.Background(), source.AgentID)
	require.NoError(t, err)
	require.Equal(t, beforeArtifacts, preservedArtifacts)

	close(cleanupRelease)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
		_, agentErr := fixture.durable.ManagedAgent(context.Background(), source.AgentID)
		if actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
	agent, agentErr := fixture.durable.ManagedAgent(context.Background(), source.AgentID)
	t.Fatalf("termination did not converge: action=%+v actionErr=%v agent=%+v agentErr=%v", action, actionErr, agent, agentErr)
}

func TestTerminateDeletesWithNonmatchingFailedRestartBindingWithoutSignaling(t *testing.T) {
	for _, test := range []struct {
		name       string
		bindingFor func(*restartExecutionFixture, store.ManagedRuntimeFence, store.ManagedRuntimeFence) *managedBinding
	}{
		{
			name: "replacement generation",
			bindingFor: func(fixture *restartExecutionFixture, source, _ store.ManagedRuntimeFence) *managedBinding {
				identity := ProcessIdentity{PID: 7004, StartMarker: "replacement-marker", Incarnation: "replacement-incarnation"}
				process := &restartExecutionProcess{identity: identity, input: &restartExecutionInput{}, done: make(chan struct{})}
				fixture.ops.mu.Lock()
				fixture.ops.process[identity.PID] = process
				fixture.ops.states[identity.PID] = ProcessOwned
				fixture.ops.mu.Unlock()
				return &managedBinding{
					agentID: source.AgentID, lane: fixture.manager.laneFor(source.AgentID), process: process, rpc: &restartExecutionRPC{},
					operationID: "replacement-operation", lifecycleAction: "restart", identity: identity, sessionID: source.PiSessionID,
					sessionEpoch: source.SessionEpoch, runtimeGeneration: "replacement-generation", manifestRevision: source.ManifestRevision, manifestHash: source.ManifestHash,
				}
			},
		},
		{
			name: "different action on same fence",
			bindingFor: func(fixture *restartExecutionFixture, source, target store.ManagedRuntimeFence) *managedBinding {
				return &managedBinding{
					agentID: source.AgentID, lane: fixture.manager.laneFor(source.AgentID), process: fixture.target, rpc: fixture.targetRPC,
					operationID: "newer-lifecycle-operation", lifecycleAction: "restart", identity: oldFenceIdentity(target), sessionID: target.PiSessionID,
					sessionEpoch: target.SessionEpoch, runtimeGeneration: target.RuntimeGeneration, manifestRevision: target.ManifestRevision, manifestHash: target.ManifestHash,
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFailedRestartTerminationFixture(t)
			_, source, target := prepareFailedRestartTargetRetry(t, fixture, "123e4567-e89b-42d3-a456-000000000111", ProcessAbsent)
			replacement := test.bindingFor(fixture, source, target)
			require.True(t, fixture.manager.installBinding(replacement))

			request := TerminateRequest{AgentID: source.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/restart-agent", RequestID: "123e4567-e89b-42d3-a456-000000000112", CanonicalBodyHash: fixture.manifest.CanonicalSHA256}
			_, err := fixture.manager.Terminate(context.Background(), request)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				action, actionErr := fixture.durable.ManagedLifecycleAction(context.Background(), request.RequestID)
				_, agentErr := fixture.durable.ManagedAgent(context.Background(), request.AgentID)
				return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
			}, 10*time.Second, time.Millisecond)
			fixture.ops.mu.Lock()
			require.Empty(t, fixture.ops.signalIdentities)
			fixture.ops.mu.Unlock()
			fixture.targetRPC.mu.Lock()
			require.Zero(t, fixture.targetRPC.abortCalls)
			fixture.targetRPC.mu.Unlock()
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

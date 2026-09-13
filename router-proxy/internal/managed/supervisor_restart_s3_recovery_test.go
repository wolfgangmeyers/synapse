package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func prepareLifecycleRestartRecoveryAction(t *testing.T, fixture *restartExecutionFixture, actionID string, marked bool) (*store.ManagedLifecycleAction, store.ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	agent, err := fixture.durable.ManagedAgent(ctx, "restart-agent")
	require.NoError(t, err)
	source := storedAgentFence(agent)
	operation := store.ManagedOperation{OperationID: actionID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: actionID, CanonicalBodyHash: fixture.manifest.CanonicalSHA256, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": actionID}, ResponseBody: []byte(`{"operation_id":"` + actionID + `"}`)}
	admission, err := fixture.durable.ReserveManagedLifecycleRestart(ctx, store.ManagedLifecycleRestartReservation{AgentID: source.AgentID, Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: actionID, CanonicalBodyHash: operation.CanonicalBodyHash, Source: "direct", CurrentOperationID: "launch", SourceFence: source, PlannedTargetRuntimeGeneration: "target-generation", Operation: operation})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(fixture.root, "session.json"))
	require.NoError(t, err)
	device, inode, err := deviceInode(info)
	require.NoError(t, err)
	artifact := store.ManagedSessionArtifact{ArtifactID: "123e4567-e89b-42d3-a456-000000000099", AgentID: source.AgentID, PiSessionID: source.PiSessionID, SessionEpoch: source.SessionEpoch, RootRelativePath: "session.json", PathSHA256: artifactPathHash("session.json"), DiscoveryState: "materialized", FileDevice: device, FileInode: inode, CleanupState: "retained", DiscoveredAt: formatManagedTime(time.Now()), CleanupUpdatedAt: formatManagedTime(time.Now())}
	require.NoError(t, fixture.durable.RecordManagedSessionArtifact(ctx, artifact))
	require.NoError(t, fixture.durable.AttachManagedLifecycleRestartResumeArtifact(ctx, actionID, source, artifact.ArtifactID))
	require.NoError(t, fixture.durable.AdvanceManagedLifecycleRestartToSourceStop(ctx, actionID, source))
	require.NoError(t, fixture.durable.MarkManagedLifecycleRestartSourceStopped(ctx, actionID, source))
	fixture.ops.mu.Lock()
	fixture.ops.states[fixture.old.identity.PID] = ProcessAbsent
	fixture.ops.mu.Unlock()
	if marked {
		require.NoError(t, fixture.durable.ClaimManagedLifecycleRestartTargetStart(ctx, actionID, actionID, source, "target-generation"))
	}
	action, err := fixture.durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	return action, source
}

// prepareLifecycleRestartRecoverySource leaves a retained restart action at
// admission with direct parent work still owned by its exact live source.
func prepareLifecycleRestartRecoverySource(t *testing.T, fixture *restartExecutionFixture, actionID string) (*store.ManagedLifecycleAction, store.ManagedRuntimeFence, store.ManagedDirectAttempt) {
	t.Helper()
	ctx := context.Background()
	agent, err := fixture.durable.ManagedAgent(ctx, "restart-agent")
	require.NoError(t, err)
	source := storedAgentFence(agent)
	attempt := queueManagedParentFinalForTest(t, fixture.durable, source, actionID+"-source-turn")
	operation := store.ManagedOperation{OperationID: actionID, Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: actionID, CanonicalBodyHash: fixture.manifest.CanonicalSHA256, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": actionID}, ResponseBody: []byte(`{"operation_id":"` + actionID + `"}`)}
	admission, err := fixture.durable.ReserveManagedLifecycleRestart(ctx, store.ManagedLifecycleRestartReservation{AgentID: source.AgentID, Method: operation.Method, CanonicalPath: operation.CanonicalPath, RequestID: actionID, CanonicalBodyHash: operation.CanonicalBodyHash, Source: "direct", CurrentOperationID: "launch", SourceFence: source, PlannedTargetRuntimeGeneration: "target-generation", Operation: operation})
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(fixture.root, "session.json"))
	require.NoError(t, err)
	device, inode, err := deviceInode(info)
	require.NoError(t, err)
	artifact := store.ManagedSessionArtifact{ArtifactID: "123e4567-e89b-42d3-a456-000000000098", AgentID: source.AgentID, PiSessionID: source.PiSessionID, SessionEpoch: source.SessionEpoch, RootRelativePath: "session.json", PathSHA256: artifactPathHash("session.json"), DiscoveryState: "materialized", FileDevice: device, FileInode: inode, CleanupState: "retained", DiscoveredAt: formatManagedTime(time.Now()), CleanupUpdatedAt: formatManagedTime(time.Now())}
	require.NoError(t, fixture.durable.RecordManagedSessionArtifact(ctx, artifact))
	require.NoError(t, fixture.durable.AttachManagedLifecycleRestartResumeArtifact(ctx, actionID, source, artifact.ArtifactID))
	action, err := fixture.durable.ManagedLifecycleAction(ctx, admission.Action.ActionID)
	require.NoError(t, err)
	return action, source, attempt
}

func recoveredLifecycleRestartManager(t *testing.T, fixture *restartExecutionFixture) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerOptions{Store: fixture.durable, Config: Config{SessionRoot: fixture.root, AllowedWorkingDirectoryRoots: []string{fixture.root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: fixture.manifest, ProcessFactory: fixture.factory, RPCFactory: &restartExecutionRPCFactory{factory: fixture.factory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource(), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: fixture.ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

func TestLifecycleRestartRecoveryInterruptsParentWorkAtEverySourceStopEntry(t *testing.T) {
	testCases := []struct {
		name       string
		process    ProcessProbeState
		sourceStop bool
		idle       bool
	}{
		{name: "admission after process loss", process: ProcessAbsent},
		{name: "admission idle owned process", process: ProcessOwned, idle: true},
		{name: "source stop after process loss", process: ProcessAbsent, sourceStop: true},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRestartExecutionFixture(t, time.Millisecond)
			actionID := "source-stop-" + string(rune('a'+index))
			action, source, attempt := prepareLifecycleRestartRecoverySource(t, fixture, actionID)
			if testCase.sourceStop {
				require.NoError(t, fixture.durable.AdvanceManagedLifecycleRestartToSourceStop(context.Background(), action.ActionID, source))
			}
			if testCase.idle {
				db, err := sql.Open("sqlite", fixture.database)
				require.NoError(t, err)
				_, err = db.Exec(`UPDATE managed_agents SET activity_state='idle' WHERE agent_id='restart-agent'`)
				require.NoError(t, err)
				require.NoError(t, db.Close())
			}
			fixture.ops.mu.Lock()
			fixture.ops.states[fixture.old.identity.PID] = testCase.process
			fixture.ops.mu.Unlock()
			recovered := recoveredLifecycleRestartManager(t, fixture)
			require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
			require.Eventually(t, func() bool {
				stored, err := fixture.durable.ManagedLifecycleAction(context.Background(), action.ActionID)
				return err == nil && stored.State == "succeeded"
			}, time.Second, time.Millisecond)
			requireManagedParentTerminalForTest(t, fixture.database, attempt, "interrupted", "interrupted")
		})
	}
}

func TestLifecycleRestartExitInterruptsParentWorkIdempotently(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	action, source, attempt := prepareLifecycleRestartRecoverySource(t, fixture, "restart-exit-source")
	binding, ok := fixture.manager.bindingFor(source.AgentID)
	require.True(t, ok)
	binding.serial.Lock()
	binding.operationID, binding.lifecycleAction = action.ActionID, "restart"
	binding.serial.Unlock()
	fixture.ops.mu.Lock()
	fixture.ops.states[fixture.old.identity.PID] = ProcessAbsent
	fixture.ops.mu.Unlock()
	require.True(t, fixture.manager.recordLifecycleRestartExit(binding))
	require.Eventually(t, func() bool {
		db, err := sql.Open("sqlite", fixture.database)
		if err != nil {
			return false
		}
		defer db.Close()
		var state string
		err = db.QueryRow(`SELECT state FROM managed_parent_work WHERE message_id=? AND message_incarnation_id=? AND attempt_number=?`, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber).Scan(&state)
		return err == nil && state == "interrupted"
	}, time.Second, time.Millisecond)
	requireManagedParentTerminalForTest(t, fixture.database, attempt, "interrupted", "interrupted")
	require.True(t, fixture.manager.recordLifecycleRestartExit(binding))
	requireManagedParentTerminalForTest(t, fixture.database, attempt, "interrupted", "interrupted")
}

func TestLifecycleRestartRecoveryPrebindMarkerFailsClosed(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	action, _ := prepareLifecycleRestartRecoveryAction(t, fixture, "prebind-restart", true)
	recovered := recoveredLifecycleRestartManager(t, fixture)
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	stored, err := fixture.durable.ManagedLifecycleAction(context.Background(), action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.State)
	require.Equal(t, "RESTART_IDENTITY_UNSAFE", stored.TerminalCode)
	fixture.factory.mu.Lock()
	starts := fixture.factory.count
	fixture.factory.mu.Unlock()
	require.Equal(t, 1, starts, "recovery must not create a second pre-bind target")
	fixture.ops.mu.Lock()
	signals := append([]syscall.Signal(nil), fixture.ops.signals...)
	fixture.ops.mu.Unlock()
	require.Empty(t, signals, "a cold pre-bind marker has no durable target identity to signal")
}

func TestLifecycleRestartRecoveryUnmarkedTargetStartsOnce(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	action, _ := prepareLifecycleRestartRecoveryAction(t, fixture, "unmarked-restart", false)
	recovered := recoveredLifecycleRestartManager(t, fixture)
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	require.Eventually(t, func() bool {
		stored, err := fixture.durable.ManagedLifecycleAction(context.Background(), action.ActionID)
		return err == nil && stored.State == "succeeded"
	}, time.Second, time.Millisecond)
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	fixture.factory.mu.Lock()
	starts := fixture.factory.count
	fixture.factory.mu.Unlock()
	require.Equal(t, 2, starts, "unmarked recovery starts exactly one retained target")
}

package managed

import (
	"context"
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

func recoveredLifecycleRestartManager(t *testing.T, fixture *restartExecutionFixture) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerOptions{Store: fixture.durable, Config: Config{SessionRoot: fixture.root, AllowedWorkingDirectoryRoots: []string{fixture.root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: fixture.manifest, ProcessFactory: fixture.factory, RPCFactory: &restartExecutionRPCFactory{factory: fixture.factory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource(), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: fixture.ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
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

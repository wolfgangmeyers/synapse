package managed

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type composedLifecycleRPC struct {
	restartExecutionRPC
	abortCalls int
}

func (r *composedLifecycleRPC) Abort(ctx context.Context) error {
	r.abortCalls++
	return r.restartExecutionRPC.Abort(ctx)
}

type composedLifecycleFactory struct {
	mu        sync.Mutex
	processes []*restartExecutionProcess
	rpcs      []*composedLifecycleRPC
	specs     []CommandSpec
	starts    int
	opens     int
}

func (f *composedLifecycleFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	process := f.processes[f.starts]
	f.rpcs[f.starts].session = spec.SessionID
	f.specs = append(f.specs, spec)
	f.starts++
	return process, nil
}

func (f *composedLifecycleFactory) Open(context.Context, Process) (RPC, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rpc := f.rpcs[f.opens]
	f.opens++
	return rpc, nil
}

func (f *composedLifecycleFactory) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// TestManagedLifecycleComposedBehavioralGate drives one agent through the
// owned Manager, SQLite ledger, and deterministic process/RPC boundary. The
// barriers make each ownership handoff observable without sleeping.
func TestManagedLifecycleComposedBehavioralGate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sourceFile := filepath.Join(sessionRoot, "source.json")
	freshFile := filepath.Join(sessionRoot, "fresh.json")
	require.NoError(t, os.WriteFile(sourceFile, []byte("source-history"), 0o600))
	require.NoError(t, os.WriteFile(freshFile, []byte("fresh-history"), 0o600))
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveTools(manifest)

	const sourceSession = "123e4567-e89b-42d3-a456-426614174001"
	const sourceGeneration = "123e4567-e89b-42d3-a456-426614174002"
	const sourceArtifact = "123e4567-e89b-42d3-a456-426614174003"
	const restartGeneration = "123e4567-e89b-42d3-a456-426614174004"
	const resetSession = "123e4567-e89b-42d3-a456-426614174005"
	const resetGeneration = "123e4567-e89b-42d3-a456-426614174006"
	const resetArtifact = "123e4567-e89b-42d3-a456-426614174007"

	processes := []*restartExecutionProcess{
		{identity: ProcessIdentity{PID: 8101, StartMarker: sourceSession, Incarnation: "source-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{}), waitEntered: make(chan struct{}), waitRelease: make(chan struct{}), waitReturned: make(chan struct{})},
		{identity: ProcessIdentity{PID: 8102, StartMarker: "restart-marker", Incarnation: "restart-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{}), waitEntered: make(chan struct{}), waitRelease: make(chan struct{}), waitReturned: make(chan struct{})},
		{identity: ProcessIdentity{PID: 8103, StartMarker: "reset-marker", Incarnation: "reset-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{}), waitEntered: make(chan struct{}), waitRelease: make(chan struct{}), waitReturned: make(chan struct{})},
	}
	rpcs := []*composedLifecycleRPC{
		{restartExecutionRPC: restartExecutionRPC{active: active, sessionFile: sourceFile, settled: make(chan struct{})}},
		{restartExecutionRPC: restartExecutionRPC{active: active, sessionFile: sourceFile, settled: make(chan struct{})}},
		{restartExecutionRPC: restartExecutionRPC{active: active, sessionFile: freshFile}},
	}
	factory := &composedLifecycleFactory{processes: processes, rpcs: rpcs}
	ops := &restartExecutionOps{
		states:  map[int]ProcessProbeState{8101: ProcessOwned, 8102: ProcessOwned, 8103: ProcessOwned},
		process: map[int]*restartExecutionProcess{8101: processes[0], 8102: processes[1], 8103: processes[2]},
	}
	ids := deterministic.NewSequenceIDSource(sourceSession, sourceGeneration, restartGeneration, sourceArtifact, resetSession, resetGeneration, resetArtifact)
	var cleanupFault atomic.Bool
	cleanupFault.Store(true)
	resetCleanupEntered := make(chan struct{}, 1)
	resetCleanupRelease := make(chan struct{})
	manager, err := NewManager(ManagerOptions{
		Store:    durable,
		Config:   Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}},
		Manifest: manifest, ProcessFactory: factory, RPCFactory: factory,
		CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: ids,
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
		BeforeManagedResetOldArtifactCleanup: func(_ context.Context, _ string) error {
			resetCleanupEntered <- struct{}{}
			<-resetCleanupRelease
			return nil
		},
		BeforeManagedTerminationArtifactCleanup: func(context.Context, string) error {
			if cleanupFault.Load() {
				return errors.New("injected cleanup fault")
			}
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "composed-agent", Cwd: root, InitialInstruction: "initial prompt", RequestID: "123e4567-e89b-42d3-a456-426614174010"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "composed-agent")
		operation, operationErr := durable.ManagedOperation(ctx, "123e4567-e89b-42d3-a456-426614174010")
		return readErr == nil && operationErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && operation.State == "terminal"
	}, time.Second, time.Millisecond)
	beforeRestart, err := durable.ManagedAgent(ctx, "composed-agent")
	require.NoError(t, err)
	oldFence := storedAgentFence(beforeRestart)
	oldBinding, ok := manager.bindingFor("composed-agent")
	require.True(t, ok)
	sourceAttempt := queueManagedParentFinalForTest(t, durable, oldFence, "composed-source-turn")
	setManagedActivityActive(t, databasePath, "composed-agent")

	firstRestart, err := manager.ReserveRestart(ctx, RestartRequest{AgentID: "composed-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/composed-agent/restart", RequestID: "123e4567-e89b-42d3-a456-426614174011", CanonicalBodyHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.NotNil(t, firstRestart.Operation)
	awaitPlannedExitCallback(t, processes[0].waitEntered)
	restartPolicy := snapshotCrashPolicy(t, durable, ctx, "composed-agent")
	select {
	case <-rpcs[0].settled:
	case <-time.After(time.Second):
		t.Fatal("active source turn did not settle")
	}
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "composed-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && agent.RuntimeGeneration == restartGeneration
	}, time.Second, time.Millisecond)
	afterRestart, err := durable.ManagedAgent(ctx, "composed-agent")
	require.NoError(t, err)
	require.Equal(t, sourceSession, afterRestart.PiSessionID)
	require.Equal(t, beforeRestart.SessionEpoch, afterRestart.SessionEpoch)
	require.NotEqual(t, oldFence, storedAgentFence(afterRestart))
	requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")
	releasePlannedExitCallback(t, processes[0].waitRelease, processes[0].waitReturned)
	assertCrashPolicyUnchanged(t, durable, ctx, "composed-agent", restartPolicy)
	require.Equal(t, 1, rpcs[0].settleCalls)
	require.Zero(t, processes[0].stopCalls, "lifecycle-owned restart uses the controlled group reaper instead of Process.Stop")
	assertComposedStaleFence(t, durable, manager, oldBinding, oldFence, "123e4567-e89b-42d3-a456-426614174012")

	currentFence := storedAgentFence(afterRestart)
	currentBinding, ok := manager.bindingFor("composed-agent")
	require.True(t, ok)
	resetAttempt := queueManagedParentFinalForTest(t, durable, currentFence, "composed-reset-turn")
	setManagedActivityActive(t, databasePath, "composed-agent")
	reset, err := manager.Reset(ctx, ResetRequest{AgentID: "composed-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/composed-agent/reset", RequestID: "123e4567-e89b-42d3-a456-426614174013", CanonicalBodyHash: strings.Repeat("c", 64)})
	require.NoError(t, err)
	require.NotNil(t, reset.Operation)
	awaitPlannedExitCallback(t, processes[1].waitEntered)
	resetPolicy := snapshotCrashPolicy(t, durable, ctx, "composed-agent")
	select {
	case <-rpcs[1].settled:
	case <-time.After(time.Second):
		t.Fatal("reset did not settle the active replacement turn")
	}
	select {
	case <-resetCleanupEntered:
	case <-time.After(time.Second):
		t.Fatal("reset did not reach old-artifact cleanup barrier")
	}
	require.FileExists(t, sourceFile, "old session history remains until the fresh target is ready")
	requireManagedParentTerminalForTest(t, databasePath, resetAttempt, "interrupted", "interrupted")
	require.Zero(t, rpcs[2].promptCalls, "reset never replays the launch-only initial prompt")
	close(resetCleanupRelease)
	releasePlannedExitCallback(t, processes[1].waitRelease, processes[1].waitReturned)
	assertCrashPolicyUnchanged(t, durable, ctx, "composed-agent", resetPolicy)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, reset.Operation.OperationID)
		agent, agentErr := durable.ManagedAgent(ctx, "composed-agent")
		return actionErr == nil && agentErr == nil && action.State == "succeeded" && agent.PiSessionID == resetSession && agent.RuntimeGeneration == resetGeneration && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	require.Equal(t, 3, factory.startCount(), "reset starts exactly one fresh process after the same-session restart")
	require.NoFileExists(t, sourceFile)
	assertComposedStaleFence(t, durable, manager, currentBinding, currentFence, "123e4567-e89b-42d3-a456-426614174015")

	terminationRequest := TerminateRequest{AgentID: "composed-agent", Method: "DELETE", CanonicalPath: "/v1/managed-agents/composed-agent", RequestID: "123e4567-e89b-42d3-a456-426614174016", CanonicalBodyHash: strings.Repeat("d", 64)}
	_, err = manager.Terminate(ctx, terminationRequest)
	require.NoError(t, err)
	awaitPlannedExitCallback(t, processes[2].waitEntered)
	terminationPolicy := snapshotCrashPolicy(t, durable, ctx, "composed-agent")
	releasePlannedExitCallback(t, processes[2].waitRelease, processes[2].waitReturned)
	assertCrashPolicyUnchanged(t, durable, ctx, "composed-agent", terminationPolicy)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, terminationRequest.RequestID)
		agent, agentErr := durable.ManagedAgent(ctx, "composed-agent")
		return actionErr == nil && agentErr == nil && action.State == "failed" && action.Stage == "agent_cleanup" && action.Retryable && agent.DesiredState == "terminated"
	}, time.Second, time.Millisecond)
	require.FileExists(t, freshFile, "the tombstone retains cleanup retry evidence")
	require.Equal(t, 1, rpcs[2].abortCalls)
	require.Zero(t, processes[2].stopCalls, "termination cleanup uses the controlled group reaper instead of Process.Stop")
	require.Equal(t, 3, signalCount(ops))

	cleanupFault.Store(false)
	retryRequest := terminationRequest
	retryRequest.RequestID = "123e4567-e89b-42d3-a456-426614174017"
	_, err = manager.Terminate(ctx, retryRequest)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, retryRequest.RequestID)
		_, agentErr := durable.ManagedAgent(ctx, "composed-agent")
		return actionErr == nil && action.State == "succeeded" && action.TerminalCode == "TERMINATED" && errors.Is(agentErr, sql.ErrNoRows)
	}, time.Second, time.Millisecond)
	require.NoFileExists(t, freshFile)
	require.Equal(t, 1, rpcs[2].abortCalls, "cleanup retry does not send Abort again")
	require.Equal(t, 3, factory.startCount())
	require.Equal(t, 3, signalCount(ops))
	assertComposedStaleFence(t, durable, manager, currentBinding, currentFence, "123e4567-e89b-42d3-a456-426614174018")
}

type crashPolicySnapshot struct {
	CrashTimes []string
	RetryAt    string
	ReadySince string
	ErrorCode  string
}

func snapshotCrashPolicy(t *testing.T, durable *store.Store, ctx context.Context, agentID string) crashPolicySnapshot {
	t.Helper()
	recovery, err := durable.ManagedCrashRecovery(ctx, agentID)
	require.NoError(t, err)
	agent, err := durable.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	return crashPolicySnapshot{CrashTimes: append([]string(nil), recovery.CrashTimes...), RetryAt: recovery.RetryAt, ReadySince: recovery.ReadySince, ErrorCode: agent.LastErrorCode}
}

func assertCrashPolicyUnchanged(t *testing.T, durable *store.Store, ctx context.Context, agentID string, before crashPolicySnapshot) {
	t.Helper()
	after := snapshotCrashPolicy(t, durable, ctx, agentID)
	require.Equal(t, before, after, "planned process-exit callback must not change crash policy")
}

func awaitPlannedExitCallback(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("planned process-exit callback did not reach its barrier")
	}
}

func releasePlannedExitCallback(t *testing.T, release, returned chan struct{}) {
	t.Helper()
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("planned process-exit callback did not complete")
	}
}

func setManagedActivityActive(t *testing.T, databasePath, agentID string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agentID)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func signalCount(ops *restartExecutionOps) int {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return len(ops.signals)
}

func assertComposedStaleFence(t *testing.T, durable *store.Store, manager *Manager, binding *managedBinding, fence store.ManagedRuntimeFence, operationID string) {
	t.Helper()
	before, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(99), "state": "idle", "outcome": "stale"})
	_, err = durable.RecordManagedUnexpectedExit(context.Background(), fence, operationID, "stale callback")
	require.Error(t, err)
	after, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after, "stale callbacks cannot publish a lifecycle mutation")
}

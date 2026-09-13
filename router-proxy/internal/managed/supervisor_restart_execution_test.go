package managed

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type restartExecutionInput struct {
	mu     sync.Mutex
	closed bool
}

func (input *restartExecutionInput) Write(value []byte) (int, error) { return len(value), nil }
func (input *restartExecutionInput) Close() error {
	input.mu.Lock()
	input.closed = true
	input.mu.Unlock()
	return nil
}

type restartExecutionProcess struct {
	identity  ProcessIdentity
	input     *restartExecutionInput
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	stopCalls int
}

func (p *restartExecutionProcess) Identity() ProcessIdentity { return p.identity }
func (p *restartExecutionProcess) Stdin() io.WriteCloser     { return p.input }
func (p *restartExecutionProcess) Stdout() io.ReadCloser     { return io.NopCloser(nilReader{}) }
func (p *restartExecutionProcess) Wait() error               { <-p.done; return nil }
func (p *restartExecutionProcess) Stop(context.Context) error {
	p.mu.Lock()
	p.stopCalls++
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
	return nil
}

type nilReader struct{}

func (nilReader) Read([]byte) (int, error) { return 0, io.EOF }

type restartExecutionOps struct {
	mu      sync.Mutex
	states  map[int]ProcessProbeState
	signals []syscall.Signal
	process map[int]*restartExecutionProcess
}

func (ops *restartExecutionOps) Probe(identity ProcessIdentity) ProcessProbe {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	return ProcessProbe{State: ops.states[identity.PID]}
}
func (ops *restartExecutionOps) CloseProcessInput(process Process) error {
	return process.Stdin().Close()
}
func (ops *restartExecutionOps) SignalVerifiedProcessGroup(identity ProcessIdentity, signal syscall.Signal) error {
	ops.mu.Lock()
	defer ops.mu.Unlock()
	if ops.states[identity.PID] != ProcessOwned {
		return syscall.ESRCH
	}
	ops.signals = append(ops.signals, signal)
	ops.states[identity.PID] = ProcessAbsent
	ops.process[identity.PID].once.Do(func() { close(ops.process[identity.PID].done) })
	return nil
}
func (ops *restartExecutionOps) SignalVerifiedProcessGroupAfterLeaderExit(identity ProcessIdentity) error {
	return ops.SignalVerifiedProcessGroup(identity, syscall.SIGKILL)
}
func (ops *restartExecutionOps) WaitForProcessGroupEmpty(ctx context.Context, identity ProcessIdentity, _ <-chan error) ProcessProbe {
	ops.mu.Lock()
	state := ops.states[identity.PID]
	ops.mu.Unlock()
	if state == ProcessAbsent {
		return ProcessProbe{State: ProcessAbsent}
	}
	<-ctx.Done()
	return ProcessProbe{State: ProcessUnknown}
}

type restartExecutionRPC struct {
	mu            sync.Mutex
	session       string
	active        []string
	settled       chan struct{}
	settleRelease chan struct{}
	settleCalls   int
	policyRelease chan struct{}
	policyErr     error
	sessionFile   string
	promptCalls   int
	prompts       []string
}

func (rpc *restartExecutionRPC) GetState(context.Context) (map[string]any, error) {
	return map[string]any{"sessionId": rpc.session, "sessionFile": rpc.sessionFile}, nil
}
func (rpc *restartExecutionRPC) GetCommands(context.Context) ([]string, error) {
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (rpc *restartExecutionRPC) Bootstrap(context.Context, map[string]any) error { return nil }
func (rpc *restartExecutionRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	return nil
}
func (rpc *restartExecutionRPC) GetActiveTools(context.Context) ([]string, error) {
	return append([]string{}, rpc.active...), nil
}
func (rpc *restartExecutionRPC) ApplyPolicy(ctx context.Context, _ map[string]any) (map[string]any, error) {
	rpc.mu.Lock()
	release, err := rpc.policyRelease, rpc.policyErr
	rpc.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (rpc *restartExecutionRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (rpc *restartExecutionRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": rpc.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (rpc *restartExecutionRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": rpc.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (rpc *restartExecutionRPC) Prompt(_ context.Context, message string) error {
	rpc.mu.Lock()
	rpc.promptCalls++
	rpc.prompts = append(rpc.prompts, message)
	rpc.mu.Unlock()
	return nil
}
func (rpc *restartExecutionRPC) Abort(context.Context) error { return nil }
func (rpc *restartExecutionRPC) Close() error                { return nil }
func (rpc *restartExecutionRPC) SettlePreparedRestartTurn(ctx context.Context, _ store.ManagedRuntimeFence) error {
	rpc.mu.Lock()
	rpc.settleCalls++
	release := rpc.settleRelease
	rpc.mu.Unlock()
	if rpc.settled != nil {
		select {
		case <-rpc.settled:
		default:
			close(rpc.settled)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

type restartExecutionFactory struct {
	processes []*restartExecutionProcess
	rpcs      []*restartExecutionRPC
	mu        sync.Mutex
	count     int
	specs     []CommandSpec
}

func (factory *restartExecutionFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.specs = append(factory.specs, spec)
	process := factory.processes[factory.count]
	factory.rpcs[factory.count].session = spec.SessionID
	factory.count++
	return process, nil
}

type restartExecutionRPCFactory struct {
	factory *restartExecutionFactory
	count   int
}

func (factory *restartExecutionRPCFactory) Open(context.Context, Process) (RPC, error) {
	rpc := factory.factory.rpcs[factory.count]
	factory.count++
	return rpc, nil
}

type restartExecutionFixture struct {
	manager   *Manager
	durable   *store.Store
	old       *restartExecutionProcess
	target    *restartExecutionProcess
	oldRPC    *restartExecutionRPC
	targetRPC *restartExecutionRPC
	ops       *restartExecutionOps
	factory   *restartExecutionFactory
	manifest  Manifest
	root      string
	database  string
}

func newRestartExecutionFixture(t *testing.T, gracefulStop time.Duration) *restartExecutionFixture {
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
	sessionFile := filepath.Join(root, "session.json")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}"), 0600))
	oldRPC.sessionFile, targetRPC.sessionFile = sessionFile, sessionFile
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{SessionRoot: root, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: gracefulStop, TermStop: time.Millisecond}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: &restartExecutionRPCFactory{factory: factory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource("session", "old-generation", "target-generation", "123e4567-e89b-42d3-a456-000000000004"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "restart-agent", Cwd: root, InitialInstruction: "one-time", RequestID: "launch"})
	require.NoError(t, err)
	// Restart All admits only agents whose launch operation is terminal. Waiting
	// for lifecycle_state alone races the asynchronous launch finalizer.
	require.Eventually(t, func() bool {
		agent, agentErr := durable.ManagedAgent(ctx, "restart-agent")
		operation, operationErr := durable.ManagedOperation(ctx, "launch")
		return agentErr == nil && operationErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && operation.State == "terminal"
	}, time.Second, time.Millisecond)
	return &restartExecutionFixture{manager: manager, durable: durable, old: old, target: target, oldRPC: oldRPC, targetRPC: targetRPC, ops: ops, factory: factory, manifest: manifest, root: root, database: databasePath}
}

func (fixture *restartExecutionFixture) reserve(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", fixture.database)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id='restart-agent'`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = fixture.manager.ReserveRestart(context.Background(), RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart", CanonicalBodyHash: "body"})
	require.NoError(t, err)
}

func (fixture *restartExecutionFixture) waitReady(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return err == nil && agent.LifecycleState == "running" && agent.RuntimeGeneration == "target-generation" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
}

// queueManagedParentFinalForTest creates the durable direct-turn and its final
// reply through the same store boundaries used by the manager.
func queueManagedParentFinalForTest(t *testing.T, durable *store.Store, fence store.ManagedRuntimeFence, id string) store.ManagedDirectAttempt {
	t.Helper()
	ctx := context.Background()
	_, err := durable.Enqueue(ctx, protocol.Envelope{ID: id, From: "sender", To: fence.AgentID, Body: "work", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, id)
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, id+"-cursor"))
	require.NoError(t, durable.AcknowledgeManagedDirect(ctx, *attempt, id+"-receipt", true))
	delivery, err := durable.QueueManagedFinalDelivery(ctx, *attempt, id+"-reply", "final reply")
	require.NoError(t, err)
	require.NotNil(t, delivery)
	return *attempt
}

func requireManagedParentTerminalForTest(t *testing.T, databasePath string, attempt store.ManagedDirectAttempt, workState, deliveryState string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer db.Close()
	var agentID, sessionID, startMarker, incarnation, generation, manifestHash, gotWorkState, gotDeliveryState string
	var epoch int64
	var pid, revision int
	require.NoError(t, db.QueryRow(`SELECT w.agent_id,w.pi_session_id,w.session_epoch,w.process_pid,w.process_start_marker,w.process_incarnation,w.runtime_generation,w.manifest_revision,w.manifest_hash,w.state,d.state
		FROM managed_parent_work w JOIN managed_parent_final_deliveries d ON d.work_id=w.work_id
		WHERE w.message_id=? AND w.message_incarnation_id=? AND w.attempt_number=?`, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber).Scan(&agentID, &sessionID, &epoch, &pid, &startMarker, &incarnation, &generation, &revision, &manifestHash, &gotWorkState, &gotDeliveryState))
	require.Equal(t, attempt.Fence.AgentID, agentID)
	require.Equal(t, attempt.Fence.PiSessionID, sessionID)
	require.Equal(t, attempt.Fence.SessionEpoch, epoch)
	require.Equal(t, attempt.Fence.ProcessPID, pid)
	require.Equal(t, attempt.Fence.ProcessStartMarker, startMarker)
	require.Equal(t, attempt.Fence.ProcessIncarnation, incarnation)
	require.Equal(t, attempt.Fence.RuntimeGeneration, generation)
	require.Equal(t, attempt.Fence.ManifestRevision, revision)
	require.Equal(t, attempt.Fence.ManifestHash, manifestHash)
	require.Equal(t, workState, gotWorkState)
	require.Equal(t, deliveryState, gotDeliveryState)
}

func TestRestartExecution(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveTools(manifest)
	old := &restartExecutionProcess{identity: ProcessIdentity{PID: 7001, StartMarker: "old", Incarnation: "old-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	target := &restartExecutionProcess{identity: ProcessIdentity{PID: 7002, StartMarker: "target", Incarnation: "target-incarnation"}, input: &restartExecutionInput{}, done: make(chan struct{})}
	oldRPC := &restartExecutionRPC{active: active, settled: make(chan struct{})}
	targetRPC := &restartExecutionRPC{active: active}
	processFactory := &restartExecutionFactory{processes: []*restartExecutionProcess{old, target}, rpcs: []*restartExecutionRPC{oldRPC, targetRPC}}
	ops := &restartExecutionOps{states: map[int]ProcessProbeState{7001: ProcessOwned, 7002: ProcessOwned}, process: map[int]*restartExecutionProcess{7001: old, 7002: target}}
	root := t.TempDir()
	sessionFile := filepath.Join(root, "session.json")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}"), 0600))
	oldRPC.sessionFile, targetRPC.sessionFile = sessionFile, sessionFile
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{SessionRoot: root, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: manifest, ProcessFactory: processFactory, RPCFactory: &restartExecutionRPCFactory{factory: processFactory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource("session", "old-generation", "target-generation", "123e4567-e89b-42d3-a456-000000000004"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	defer manager.Close(context.Background())
	_, err = manager.Launch(ctx, LaunchRequest{Name: "restart-agent", Cwd: root, InitialInstruction: "one-time", RequestID: "launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "restart-agent")
		return readErr == nil && agent.LifecycleState == "running"
	}, time.Second, time.Millisecond)
	sourceAgent, err := durable.ManagedAgent(ctx, "restart-agent")
	require.NoError(t, err)
	sourceAttempt := queueManagedParentFinalForTest(t, durable, storedAgentFence(sourceAgent), "restart-source-turn")
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id='restart-agent'`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = manager.ReserveRestart(ctx, RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart", CanonicalBodyHash: "body"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		select {
		case <-oldRPC.settled:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "restart-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.RuntimeGeneration == "target-generation" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")
	old.input.mu.Lock()
	closed := old.input.closed
	old.input.mu.Unlock()
	require.True(t, closed)
	ops.mu.Lock()
	signals := append([]syscall.Signal(nil), ops.signals...)
	ops.mu.Unlock()
	require.Contains(t, signals, syscall.SIGTERM)
	targetRPC.mu.Lock()
	prompts := targetRPC.promptCalls
	targetRPC.mu.Unlock()
	require.Zero(t, prompts, "the original initial instruction is launch-only")
	processFactory.mu.Lock()
	count := processFactory.count
	targetSpec := processFactory.specs[1]
	processFactory.mu.Unlock()
	require.Equal(t, 2, count)
	require.Equal(t, ResumeExisting, targetSpec.LaunchMode)
	require.Equal(t, "session", targetSpec.SessionID)
	require.NotEmpty(t, targetSpec.SessionRoot)
}

func TestRestartSettlementInterruptsOldContinuation(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	fixture.reserve(t)
	fixture.waitReady(t)

	// This simulates Pi starting a continuation after agent_settled. It stays
	// attached to the old RPC; replacement readiness never forwards it.
	require.NoError(t, fixture.oldRPC.Prompt(context.Background(), "old-continuation"))
	require.NoError(t, fixture.manager.AdmitPrompt(context.Background(), "restart-agent", "new-input"))
	fixture.oldRPC.mu.Lock()
	oldPrompts := append([]string(nil), fixture.oldRPC.prompts...)
	fixture.oldRPC.mu.Unlock()
	fixture.targetRPC.mu.Lock()
	targetPrompts := append([]string(nil), fixture.targetRPC.prompts...)
	fixture.targetRPC.mu.Unlock()
	require.Contains(t, oldPrompts, "old-continuation")
	require.Equal(t, []string{"new-input"}, targetPrompts)
}

func TestRestartTargetNeedsOwnReadinessAndNewInput(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	fixture.targetRPC.policyRelease = make(chan struct{})
	fixture.reserve(t)
	require.Eventually(t, func() bool {
		agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return err == nil && agent.LifecycleState == "restarting" && agent.ActivityState == "starting" && !agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	fixture.targetRPC.mu.Lock()
	require.Zero(t, fixture.targetRPC.promptCalls)
	fixture.targetRPC.mu.Unlock()
	close(fixture.targetRPC.policyRelease)
	fixture.waitReady(t)
	require.NoError(t, fixture.manager.AdmitPrompt(context.Background(), "restart-agent", "new-input"))
	fixture.targetRPC.mu.Lock()
	prompts := append([]string(nil), fixture.targetRPC.prompts...)
	fixture.targetRPC.mu.Unlock()
	require.Equal(t, []string{"new-input"}, prompts)
}

func TestRestartCloseAndExitArbitration(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	fixture.targetRPC.policyRelease = make(chan struct{})
	fixture.reserve(t)
	require.Eventually(t, func() bool {
		agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return err == nil && agent.LifecycleState == "restarting" && agent.ActivityState == "starting"
	}, time.Second, time.Millisecond)
	require.NoError(t, fixture.manager.Close(context.Background()))
	fixture.target.mu.Lock()
	stopCalls := fixture.target.stopCalls
	fixture.target.mu.Unlock()
	require.Zero(t, stopCalls, "restart close must use the controlled group reaper")
	action, err := fixture.durable.ManagedLifecycleAction(context.Background(), "restart")
	require.NoError(t, err)
	require.Equal(t, "starting_target", action.State)
	require.Equal(t, "target_start", action.Stage)
	require.Nil(t, action.TargetFence)
}

func TestRestartCloseSettlesActiveSourceWithoutGenericStop(t *testing.T) {
	fixture := newRestartExecutionFixture(t, 100*time.Millisecond)
	fixture.oldRPC.settleRelease = make(chan struct{})
	fixture.reserve(t)
	require.Eventually(t, func() bool {
		action, err := fixture.durable.ManagedLifecycleAction(context.Background(), "restart")
		return err == nil && action.State == "awaiting_settle" && action.Stage == "source_settle"
	}, time.Second, time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- fixture.manager.Close(context.Background()) }()
	require.Eventually(t, fixture.manager.isClosed, time.Second, time.Millisecond)
	close(fixture.oldRPC.settleRelease)
	require.NoError(t, <-closeDone)

	fixture.oldRPC.mu.Lock()
	settleCalls := fixture.oldRPC.settleCalls
	fixture.oldRPC.mu.Unlock()
	require.Equal(t, 1, settleCalls, "active source must settle after shutdown cancels runCtx")
	fixture.old.mu.Lock()
	stopCalls := fixture.old.stopCalls
	fixture.old.mu.Unlock()
	require.Zero(t, stopCalls, "lifecycle-owned shutdown must not fall through to Process.Stop")
	fixture.factory.mu.Lock()
	starts := fixture.factory.count
	fixture.factory.mu.Unlock()
	require.Equal(t, 1, starts, "Close leaves target_start for recovery and starts no replacement")
	action, err := fixture.durable.ManagedLifecycleAction(context.Background(), "restart")
	require.NoError(t, err)
	require.Equal(t, "starting_target", action.State)
	require.Equal(t, "target_start", action.Stage)
	_, err = fixture.durable.ManagedRestartTransition(context.Background(), "restart-agent")
	require.Error(t, err, "lifecycle-owned shutdown must not write 025")
}

func TestRestartRecovery(t *testing.T) {
	fixture := newRestartExecutionFixture(t, 100*time.Millisecond)
	sourceAgent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	sourceAttempt := queueManagedParentFinalForTest(t, fixture.durable, storedAgentFence(sourceAgent), "restart-recovery-source-turn")
	fixture.oldRPC.settleRelease = make(chan struct{})
	fixture.reserve(t)
	require.Eventually(t, func() bool {
		action, err := fixture.durable.ManagedLifecycleAction(context.Background(), "restart")
		return err == nil && action.State == "awaiting_settle" && action.Stage == "source_settle"
	}, time.Second, time.Millisecond)
	fixture.ops.mu.Lock()
	fixture.ops.states[fixture.old.identity.PID] = ProcessAbsent
	fixture.old.once.Do(func() { close(fixture.old.done) })
	fixture.ops.mu.Unlock()

	recovered, err := NewManager(ManagerOptions{Store: fixture.durable, Config: Config{SessionRoot: fixture.root, AllowedWorkingDirectoryRoots: []string{fixture.root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: fixture.manifest, ProcessFactory: fixture.factory, RPCFactory: &restartExecutionRPCFactory{factory: fixture.factory}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource(), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: fixture.ops})
	require.NoError(t, err)
	defer recovered.Close(context.Background())
	require.NoError(t, recovered.RecoverManagedLifecycleControls(context.Background()))
	require.Eventually(t, func() bool {
		agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return err == nil && agent.LifecycleState == "running" && agent.RuntimeGeneration == "target-generation" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	requireManagedParentTerminalForTest(t, fixture.database, sourceAttempt, "interrupted", "interrupted")
}

func TestRestartStaleBindingCleanup(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	fixture.reserve(t)
	fixture.waitReady(t)
	binding, ok := fixture.manager.bindingFor("restart-agent")
	require.True(t, ok)
	require.Equal(t, fixture.target.identity, binding.identity)
	fixture.manager.mu.Lock()
	mapped := fixture.manager.processes["restart-agent"]
	fixture.manager.mu.Unlock()
	require.Equal(t, fixture.target, mapped)
}

func TestRestartFailureRedaction(t *testing.T) {
	fixture := newRestartExecutionFixture(t, time.Millisecond)
	fixture.targetRPC.policyErr = errors.New("replacement token=secret-value")
	fixture.reserve(t)
	require.Eventually(t, func() bool {
		agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
		return err == nil && agent.LastErrorCode == "RESTART_REPLACEMENT_FAILED"
	}, time.Second, time.Millisecond)
	agent, err := fixture.durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "replacement process did not become ready", agent.LastErrorMessage)
	require.NotContains(t, agent.LastErrorMessage, "secret")
}

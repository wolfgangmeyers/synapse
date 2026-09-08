package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type readinessRPC struct {
	calls    []string
	active   []string
	envelope map[string]any
}

func (r *readinessRPC) GetState(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "get_state")
	return map[string]any{"sessionId": "session"}, nil
}
func (r *readinessRPC) GetCommands(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_commands")
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *readinessRPC) Bootstrap(_ context.Context, envelope map[string]any) error {
	r.calls = append(r.calls, "bootstrap")
	r.envelope = envelope
	return nil
}
func (r *readinessRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.calls = append(r.calls, "receipt")
	return nil
}
func (r *readinessRPC) GetActiveTools(context.Context) ([]string, error) {
	r.calls = append(r.calls, "get_active_tools")
	return r.active, nil
}
func (r *readinessRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "policy")
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r *readinessRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	r.calls = append(r.calls, "policy_receipt")
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r *readinessRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	r.calls = append(r.calls, "child_snapshot")
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": int64(0), "runs": []any{}}, nil
}
func (r *readinessRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	r.calls = append(r.calls, "snapshot_receipt")
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": "owner", "sequence": int64(0), "runs": []any{}}, nil
}
func (r *readinessRPC) Prompt(context.Context, string) error {
	r.calls = append(r.calls, "prompt")
	return nil
}
func (r *readinessRPC) Close() error { return nil }

type admissionProcess struct {
	*launchProcess
	session string
}

type admissionFactory struct {
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
	active  []string
}

func (f *admissionFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.mu.Lock()
	f.count++
	process := &admissionProcess{launchProcess: &launchProcess{identity: ProcessIdentity{PID: 1000 + f.count, StartMarker: spec.SessionID, Incarnation: spec.SessionID}, stopped: make(chan struct{})}, session: spec.SessionID}
	if f.count == 32 {
		close(f.started)
	}
	f.mu.Unlock()
	<-f.release
	return process, nil
}

type admissionRPCFactory struct{ active []string }

func (f admissionRPCFactory) Open(_ context.Context, process Process) (RPC, error) {
	return &launchRPC{session: process.(*admissionProcess).session, active: f.active}, nil
}

type recoveryDirectRPC struct {
	launchRPC
	recovery      DirectMailboxRecovery
	recoveryCalls int
	promptCalls   int
}

func (r *recoveryDirectRPC) PrepareManagedMailbox(context.Context) (string, error) {
	return "recovery-cursor", nil
}
func (r *recoveryDirectRPC) PromptManagedMailbox(context.Context, store.ManagedDirectAttempt) (<-chan DirectMailboxEvent, error) {
	r.promptCalls++
	events := make(chan DirectMailboxEvent)
	close(events)
	return events, nil
}
func (r *recoveryDirectRPC) RecoverManagedMailbox(context.Context, store.ManagedDirectAttempt) (DirectMailboxRecovery, error) {
	r.recoveryCalls++
	return r.recovery, nil
}

type recoveryDirectFactory struct {
	process *launchProcess
	rpc     *recoveryDirectRPC
}

func (f recoveryDirectFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type recoveryDirectRPCFactory struct{ rpc *recoveryDirectRPC }

func (f recoveryDirectRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func TestManagedDirectRecoveryWakeup(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 991, StartMarker: "start", Incarnation: "incarnation"}, stopped: make(chan struct{})}
	rpc := &recoveryDirectRPC{recovery: DirectMailboxRecovery{Receipt: &DirectMailboxReceipt{EntryIdentity: "persisted-append"}}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: recoveryDirectFactory{process: process, rpc: rpc}, RPCFactory: recoveryDirectRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	// Launch schedules direct recovery asynchronously. Keep that lane closed
	// until the crash-boundary attempt exists, so the normal empty-recovery
	// dispatcher cannot claim the test row before it is marked prompted.
	lane := manager.laneFor("recovery-agent")
	lane.serial.Lock()
	laneLocked := true
	defer func() {
		if laneLocked {
			lane.serial.Unlock()
		}
	}()
	_, err = manager.Launch(ctx, LaunchRequest{Name: "recovery-agent", Cwd: root, RequestID: "recovery-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "recovery-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, "recovery-agent")
	require.NoError(t, err)
	fence := store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "recovery-direct", From: "sender", To: agent.Name, Body: "persisted body", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "recovery-direct")
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "recovery-cursor"))
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- manager.RecoverManagedDirectAttempts(ctx) }()
	lane.serial.Unlock()
	laneLocked = false
	select {
	case recoveryErr := <-recoveryDone:
		require.NoError(t, recoveryErr)
	case <-time.After(time.Second):
		t.Fatal("managed direct recovery did not finish")
	}
	require.Eventually(t, func() bool {
		message, readErr := durable.MessageFor(ctx, "recovery-direct")
		return readErr == nil && message == nil
	}, time.Second, time.Millisecond)
	require.Equal(t, 1, rpc.recoveryCalls)
	require.Zero(t, rpc.promptCalls, "a recovered append must not send a second command")
	require.NoError(t, manager.Close(context.Background()))
}

func TestManagedDirectOfferUsesPrivateRPCCapability(t *testing.T) {
	// Direct delivery is deliberately a private capability, not another method
	// on the launch/readiness RPC interface used by existing manager fakes.
	var direct DirectMailboxRPC = &rpcClient{}
	require.NotNil(t, direct)
}

func TestManagedProcessLimitRejectsThirtyThirdConcurrentStart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	active := expectedManagedActiveTools(manifest)
	factory := &admissionFactory{started: make(chan struct{}), release: make(chan struct{}), active: active}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: admissionRPCFactory{active: active}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	for index := 0; index < 32; index++ {
		_, err = manager.Launch(ctx, LaunchRequest{Name: fmt.Sprintf("agent-%02d", index), Cwd: root, RequestID: fmt.Sprintf("request-%02d", index)})
		require.NoError(t, err)
	}
	select {
	case <-factory.started:
	case <-time.After(2 * time.Second):
		t.Fatal("32 concurrent starts did not reach the process factory")
	}
	rows, err := durable.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 32)
	for _, row := range rows {
		require.True(t, row.SlotHeld, row.Name)
	}
	_, err = manager.Launch(ctx, LaunchRequest{Name: "agent-33", Cwd: root, RequestID: "request-33"})
	var limit *store.ManagedSlotLimitError
	require.ErrorAs(t, err, &limit)
	factory.mu.Lock()
	require.Equal(t, 32, factory.count)
	factory.mu.Unlock()
	close(factory.release)
	require.Eventually(t, func() bool {
		rows, readErr := durable.ManagedAgents(ctx)
		if readErr != nil || len(rows) != 32 {
			return false
		}
		for _, row := range rows {
			if row.LifecycleState != "running" {
				return false
			}
		}
		return true
	}, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, manager.Close(context.Background()))
	rows, err = durable.ManagedAgents(ctx)
	require.NoError(t, err)
	for _, row := range rows {
		require.False(t, row.SlotHeld, row.Name)
		require.Equal(t, "stopped", row.ActivityState, row.Name)
	}

	reserve := func(name, operation string) {
		require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: name, Name: name, SlotHeld: true, Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: "manifest"}, store.ManagedOperation{OperationID: operation, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: operation, CanonicalBodyHash: "body-" + name, ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{"operation_id":"` + operation + `"}`)}))
	}
	reserve("failed-start", "failed-start-operation")
	versionBeforeFailure, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedFailed(ctx, "failed-start", "PROCESS_START_FAILED", "start failed"))
	failedStart, err := durable.ManagedAgent(ctx, "failed-start")
	require.NoError(t, err)
	require.False(t, failedStart.SlotHeld)
	versionAfterFailure, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Greater(t, versionAfterFailure, versionBeforeFailure)
	require.NoError(t, durable.MarkManagedFailed(ctx, "failed-start", "PROCESS_EXITED", "stale exit"))
	require.NoError(t, durable.ReleaseManagedSlot(ctx, "failed-start"))
	versionAfterRepeatedRelease, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionAfterFailure, versionAfterRepeatedRelease)

	reserve("current-exit", "current-exit-operation")
	reserve("stale-exit", "stale-exit-operation")
	require.NoError(t, durable.MarkManagedFailed(ctx, "stale-exit", "PROCESS_EXITED", "old process exited"))
	current, err := durable.ManagedAgent(ctx, "current-exit")
	require.NoError(t, err)
	require.True(t, current.SlotHeld)
	require.NoError(t, durable.MarkManagedFailed(ctx, "current-exit", "PROCESS_EXITED", "current process exited"))
	current, err = durable.ManagedAgent(ctx, "current-exit")
	require.NoError(t, err)
	require.False(t, current.SlotHeld)
}

func TestPhase1ReadinessGatesRemainUnchanged(t *testing.T) {
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)}
	require.NoError(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.Equal(t, []string{"get_state", "get_commands", "bootstrap", "receipt", "get_active_tools"}, rpc.calls)
	require.Equal(t, "mecha.bootstrap/v1", rpc.envelope["protocol"])
	require.Equal(t, manifest.Raw, rpc.envelope["desired_manifest"])
	require.Equal(t, manifest.CanonicalSHA256, rpc.envelope["manifest_sha256"])
}

type restartPreparationRPC struct {
	mu               sync.Mutex
	session          string
	active           []string
	stateCalls       int
	commandCalls     int
	bootstrapCalls   int
	receiptCalls     int
	activeCalls      int
	prepareStarted   chan struct{}
	releasePrepare   chan struct{}
	preparationError error
	promptCalls      int
	closeCalls       int
}

func (r *restartPreparationRPC) GetState(ctx context.Context) (map[string]any, error) {
	r.mu.Lock()
	r.stateCalls++
	call := r.stateCalls
	err := r.preparationError
	started, release := r.prepareStarted, r.releasePrepare
	r.mu.Unlock()
	if call > 1 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if started != nil {
			select {
			case <-started:
			default:
				close(started)
			}
		}
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
	}
	return map[string]any{"sessionId": r.session}, nil
}
func (r *restartPreparationRPC) GetCommands(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commandCalls++
	return append(append([]string{}, hiddenCommands...), legacyCommands...), nil
}
func (r *restartPreparationRPC) Bootstrap(context.Context, map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bootstrapCalls++
	return nil
}
func (r *restartPreparationRPC) WaitForReceipt(context.Context, string, string, int, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.receiptCalls++
	return nil
}
func (r *restartPreparationRPC) GetActiveTools(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeCalls++
	return append([]string{}, r.active...), nil
}
func (r *restartPreparationRPC) ApplyPolicy(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"inlineEnabled": false, "inlineCredits": map[string]any{}, "recallContent": "", "settledEnabled": false, "goalLockEnabled": false, "compactionDeferred": false}, nil
}
func (r *restartPreparationRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{"inline_enabled": false, "inline_credits": map[string]any{}, "recall_content": "", "settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false}}, nil
}
func (r *restartPreparationRPC) RequestChildSnapshot(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *restartPreparationRPC) WaitForChildSnapshot(context.Context) (map[string]any, error) {
	return map[string]any{"protocol": "mecha.children/v1", "snapshot_id": "snapshot", "owner_session_id": r.session, "sequence": int64(0), "runs": []any{}}, nil
}
func (r *restartPreparationRPC) Prompt(context.Context, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.promptCalls++
	return nil
}
func (r *restartPreparationRPC) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCalls++
	return nil
}
func (r *restartPreparationRPC) counts() (state, commands, bootstrap, receipt, active, prompts, closes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stateCalls, r.commandCalls, r.bootstrapCalls, r.receiptCalls, r.activeCalls, r.promptCalls, r.closeCalls
}

type restartPreparationFactory struct {
	process *launchProcess
	rpc     *restartPreparationRPC
}

func (f restartPreparationFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.rpc.session = spec.SessionID
	return f.process, nil
}

type restartPreparationRPCFactory struct{ rpc *restartPreparationRPC }

func (f restartPreparationRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func readyRestartManager(t *testing.T, preparationError error) (*Manager, *store.Store, *restartPreparationRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 321, StartMarker: "restart-start", Incarnation: "restart-incarnation"}, stopped: make(chan struct{})}
	rpc := &restartPreparationRPC{active: expectedManagedActiveTools(manifest), preparationError: preparationError}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: restartPreparationFactory{process: process, rpc: rpc}, RPCFactory: restartPreparationRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource("restart-session", "initial-generation", "replacement-generation"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	_, err = manager.Launch(ctx, LaunchRequest{Name: "restart-agent", Cwd: root, RequestID: "launch-operation"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "restart-agent")
		if readErr != nil || agent.LifecycleState != "running" {
			return false
		}
		op, opErr := durable.ManagedOperation(ctx, "launch-operation")
		return opErr == nil && op.State == "terminal"
	}, 2*time.Second, time.Millisecond)
	return manager, durable, rpc
}

func TestReserveManagedRestartAtomicallyDisablesPromptAdmission(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	require.NoError(t, manager.AdmitPrompt(context.Background(), "restart-agent", "before"))
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	request := RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
	result, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.False(t, result.Replayed)
	select {
	case <-rpc.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("restart preparation did not start")
	}
	var notReady *store.ManagedAgentNotReadyError
	require.ErrorAs(t, manager.AdmitPrompt(context.Background(), "restart-agent", "after"), &notReady)
	_, _, _, _, _, prompts, _ := rpc.counts()
	require.Equal(t, 1, prompts)
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)
	close(rpc.releasePrepare)
}

func TestRestartPreparationFailurePersistsExactDurableError(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, errors.New("state failed"))
	defer manager.Close(context.Background())
	request := RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
	_, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(context.Background(), "restart-agent")
		return readErr == nil && agent.LastErrorCode == "RESTART_PREPARATION_FAILED"
	}, time.Second, time.Millisecond)
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.True(t, stored.AcceptingPrompts)
	require.Equal(t, "RESTART_PREPARATION_FAILED", stored.LastErrorCode)
	require.Equal(t, "restart preparation failed", stored.LastErrorMessage)
	op, err := durable.ManagedOperation(context.Background(), request.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	states, _, _, _, _, prompts, closes := rpc.counts()
	require.Equal(t, 2, states)
	require.Equal(t, 0, closes)
	require.Equal(t, 0, prompts)
}

func restartRequest() RestartRequest {
	return RestartRequest{AgentID: "restart-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/restart-agent/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object"}
}

func TestRestartPreparationDeadlineAndSessionMismatchPersistExactError(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*Manager, *restartPreparationRPC)
	}{
		{name: "deadline", configure: func(manager *Manager, _ *restartPreparationRPC) {
			manager.preparationDeadline = func(parent context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
				require.Equal(t, 5*time.Second, duration)
				child, cancel := context.WithCancel(parent)
				cancel()
				return child, func() {}
			}
		}},
		{name: "session mismatch", configure: func(_ *Manager, rpc *restartPreparationRPC) { rpc.session = "wrong-session" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, rpc := readyRestartManager(t, nil)
			defer manager.Close(context.Background())
			test.configure(manager, rpc)
			_, err := manager.ReserveRestart(context.Background(), restartRequest())
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				agent, readErr := durable.ManagedAgent(context.Background(), "restart-agent")
				return readErr == nil && agent.LastErrorCode == "RESTART_PREPARATION_FAILED"
			}, time.Second, time.Millisecond)
			stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
			require.NoError(t, err)
			require.Equal(t, "running", stored.LifecycleState)
			require.True(t, stored.AcceptingPrompts)
			require.Equal(t, "restart preparation failed", stored.LastErrorMessage)
			_, commands, _, _, _, prompts, closes := rpc.counts()
			require.Equal(t, 0, prompts)
			require.Equal(t, 0, closes)
			require.Equal(t, 1, commands)
		})
	}
}

func TestReserveManagedRestartRejectsInvalidLiveBindingWithoutEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Manager, *managedBinding)
	}{
		{name: "group leader", mutate: func(manager *Manager, _ *managedBinding) {
			manager.groupLeader = func(ProcessIdentity) bool { return false }
		}},
		{name: "missing rpc", mutate: func(_ *Manager, binding *managedBinding) { binding.rpc = nil }},
		{name: "mismatched incarnation", mutate: func(_ *Manager, binding *managedBinding) { binding.identity.Incarnation = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, _ := readyRestartManager(t, nil)
			defer manager.Close(context.Background())
			binding, ok := manager.bindingFor("restart-agent")
			require.True(t, ok)
			binding.serial.Lock()
			test.mutate(manager, binding)
			binding.serial.Unlock()
			before, err := durable.GetProjectionVersion(context.Background())
			require.NoError(t, err)
			_, err = manager.ReserveRestart(context.Background(), restartRequest())
			var notReady *store.ManagedAgentNotReadyError
			require.ErrorAs(t, err, &notReady)
			after, err := durable.GetProjectionVersion(context.Background())
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = durable.ManagedRestartTransition(context.Background(), "restart-agent")
			require.Error(t, err)
		})
	}
}

func TestRestartTransitionFirstAdmissionRejectsFreshManagerWithoutBinding(t *testing.T) {
	manager, durable, _ := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	manager.mu.Lock()
	delete(manager.bindings, "restart-agent")
	delete(manager.processes, "restart-agent")
	delete(manager.rpcs, "restart-agent")
	manager.mu.Unlock()
	_, err := manager.ReserveRestart(context.Background(), restartRequest())
	var notReady *store.ManagedAgentNotReadyError
	require.ErrorAs(t, err, &notReady)
	_, err = durable.ManagedRestartTransition(context.Background(), "restart-agent")
	require.Error(t, err)
}

func TestRestartPreparationSuccessIsBoundedAndDoesNotReplace(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	_, err := manager.ReserveRestart(context.Background(), restartRequest())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		transition, readErr := durable.ManagedRestartTransition(context.Background(), "restart-agent")
		return readErr == nil && transition.Stage == "prepared"
	}, time.Second, time.Millisecond)
	stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.Equal(t, "restart-incarnation", stored.ProcessIncarnation)
	states, commands, bootstrap, receipt, active, prompts, closes := rpc.counts()
	require.Equal(t, 2, states)
	require.Equal(t, 1, commands)
	require.Equal(t, 1, bootstrap)
	require.Equal(t, 1, receipt)
	require.Equal(t, 1, active)
	require.Equal(t, 0, prompts)
	require.Equal(t, 0, closes)
}

func TestReserveManagedRestartReplaysExactOperationWithoutSecondPreparation(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
	request := restartRequest()
	first, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	select {
	case <-rpc.prepareStarted:
	case <-time.After(time.Second):
		t.Fatal("restart preparation did not start")
	}
	replayed, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replayed.Operation.ResponseBody)
	changedPath := request
	changedPath.CanonicalPath += "/other"
	var conflict *store.ManagedIdempotencyConflictError
	_, err = manager.ReserveRestart(context.Background(), changedPath)
	require.ErrorAs(t, err, &conflict)
	changedBody := request
	changedBody.CanonicalBodyHash = "different"
	_, err = manager.ReserveRestart(context.Background(), changedBody)
	require.ErrorAs(t, err, &conflict)
	var response map[string]any
	require.NoError(t, json.Unmarshal(first.Operation.ResponseBody, &response))
	require.Equal(t, request.RequestID, response["operation_id"])
	require.Equal(t, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": request.RequestID}, first.Operation.ResponseHeaders)
	_, hasPrivatePID := response["agent"].(map[string]any)["process_pid"]
	require.False(t, hasPrivatePID)
	states, _, _, _, _, _, _ := rpc.counts()
	require.Equal(t, 2, states)
	close(rpc.releasePrepare)
	_ = durable
}

func TestRestartRollbackReopensPromptAdmissionAndReplaysImmutableOperation(t *testing.T) {
	manager, durable, rpc := readyRestartManager(t, errors.New("preparation failed"))
	defer manager.Close(context.Background())
	request := restartRequest()
	first, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		op, readErr := durable.ManagedOperation(context.Background(), request.RequestID)
		return readErr == nil && op.State == "terminal"
	}, time.Second, time.Millisecond)
	require.NoError(t, manager.AdmitPrompt(context.Background(), "restart-agent", "after rollback"))
	replayed, err := manager.ReserveRestart(context.Background(), request)
	require.NoError(t, err)
	require.True(t, replayed.Replayed)
	require.Equal(t, first.Operation.ResponseBody, replayed.Operation.ResponseBody)
	op, err := durable.ManagedOperation(context.Background(), request.RequestID)
	require.NoError(t, err)
	require.NotEmpty(t, op.ExpiresAt)
	states, _, _, _, _, prompts, _ := rpc.counts()
	require.Equal(t, 1, prompts)
	require.Equal(t, 2, states)
}

func TestReserveManagedRestartRejectsPendingLaunchOperationWithoutEffects(t *testing.T) {
	manager, durable, _ := readyRestartManager(t, nil)
	defer manager.Close(context.Background())
	binding, ok := manager.bindingFor("restart-agent")
	require.True(t, ok)
	pending := store.ManagedOperation{OperationID: "pending-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "pending-launch", CanonicalBodyHash: "pending", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.PutManagedOperation(context.Background(), pending))
	binding.serial.Lock()
	binding.operationID, binding.lifecycleAction = pending.OperationID, "launch"
	binding.serial.Unlock()
	before, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	_, err = manager.ReserveRestart(context.Background(), restartRequest())
	var conflict *store.ManagedRestartConflictError
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoError(t, durable.MarkManagedOperationTerminal(context.Background(), pending.OperationID))
	binding.serial.Lock()
	binding.lifecycleAction = ""
	binding.serial.Unlock()
	_, err = manager.ReserveRestart(context.Background(), restartRequest())
	require.NoError(t, err)
}

func TestRestartShutdownPreservesTransitionAtReservedAndPrepared(t *testing.T) {
	for _, test := range []struct {
		name  string
		block bool
	}{{name: "reserved", block: true}, {name: "prepared"}} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, rpc := readyRestartManager(t, nil)
			if test.block {
				rpc.prepareStarted, rpc.releasePrepare = make(chan struct{}), make(chan struct{})
			}
			_, err := manager.ReserveRestart(context.Background(), restartRequest())
			require.NoError(t, err)
			if test.block {
				select {
				case <-rpc.prepareStarted:
				case <-time.After(time.Second):
					t.Fatal("restart preparation did not start")
				}
			} else {
				require.Eventually(t, func() bool {
					transition, readErr := durable.ManagedRestartTransition(context.Background(), "restart-agent")
					return readErr == nil && transition.Stage == "prepared"
				}, time.Second, time.Millisecond)
			}
			require.NoError(t, manager.Close(context.Background()))
			stored, err := durable.ManagedAgent(context.Background(), "restart-agent")
			require.NoError(t, err)
			require.Equal(t, "running", stored.DesiredState)
			require.Equal(t, "restarting", stored.LifecycleState)
			require.Equal(t, "stopped", stored.ActivityState)
			require.False(t, stored.AcceptingPrompts)
			require.True(t, stored.SlotHeld)
			transition, err := durable.ManagedRestartTransition(context.Background(), "restart-agent")
			require.NoError(t, err)
			require.Equal(t, restartRequest().RequestID, transition.OperationID)
			op, err := durable.ManagedOperation(context.Background(), restartRequest().RequestID)
			require.NoError(t, err)
			require.Equal(t, "pending", op.State)
		})
	}
}

func TestReadinessGateRejectsExtraCommandOrActiveTool(t *testing.T) {
	commands := append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands = append(commands, hiddenCommands[0])
	require.Error(t, requireManagedCommands(commands))
	commands = append(append([]string{}, hiddenCommands...), legacyCommands...)
	commands[0] = "/" + commands[0] + ":2"
	require.Error(t, requireManagedCommands(commands))
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, _, err := LoadManifest(path)
	require.NoError(t, err)
	rpc := &readinessRPC{active: expectedManagedActiveTools(manifest)[:18]}
	require.Error(t, readinessGate(context.Background(), rpc, manifest, "session", 1, "generation"))
	require.NotContains(t, rpc.calls, "prompt")
}

package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type coldRecoveryOps struct {
	mu          sync.Mutex
	states      map[int]ProcessProbeState
	signals     []ProcessIdentity
	closed      []ProcessIdentity
	waited      []ProcessIdentity
	failReapFor map[int]bool
}

func (o *coldRecoveryOps) Probe(identity ProcessIdentity) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	state, ok := o.states[identity.PID]
	if !ok {
		return ProcessProbe{State: ProcessUnknown}
	}
	return ProcessProbe{State: state}
}
func (o *coldRecoveryOps) CloseProcessInput(process Process) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = append(o.closed, process.Identity())
	return nil
}
func (o *coldRecoveryOps) SignalVerifiedProcessGroup(identity ProcessIdentity, _ syscall.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	return nil
}
func (o *coldRecoveryOps) SignalVerifiedProcessGroupAfterLeaderExit(identity ProcessIdentity) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	return nil
}
func (o *coldRecoveryOps) WaitForProcessGroupEmpty(_ context.Context, identity ProcessIdentity, _ <-chan error) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.waited = append(o.waited, identity)
	if o.failReapFor != nil && o.failReapFor[identity.PID] {
		return ProcessProbe{State: ProcessOwned}
	}
	o.states[identity.PID] = ProcessAbsent
	return ProcessProbe{State: ProcessAbsent}
}

type identityColdRecoveryOps struct {
	mu      sync.Mutex
	states  map[ProcessIdentity]ProcessProbeState
	signals []ProcessIdentity
	closed  []ProcessIdentity
	waited  []ProcessIdentity
}

func (o *identityColdRecoveryOps) Probe(identity ProcessIdentity) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	return ProcessProbe{State: o.states[identity]}
}
func (o *identityColdRecoveryOps) CloseProcessInput(process Process) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = append(o.closed, process.Identity())
	return nil
}
func (o *identityColdRecoveryOps) SignalVerifiedProcessGroup(identity ProcessIdentity, _ syscall.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	o.states[identity] = ProcessAbsent
	return nil
}
func (o *identityColdRecoveryOps) SignalVerifiedProcessGroupAfterLeaderExit(identity ProcessIdentity) error {
	return o.SignalVerifiedProcessGroup(identity, syscall.SIGKILL)
}
func (o *identityColdRecoveryOps) WaitForProcessGroupEmpty(_ context.Context, identity ProcessIdentity, _ <-chan error) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.waited = append(o.waited, identity)
	return ProcessProbe{State: o.states[identity]}
}

func setupColdRecovery(t *testing.T, factory ProcessFactory, rpcFactory RPCFactory, ops ProcessGroupOps, process *launchProcess, rpc *launchRPC) (*Manager, *store.Store, store.ManagedAgentRecord, *coldRecoveryOps, string) {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "fault-agent", Name: "fault-agent", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "fault-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "fault-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	if rpc == nil {
		rpc = &launchRPC{active: expectedManagedActiveTools(manifest)}
	}
	if process == nil {
		process = &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	}
	if factory == nil {
		factory = &launchFactory{process: process, rpc: rpc}
	}
	if rpcFactory == nil {
		rpcFactory = launchRPCFactory{rpc: rpc}
	}
	if ops == nil {
		ops = &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}
	}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory, RPCFactory: rpcFactory, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	recipe, err := manager.managedLaunchRecipe(agent)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, agent.AgentID, recipe))
	t.Cleanup(func() {
		_ = manager.Close(context.Background())
		_ = durable.Close()
	})
	tracked, _ := ops.(*coldRecoveryOps)
	return manager, durable, agent, tracked, databasePath
}

func TestColdRecoveryManagedLaunchRecipeMismatchOptIn(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name            string
		allowMismatch   bool
		manifestHash    string
		wantFailureCode string
	}{
		{name: "default rejects mismatch", wantFailureCode: "RECOVERY_RECIPE_MISMATCH"},
		{name: "opt in proceeds", allowMismatch: true},
		{name: "opt in still checks manifest", allowMismatch: true, manifestHash: "wrong-manifest", wantFailureCode: "ORPHAN_IDENTITY_UNSAFE"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, durable, agent, _, _ := setupColdRecovery(t, nil, nil, nil, nil, nil)
			stored, err := durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, err)
			require.NotNil(t, stored.LaunchRecipe)

			manager.config.AllowManagedLaunchRecipeMismatch = testCase.allowMismatch
			stored.SelectedModel.ID = "mismatched-model"
			if testCase.manifestHash != "" {
				manager.manifest.CanonicalSHA256 = testCase.manifestHash
			}

			err = manager.validateColdRecoveryPreflight(ctx, stored, storedAgentFence(stored), ProcessAbsent)
			require.NoError(t, err, "cold-recovery preflight persists a safe failure instead of returning it")
			projected, readErr := durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, readErr)
			if testCase.wantFailureCode == "" {
				require.Equal(t, "running", projected.LifecycleState)
				require.Empty(t, projected.LastErrorCode)
				return
			}
			require.Equal(t, "failed", projected.LifecycleState)
			require.Equal(t, testCase.wantFailureCode, projected.LastErrorCode)
		})
	}
}

func TestRecoverFailedStopUsesOnlyExactOwnedFence(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		state      ProcessProbeState
		wantLaunch bool
		wantSignal int
		wantWait   int
	}{
		{name: "absent", state: ProcessAbsent, wantLaunch: true},
		{name: "owned", state: ProcessOwned, wantLaunch: true, wantWait: 1},
		{name: "mismatch", state: ProcessMismatch},
		{name: "unknown", state: ProcessUnknown},
		{name: "present", state: ProcessPresent},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
			rpc := &launchRPC{}
			ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: testCase.state, 902: ProcessOwned}}
			manager, durable, agent, tracked, _ := setupColdRecovery(t, &launchFactory{process: process, rpc: rpc}, launchRPCFactory{rpc: rpc}, ops, process, rpc)
			fence := storedAgentFence(&agent)
			require.NoError(t, durable.MarkManagedFailedForFenceAndOperation(ctx, fence, "fault-operation", "PROCESS_STOP_FAILED", "stop failed"))
			factory := manager.processFactory.(*launchFactory)
			require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
			require.Equal(t, testCase.wantLaunch, factory.startCalls == 1)
			tracked.mu.Lock()
			signals := len(tracked.signals)
			tracked.mu.Unlock()
			require.Equal(t, testCase.wantSignal, signals)
			tracked.mu.Lock()
			waits := len(tracked.waited)
			tracked.mu.Unlock()
			require.Equal(t, testCase.wantWait, waits)
			current, err := durable.ManagedAgent(ctx, agent.AgentID)
			require.NoError(t, err)
			if testCase.wantLaunch {
				require.Equal(t, "running", current.LifecycleState)
				require.True(t, current.AcceptingPrompts)
			} else {
				require.Equal(t, "PROCESS_STOP_FAILED", current.LastErrorCode)
			}
		})
	}
}

func installColdRecoveryTrigger(t *testing.T, databasePath, statement string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = db.Exec(statement)
	require.NoError(t, err)
	return db
}

func seedColdRecoveryAgent(t *testing.T, durable *store.Store, agent store.ManagedAgentRecord, operation store.ManagedOperation) store.ManagedRuntimeFence {
	t.Helper()
	ctx := context.Background()
	// Reserve and install the process through the same starting-state path as production.
	// Cold recovery then sees the durable row after the launch has reached running.
	agent.LifecycleState = "starting"
	agent.ActivityState = "starting"
	agent.AcceptingPrompts = false
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, operation))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: agent.ProcessPID, StartMarker: agent.ProcessStartMarker, Incarnation: agent.ProcessIncarnation}, agent.RuntimeGeneration))
	fence := store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
	if err := durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()); err != nil {
		t.Fatalf("policy confirmation: %v", err)
	}
	canonical := []byte(`{"owner_session_id":"` + agent.PiSessionID + `","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"seed"}`)
	sum := sha256.Sum256(canonical)
	if err := durable.ConfirmManagedChildSnapshot(ctx, fence, store.ManagedChildSnapshot{SnapshotID: "seed", OwnerSessionID: agent.PiSessionID, Sequence: 0, Canonical: canonical, SHA256: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("snapshot confirmation: %v", err)
	}
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	return store.ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
}

func TestRecoverManagedRootColdStartStoreFaultsLeavePriorStageRecoverable(t *testing.T) {
	for _, test := range []struct {
		name      string
		from, to  string
		oldStage  string
		triggered string
	}{
		{name: "claimed to reaping", from: "claimed", to: "reaping", oldStage: "claimed", triggered: "claimed_reaping"},
		{name: "reaping to stopped", from: "reaping", to: "stopped", oldStage: "reaping", triggered: "reaping_stopped"},
		{name: "target reservation", from: "stopped", to: "starting", oldStage: "stopped", triggered: "stopped_starting"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, agent, _, databasePath := setupColdRecovery(t, nil, nil, nil, nil, nil)
			db := installColdRecoveryTrigger(t, databasePath, `CREATE TRIGGER reject_recovery_transition BEFORE UPDATE OF stage ON managed_root_recovery_actions
				WHEN OLD.stage = '`+test.from+`' AND NEW.stage = '`+test.to+`' BEGIN SELECT RAISE(FAIL, 'recovery transition failed'); END`)
			require.Error(t, manager.RecoverManagedRootColdStarts(context.Background()))
			action, err := durable.ManagedRootRecoveryAction(context.Background(), agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, test.oldStage, action.Stage, test.triggered)
			_, err = db.Exec("DROP TRIGGER reject_recovery_transition")
			require.NoError(t, err)
			// A crash-policy write commits before a later recovery-stage trigger
			// can fail. Make that persisted retry due so this test exercises the
			// same recovery transition after the injected fault is removed.
			_, err = db.Exec("UPDATE managed_crash_recovery SET retry_at='1970-01-01T00:00:00.000Z'")
			require.NoError(t, err)
			require.NoError(t, manager.RecoverManagedRootColdStarts(context.Background()))
			action, err = durable.ManagedRootRecoveryAction(context.Background(), agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, "ready", action.Stage)
			require.NoError(t, db.Close())
		})
	}
}

func TestRecoverManagedRootColdStartFailurePathsCleanUpAndTerminalize(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
		start   bool
		open    bool
		ready   string
		want    string
	}{
		{name: "process start", start: true, want: "RECOVERY_TARGET_START_FAILED"},
		{name: "target identity install", trigger: `CREATE TRIGGER reject_target_install BEFORE UPDATE OF target_process_pid ON managed_root_recovery_actions WHEN NEW.target_process_pid = 902 BEGIN SELECT RAISE(FAIL, 'target install failed'); END`, want: "RECOVERY_TARGET_START_FAILED"},
		{name: "target fence install", trigger: `CREATE TRIGGER reject_target_fence BEFORE UPDATE OF process_pid ON managed_agents WHEN OLD.process_pid = 901 AND NEW.process_pid = 902 BEGIN SELECT RAISE(FAIL, 'target fence failed'); END`, want: "RECOVERY_TARGET_START_FAILED"},
		{name: "RPC open", open: true, want: "RECOVERY_READINESS_FAILED"},
		{name: "readiness", ready: "policy-command", want: "RECOVERY_READINESS_FAILED"},
		{name: "CompleteReady", trigger: `CREATE TRIGGER reject_recovery_ready BEFORE UPDATE OF lifecycle_state ON managed_agents WHEN OLD.lifecycle_state = 'starting' AND NEW.lifecycle_state = 'running' BEGIN SELECT RAISE(FAIL, 'ready failed'); END`, want: "RECOVERY_READINESS_FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
			rpc := &launchRPC{failAt: test.ready}
			var factory ProcessFactory
			if test.start {
				factory = ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
					return nil, errors.New("scripted process start failure")
				})
			} else {
				factory = &launchFactory{process: process, rpc: rpc}
			}
			var rpcFactory RPCFactory
			if test.open {
				rpcFactory = RPCFactoryFunc(func(context.Context, Process) (RPC, error) { return nil, errors.New("scripted RPC open failure") })
			}
			manager, durable, agent, ops, databasePath := setupColdRecovery(t, factory, rpcFactory, nil, process, rpc)
			if test.trigger != "" {
				db := installColdRecoveryTrigger(t, databasePath, test.trigger)
				t.Cleanup(func() { _ = db.Close() })
			}
			require.NoError(t, manager.RecoverManagedRootColdStarts(context.Background()))
			action, err := durable.ManagedRootRecoveryAction(context.Background(), agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", action.Stage)
			require.Equal(t, test.want, action.FailureCode)
			current, err := durable.ManagedAgent(context.Background(), agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", current.LifecycleState)
			_, bound := manager.bindingFor(agent.AgentID)
			require.False(t, bound)
			manager.mu.Lock()
			require.NotContains(t, manager.bindings, agent.AgentID)
			require.NotContains(t, manager.processes, agent.AgentID)
			require.NotContains(t, manager.rpcs, agent.AgentID)
			require.NotContains(t, manager.operationIDs, agent.AgentID)
			manager.mu.Unlock()
			if !test.start {
				ops.mu.Lock()
				require.Equal(t, []ProcessIdentity{process.identity}, ops.closed)
				require.Contains(t, ops.waited, process.identity)
				ops.mu.Unlock()
			}
		})
	}
}

func TestRecoverManagedRootColdStartRetriesWakeupWithoutReapingReadyBinding(t *testing.T) {
	ctx := context.Background()
	process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	manager, durable, agent, _, databasePath := setupColdRecovery(t, &launchFactory{process: process, rpc: rpc}, launchRPCFactory{rpc: rpc}, &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}, process, rpc)
	triggerDB := installColdRecoveryTrigger(t, databasePath, "CREATE TRIGGER reject_recovery_wakeup BEFORE INSERT ON messages WHEN NEW.id LIKE 'mrrw_%' BEGIN SELECT RAISE(FAIL, 'wakeup insert failed'); END")
	defer triggerDB.Close()

	err = manager.RecoverManagedRootColdStarts(ctx)
	require.ErrorContains(t, err, "wakeup insert failed")
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.True(t, current.AcceptingPrompts)
	_, bound := manager.bindingFor(agent.AgentID)
	require.True(t, bound, "a retryable wakeup failure must retain the ready replacement binding")
	ready, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Stage)
	var messages, markers int
	require.NoError(t, triggerDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE id LIKE 'mrrw_%'`).Scan(&messages))
	require.NoError(t, triggerDB.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups`).Scan(&markers))
	require.Zero(t, messages)
	require.Zero(t, markers)

	_, err = triggerDB.Exec(`DROP TRIGGER reject_recovery_wakeup`)
	require.NoError(t, err)
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, triggerDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE id LIKE 'mrrw_%'`).Scan(&messages))
	require.NoError(t, triggerDB.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups`).Scan(&markers))
	require.Equal(t, 1, messages)
	require.Equal(t, 1, markers)
	current, err = durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, 902, current.ProcessPID)
	_, bound = manager.bindingFor(agent.AgentID)
	require.True(t, bound)
}

func TestRecoverManagedRootColdStartResumesExactSessionAndPublishesAfterReadiness(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	old := store.ManagedAgentRecord{AgentID: "cold-agent", Name: "cold-agent", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	op := store.ManagedOperation{OperationID: "cold-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "cold-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	oldFence := seedColdRecoveryAgent(t, durable, old, op)
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, op.OperationID))
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("target-runtime", "restart-runtime"), ProcessFactory: &launchFactory{process: &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	recipe, err := manager.managedLaunchRecipe(old)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	current, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.True(t, current.AcceptingPrompts)
	require.Equal(t, old.PiSessionID, current.PiSessionID)
	require.Equal(t, old.SessionEpoch, current.SessionEpoch)
	require.Equal(t, 902, current.ProcessPID)
	require.Equal(t, "new-inc", current.ProcessIncarnation)
	require.Equal(t, "target-runtime", current.RuntimeGeneration)
	require.Equal(t, op.OperationID, current.LaunchOperationID)
	binding, bound := manager.bindingFor(old.AgentID)
	require.True(t, bound)
	binding.serial.Lock()
	require.Equal(t, current.LaunchOperationID, binding.operationID)
	require.Empty(t, binding.lifecycleAction)
	binding.serial.Unlock()
	manager.mu.Lock()
	require.Equal(t, current.LaunchOperationID, manager.operationIDs[old.AgentID])
	manager.mu.Unlock()
	factory := manager.processFactory.(*launchFactory)
	require.Equal(t, old.PiSessionID, factory.spec.SessionID)
	require.Equal(t, ResumeExisting, factory.spec.LaunchMode)
	require.Contains(t, factory.spec.Environment, "MECHA_RPC_RUNTIME_GENERATION=target-runtime")
	require.Contains(t, factory.spec.Environment, "MECHA_RPC_SESSION_EPOCH=1")
	action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "ready", action.Stage)
	require.Equal(t, oldFence.ProcessPID, action.OldProcessPID)
	require.Equal(t, oldFence.ProcessStartMarker, action.OldProcessStartMarker)
	require.Equal(t, oldFence.ProcessIncarnation, action.OldProcessIncarnation)
	require.Equal(t, oldFence.RuntimeGeneration, action.OldRuntimeGeneration)
	require.Equal(t, 902, action.TargetProcessPID)
	require.Equal(t, "new-start", action.TargetProcessStartMarker)
	require.Equal(t, "new-inc", action.TargetProcessIncarnation)
	require.Equal(t, "target-runtime", action.TargetRuntimeGeneration)
	require.NotEqual(t, action.OldProcessPID, action.TargetProcessPID)
	require.NotEqual(t, action.OldProcessStartMarker, action.TargetProcessStartMarker)
	require.NotEqual(t, action.OldProcessIncarnation, action.TargetProcessIncarnation)
	require.NotEqual(t, action.OldRuntimeGeneration, action.TargetRuntimeGeneration)
	// A delayed recovery callback must not retarget a newer owner. Keep the
	// durable fence unchanged and replace only the manager owner map, then call
	// the same restoration helper the stale callback would use.
	targetFence := storedAgentFence(current)
	stale := &managedBinding{
		agentID: old.AgentID, lane: binding.lane, process: binding.process, rpc: binding.rpc,
		identity: binding.identity, sessionID: binding.sessionID, sessionEpoch: binding.sessionEpoch,
		runtimeGeneration: binding.runtimeGeneration, manifestRevision: binding.manifestRevision,
		manifestHash: binding.manifestHash, operationID: action.ActionID, lifecycleAction: "root-recovery",
	}
	newer := &managedBinding{
		agentID: old.AgentID, lane: binding.lane, process: binding.process, rpc: binding.rpc,
		identity: binding.identity, sessionID: binding.sessionID, sessionEpoch: binding.sessionEpoch,
		runtimeGeneration: binding.runtimeGeneration, manifestRevision: binding.manifestRevision,
		manifestHash: binding.manifestHash, operationID: "newer-owner", lifecycleAction: "restart",
	}
	manager.mu.Lock()
	manager.bindings[old.AgentID] = newer
	manager.operationIDs[old.AgentID] = newer.operationID
	manager.mu.Unlock()
	restored, err := manager.restoreRecoveredBindingOwner(ctx, stale, action.ActionID, targetFence)
	require.NoError(t, err)
	require.False(t, restored)
	manager.mu.Lock()
	require.Same(t, newer, manager.bindings[old.AgentID])
	require.Equal(t, "newer-owner", manager.operationIDs[old.AgentID])
	manager.mu.Unlock()
	// Restore the live binding for the remainder of this test's public restart
	// regression check.
	manager.mu.Lock()
	manager.bindings[old.AgentID] = binding
	manager.operationIDs[old.AgentID] = current.LaunchOperationID
	manager.mu.Unlock()
	require.ErrorIs(t, func() error {
		_, readErr := durable.ManagedOperation(ctx, action.ActionID)
		return readErr
	}(), sql.ErrNoRows)

	result, err := manager.ReserveRestart(ctx, RestartRequest{
		AgentID: "cold-agent", Method: "POST", CanonicalPath: "/v1/managed-agents/cold-agent/restart",
		RequestID: "fresh-restart", CanonicalBodyHash: "fresh-restart-body",
	})
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, "fresh-restart", result.Operation.OperationID)
	fresh, err := durable.ManagedOperation(ctx, "fresh-restart")
	require.NoError(t, err)
	require.Equal(t, "fresh-restart", fresh.OperationID)
}

func TestRecoverManagedRootColdStartMissingLaunchOwnerReapsBeforeReady(t *testing.T) {
	ctx := context.Background()
	process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{active: nil}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}
	manager, durable, agent, tracked, databasePath := setupColdRecovery(t, &launchFactory{process: process, rpc: rpc}, launchRPCFactory{rpc: rpc}, ops, process, rpc)
	db := installColdRecoveryTrigger(t, databasePath, "SELECT 1")
	defer db.Close()
	_, err := db.Exec(`UPDATE managed_agents SET launch_operation_id=NULL WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	action, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", action.Stage)
	require.Equal(t, "RECOVERY_LAUNCH_OWNER_MISSING", action.FailureCode)
	require.Equal(t, "managed launch operation owner is unavailable", action.FailureMessage)
	require.NotContains(t, action.FailureMessage, "secret")
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.Name, current.Name)
	require.Equal(t, "running", current.DesiredState)
	require.Equal(t, "failed", current.LifecycleState)
	require.Equal(t, "failed", current.ActivityState)
	require.False(t, current.AcceptingPrompts)
	require.False(t, current.SlotHeld)
	require.Equal(t, process.identity.PID, current.ProcessPID)

	manager.mu.Lock()
	require.NotContains(t, manager.bindings, agent.AgentID)
	require.NotContains(t, manager.processes, agent.AgentID)
	require.NotContains(t, manager.rpcs, agent.AgentID)
	require.NotContains(t, manager.operationIDs, agent.AgentID)
	manager.mu.Unlock()
	tracked.mu.Lock()
	require.Equal(t, []ProcessIdentity{process.identity}, tracked.closed)
	require.Contains(t, tracked.waited, process.identity)
	require.Empty(t, tracked.signals)
	require.Equal(t, ProcessAbsent, tracked.states[process.identity.PID])
	tracked.mu.Unlock()
	operation, err := durable.ManagedOperation(ctx, "fault-operation")
	require.NoError(t, err)
	require.Equal(t, "pending", operation.State)
}

func TestRecoveredTargetCleanupDoesNotReapNewerProcessWithReusedPID(t *testing.T) {
	stale := ProcessIdentity{PID: 902, StartMarker: "stale-start", Incarnation: "stale-inc"}
	newer := ProcessIdentity{PID: 902, StartMarker: "newer-start", Incarnation: "newer-inc"}
	ops := &identityColdRecoveryOps{states: map[ProcessIdentity]ProcessProbeState{stale: ProcessMismatch, newer: ProcessOwned}}
	manager := &Manager{processOps: ops}
	process := &launchProcess{identity: stale, stopped: make(chan struct{})}

	require.False(t, manager.cleanupRecoveredTarget(&managedBinding{process: process}, store.ManagedRuntimeFence{ProcessPID: stale.PID, ProcessStartMarker: stale.StartMarker, ProcessIncarnation: stale.Incarnation}))
	ops.mu.Lock()
	require.Empty(t, ops.signals)
	require.Equal(t, ProcessOwned, ops.states[newer])
	ops.mu.Unlock()
}

func TestRecoverManagedRootColdStartReadinessFailureClosesExactTarget(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	old := store.ManagedAgentRecord{AgentID: "readiness-failure", Name: "readiness-failure", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "readiness-failure-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "readiness-failure-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest), failAt: "policy-command"}
	process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	factory := &launchFactory{process: process, rpc: rpc}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	defer manager.Close(ctx)
	recipe, err := manager.managedLaunchRecipe(old)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", action.Stage)
	require.Equal(t, "RECOVERY_READINESS_FAILED", action.FailureCode)
	current, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", current.LifecycleState)
	ops.mu.Lock()
	defer ops.mu.Unlock()
	require.Equal(t, []ProcessIdentity{process.identity}, ops.closed)
	require.Contains(t, ops.waited, process.identity)
	require.Empty(t, ops.signals)
}

func TestRecoverManagedRootColdStartCleanupFailureRecordsUnsafeOrphan(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	old := store.ManagedAgentRecord{AgentID: "cleanup-failure", Name: "cleanup-failure", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "cleanup-failure-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "cleanup-failure-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest), failAt: "policy-command"}
	process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}, failReapFor: map[int]bool{902: true}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: &launchFactory{process: process, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	defer manager.Close(ctx)
	recipe, err := manager.managedLaunchRecipe(old)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", action.Stage)
	require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", action.FailureCode)
	_, bound := manager.bindingFor(old.AgentID)
	require.False(t, bound)
	ops.mu.Lock()
	defer ops.mu.Unlock()
	require.Equal(t, []ProcessIdentity{process.identity}, ops.closed)
	require.GreaterOrEqual(t, len(ops.signals), 1)
	for _, signal := range ops.signals {
		require.Equal(t, process.identity, signal)
	}
}

func TestFailedColdRecoveryTargetIsCleanedByNextManagerWithoutReplacement(t *testing.T) {
	ctx := context.Background()
	process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{failAt: "policy-command"}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}, failReapFor: map[int]bool{902: true}}
	manager, durable, agent, _, _ := setupColdRecovery(t, &launchFactory{process: process, rpc: rpc}, launchRPCFactory{rpc: rpc}, ops, process, rpc)
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	failed, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Stage)
	require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", failed.FailureCode)
	require.Equal(t, process.identity.PID, failed.TargetProcessPID)
	require.NoError(t, manager.Close(ctx))

	ops.failReapFor = nil
	secondProcess := &launchProcess{identity: ProcessIdentity{PID: 903, StartMarker: "replacement", Incarnation: "replacement-inc"}, stopped: make(chan struct{})}
	second, err := NewManager(ManagerOptions{
		Store: durable, Config: manager.config, Manifest: manager.manifest,
		ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
			t.Fatal("cleanup-only recovery must not start a replacement")
			return secondProcess, nil
		}), ProcessGroupOps: ops,
	})
	require.NoError(t, err)
	defer second.Close(ctx)
	require.NoError(t, second.RecoverManagedRootColdStarts(ctx))
	cleaned, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", cleaned.Stage)
	require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", cleaned.FailureCode)
	require.Zero(t, cleaned.TargetProcessPID)
	require.Empty(t, cleaned.TargetProcessStartMarker)
	require.Empty(t, cleaned.TargetProcessIncarnation)
	ops.mu.Lock()
	require.Contains(t, ops.signals, process.identity)
	ops.mu.Unlock()
}

func TestFailedColdRecoveryTargetUnsafeProbeIsRetainedWithoutSignal(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessMismatch, ProcessUnknown, ProcessPresent} {
		t.Run(string(state), func(t *testing.T) {
			process := &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}
			ops := &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: state}}
			manager, durable, agent, tracked, _ := setupColdRecovery(t, &launchFactory{process: process, rpc: &launchRPC{failAt: "policy-command"}}, launchRPCFactory{rpc: &launchRPC{failAt: "policy-command"}}, ops, process, nil)
			recipe, err := manager.managedLaunchRecipe(agent)
			require.NoError(t, err)
			action, _, err := durable.ClaimManagedRootRecovery(context.Background(), agent.AgentID, storedAgentFence(&agent), recipe.SHA256, "target-runtime")
			require.NoError(t, err)
			require.NoError(t, durable.AdvanceManagedRootRecovery(context.Background(), action.ActionID, "claimed", "reaping"))
			require.NoError(t, durable.AdvanceManagedRootRecovery(context.Background(), action.ActionID, "reaping", "stopped"))
			require.NoError(t, durable.BeginManagedRootRecoveryTarget(context.Background(), action.ActionID, "target-runtime"))
			require.NoError(t, durable.InstallManagedRootRecoveryTarget(context.Background(), action.ActionID, store.ManagedProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, "target-runtime"))
			require.NoError(t, durable.FailManagedRootRecovery(context.Background(), action.ActionID, "RECOVERY_READINESS_FAILED", "initial cleanup failed"))
			require.NoError(t, manager.RecoverManagedRootColdStarts(context.Background()))
			failed, err := durable.ManagedRootRecoveryAction(context.Background(), agent.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", failed.Stage)
			require.Equal(t, 902, failed.TargetProcessPID)
			tracked.mu.Lock()
			require.Empty(t, tracked.signals)
			tracked.mu.Unlock()
		})
	}
}

func TestManagedRootRecoveryPruneRetainsExpiredFailedTargetUntilCleared(t *testing.T) {
	ctx := context.Background()
	manager, durable, agent, _, databasePath := setupColdRecovery(t, nil, nil, nil, nil, nil)
	recipe, err := manager.managedLaunchRecipe(agent)
	require.NoError(t, err)
	action, _, err := durable.ClaimManagedRootRecovery(ctx, agent.AgentID, storedAgentFence(&agent), recipe.SHA256, "target-runtime")
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"))
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"))
	require.NoError(t, durable.BeginManagedRootRecoveryTarget(ctx, action.ActionID, action.TargetRuntimeGeneration))
	require.NoError(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, store.ManagedProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, action.TargetRuntimeGeneration))
	require.NoError(t, durable.FailManagedRootRecovery(ctx, action.ActionID, "ORPHAN_IDENTITY_UNSAFE", "target retained"))
	db := installColdRecoveryTrigger(t, databasePath, "SELECT 1")
	defer db.Close()
	_, err = db.Exec(`UPDATE managed_root_recovery_actions SET expires_at=? WHERE action_id=?`, "2000-01-01T00:00:00.000Z", action.ActionID)
	require.NoError(t, err)
	removed, err := durable.PruneManagedRootRecovery(ctx)
	require.NoError(t, err)
	require.Zero(t, removed)
	stored, err := durable.ManagedRootRecoveryActionByID(ctx, action.ActionID)
	require.NoError(t, err)
	require.Equal(t, 902, stored.TargetProcessPID)
	require.NoError(t, durable.ClearManagedRootRecoveryTargetAfterVerifiedAbsence(ctx, action.ActionID, store.ManagedProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}))
	removed, err = durable.PruneManagedRootRecovery(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.NoError(t, manager.Close(ctx))
}

func TestRecoverManagedRootColdStartDoesNotStartAfterReadyTargetOnSecondRouter(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	old := store.ManagedAgentRecord{AgentID: "ready-target", Name: "ready-target", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "ready-target-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "ready-target-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	recipe, err := (&Manager{config: config}).managedLaunchRecipe(old)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{old.ProcessPID: ProcessAbsent, 902: ProcessOwned, 903: ProcessOwned}}
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	first, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("target-runtime"), ProcessFactory: &launchFactory{process: &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	require.NoError(t, first.RecoverManagedRootColdStarts(ctx))
	target, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, 902, target.ProcessPID)
	action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "ready", action.Stage)

	secondProcess := &launchProcess{identity: ProcessIdentity{PID: 903, StartMarker: "second-start", Incarnation: "second-inc"}, stopped: make(chan struct{})}
	second, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("second-runtime"), ProcessFactory: &launchFactory{process: secondProcess, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops})
	require.NoError(t, err)
	require.NoError(t, second.RecoverManagedRootColdStarts(ctx))
	current, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.True(t, current.AcceptingPrompts)
	require.Equal(t, 903, current.ProcessPID)
	require.Equal(t, "second-inc", current.ProcessIncarnation)
	require.Equal(t, "second-runtime", current.RuntimeGeneration)
	_, bound := second.bindingFor(old.AgentID)
	require.True(t, bound, "a running recovered agent must have a live binding")
	secondAction, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "ready", secondAction.Stage)
	require.Equal(t, 903, secondAction.TargetProcessPID)
	priorAction, err := durable.ManagedRootRecoveryActionByID(ctx, action.ActionID)
	require.NoError(t, err)
	require.Equal(t, "failed", priorAction.Stage)
	require.Equal(t, "RECOVERY_SUPERSEDED", priorAction.FailureCode)
	require.NotEqual(t, priorAction.ActionID, secondAction.ActionID)
	require.NoError(t, first.Close(ctx))
	require.NoError(t, second.Close(ctx))
}

func TestRecoverManagedRootColdStartClosesIncompleteInstalledTargetWithoutStartingAgain(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "incomplete-target", Name: "incomplete-target", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 901, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	old := seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "incomplete-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "incomplete-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	recipe, err := (&Manager{config: config}).managedLaunchRecipe(agent)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, agent.AgentID, recipe))
	action, _, err := durable.ClaimManagedRootRecovery(ctx, agent.AgentID, old, recipe.SHA256, "target-runtime")
	require.NoError(t, err)
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "claimed", "reaping"))
	require.NoError(t, durable.AdvanceManagedRootRecovery(ctx, action.ActionID, "reaping", "stopped"))
	require.NoError(t, durable.BeginManagedRootRecoveryTarget(ctx, action.ActionID, action.TargetRuntimeGeneration))
	require.NoError(t, durable.InstallManagedRootRecoveryTarget(ctx, action.ActionID, store.ManagedProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, action.TargetRuntimeGeneration))
	target := old
	target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration = 902, "new-start", "new-inc", "target-runtime"
	require.NoError(t, durable.InstallManagedRootRecoveryFence(ctx, old, target))
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{902: ProcessOwned}}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
		t.Fatal("an installed recovery target must not be started again")
		return nil, nil
	}), ProcessGroupOps: ops})
	require.NoError(t, err)
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", current.LifecycleState)
	require.Equal(t, "RECOVERY_TARGET_UNKNOWN", current.LastErrorCode)
	stored, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.Stage)
	require.NoError(t, manager.Close(ctx))
}

func TestRecoverManagedRootColdStartNeverSignalsMismatchedOrUnknownProcess(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessMismatch, ProcessUnknown, ProcessPresent} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
			require.NoError(t, err)
			defer durable.Close()
			config, paths := managedLaunchConfigFixture(t)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			old := store.ManagedAgentRecord{AgentID: "unsafe-agent", Name: "unsafe-agent", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 903, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
			seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "unsafe-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "unsafe-operation", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
			manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("target-runtime"), ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
				t.Fatal("unsafe process must not start")
				return nil, nil
			}), ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{903: state}}})
			require.NoError(t, err)
			defer manager.Close(ctx)
			recipe, err := manager.managedLaunchRecipe(old)
			require.NoError(t, err)
			require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))
			ops := manager.processOps.(*coldRecoveryOps)
			require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
			action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", action.Stage)
			require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", action.FailureCode)
			ops.mu.Lock()
			require.Empty(t, ops.signals)
			ops.mu.Unlock()
		})
	}
}

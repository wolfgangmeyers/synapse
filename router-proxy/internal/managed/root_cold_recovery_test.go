package managed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type coldRecoveryOps struct {
	mu      sync.Mutex
	states  map[int]ProcessProbeState
	signals []ProcessIdentity
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
func (o *coldRecoveryOps) CloseProcessInput(Process) error { return nil }
func (o *coldRecoveryOps) SignalVerifiedProcessGroup(identity ProcessIdentity, _ syscall.Signal) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.signals = append(o.signals, identity)
	return nil
}
func (o *coldRecoveryOps) WaitForProcessGroupEmpty(_ context.Context, identity ProcessIdentity, _ <-chan error) ProcessProbe {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.states[identity.PID] = ProcessAbsent
	return ProcessProbe{State: ProcessAbsent}
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
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("target-runtime"), ProcessFactory: &launchFactory{process: &launchProcess{identity: ProcessIdentity{PID: 902, StartMarker: "new-start", Incarnation: "new-inc"}, stopped: make(chan struct{})}, rpc: rpc}, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{901: ProcessAbsent, 902: ProcessOwned}}})
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

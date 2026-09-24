package managed

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// crashRecoveryClock keeps wall and monotonic time in one fixture. Manager's
// current Clock seam exposes only Now; retaining both here makes the missing
// monotonic input explicit without adding a production seam in a test-only
// change.
type crashRecoveryClock struct {
	wall *deterministic.FixedClock
	mu   sync.Mutex
	mono time.Duration
}

func newCrashRecoveryClock() *crashRecoveryClock {
	return &crashRecoveryClock{wall: deterministic.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))}
}

func (c *crashRecoveryClock) Now() time.Time { return c.wall.Now() }

func (c *crashRecoveryClock) Advance(delta time.Duration) error {
	if err := c.wall.Advance(delta); err != nil {
		return err
	}
	c.mu.Lock()
	c.mono += delta
	c.mu.Unlock()
	return nil
}

func (c *crashRecoveryClock) Monotonic() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

// crashRecoveryProcessFactory is a deterministic process identity seam. The
// started channel is the process-start barrier; callers never poll or sleep.
type crashRecoveryProcessFactory struct {
	mu        sync.Mutex
	processes []Process
	rpc       *launchRPC
	calls     int
	started   chan CommandSpec
}

func (f *crashRecoveryProcessFactory) Start(_ context.Context, spec CommandSpec) (Process, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	f.mu.Unlock()
	f.started <- spec
	if index >= len(f.processes) {
		return nil, errors.New("deterministic process factory exhausted")
	}
	if f.rpc != nil {
		f.rpc.session = spec.SessionID
		if spec.SessionPath != "" {
			f.rpc.sessionFile = spec.SessionPath
		}
	}
	return f.processes[index], nil
}

func (f *crashRecoveryProcessFactory) StartCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newCrashRecoveryManager(t *testing.T, durable *store.Store, config Config, manifest Manifest, clock deterministic.Clock, ids deterministic.IDSource, factory ProcessFactory, rpc *launchRPC, ops ProcessGroupOps) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerOptions{
		Store:                      durable,
		Config:                     config,
		Manifest:                   manifest,
		Clock:                      clock,
		IDSource:                   ids,
		ProcessFactory:             factory,
		RPCFactory:                 launchRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
		ProcessGroupOps:            ops,
	})
	require.NoError(t, err)
	return manager
}

func TestManagedCrashRecoveryBehavioralFunctional_PersistedBackoffResumesAfterRouterRestart(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	first := &launchProcess{identity: ProcessIdentity{PID: 2101, StartMarker: "start-1", Incarnation: "inc-1"}, stopped: make(chan struct{}), waitDone: make(chan struct{}), waitErr: errors.New("signal: killed")}
	second := &launchProcess{identity: ProcessIdentity{PID: 2102, StartMarker: "start-2", Incarnation: "inc-2"}, stopped: make(chan struct{}), waitDone: make(chan struct{})}
	rpc := &launchRPC{promptDone: make(chan struct{}), active: expectedManagedActiveTools(manifest)}
	factory := &crashRecoveryProcessFactory{processes: []Process{first, second}, rpc: rpc, started: make(chan CommandSpec, 4)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{2101: ProcessOwned, 2102: ProcessOwned}}
	manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("session-1", "runtime-1"), factory, rpc, ops)
	projection := make(chan struct{}, 4)
	manager.SetProjectionPublisher(func() { projection <- struct{}{} })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "crash-agent", Cwd: paths["work"], InitialInstruction: "first instruction", RequestID: "launch-1"})
	require.NoError(t, err)
	select {
	case <-rpc.promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("managed launch did not reach its readiness barrier")
	}
	require.Equal(t, 1, factory.StartCalls())

	// Reap is the only crash injection. The fake wait barrier proves the
	// manager observed the process exit before the durable restart boundary.
	first.Reap()
	select {
	case <-first.waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("process waiter did not observe the injected crash")
	}
	ops.mu.Lock()
	ops.states[first.identity.PID] = ProcessAbsent
	ops.mu.Unlock()
	select {
	case <-projection:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not publish the durable crash transition")
	}
	stored, err := durable.ManagedAgent(ctx, "crash-agent")
	require.NoError(t, err)
	retryBefore, err := durable.ManagedCrashRecovery(ctx, stored.AgentID)
	require.NoError(t, err)
	versionBefore, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	_, err = durable.RecordManagedUnexpectedExit(ctx, storedAgentFence(stored), "launch-1", "duplicate exit callback")
	require.NoError(t, err)
	retryAfter, err := durable.ManagedCrashRecovery(ctx, stored.AgentID)
	require.NoError(t, err)
	require.Equal(t, retryBefore.RetryAt, retryAfter.RetryAt)
	versionAfter, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionBefore, versionAfter, "duplicate exit callback must not publish a second transition")
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, durable.Close())

	// Reopen the same SQLite file with the same controlled clock. Before the
	// one-second due boundary, recovery must not spawn a duplicate target.
	durable, err = store.Open(ctx, databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	rpc = &launchRPC{active: expectedManagedActiveTools(manifest)}
	readinessStarted := make(chan struct{})
	releaseReadiness := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseReadiness:
		default:
			close(releaseReadiness)
		}
	})
	rpc.beforeState = func() {
		close(readinessStarted)
		<-releaseReadiness
	}
	factory = &crashRecoveryProcessFactory{processes: []Process{second}, rpc: rpc, started: make(chan CommandSpec, 2)}
	manager = newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("recovery-runtime"), factory, rpc, ops)
	defer manager.Close(ctx)

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 0, factory.StartCalls(), "recovery must honor the persisted first-crash backoff")
	require.NoError(t, clock.Advance(time.Second-time.Millisecond))
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 0, factory.StartCalls(), "recovery must wait until the exact due timestamp")
	require.NoError(t, clock.Advance(time.Millisecond))
	recoveryDone := make(chan error, 1)
	go func() { recoveryDone <- manager.RecoverManagedRootColdStarts(ctx) }()
	var startedSpec CommandSpec
	select {
	case startedSpec = <-factory.started:
	case <-time.After(2 * time.Second):
		current, readErr := durable.ManagedAgent(ctx, "crash-agent")
		t.Fatalf("persisted crash backoff did not resume at its exact due time (read error: %v, agent: %+v, process starts: %d)", readErr, current, factory.StartCalls())
	}
	require.Equal(t, ResumeExisting, startedSpec.LaunchMode)
	require.Equal(t, "session-1", startedSpec.SessionID)
	select {
	case <-readinessStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not reach the readiness barrier")
	}
	current, err := durable.ManagedAgent(ctx, "crash-agent")
	require.NoError(t, err)
	require.False(t, current.AcceptingPrompts, "replacement cannot accept before readiness")
	require.Equal(t, "starting", current.LifecycleState)
	require.Equal(t, "starting", current.ActivityState)
	close(releaseReadiness)
	require.NoError(t, <-recoveryDone)
	current, err = durable.ManagedAgent(ctx, "crash-agent")
	require.NoError(t, err)
	require.True(t, current.AcceptingPrompts)
	require.Equal(t, "running", current.LifecycleState)
	// Re-running the recovery scan at the same clock position must not start a
	// second process for the same persisted retry.
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, factory.StartCalls())
}

func TestManagedCrashRecoveryBehavioralFunctional_ColdRestartRejectsUnsafeOrphanIdentity(t *testing.T) {
	for _, probeState := range []ProcessProbeState{ProcessMismatch, ProcessUnknown} {
		t.Run(string(probeState), func(t *testing.T) {
			ctx := context.Background()
			clock := newCrashRecoveryClock()
			databasePath := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, databasePath, store.Options{Clock: clock})
			require.NoError(t, err)
			config, paths := managedLaunchConfigFixture(t)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			old := store.ManagedAgentRecord{
				AgentID: "unsafe-orphan", Name: "unsafe-orphan", SlotHeld: true, Cwd: paths["work"],
				SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
				DesiredState:  "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
				PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 2201, ProcessStartMarker: "stored-start", ProcessIncarnation: "stored-inc", RuntimeGeneration: "old-runtime",
				ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256,
			}
			seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "unsafe-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "unsafe-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})

			firstFactory := &crashRecoveryProcessFactory{started: make(chan CommandSpec, 1)}
			ops := &coldRecoveryOps{states: map[int]ProcessProbeState{old.ProcessPID: probeState}}
			firstRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
			firstManager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("unused"), firstFactory, firstRPC, ops)
			recipe, err := firstManager.managedLaunchRecipe(old)
			require.NoError(t, err)
			require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))
			require.NoError(t, firstManager.Close(ctx))
			require.NoError(t, durable.Close())

			// A second manager owns the cold-start attempt, but the exact stored
			// identity is deliberately not owned by the probe.
			durable, err = store.Open(ctx, databasePath, store.Options{Clock: clock})
			require.NoError(t, err)
			defer durable.Close()
			secondFactory := &crashRecoveryProcessFactory{started: make(chan CommandSpec, 1)}
			secondRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
			secondManager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("replacement"), secondFactory, secondRPC, ops)
			defer secondManager.Close(ctx)

			require.NoError(t, secondManager.RecoverManagedRootColdStarts(ctx))
			current, err := durable.ManagedAgent(ctx, old.AgentID)
			require.NoError(t, err)
			require.Equal(t, "running", current.DesiredState)
			require.Equal(t, "failed", current.LifecycleState)
			require.False(t, current.AcceptingPrompts)
			require.False(t, current.SlotHeld)
			require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", current.LastErrorCode)
			require.Equal(t, old.ProcessPID, current.ProcessPID)
			require.Equal(t, old.ProcessStartMarker, current.ProcessStartMarker)
			require.Equal(t, old.ProcessIncarnation, current.ProcessIncarnation)
			require.Equal(t, old.RuntimeGeneration, current.RuntimeGeneration)
			action, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", action.Stage)
			require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", action.FailureCode)
			require.Equal(t, old.ProcessPID, action.OldProcessPID)
			require.Equal(t, old.ProcessStartMarker, action.OldProcessStartMarker)
			require.Equal(t, old.ProcessIncarnation, action.OldProcessIncarnation)
			require.Equal(t, old.RuntimeGeneration, action.OldRuntimeGeneration)
			require.Zero(t, action.TargetProcessPID)
			require.Empty(t, action.TargetProcessStartMarker)
			require.Empty(t, action.TargetProcessIncarnation)
			require.Equal(t, 0, secondFactory.StartCalls())
			ops.mu.Lock()
			require.Empty(t, ops.signals)
			require.Empty(t, ops.waited)
			ops.mu.Unlock()
		})
	}
}

func TestManagedOrderlyCloseReopensFreshDesiredRunningChild(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	first := &launchProcess{identity: ProcessIdentity{PID: 2301, StartMarker: "orderly-start-1", Incarnation: "orderly-inc-1"}, stopped: make(chan struct{})}
	second := &launchProcess{identity: ProcessIdentity{PID: 2302, StartMarker: "orderly-start-2", Incarnation: "orderly-inc-2"}, stopped: make(chan struct{})}
	rpc := &launchRPC{promptDone: make(chan struct{}), active: expectedManagedActiveTools(manifest)}
	factory := &crashRecoveryProcessFactory{processes: []Process{first}, rpc: rpc, started: make(chan CommandSpec, 2)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{2301: ProcessOwned, 2302: ProcessOwned}}
	manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("orderly-session", "orderly-runtime"), factory, rpc, ops)

	_, err = manager.Launch(ctx, LaunchRequest{Name: "orderly-agent", Cwd: paths["work"], RequestID: "orderly-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		current, readErr := durable.ManagedAgent(ctx, "orderly-agent")
		return readErr == nil && current.ProcessPID == first.identity.PID && current.LifecycleState == "running" && current.AcceptingPrompts
	}, 2*time.Second, time.Millisecond)
	_, err = durable.AppendManagedCompleteHistory(ctx, "orderly-agent", "test", "info", "durable history")
	require.NoError(t, err)
	beforeClose, err := durable.ManagedAgent(ctx, "orderly-agent")
	require.NoError(t, err)
	beforeHistory, err := durable.ManagedHistoryPage(ctx, "orderly-agent", "complete", 0, 10)
	require.NoError(t, err)
	beforeCrash, err := durable.ManagedCrashRecovery(ctx, "orderly-agent")
	require.NoError(t, err)

	require.NoError(t, manager.Close(ctx))
	ops.mu.Lock()
	require.Empty(t, ops.signals, "successful orderly stop must not signal the old process")
	require.Empty(t, ops.waited, "successful orderly stop must not reap the old process")
	ops.mu.Unlock()
	afterClose, err := durable.ManagedAgent(ctx, "orderly-agent")
	require.NoError(t, err)
	require.Equal(t, "running", afterClose.DesiredState)
	require.Equal(t, beforeClose.Cwd, afterClose.Cwd)
	require.Equal(t, beforeClose.SelectedModel, afterClose.SelectedModel)
	require.Equal(t, beforeClose.PiSessionID, afterClose.PiSessionID)
	marker, err := durable.ManagedOrderlyStopMarker(ctx, "orderly-agent")
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.True(t, marker.Confirmed, "successful Close confirms the orderly-stop marker")
	require.Equal(t, beforeClose.ProcessPID, marker.ProcessPID)
	require.Equal(t, beforeClose.ProcessStartMarker, marker.ProcessStartMarker)
	require.Equal(t, beforeClose.ProcessIncarnation, marker.ProcessIncarnation)
	require.Equal(t, beforeClose.RuntimeGeneration, marker.RuntimeGeneration)
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	ops.mu.Lock()
	ops.states[2301] = ProcessMismatch
	probesBeforeRecovery := len(ops.probes)
	ops.mu.Unlock()
	recoveryConfig := config
	// The persisted recipe still contains config's launch paths. Orderly
	// recovery must use the current manager configuration instead.
	recoveryConfig.PiExecutable = paths["helper"]
	recoveryConfig.SessionRoot = paths["home"]
	recoveryConfig.MechaRPCExtension = paths["cursor"]
	recoveryConfig.PiSubagentsExtension = paths["subagents"]
	recoveryConfig.CursorExtension = paths["mecha"]
	recoveryConfig.RouterURL = "http://127.0.0.1:19999"
	recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{second}, rpc: recoveryRPC, started: make(chan CommandSpec, 2)}
	recovery := newCrashRecoveryManager(t, durable, recoveryConfig, manifest, clock, deterministic.NewSequenceIDSource("replacement-runtime"), recoveryFactory, recoveryRPC, ops)
	defer recovery.Close(ctx)

	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls(), "orderly router shutdown must start one fresh child")
	started := <-recoveryFactory.started
	require.Equal(t, ResumeExisting, started.LaunchMode)
	require.Equal(t, recoveryConfig.PiExecutable, started.PiExecutable)
	require.Equal(t, recoveryConfig.SessionRoot, started.SessionRoot)
	require.Equal(t, beforeClose.Cwd, started.Cwd)
	require.Equal(t, beforeClose.SelectedModel.Provider, started.Provider)
	require.Equal(t, beforeClose.SelectedModel.ID, started.ModelID)
	require.Equal(t, beforeClose.PiSessionID, started.SessionID)
	require.Equal(t, recoveryConfig.MechaRPCExtension, started.MechaRPCExtension)
	require.Equal(t, recoveryConfig.PiSubagentsExtension, started.PiSubagentsExtension)
	require.Equal(t, recoveryConfig.CursorExtension, started.CursorExtension)
	require.Contains(t, started.Environment, "MECHA_ROUTER_URL="+recoveryConfig.RouterURL)

	current, err := durable.ManagedAgent(ctx, "orderly-agent")
	require.NoError(t, err)
	require.Equal(t, "running", current.DesiredState)
	require.Equal(t, "running", current.LifecycleState)
	require.True(t, current.AcceptingPrompts)
	require.Equal(t, beforeClose.Cwd, current.Cwd)
	require.Equal(t, beforeClose.SelectedModel, current.SelectedModel)
	require.Equal(t, beforeClose.PiSessionID, current.PiSessionID)
	require.NotEqual(t, beforeClose.ProcessPID, current.ProcessPID)
	require.NotEqual(t, beforeClose.ProcessStartMarker, current.ProcessStartMarker)
	require.NotEqual(t, beforeClose.ProcessIncarnation, current.ProcessIncarnation)
	require.NotEqual(t, beforeClose.RuntimeGeneration, current.RuntimeGeneration)
	require.Equal(t, second.identity.StartMarker, current.ProcessStartMarker)
	require.Equal(t, second.identity.Incarnation, current.ProcessIncarnation)
	require.Contains(t, started.Environment, "MECHA_RPC_RUNTIME_GENERATION="+current.RuntimeGeneration)
	afterHistory, err := durable.ManagedHistoryPage(ctx, "orderly-agent", "complete", 0, 10)
	require.NoError(t, err)
	require.Equal(t, beforeHistory.Entries, afterHistory.Entries)
	afterCrash, err := durable.ManagedCrashRecovery(ctx, "orderly-agent")
	require.NoError(t, err)
	require.Equal(t, beforeCrash.CrashTimes, afterCrash.CrashTimes)
	require.Empty(t, afterCrash.RetryAt)
	marker, err = durable.ManagedOrderlyStopMarker(ctx, "orderly-agent")
	require.NoError(t, err)
	require.Nil(t, marker, "a successful orderly replacement consumes its marker")
	action, err := durable.ManagedRootRecoveryAction(ctx, "orderly-agent")
	require.NoError(t, err)
	require.Equal(t, current.ProcessPID, action.TargetProcessPID)
	require.Equal(t, current.ProcessStartMarker, action.TargetProcessStartMarker)
	require.Equal(t, current.ProcessIncarnation, action.TargetProcessIncarnation)
	require.Equal(t, current.RuntimeGeneration, action.TargetRuntimeGeneration)
	ops.mu.Lock()
	require.Empty(t, ops.signals, "orderly recovery must not signal the old process")
	require.Empty(t, ops.waited, "orderly recovery must not reap the old process")
	for _, probe := range ops.probes[probesBeforeRecovery:] {
		require.NotEqual(t, first.identity, probe, "confirmed orderly recovery must not probe the old fence")
	}
	ops.mu.Unlock()
}

func TestManagedUnconfirmedOrderlyMarkerProbesBeforeReplacement(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		state      ProcessProbeState
		wait       bool
		wantLaunch bool
	}{
		{name: "absent", state: ProcessAbsent, wantLaunch: true},
		{name: "owned", state: ProcessOwned, wait: true, wantLaunch: true},
		{name: "mismatch", state: ProcessMismatch},
		{name: "present", state: ProcessPresent},
		{name: "unknown", state: ProcessUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newCrashRecoveryClock()
			path := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			defer durable.Close()
			config, paths := managedLaunchConfigFixture(t)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			old := store.ManagedAgentRecord{AgentID: "unconfirmed-" + testCase.name, Name: "unconfirmed-" + testCase.name, SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "unconfirmed-session", SessionEpoch: 1, ProcessPID: 2301, ProcessStartMarker: "unconfirmed-old", ProcessIncarnation: "unconfirmed-inc", RuntimeGeneration: "unconfirmed-old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
			fence := seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "unconfirmed-launch-" + testCase.name, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "unconfirmed-launch-" + testCase.name, CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
			require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))
			marker, err := durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
			require.NoError(t, err)
			require.NotNil(t, marker)
			require.False(t, marker.Confirmed)
			recipe, err := (&Manager{config: config}).managedLaunchRecipe(old)
			require.NoError(t, err)
			require.NoError(t, durable.PutManagedLaunchRecipe(ctx, old.AgentID, recipe))
			replacement := &launchProcess{identity: ProcessIdentity{PID: 2302, StartMarker: "unconfirmed-new", Incarnation: "unconfirmed-new-inc"}, stopped: make(chan struct{})}
			rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
			factory := &crashRecoveryProcessFactory{processes: []Process{replacement}, rpc: rpc, started: make(chan CommandSpec, 1)}
			ops := &coldRecoveryOps{states: map[int]ProcessProbeState{fence.ProcessPID: testCase.state, replacement.identity.PID: ProcessOwned}}
			manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("unconfirmed-recovery"), factory, rpc, ops)
			defer manager.Close(ctx)
			require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
			require.Equal(t, testCase.wantLaunch, factory.StartCalls() == 1)
			marker, err = durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
			require.NoError(t, err)
			if testCase.wantLaunch {
				require.Nil(t, marker)
			} else {
				require.NotNil(t, marker)
				current, readErr := durable.ManagedAgent(ctx, fence.AgentID)
				require.NoError(t, readErr)
				require.Equal(t, "failed", current.LifecycleState)
				require.Equal(t, "ORPHAN_IDENTITY_UNSAFE", current.LastErrorCode)
				require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
				require.Equal(t, 0, factory.StartCalls())
			}
			ops.mu.Lock()
			if testCase.wait {
				require.Contains(t, ops.waited, oldFenceIdentity(fence))
			} else {
				require.NotContains(t, ops.waited, oldFenceIdentity(fence))
			}
			ops.mu.Unlock()
		})
	}
}

func TestManagedOrderlyStopFailureRetainsMarkerAndUsesOnlyExactFence(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		state      ProcessProbeState
		wantLaunch bool
		wantWait   int
	}{
		{name: "owned", state: ProcessOwned, wantLaunch: true, wantWait: 1},
		{name: "mismatch", state: ProcessMismatch},
		{name: "unknown", state: ProcessUnknown},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newCrashRecoveryClock()
			path := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			config, paths := managedLaunchConfigFixture(t)
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			oldProcess := &launchProcess{identity: ProcessIdentity{PID: 2351, StartMarker: "orderly-failed-start", Incarnation: "orderly-failed-inc"}, stopped: make(chan struct{}), stopErr: errors.New("scripted stop failure")}
			oldRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
			factory := &crashRecoveryProcessFactory{processes: []Process{oldProcess}, rpc: oldRPC, started: make(chan CommandSpec, 1)}
			ops := &coldRecoveryOps{states: map[int]ProcessProbeState{oldProcess.identity.PID: testCase.state, 2352: ProcessOwned}}
			manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("orderly-session", "orderly-runtime"), factory, oldRPC, ops)
			_, err = manager.Launch(ctx, LaunchRequest{Name: "orderly-failed", Cwd: paths["work"], RequestID: "orderly-failed-launch"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				current, readErr := durable.ManagedAgent(ctx, "orderly-failed")
				return readErr == nil && current.LifecycleState == "running" && current.AcceptingPrompts
			}, 2*time.Second, time.Millisecond)
			beforeClose, err := durable.ManagedAgent(ctx, "orderly-failed")
			require.NoError(t, err)
			require.Error(t, manager.Close(ctx))
			failed, err := durable.ManagedAgent(ctx, "orderly-failed")
			require.NoError(t, err)
			require.Equal(t, "failed", failed.LifecycleState)
			require.Equal(t, "PROCESS_STOP_FAILED", failed.LastErrorCode)
			require.Equal(t, beforeClose.ProcessPID, failed.ProcessPID)
			marker, err := durable.ManagedOrderlyStopMarker(ctx, "orderly-failed")
			require.NoError(t, err)
			require.NotNil(t, marker)
			require.False(t, marker.Confirmed, "a failed Process.Stop must leave the marker unconfirmed")
			require.Equal(t, beforeClose.ProcessPID, marker.ProcessPID)
			require.Equal(t, beforeClose.RuntimeGeneration, marker.RuntimeGeneration)
			require.NoError(t, durable.Close())

			durable, err = store.Open(ctx, path, store.Options{Clock: clock})
			require.NoError(t, err)
			defer durable.Close()
			replacement := &launchProcess{identity: ProcessIdentity{PID: 2352, StartMarker: "orderly-replacement-start", Incarnation: "orderly-replacement-inc"}, stopped: make(chan struct{})}
			recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
			recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{replacement}, rpc: recoveryRPC, started: make(chan CommandSpec, 1)}
			recovery := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("orderly-recovery"), recoveryFactory, recoveryRPC, ops)
			defer recovery.Close(ctx)
			require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
			require.Equal(t, testCase.wantLaunch, recoveryFactory.StartCalls() == 1)
			require.Equal(t, testCase.wantWait, len(ops.waited))
			if testCase.wantLaunch {
				current, readErr := durable.ManagedAgent(ctx, "orderly-failed")
				require.NoError(t, readErr)
				require.Equal(t, "running", current.LifecycleState)
				require.True(t, current.AcceptingPrompts)
				marker, readErr = durable.ManagedOrderlyStopMarker(ctx, "orderly-failed")
				require.NoError(t, readErr)
				require.Nil(t, marker)
			} else {
				current, readErr := durable.ManagedAgent(ctx, "orderly-failed")
				require.NoError(t, readErr)
				require.Equal(t, "PROCESS_STOP_FAILED", current.LastErrorCode)
				marker, readErr = durable.ManagedOrderlyStopMarker(ctx, "orderly-failed")
				require.NoError(t, readErr)
				require.NotNil(t, marker)
			}
		})
	}
}

func TestManagerCloseRetriesOrderlyMarkerAfterPersistenceFault(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	process := &launchProcess{identity: ProcessIdentity{PID: 2411, StartMarker: "close-retry-start", Incarnation: "close-retry-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("close-retry-session", "close-retry-runtime"), &crashRecoveryProcessFactory{processes: []Process{process}, rpc: rpc, started: make(chan CommandSpec, 1)}, rpc, &coldRecoveryOps{states: map[int]ProcessProbeState{process.identity.PID: ProcessOwned}})
	_, err = manager.Launch(ctx, LaunchRequest{Name: "close-retry", Cwd: paths["work"], RequestID: "close-retry-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "close-retry")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 2*time.Second, time.Millisecond)
	faultDB := installColdRecoveryTrigger(t, path, `CREATE TRIGGER reject_orderly_marker BEFORE INSERT ON managed_orderly_stop_markers BEGIN SELECT RAISE(FAIL, 'marker persistence failed'); END`)
	require.Error(t, manager.Close(ctx))
	require.Zero(t, process.Stops(), "a marker failure must fence before Process.Stop")
	_, err = faultDB.Exec(`DROP TRIGGER reject_orderly_marker`)
	require.NoError(t, err)
	require.NoError(t, manager.Close(ctx))
	require.Equal(t, 1, process.Stops(), "retry must stop the retained process exactly once")
	manager.mu.Lock()
	_, bound := manager.bindings["close-retry"]
	manager.mu.Unlock()
	require.False(t, bound)
	marker, err := durable.ManagedOrderlyStopMarker(ctx, "close-retry")
	require.NoError(t, err)
	require.NotNil(t, marker)
}

func TestManagedOrderlyRecoveryReusesClaimedTargetGeneration(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "orderly-claimed", Name: "orderly-claimed", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "claimed-session", SessionEpoch: 1, ProcessPID: 2361, ProcessStartMarker: "claimed-old-start", ProcessIncarnation: "claimed-old-inc", RuntimeGeneration: "claimed-old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "claimed-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "claimed-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	old := storedAgentFence(&agent)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, old))
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("claimed-new-generation"), ProcessFactory: ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
		return nil, errors.New("not started before claim recovery")
	}), ProcessGroupOps: &coldRecoveryOps{states: map[int]ProcessProbeState{2361: ProcessAbsent}}})
	require.NoError(t, err)
	recipe, err := manager.managedLaunchRecipe(agent)
	require.NoError(t, err)
	require.NoError(t, durable.PutManagedLaunchRecipe(ctx, agent.AgentID, recipe))
	action, _, err := durable.ClaimManagedRootRecovery(ctx, agent.AgentID, old, recipe.SHA256, "claimed-target-generation")
	require.NoError(t, err)
	require.Equal(t, "claimed", action.Stage)
	require.NoError(t, manager.Close(ctx))

	replacement := &launchProcess{identity: ProcessIdentity{PID: 2362, StartMarker: "claimed-target-start", Incarnation: "claimed-target-inc"}, stopped: make(chan struct{})}
	recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{replacement}, rpc: recoveryRPC, started: make(chan CommandSpec, 1)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{2361: ProcessAbsent, 2362: ProcessOwned}}
	recovery := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("claimed-recovery-new-id"), recoveryFactory, recoveryRPC, ops)
	defer recovery.Close(ctx)
	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls())
	started := <-recoveryFactory.started
	require.Contains(t, started.Environment, "MECHA_RPC_RUNTIME_GENERATION=claimed-target-generation")
	storedAction, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, action.ActionID, storedAction.ActionID)
	require.Equal(t, "claimed-target-generation", storedAction.TargetRuntimeGeneration)
	require.Equal(t, "ready", storedAction.Stage)
	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls())
}

func TestManagedOrderlyRecoveryRestoresMarkerFenceAfterReadinessFailure(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	old := store.ManagedAgentRecord{AgentID: "orderly-readiness-retry", Name: "orderly-readiness-retry", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "readiness-old-session", SessionEpoch: 1, ProcessPID: 2371, ProcessStartMarker: "readiness-old-start", ProcessIncarnation: "readiness-old-inc", RuntimeGeneration: "readiness-old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, old, store.ManagedOperation{OperationID: "readiness-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "readiness-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	oldFence := storedAgentFence(&old)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, oldFence))

	failedReplacement := &launchProcess{identity: ProcessIdentity{PID: 2372, StartMarker: "readiness-failed-start", Incarnation: "readiness-failed-inc"}, stopped: make(chan struct{})}
	failedRPC := &launchRPC{active: expectedManagedActiveTools(manifest), failAt: "policy-command"}
	failedFactory := &crashRecoveryProcessFactory{processes: []Process{failedReplacement}, rpc: failedRPC, started: make(chan CommandSpec, 1)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{oldFence.ProcessPID: ProcessAbsent, failedReplacement.identity.PID: ProcessOwned}}
	failedManager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("failed-readiness"), failedFactory, failedRPC, ops)
	require.NoError(t, failedManager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, failedManager.Close(ctx))
	failedAgent, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedAgent.LifecycleState)
	require.Equal(t, "stopped", failedAgent.ActivityState)
	require.Equal(t, oldFence.ProcessPID, failedAgent.ProcessPID)
	require.Equal(t, oldFence.RuntimeGeneration, failedAgent.RuntimeGeneration)
	marker, err := durable.ManagedOrderlyStopMarker(ctx, old.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker)
	failedAction, err := durable.ManagedRootRecoveryAction(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedAction.Stage)
	require.Zero(t, failedAction.TargetProcessPID)

	replacement := &launchProcess{identity: ProcessIdentity{PID: 2373, StartMarker: "readiness-retry-start", Incarnation: "readiness-retry-inc"}, stopped: make(chan struct{})}
	recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	readinessStarted := make(chan struct{})
	releaseReadiness := make(chan struct{})
	recoveryRPC.beforeState = func() {
		close(readinessStarted)
		<-releaseReadiness
	}
	recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{replacement}, rpc: recoveryRPC, started: make(chan CommandSpec, 1)}
	ops.mu.Lock()
	ops.states[replacement.identity.PID] = ProcessOwned
	ops.mu.Unlock()
	recovery := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("successful-readiness-retry"), recoveryFactory, recoveryRPC, ops)
	defer func() {
		select {
		case <-releaseReadiness:
		default:
			close(releaseReadiness)
		}
		_ = recovery.Close(ctx)
		_ = durable.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- recovery.RecoverManagedRootColdStarts(ctx) }()
	<-recoveryFactory.started
	<-readinessStarted
	current, err := durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", current.LifecycleState)
	marker, err = durable.ManagedOrderlyStopMarker(ctx, old.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker, "the marker stays until the replacement is ready")
	close(releaseReadiness)
	require.NoError(t, <-done)
	current, err = durable.ManagedAgent(ctx, old.AgentID)
	require.NoError(t, err)
	require.Equal(t, replacement.identity.PID, current.ProcessPID)
	require.Equal(t, "running", current.LifecycleState)
	marker, err = durable.ManagedOrderlyStopMarker(ctx, old.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
	require.NoError(t, durable.CompleteManagedRootRecoveryReady(ctx, old.AgentID, storedAgentFence(current)))
}

func TestManagedColdRecoveryDoesNotRelaunchPlainStoppedDesiredRunningRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "plain-stopped", Name: "plain-stopped", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "plain-session", SessionEpoch: 1, ProcessPID: 2401, ProcessStartMarker: "plain-start", ProcessIncarnation: "plain-inc", RuntimeGeneration: "plain-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "plain-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "plain-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	require.NoError(t, durable.MarkManagedStopped(ctx, agent.AgentID))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	starts := 0
	manager := newCrashRecoveryManager(t, durable, config, manifest, newCrashRecoveryClock(), deterministic.NewSequenceIDSource("plain-recovery"), ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
		starts++
		return nil, errors.New("plain stopped row must not start")
	}), rpc, &coldRecoveryOps{states: map[int]ProcessProbeState{agent.ProcessPID: ProcessAbsent}})
	defer manager.Close(ctx)

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Zero(t, starts)
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.DesiredState)
	require.Equal(t, "stopped", current.ActivityState)
	require.False(t, current.AcceptingPrompts)
}

func TestManagedColdRecoveryDoesNotRelaunchTerminatedRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "terminated-row", Name: "terminated-row", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "terminated-session", SessionEpoch: 1, ProcessPID: 2402, ProcessStartMarker: "terminated-start", ProcessIncarnation: "terminated-inc", RuntimeGeneration: "terminated-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "terminated-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "terminated-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	terminationID := "123e4567-e89b-42d3-a456-426614174601"
	termination := store.ManagedOperation{OperationID: terminationID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/terminated-row", RequestID: terminationID, CanonicalBodyHash: strings.Repeat("a", 64), State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	_, err = durable.ReserveManagedLifecycle(ctx, store.ManagedLifecycleReservation{AgentID: agent.AgentID, Kind: "terminate", Source: "direct", Method: termination.Method, CanonicalPath: termination.CanonicalPath, RequestID: termination.RequestID, CanonicalBodyHash: termination.CanonicalBodyHash, Fence: storedAgentFence(&agent), Operation: termination})
	require.NoError(t, err)
	terminated, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "terminated", terminated.DesiredState)
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	starts := 0
	manager := newCrashRecoveryManager(t, durable, config, manifest, newCrashRecoveryClock(), deterministic.NewSequenceIDSource("terminated-recovery"), ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
		starts++
		return nil, errors.New("terminated row must not start")
	}), rpc, &coldRecoveryOps{states: map[int]ProcessProbeState{agent.ProcessPID: ProcessAbsent}})
	defer manager.Close(ctx)

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Zero(t, starts)
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "terminated", current.DesiredState)
}

func TestManagedOrderlyRecoveryRetriesAfterFenceInstallFault(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "orderly-fence-retry", Name: "orderly-fence-retry", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 2412, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "orderly-fence-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "orderly-fence-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, storedAgentFence(&agent)))
	first := &launchProcess{identity: ProcessIdentity{PID: 2413, StartMarker: "first-target", Incarnation: "first-target-inc"}, stopped: make(chan struct{})}
	second := &launchProcess{identity: ProcessIdentity{PID: 2414, StartMarker: "second-target", Incarnation: "second-target-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest)}
	factory := &crashRecoveryProcessFactory{processes: []Process{first, second}, rpc: rpc, started: make(chan CommandSpec, 4)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{2412: ProcessAbsent, 2413: ProcessOwned, 2414: ProcessOwned}}
	manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("fence-retry-session", "fence-retry-runtime", "fence-retry-runtime-2"), factory, rpc, ops)
	triggerDB := installColdRecoveryTrigger(t, path, `CREATE TRIGGER reject_orderly_fence BEFORE UPDATE OF process_pid ON managed_agents WHEN OLD.process_pid = 2412 AND NEW.process_pid = 2413 BEGIN SELECT RAISE(FAIL, 'fence install failed'); END`)
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	action, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", action.Stage)
	require.Equal(t, "RECOVERY_TARGET_START_FAILED", action.FailureCode)
	failed, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.LifecycleState)
	_, err = triggerDB.Exec(`DROP TRIGGER reject_orderly_fence`)
	require.NoError(t, err)
	require.NoError(t, triggerDB.Close())
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 2, factory.StartCalls(), "restored orderly replacement must retry exactly once")
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.Equal(t, 2414, current.ProcessPID)
	marker, err := durable.ManagedOrderlyStopMarker(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
}

func TestManagedOrderlyRecoveryRetriesAfterRestoreFault(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "orderly-restore-retry", Name: "orderly-restore-retry", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "old-session", SessionEpoch: 1, ProcessPID: 2421, ProcessStartMarker: "old-start", ProcessIncarnation: "old-inc", RuntimeGeneration: "old-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "orderly-restore-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "orderly-restore-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, storedAgentFence(&agent)))
	first := &launchProcess{identity: ProcessIdentity{PID: 2422, StartMarker: "first-target", Incarnation: "first-target-inc"}, stopped: make(chan struct{})}
	second := &launchProcess{identity: ProcessIdentity{PID: 2423, StartMarker: "second-target", Incarnation: "second-target-inc"}, stopped: make(chan struct{})}
	rpc := &launchRPC{active: expectedManagedActiveTools(manifest), failAt: "policy-command"}
	factory := &crashRecoveryProcessFactory{processes: []Process{first, second}, rpc: rpc, started: make(chan CommandSpec, 4)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{2421: ProcessAbsent, 2422: ProcessOwned, 2423: ProcessOwned}}
	manager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("restore-retry-session", "restore-retry-runtime", "restore-retry-runtime-2"), factory, rpc, ops)
	triggerDB := installColdRecoveryTrigger(t, path, `CREATE TRIGGER reject_orderly_restore BEFORE UPDATE OF process_pid ON managed_agents WHEN OLD.process_pid = 2422 AND NEW.process_pid = 2421 BEGIN SELECT RAISE(FAIL, 'restore failed'); END`)
	require.Error(t, manager.RecoverManagedRootColdStarts(ctx))
	failedTarget, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", failedTarget.Stage)
	require.Equal(t, 2422, failedTarget.TargetProcessPID)
	_, err = triggerDB.Exec(`DROP TRIGGER reject_orderly_restore`)
	require.NoError(t, err)
	require.NoError(t, triggerDB.Close())
	rpc.failAt = ""
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, factory.StartCalls(), "a starting target with no owner lease must remain untouched")
	retained, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", retained.LifecycleState)
	action, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", action.Stage)
	require.Equal(t, 2422, action.TargetProcessPID)
}

func TestManagedOrderlyRecoveryRetainsMarkerForFailedReplacementAndRetriesOnce(t *testing.T) {
	ctx := context.Background()
	clock := newCrashRecoveryClock()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "orderly-retry", Name: "orderly-retry", SlotHeld: true, Cwd: paths["work"], SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: "orderly-retry-session", SessionEpoch: 1, ProcessPID: 2403, ProcessStartMarker: "orderly-retry-start", ProcessIncarnation: "orderly-retry-inc", RuntimeGeneration: "orderly-retry-runtime", ManifestRevision: manifest.Revision, ManifestHash: manifest.CanonicalSHA256}
	seedColdRecoveryAgent(t, durable, agent, store.ManagedOperation{OperationID: "orderly-retry-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "orderly-retry-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)})
	fence := storedAgentFence(&agent)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))

	failedManager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("failed-recovery"), ProcessFactoryFunc(func(context.Context, CommandSpec) (Process, error) {
		return nil, noChildStartError{errors.New("scripted replacement start failure")}
	}), &launchRPC{active: expectedManagedActiveTools(manifest)}, &coldRecoveryOps{states: map[int]ProcessProbeState{agent.ProcessPID: ProcessAbsent}})
	require.NoError(t, failedManager.RecoverManagedRootColdStarts(ctx))
	require.NoError(t, failedManager.Close(ctx))
	failed, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.LifecycleState)
	marker, err := durable.ManagedOrderlyStopMarker(ctx, agent.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker, "a failed orderly replacement must retain its marker")
	failedAction, err := durable.ManagedRootRecoveryAction(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedAction.Stage)
	require.Zero(t, failedAction.TargetProcessPID)
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	replacement := &launchProcess{identity: ProcessIdentity{PID: 2404, StartMarker: "orderly-replacement-start", Incarnation: "orderly-replacement-inc"}, stopped: make(chan struct{})}
	recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{replacement}, rpc: recoveryRPC, started: make(chan CommandSpec, 1)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{agent.ProcessPID: ProcessAbsent, replacement.identity.PID: ProcessOwned}}
	recoveryManager := newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("successful-recovery"), recoveryFactory, recoveryRPC, ops)
	defer recoveryManager.Close(ctx)

	require.NoError(t, recoveryManager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls())
	require.Equal(t, ResumeExisting, (<-recoveryFactory.started).LaunchMode)
	current, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", current.LifecycleState)
	require.Equal(t, replacement.identity.PID, current.ProcessPID)
	marker, err = durable.ManagedOrderlyStopMarker(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker, "the successful retry consumes the retained marker")
	require.NoError(t, recoveryManager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls(), "one successful retry owns the replacement")
}

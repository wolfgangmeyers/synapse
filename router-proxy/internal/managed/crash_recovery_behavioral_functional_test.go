package managed

import (
	"context"
	"errors"
	"path/filepath"
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
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, durable.Close())

	// Reopen the same SQLite file with the same controlled clock. Before the
	// one-second due boundary, recovery must not spawn a duplicate target.
	durable, err = store.Open(ctx, databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	rpc = &launchRPC{active: expectedManagedActiveTools(manifest)}
	factory = &crashRecoveryProcessFactory{processes: []Process{second}, rpc: rpc, started: make(chan CommandSpec, 2)}
	manager = newCrashRecoveryManager(t, durable, config, manifest, clock, deterministic.NewSequenceIDSource("recovery-runtime"), factory, rpc, ops)
	defer manager.Close(ctx)

	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 0, factory.StartCalls(), "recovery must honor the persisted first-crash backoff")
	require.NoError(t, clock.Advance(time.Second))
	require.NoError(t, manager.RecoverManagedRootColdStarts(ctx))
	select {
	case spec := <-factory.started:
		require.Equal(t, ResumeExisting, spec.LaunchMode)
		require.Equal(t, "session-1", spec.SessionID)
	case <-time.After(2 * time.Second):
		current, readErr := durable.ManagedAgent(ctx, "crash-agent")
		t.Fatalf("persisted crash backoff did not resume at its exact due time (read error: %v, agent: %+v, process starts: %d)", readErr, current, factory.StartCalls())
	}
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

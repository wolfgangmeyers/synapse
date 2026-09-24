package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type observingLaunchFactory struct {
	delegate    *launchFactory
	beforeStart func(CommandSpec) error
	startErr    error
	startCalls  int
}

func (f *observingLaunchFactory) Start(ctx context.Context, spec CommandSpec) (Process, error) {
	f.startCalls++
	if f.beforeStart != nil {
		if err := f.beforeStart(spec); err != nil {
			return nil, err
		}
	}
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.delegate == nil {
		return nil, errors.New("unexpected process start")
	}
	return f.delegate.Start(ctx, spec)
}

func TestManagedFreshLaunchMaterializesOfficialHeaderBeforeProcessStart(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	process := &launchProcess{identity: ProcessIdentity{PID: 9140, StartMarker: "stock-header", Incarnation: "stock-header-incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	base := &launchFactory{process: process, rpc: rpc}
	observed := make(chan map[string]any, 1)
	factory := &observingLaunchFactory{delegate: base, beforeStart: func(spec CommandSpec) error {
		info, statErr := os.Lstat(spec.SessionPath)
		require.NoError(t, statErr)
		require.True(t, info.Mode().IsRegular())
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		contents, readErr := os.ReadFile(spec.SessionPath)
		require.NoError(t, readErr)
		var header map[string]any
		require.NoError(t, json.Unmarshal(contents, &header))
		require.Equal(t, map[string]any{
			"type": "session", "version": float64(3), "id": spec.SessionID,
			"timestamp": header["timestamp"], "cwd": spec.Cwd,
		}, header)
		_, parseErr := time.Parse(time.RFC3339Nano, header["timestamp"].(string))
		require.NoError(t, parseErr)
		observed <- header
		return nil
	}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	result, err := manager.Launch(ctx, LaunchRequest{Name: "stock-header-agent", Cwd: paths["work"], RequestID: "stock-header-launch"})
	require.NoError(t, err)
	select {
	case header := <-observed:
		require.Equal(t, result.Agent.PiSessionID, header["id"])
	case <-time.After(2 * time.Second):
		t.Fatal("process start did not observe the reserved session header")
	}
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "stock-header-agent")
		return readErr == nil && agent.LifecycleState == "running"
	}, 2*time.Second, time.Millisecond)
	agent, err := durable.ManagedAgent(ctx, "stock-header-agent")
	require.NoError(t, err)
	require.Equal(t, result.Agent.PiSessionID, rpc.session)
	require.Equal(t, result.Agent.PiSessionID, agent.PiSessionID)
}

func TestManagedFreshLaunchReservationRollbackRemovesOnlyOwnedHeader(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	observedPath := make(chan string, 1)
	factory := &observingLaunchFactory{startErr: errors.New("scripted process start failure"), beforeStart: func(spec CommandSpec) error {
		_, statErr := os.Lstat(spec.SessionPath)
		require.NoError(t, statErr)
		observedPath <- spec.SessionPath
		return nil
	}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory,
		CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "stock-rollback-agent", Cwd: paths["work"], RequestID: "stock-rollback-launch"})
	require.NoError(t, err)
	var sessionPath string
	select {
	case sessionPath = <-observedPath:
	case <-time.After(2 * time.Second):
		t.Fatal("process start did not observe the reserved session header")
	}
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "stock-rollback-agent")
		return readErr == nil && agent.LifecycleState == "failed"
	}, 2*time.Second, time.Millisecond)
	_, err = os.Lstat(sessionPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestManagedFreshLaunchRefusesPreexistingReservedPathWithoutRemovingIt(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(*testing.T, string)
	}{
		{name: "regular", create: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("owned elsewhere\n"), 0o600))
		}},
		{name: "directory", create: func(t *testing.T, path string) { require.NoError(t, os.Mkdir(path, 0o700)) }},
		{name: "symlink", create: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			require.NoError(t, os.WriteFile(target, []byte("owned elsewhere\n"), 0o600))
			require.NoError(t, os.Symlink(target, path))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
			require.NoError(t, err)
			t.Cleanup(func() { _ = durable.Close() })
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			config, paths := managedLaunchConfigFixture(t)
			requestID := "stock-existing-" + test.name
			sessionPath := filepath.Join(config.SessionRoot, hashString(requestID)+".jsonl")
			test.create(t, sessionPath)
			before, err := os.Lstat(sessionPath)
			require.NoError(t, err)
			factory := &observingLaunchFactory{}
			manager, err := NewManager(ManagerOptions{
				Store: durable, Config: config, Manifest: manifest, ProcessFactory: factory,
				CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = manager.Close(context.Background()) })

			_, err = manager.Launch(ctx, LaunchRequest{Name: "stock-existing-agent-" + test.name, Cwd: paths["work"], RequestID: requestID})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				agent, readErr := durable.ManagedAgent(ctx, "stock-existing-agent-"+test.name)
				return readErr == nil && agent.LifecycleState == "failed"
			}, 2*time.Second, time.Millisecond)
			after, err := os.Lstat(sessionPath)
			require.NoError(t, err)
			require.Equal(t, before.Mode(), after.Mode())
			require.Equal(t, 0, factory.startCalls)
		})
	}
}

func TestManagedRestartResumesBoundStockPiSessionWithoutRebinding(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	config, paths := managedLaunchConfigFixture(t)
	first := &launchProcess{identity: ProcessIdentity{PID: 9131, StartMarker: "stock-restart-1", Incarnation: "stock-restart-inc-1"}, stopped: make(chan struct{})}
	firstRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	firstFactory := &crashRecoveryProcessFactory{processes: []Process{first}, rpc: firstRPC, started: make(chan CommandSpec, 1)}
	ops := &coldRecoveryOps{states: map[int]ProcessProbeState{first.identity.PID: ProcessOwned}}
	manager := newCrashRecoveryManager(t, durable, config, manifest, newCrashRecoveryClock(), nil, firstFactory, firstRPC, ops)

	_, err = manager.Launch(ctx, LaunchRequest{Name: "stock-restart-agent", Cwd: paths["work"], RequestID: "stock-restart-launch"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "stock-restart-agent")
		return readErr == nil && agent.LifecycleState == "running"
	}, 2*time.Second, time.Millisecond)
	launched := <-firstFactory.started
	require.NotEmpty(t, launched.SessionPath)
	boundIdentity := firstRPC.session
	require.NotEmpty(t, boundIdentity)
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	second := &launchProcess{identity: ProcessIdentity{PID: 9132, StartMarker: "stock-restart-2", Incarnation: "stock-restart-inc-2"}, stopped: make(chan struct{})}
	recoveryRPC := &launchRPC{active: expectedManagedActiveTools(manifest)}
	recoveryFactory := &crashRecoveryProcessFactory{processes: []Process{second}, rpc: recoveryRPC, started: make(chan CommandSpec, 1)}
	recovery := newCrashRecoveryManager(t, durable, config, manifest, newCrashRecoveryClock(), nil, recoveryFactory, recoveryRPC, ops)
	defer recovery.Close(ctx)

	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	resumed := <-recoveryFactory.started
	require.Equal(t, ResumeExisting, resumed.LaunchMode)
	require.Equal(t, launched.SessionPath, resumed.SessionPath)
	require.Equal(t, boundIdentity, resumed.SessionID)
	require.NoError(t, recovery.RecoverManagedRootColdStarts(ctx))
	require.Equal(t, 1, recoveryFactory.StartCalls(), "recovery must not relaunch an already rebound agent")

	inspection, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer inspection.Close()
	var reservations int
	var state, piSessionID, relativePath string
	require.NoError(t, inspection.QueryRow(`SELECT COUNT(*) FROM managed_stock_pi_session_reservations WHERE agent_id=?`, "stock-restart-agent").Scan(&reservations))
	require.Equal(t, 1, reservations, "resume must not reserve a second stock Pi session")
	require.NoError(t, inspection.QueryRow(`SELECT state,pi_session_id,root_relative_path FROM managed_stock_pi_session_reservations WHERE agent_id=?`, "stock-restart-agent").Scan(&state, &piSessionID, &relativePath))
	require.Equal(t, "bound", state)
	require.Equal(t, boundIdentity, piSessionID)
	require.Equal(t, launched.SessionPath, filepath.Join(config.SessionRoot, relativePath))
}

func TestManagedLaunchBindsGetStateSessionIDToExactOperationAndProcess(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	process := &launchProcess{identity: ProcessIdentity{PID: 9123, StartMarker: "stock-start", Incarnation: "stock-incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc},
		CatalogSource:              CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "stock-bind-agent", Cwd: root, RequestID: "stock-bind-launch"})
	require.NoError(t, err)
	var persisted *store.ManagedAgentRecord
	require.Eventually(t, func() bool {
		var readErr error
		persisted, readErr = durable.ManagedAgent(ctx, "stock-bind-agent")
		return readErr == nil && persisted.LifecycleState != "starting"
	}, 2*time.Second, time.Millisecond)
	require.NotNil(t, persisted)

	require.NotEmpty(t, factory.spec.SessionPath, "launch must pass its exact reserved absolute session path to stock Pi")
	require.True(t, filepath.IsAbs(factory.spec.SessionPath))
	require.Equal(t, sessionRoot, filepath.Dir(factory.spec.SessionPath))
	inspection, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer inspection.Close()
	var state, boundSessionID, relativePath string
	err = inspection.QueryRow(`SELECT state,COALESCE(pi_session_id,''),root_relative_path FROM managed_stock_pi_session_reservations WHERE launch_operation_id=?`, "stock-bind-launch").Scan(&state, &boundSessionID, &relativePath)
	require.NoError(t, err)
	require.Equal(t, "running", persisted.LifecycleState, "reservation state=%s bound=%s relative=%s agent=%+v", state, boundSessionID, relativePath, persisted)
	require.Equal(t, "bound", state)
	require.Equal(t, rpc.session, boundSessionID)
	require.Equal(t, factory.spec.SessionPath, filepath.Join(sessionRoot, relativePath))
}

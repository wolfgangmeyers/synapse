package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestTerminateFencesAbortButReapsAndDeletesArtifactsAfterEOF(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	sessionFile := filepath.Join(sessionRoot, "termination.json")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}"), 0o600))
	process := &launchProcess{identity: ProcessIdentity{PID: 701, StartMarker: "123e4567-e89b-42d3-a456-426614174201", Incarnation: "termination-incarnation"}, stopped: make(chan struct{})}
	rpc := &resetRPC{launchRPC: &launchRPC{abortErr: io.EOF}, file: sessionFile}
	factory := &resetProcessFactory{processes: []*launchProcess{process}}
	rpcs := &resetRPCFactory{rpcs: []*resetRPC{rpc}}
	ops := &resetProcessOps{present: map[int]bool{701: true}}
	artifactCleanupFails := true
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: rpcs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		IDSource: &resetTestIDs{values: []string{"123e4567-e89b-42d3-a456-426614174201", "123e4567-e89b-42d3-a456-426614174202", "123e4567-e89b-42d3-a456-426614174205"}}, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
		BeforeManagedTerminationArtifactCleanup: func(context.Context, string) error {
			if artifactCleanupFails {
				return errors.New("injected session artifact cleanup failure")
			}
			return nil
		},
	})
	require.NoError(t, err)
	var projectionPublications atomic.Int32
	manager.SetProjectionPublisher(func() { projectionPublications.Add(1) })
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "termination-agent", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174203"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "termination-agent")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, 3*time.Second, time.Millisecond)
	sourceAgent, err := durable.ManagedAgent(ctx, "termination-agent")
	require.NoError(t, err)
	sourceAttempt := queueManagedParentFinalForTest(t, durable, storedAgentFence(sourceAgent), "termination-source-turn")

	request := TerminateRequest{AgentID: "termination-agent", Method: "DELETE", CanonicalPath: "/v1/managed-agents/termination-agent", RequestID: "123e4567-e89b-42d3-a456-426614174204", CanonicalBodyHash: "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"}
	first, err := manager.Terminate(ctx, request)
	require.NoError(t, err)
	require.NotNil(t, first.Operation)
	requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")
	require.Eventually(t, func() bool { return projectionPublications.Load() > 0 }, time.Second, time.Millisecond, "termination admission must wake UI-live before cleanup can remove the row")
	replay, err := manager.Terminate(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, request.RequestID)
		agent, agentErr := durable.ManagedAgent(ctx, request.AgentID)
		return actionErr == nil && agentErr == nil && action.State == "failed" && action.Stage == "agent_cleanup" && action.Retryable && agent.DesiredState == "terminated"
	}, 3*time.Second, time.Millisecond)
	require.Equal(t, 1, rpc.abortCalls, "Abort is fenced once even when its RPC ends at EOF")
	requireTerminationDiagnostic(t, databasePath, request.RequestID, "ABORT_EOF")
	require.GreaterOrEqual(t, ops.waitCount(701), 1, "the exact owned process group is reaped after Abort EOF")
	_, err = os.Stat(sessionFile)
	require.NoError(t, err, "a failed artifact cleanup retains the durable retry boundary")

	artifactCleanupFails = false
	retryRequest := request
	retryRequest.RequestID = "123e4567-e89b-42d3-a456-426614174206"
	retry, err := manager.Terminate(ctx, retryRequest)
	require.NoError(t, err)
	require.NotNil(t, retry.Operation)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, retryRequest.RequestID)
		_, agentErr := durable.ManagedAgent(ctx, retryRequest.AgentID)
		return actionErr == nil && action.State == "succeeded" && action.TerminalCode == "TERMINATED" && errors.Is(agentErr, sql.ErrNoRows)
	}, 3*time.Second, time.Millisecond)
	require.Equal(t, 1, rpc.abortCalls, "retry resumes agent cleanup without sending another Abort")
	_, err = os.Stat(sessionFile)
	require.ErrorIs(t, err, os.ErrNotExist)
	stored, err := durable.FindManagedOperation(ctx, "DELETE", retryRequest.RequestID)
	require.NoError(t, err)
	require.Equal(t, "terminal", stored.State)
}

func TestTerminateRecordsEveryAbortOutcomeAndStillReapsTheOwnedGroup(t *testing.T) {
	testCases := []struct {
		name, diagnostic string
		abortErr         error
	}{
		{name: "timeout", diagnostic: "ABORT_TIMEOUT", abortErr: context.DeadlineExceeded},
		{name: "EOF", diagnostic: "ABORT_EOF", abortErr: io.EOF},
		{name: "malformed response", diagnostic: "ABORT_MALFORMED", abortErr: ErrAbortResponseMalformed},
		{name: "explicit RPC error", diagnostic: "ABORT_ERROR", abortErr: errors.New("Pi rejected Abort")},
	}
	for index, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			databasePath := filepath.Join(t.TempDir(), "router.db")
			durable, err := store.Open(ctx, databasePath, store.Options{})
			require.NoError(t, err)
			t.Cleanup(func() { _ = durable.Close() })
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			root := t.TempDir()
			sessionRoot := filepath.Join(root, "sessions")
			require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
			sessionID := fmt.Sprintf("123e4567-e89b-42d3-a456-4266141742%02d", index+30)
			sessionFile := filepath.Join(sessionRoot, "abort-"+testCase.diagnostic+".json")
			require.NoError(t, os.WriteFile(sessionFile, []byte("{}"), 0o600))
			process := &launchProcess{identity: ProcessIdentity{PID: 720 + index, StartMarker: sessionID, Incarnation: "abort-outcome" + testCase.diagnostic}, stopped: make(chan struct{})}
			rpc := &resetRPC{launchRPC: &launchRPC{abortErr: testCase.abortErr}, file: sessionFile}
			factory := &resetProcessFactory{processes: []*launchProcess{process}}
			rpcs := &resetRPCFactory{rpcs: []*resetRPC{rpc}}
			ops := &resetProcessOps{present: map[int]bool{process.identity.PID: true}}
			manager, err := NewManager(ManagerOptions{
				Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: time.Millisecond, TermStop: time.Millisecond}}, Manifest: manifest,
				ProcessFactory: factory, RPCFactory: rpcs, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				IDSource: &resetTestIDs{values: []string{sessionID, fmt.Sprintf("123e4567-e89b-42d3-a456-4266141742%02d", index+40)}}, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = manager.Close(context.Background()) })
			t.Cleanup(func() { process.Reap() })
			agentID := fmt.Sprintf("abort-outcome-%d", index)
			_, err = manager.Launch(ctx, LaunchRequest{Name: agentID, Cwd: root, RequestID: fmt.Sprintf("123e4567-e89b-42d3-a456-4266141742%02d", index+50)})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				agent, readErr := durable.ManagedAgent(ctx, agentID)
				return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
			}, time.Second, time.Millisecond)
			terminationID := fmt.Sprintf("123e4567-e89b-42d3-a456-4266141742%02d", index+60)
			_, err = manager.Terminate(ctx, TerminateRequest{AgentID: agentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + agentID, RequestID: terminationID, CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				action, actionErr := durable.ManagedLifecycleAction(ctx, terminationID)
				_, agentErr := durable.ManagedAgent(ctx, agentID)
				return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
			}, time.Second, time.Millisecond)
			requireTerminationDiagnostic(t, databasePath, terminationID, testCase.diagnostic)
			require.GreaterOrEqual(t, ops.waitCount(process.identity.PID), 1)
		})
	}
}

type blockingAbortRPC struct {
	*resetRPC
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingAbortRPC) Abort(ctx context.Context) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestManagerCloseLeavesTerminationToItsLaneOwner(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	const sessionID = "123e4567-e89b-42d3-a456-426614174270"
	sessionFile := filepath.Join(sessionRoot, "close-termination.json")
	require.NoError(t, os.WriteFile(sessionFile, []byte("{}"), 0o600))
	process := &launchProcess{identity: ProcessIdentity{PID: 727, StartMarker: sessionID, Incarnation: "close-termination"}, stopped: make(chan struct{})}
	rpc := &blockingAbortRPC{resetRPC: &resetRPC{launchRPC: &launchRPC{}, file: sessionFile}, entered: make(chan struct{}), release: make(chan struct{})}
	factory := ProcessFactoryFunc(func(_ context.Context, spec CommandSpec) (Process, error) {
		rpc.session = spec.SessionID
		return process, nil
	})
	ops := &resetProcessOps{present: map[int]bool{process.identity.PID: true}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{Enabled: false, SessionRoot: sessionRoot, AllowedWorkingDirectoryRoots: []string{root}, Limits: Limits{GracefulStop: time.Second, TermStop: time.Millisecond}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: RPCFactoryFunc(func(context.Context, Process) (RPC, error) { return rpc, nil }), CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		IDSource: &resetTestIDs{values: []string{sessionID, "123e4567-e89b-42d3-a456-426614174271"}}, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
	})
	require.NoError(t, err)
	t.Cleanup(func() { process.Reap() })
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "close-termination", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174272"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "close-termination")
		return readErr == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts
	}, time.Second, time.Millisecond)
	_, err = manager.Terminate(ctx, TerminateRequest{AgentID: "close-termination", Method: "DELETE", CanonicalPath: "/v1/managed-agents/close-termination", RequestID: "123e4567-e89b-42d3-a456-426614174273", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	select {
	case <-rpc.entered:
	case <-time.After(time.Second):
		t.Fatal("termination did not enter Abort")
	}
	closed := make(chan error, 1)
	go func() { closed <- manager.Close(context.Background()) }()
	close(rpc.release)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174273")
		return actionErr == nil && action.State == "succeeded"
	}, time.Second, time.Millisecond)
	process.Reap()
	select {
	case closeErr := <-closed:
		require.NoError(t, closeErr)
	case <-time.After(time.Second):
		t.Fatal("manager close did not complete the termination lane")
	}
	require.Zero(t, process.Stops(), "Close must not bypass termination with Process.Stop")
}

func TestTerminateBeforeLaunchStartUsesTheLaunchLane(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 702, StartMarker: "launch-start", Incarnation: "launch-incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	launchEntered := make(chan struct{})
	releaseLaunch := make(chan struct{})
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{Enabled: false, SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true },
		BeforeManagedLaunchStart: func(context.Context, string) {
			close(launchEntered)
			<-releaseLaunch
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	_, err = manager.Launch(ctx, LaunchRequest{Name: "launch-lane", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174211"})
	require.NoError(t, err)
	select {
	case <-launchEntered:
	case <-time.After(time.Second):
		t.Fatal("launch did not reach the controlled post-reservation handoff")
	}
	termination, err := manager.Terminate(ctx, TerminateRequest{AgentID: "launch-lane", Method: "DELETE", CanonicalPath: "/v1/managed-agents/launch-lane", RequestID: "123e4567-e89b-42d3-a456-426614174212", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.NotNil(t, termination.Operation)
	close(releaseLaunch)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174212")
		_, agentErr := durable.ManagedAgent(ctx, "launch-lane")
		return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
	}, time.Second, time.Millisecond)
	require.Empty(t, factory.spec.SessionID, "the start task must observe the tombstone before ProcessFactory.Start")
}

func TestTerminateWithoutLiveBindingRecordsAbortUnavailableBeforeReaping(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 703, StartMarker: "missing-rpc", Incarnation: "missing-rpc-incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	ops := &resetProcessOps{present: map[int]bool{703: true}}
	manager, err := NewManager(ManagerOptions{
		Store: durable, Config: Config{Enabled: false, SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "missing-rpc", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174221"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "missing-rpc")
		return readErr == nil && agent.LifecycleState == "running"
	}, time.Second, time.Millisecond)
	binding, live := manager.bindingFor("missing-rpc")
	require.True(t, live)
	manager.clearBinding(binding)

	_, err = manager.Terminate(ctx, TerminateRequest{AgentID: "missing-rpc", Method: "DELETE", CanonicalPath: "/v1/managed-agents/missing-rpc", RequestID: "123e4567-e89b-42d3-a456-426614174222", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		action, actionErr := durable.ManagedLifecycleAction(ctx, "123e4567-e89b-42d3-a456-426614174222")
		_, agentErr := durable.ManagedAgent(ctx, "missing-rpc")
		return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
	}, time.Second, time.Millisecond)
	require.Zero(t, rpc.abortCalls)
	requireTerminationDiagnostic(t, databasePath, "123e4567-e89b-42d3-a456-426614174222", "ABORT_UNAVAILABLE")
	require.GreaterOrEqual(t, ops.waitCount(703), 1)
	process.Reap()
}

func TestTerminationRecoveryWithoutBindingRecordsAbortUnavailableAndReaps(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	root := t.TempDir()
	process := &launchProcess{identity: ProcessIdentity{PID: 704, StartMarker: "recovery-rpc", Incarnation: "recovery-rpc-incarnation"}, stopped: make(chan struct{})}
	rpc := &launchRPC{}
	factory := &launchFactory{process: process, rpc: rpc}
	ops := &resetProcessOps{present: map[int]bool{704: true}}
	options := ManagerOptions{
		Store: durable, Config: Config{Enabled: false, SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest,
		ProcessFactory: factory, RPCFactory: launchRPCFactory{rpc: rpc}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: ops,
	}
	launchManager, err := NewManager(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = launchManager.Close(context.Background()) })
	_, err = launchManager.Launch(ctx, LaunchRequest{Name: "recovery-rpc", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174281"})
	require.NoError(t, err)
	var fence store.ManagedRuntimeFence
	require.Eventually(t, func() bool {
		agent, readErr := durable.ManagedAgent(ctx, "recovery-rpc")
		if readErr != nil || agent.LifecycleState != "running" {
			return false
		}
		fence = storedAgentFence(agent)
		return validRestartFence(fence)
	}, time.Second, time.Millisecond)

	// This direct reservation is the durable crash boundary: the prior owner
	// tombstoned the agent but disappeared before it could send Abort. Recovery
	// must not reconstruct that live binding or signal an unknown process.
	operationID := "123e4567-e89b-42d3-a456-426614174282"
	admission, err := durable.ReserveManagedLifecycle(ctx, store.ManagedLifecycleReservation{
		AgentID: "recovery-rpc", Kind: "terminate", Source: "direct", Method: "DELETE", CanonicalPath: "/v1/managed-agents/recovery-rpc", RequestID: operationID,
		CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Fence: fence,
		Operation: store.ManagedOperation{OperationID: operationID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/recovery-rpc", RequestID: operationID, CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)},
	})
	require.NoError(t, err)
	require.False(t, admission.Replayed)

	recoveryManager, err := NewManager(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = recoveryManager.Close(context.Background()) })
	require.NoError(t, recoveryManager.RecoverManagedLifecycleControls(ctx))
	action, err := durable.ManagedLifecycleAction(ctx, operationID)
	require.NoError(t, err)
	_, agentErr := durable.ManagedAgent(ctx, "recovery-rpc")
	require.Equal(t, "succeeded", action.State)
	require.Equal(t, "TERMINATED", action.TerminalCode)
	require.ErrorIs(t, agentErr, sql.ErrNoRows)
	require.Zero(t, rpc.abortCalls, "recovery must not recreate the lost live RPC binding")
	requireTerminationDiagnostic(t, databasePath, operationID, "ABORT_UNAVAILABLE")
	require.GreaterOrEqual(t, ops.waitCount(704), 1)
	process.Reap()
}

func TestFailedRootRecoveryTerminationRetiresUnboundUnsafeRowWithoutSignallingStoredIdentity(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessAbsent, ProcessMismatch, ProcessUnknown, ProcessPresent} {
		t.Run(string(state), func(t *testing.T) {
			manager, durable, agent, tracked := prepareFailedRootRecoveryOperatorTest(t, state)
			request := TerminateRequest{AgentID: agent.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + agent.AgentID, RequestID: "123e4567-e89b-42d3-a456-000000000103", CanonicalBodyHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}
			first, err := manager.Terminate(context.Background(), request)
			require.NoError(t, err)
			require.False(t, first.Replayed)
			require.Eventually(t, func() bool {
				op, operationErr := durable.FindManagedOperation(context.Background(), request.Method, request.RequestID)
				_, agentErr := durable.ManagedAgent(context.Background(), agent.AgentID)
				return operationErr == nil && op.State == "terminal" && errors.Is(agentErr, sql.ErrNoRows)
			}, 3*time.Second, time.Millisecond)
			tracked.mu.Lock()
			require.Empty(t, tracked.signals)
			tracked.mu.Unlock()
			replayed, err := manager.Terminate(context.Background(), request)
			require.NoError(t, err)
			require.True(t, replayed.Replayed)
		})
	}
}

func requireTerminationDiagnostic(t *testing.T, databasePath, actionID, code string) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	defer database.Close()
	var found string
	require.NoError(t, database.QueryRow(`SELECT code FROM managed_termination_diagnostics WHERE action_id=? AND code=?`, actionID, code).Scan(&found))
	require.Equal(t, code, found)
}

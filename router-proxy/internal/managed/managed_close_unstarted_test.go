package managed

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Close snapshots the reservation before canceling startup. The existing fake
// factory returns nil,error after cancellation, deterministically leaving Close
// with that exact no-child pointer even if start's defer removes its map entry.
func TestManagedCloseFailedUnstartedLaunchReservation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	entered := make(chan struct{})
	var manager *Manager
	factory := &observingLaunchFactory{beforeStart: func(CommandSpec) error {
		close(entered)
		<-manager.runCtx.Done()
		return errors.New("incomplete managed process specification")
	}}
	manager, err = NewManager(ManagerOptions{Store: durable, Manifest: manifest,
		Config:         Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		ProcessFactory: factory, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true})})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close(ctx)) })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "default-rpc", Cwd: root, RequestID: "failed-unstarted-launch"})
	require.NoError(t, err)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("fake Start was not entered")
	}
	manager.mu.Lock()
	binding := manager.bindings["default-rpc"]
	owned := manager.processes["default-rpc"]
	operationID := manager.operationIDs["default-rpc"]
	manager.mu.Unlock()
	require.NotNil(t, binding)
	require.Nil(t, owned)
	require.Equal(t, "failed-unstarted-launch", operationID)
	binding.serial.Lock()
	fence := bindingFence(binding.agentID, binding)
	require.Nil(t, binding.process)
	require.Nil(t, binding.rpc)
	require.Nil(t, binding.waitDone)
	require.Nil(t, binding.waitSignal)
	require.Equal(t, ProcessIdentity{}, binding.identity)
	binding.serial.Unlock()
	t.Logf("captured private fence=%+v operation=%s ownedProcess=nil process=nil RPC=nil Wait=nil", fence, operationID)
	closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	require.NoError(t, manager.Close(closeCtx), "pre-fix Close writes an invalid PID-zero orderly-stop fence")
	// Close has joined Start, so all factory fields and the failed row are settled.
	require.Equal(t, 1, factory.startCalls)
	row, err := durable.ManagedAgent(ctx, "default-rpc")
	require.NoError(t, err)
	require.Equal(t, "rpc", row.Runtime)
	require.Equal(t, "failed", row.LifecycleState)
	require.Equal(t, "PROCESS_START_FAILED", row.LastErrorCode)
	require.Equal(t, "incomplete managed process specification", row.LastErrorMessage)
	require.Equal(t, fence, storedAgentFence(row))
	require.Zero(t, row.ProcessPID)
	require.Empty(t, row.ProcessIncarnation)
	require.Empty(t, row.ProcessStartMarker)
	op, err := durable.ManagedOperation(ctx, operationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	before := *row
	t.Logf("post Close durable fence=%+v lifecycle=%s code=%s operation=%s/%s", storedAgentFence(row), row.LifecycleState, row.LastErrorCode, op.OperationID, op.State)
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	var pid sql.NullInt64
	var marker, incarnation sql.NullString
	require.NoError(t, check.QueryRowContext(ctx, `SELECT process_pid,process_start_marker,process_incarnation FROM managed_agents WHERE agent_id='default-rpc'`).Scan(&pid, &marker, &incarnation))
	require.False(t, pid.Valid)
	require.False(t, marker.Valid)
	require.False(t, incarnation.Valid)
	var markers int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM managed_orderly_stop_markers`).Scan(&markers))
	require.Zero(t, markers, "no fabricated orderly-stop intent for a child that never existed")
	manager.mu.Lock()
	require.Empty(t, manager.bindings)
	require.Empty(t, manager.processes)
	require.Empty(t, manager.rpcs)
	require.Empty(t, manager.operationIDs)
	manager.mu.Unlock()

	// An old failure cannot qualify for cleanup once a newer pointer owns the
	// maps. clearBinding remains pointer-safe even for delayed old callbacks.
	newBinding := &managedBinding{agentID: binding.agentID, operationID: "new-operation", runtimeGeneration: "new-generation"}
	manager.mu.Lock()
	manager.bindings[binding.agentID] = newBinding
	manager.operationIDs[binding.agentID] = newBinding.operationID
	manager.mu.Unlock()
	require.False(t, manager.failedUnstartedLaunch(binding))
	manager.clearBinding(binding)
	manager.mu.Lock()
	require.Same(t, newBinding, manager.bindings[binding.agentID])
	require.Equal(t, "new-operation", manager.operationIDs[binding.agentID])
	delete(manager.bindings, binding.agentID)
	delete(manager.operationIDs, binding.agentID)
	manager.mu.Unlock()

	// A real owned process, even on a failed durable row, must not use this path.
	binding.process = &launchProcess{}
	require.False(t, manager.failedUnstartedLaunch(binding))
	binding.process = nil
	binding.operationID = "different-operation"
	require.False(t, manager.failedUnstartedLaunch(binding))
	binding.operationID = operationID
	binding.runtimeGeneration = "different-generation"
	require.False(t, manager.failedUnstartedLaunch(binding))
	binding.runtimeGeneration = fence.RuntimeGeneration
	require.True(t, manager.failedUnstartedLaunch(binding))
	require.NoError(t, manager.Close(closeCtx))
	row, err = durable.ManagedAgent(ctx, "default-rpc")
	require.NoError(t, err)
	require.Equal(t, before, *row, "repeated Close preserves the failure and launch operation")
}

package managed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeTerminationReservesBeforeInterruptAndDrains(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-termination", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	wakes := make(chan struct{}, 4)
	m.SetProjectionPublisher(func() { wakes <- struct{}{} })
	lane := m.laneFor(binding.AgentID)
	active := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[binding.AgentID] = active
	m.activeSessionsMu.Unlock()
	lane.serial.Lock()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174299", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	done := make(chan error, 1)
	go func() { _, err := m.Terminate(ctx, req); done <- err }()
	select {
	case <-active.interrupted:
	case <-time.After(5 * time.Second):
		lane.serial.Unlock()
		t.Fatal("no interrupt")
	}
	select {
	case <-wakes:
	default:
		t.Fatal("projection not published before interrupt and drain")
	}
	projected, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "terminating", projected.LifecycleState)
	require.Equal(t, "terminated", projected.DesiredState)
	op, err := db.FindManagedOperation(ctx, req.Method, req.RequestID)
	require.NoError(t, err, "reservation must precede interrupt")
	require.NotNil(t, op)
	require.True(t, lane.admission.TryLock(), "admission held during drain")
	require.Positive(t, lane.terminationPending)
	lane.admission.Unlock()
	replay, err := m.Terminate(ctx, req)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	select {
	case <-wakes:
		t.Fatal("replay published projection")
	default:
	}
	select {
	case <-done:
		t.Fatal("returned before serial drain")
	default:
	}
	lane.serial.Unlock()
	require.NoError(t, <-done)
	m.activeSessionsMu.Lock()
	delete(m.activeEmbeddedSessions, binding.AgentID)
	m.activeSessionsMu.Unlock()
}

func TestClaudeTerminationRejectedDoesNotInterrupt(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-rejected", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	wakes := make(chan struct{}, 1)
	m.SetProjectionPublisher(func() { wakes <- struct{}{} })
	active := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[binding.AgentID] = active
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		delete(m.activeEmbeddedSessions, binding.AgentID)
		m.activeSessionsMu.Unlock()
	}()
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "invalid", CanonicalBodyHash: "bad"}
	_, err = m.Terminate(ctx, req)
	require.Error(t, err)
	select {
	case <-wakes:
		t.Fatal("rejected reservation published projection")
	default:
	}
	select {
	case <-active.interrupted:
		t.Fatal("rejected request interrupted active turn")
	default:
	}
	require.True(t, lane.admission.TryLock())
	require.Zero(t, lane.terminationPending)
	lane.admission.Unlock()
}

func TestClaudeTerminationConcurrentDuplicateReplaysDuringDrain(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-duplicate", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	lane := m.laneFor(binding.AgentID)
	active := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[binding.AgentID] = active
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		delete(m.activeEmbeddedSessions, binding.AgentID)
		m.activeSessionsMu.Unlock()
	}()
	lane.serial.Lock()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174297", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	type result struct {
		value TerminateResult
		err   error
	}
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { r, e := m.Terminate(ctx, req); done <- result{r, e} }()
	}
	select {
	case <-active.interrupted:
	case <-time.After(5 * time.Second):
		lane.serial.Unlock()
		t.Fatal("no interrupt")
	}
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.True(t, r.value.Replayed)
	case <-time.After(5 * time.Second):
		lane.serial.Unlock()
		t.Fatal("duplicate did not replay during drain")
	}
	lane.serial.Unlock()
	select {
	case r := <-done:
		require.NoError(t, r.err)
		require.False(t, r.value.Replayed)
	case <-time.After(5 * time.Second):
		t.Fatal("original did not drain")
	}
}

func TestClaudeStopEntryDoesNotRevokeReplacement(t *testing.T) {
	old := &claudeCodeProcessEntry{}
	revoked := false
	replacement := &claudeCodeProcessEntry{revoke: func() { revoked = true }}
	pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"agent": replacement}}
	pool.stopEntry("agent", old)
	require.Same(t, replacement, pool.entries["agent"])
	require.False(t, revoked)
}

func TestDirectTerminationPostCommitPermissionErrorHandsOff(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-permission", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	wakes := make(chan struct{}, 4)
	m.SetProjectionPublisher(func() { wakes <- struct{}{} })
	active := &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[binding.AgentID] = active
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		delete(m.activeEmbeddedSessions, binding.AgentID)
		m.activeSessionsMu.Unlock()
	}()
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	locked := true
	defer func() {
		if locked {
			lane.serial.Unlock()
		}
	}()
	wal := filepath.Join(root, "router.db-wal")
	saved := wal + ".saved"
	require.NoError(t, os.Rename(wal, saved))
	require.NoError(t, os.Symlink(filepath.Base(wal), wal))
	restored := false
	restore := func() {
		if !restored {
			require.NoError(t, os.Remove(wal))
			require.NoError(t, os.Rename(saved, wal))
			restored = true
		}
	}
	defer restore()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174296", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	done := make(chan error, 1)
	go func() { _, e := m.Terminate(ctx, req); done <- e }()
	select {
	case <-active.interrupted:
	case e := <-done:
		t.Fatalf("returned before committed cleanup handoff: %v", e)
	case <-time.After(5 * time.Second):
		t.Fatal("no interrupt")
	}
	select {
	case <-wakes:
	default:
		t.Fatal("committed reservation with error did not publish before drain")
	}
	restore()
	replay, err := m.Terminate(ctx, req)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	lane.serial.Unlock()
	locked = false
	require.ErrorContains(t, <-done, "stat SQLite state file")
	require.Eventually(t, func() bool { _, e := db.ManagedAgent(ctx, binding.AgentID); return e != nil }, 5*time.Second, 10*time.Millisecond)
}

type terminationReplacementRuntime struct {
	*resetBarrierRuntime
	entered chan struct{}
	proceed chan struct{}
}

func (r *terminationReplacementRuntime) Interrupt() { close(r.entered); <-r.proceed }

func TestClaudeTerminationDoesNotStopReplacementAfterSnapshot(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-replacement", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	active := &terminationReplacementRuntime{resetBarrierRuntime: &resetBarrierRuntime{}, entered: make(chan struct{}), proceed: make(chan struct{})}
	m.activeSessionsMu.Lock()
	m.activeEmbeddedSessions[binding.AgentID] = active
	m.activeSessionsMu.Unlock()
	defer func() {
		m.activeSessionsMu.Lock()
		delete(m.activeEmbeddedSessions, binding.AgentID)
		m.activeSessionsMu.Unlock()
	}()
	old := &claudeCodeProcessEntry{}
	replacement := &claudeCodeProcessEntry{}
	m.claudeCodeProcesses.mu.Lock()
	m.claudeCodeProcesses.entries = map[string]*claudeCodeProcessEntry{binding.AgentID: old}
	m.claudeCodeProcesses.mu.Unlock()
	defer func() {
		m.claudeCodeProcesses.mu.Lock()
		delete(m.claudeCodeProcesses.entries, binding.AgentID)
		m.claudeCodeProcesses.mu.Unlock()
	}()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174295", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	done := make(chan error, 1)
	go func() { _, err := m.Terminate(ctx, req); done <- err }()
	select {
	case <-active.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt not reached")
	}
	m.claudeCodeProcesses.mu.Lock()
	m.claudeCodeProcesses.entries[binding.AgentID] = replacement
	m.claudeCodeProcesses.mu.Unlock()
	close(active.proceed)
	require.NoError(t, <-done)
	m.claudeCodeProcesses.mu.Lock()
	current := m.claudeCodeProcesses.entries[binding.AgentID]
	m.claudeCodeProcesses.mu.Unlock()
	require.Same(t, replacement, current)
}

func TestClaudeTerminationUnconfirmedStopFailsCommittedOperation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "claude-unconfirmed", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	p := cleanupTestProcess(poolTestStdin{})
	p.recordWaitError(errClaudeCodeGroupUnconfirmed)
	entry := &claudeCodeProcessEntry{process: p}
	m.claudeCodeProcesses.mu.Lock()
	m.claudeCodeProcesses.entries = map[string]*claudeCodeProcessEntry{binding.AgentID: entry}
	m.claudeCodeProcesses.mu.Unlock()
	req := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174294", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	result, err := m.Terminate(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, result.Operation)
	require.Eventually(t, func() bool {
		action, e := db.ManagedLifecycleAction(ctx, req.RequestID)
		return e == nil && action.State == "failed"
	}, 5*time.Second, 10*time.Millisecond)
	action, err := db.ManagedLifecycleAction(ctx, req.RequestID)
	require.NoError(t, err)
	require.Equal(t, "TERMINATION_CLEANUP_FAILED", action.TerminalCode)
	_, err = db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	m.claudeCodeProcesses.mu.Lock()
	current, closed := m.claudeCodeProcesses.entries[binding.AgentID], m.claudeCodeProcesses.closed
	m.claudeCodeProcesses.mu.Unlock()
	require.Same(t, entry, current)
	require.True(t, closed)
	replay, err := m.Terminate(ctx, req)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
}

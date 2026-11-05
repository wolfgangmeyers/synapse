package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBackgroundExpiryUsesCreationAgeOnly(t *testing.T) {
	now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	old := store.ManagedAgentRecord{IsBackground: true, CreatedAt: now.Add(-24 * time.Hour).Format(time.RFC3339Nano), LifecycleState: "running", ActivityState: "working"}
	require.True(t, backgroundExpired(&old, now))
	old.ActivityState = "snoozed"
	require.True(t, backgroundExpired(&old, now))
	old.LifecycleState = "failed"
	require.True(t, backgroundExpired(&old, now))
	old.CreatedAt = now.Add(-24*time.Hour + time.Nanosecond).Format(time.RFC3339Nano)
	require.False(t, backgroundExpired(&old, now))
	old.CreatedAt = now.Add(-25 * time.Hour).Format(time.RFC3339Nano)
	old.IsBackground = false
	require.False(t, backgroundExpired(&old, now))
	old.IsBackground = true
	old.CreatedAt = "invalid"
	require.False(t, backgroundExpired(&old, now))
}

func TestBackgroundExpiryBlockedWorkerDoesNotBlockSweepOrOverlap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	for i := 0; i < 12; i++ {
		subagentTestParentWithModel(t, db, fmt.Sprintf("a-blocked-%02d", i), store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	}
	subagentTestParentWithModel(t, db, "b-other", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	for i := 0; i < 12; i++ {
		lane := m.laneFor(fmt.Sprintf("a-blocked-%02d", i))
		lane.serial.Lock()
		defer lane.serial.Unlock()
	}
	m.TerminateExpiredBackgroundWorkers(ctx)
	require.Eventually(t, func() bool {
		m.TerminateExpiredBackgroundWorkers(ctx)
		var count int
		err := raw.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id='b-other' AND kind='terminate'`).Scan(&count)
		return err == nil && count == 1
	}, 5*time.Second, 10*time.Millisecond)
	for i := 0; i < 20; i++ {
		m.TerminateExpiredBackgroundWorkers(ctx)
	}
	var count int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id='a-blocked-00' AND kind='terminate'`).Scan(&count))
	require.LessOrEqual(t, count, 1)
}

func TestBackgroundExpiryDispatchDeduplicationRetryAndCancellation(t *testing.T) {
	m := &Manager{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	started := make(chan string, 32)
	terminate := func(_ context.Context, id string) { started <- id; <-release }
	for i := 0; i < 8; i++ {
		m.dispatchBackgroundExpiry(ctx, fmt.Sprint(i), terminate)
	}
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("dispatch did not start")
		}
	}
	for sweep := 0; sweep < 20; sweep++ {
		for i := 0; i < 8; i++ {
			m.dispatchBackgroundExpiry(ctx, fmt.Sprint(i), terminate)
		}
	}
	m.mu.Lock()
	require.Len(t, m.backgroundExpiryInFlight, 8)
	m.mu.Unlock()
	select {
	case id := <-started:
		t.Fatalf("duplicate dispatch %s", id)
	default:
	}
	close(release)
	m.startWG.Wait()
	// A returned attempt (including a failure) is eligible on the next sweep.
	m.dispatchBackgroundExpiry(ctx, "0", terminate)
	m.startWG.Wait()
	require.Equal(t, "0", <-started)
	cancel()
	m.dispatchBackgroundExpiry(ctx, "cancelled", terminate)
	select {
	case id := <-started:
		t.Fatalf("unexpected dispatch %s", id)
	default:
	}
}

func TestBackgroundExpiryPersistentFailuresDoNotPreventOtherAttempts(t *testing.T) {
	m := &Manager{}
	ctx := context.Background()
	attempts := make(chan string, 32)
	// Returning without terminating the agent models a persistent failure;
	// each periodic sweep will still see it as eligible.
	fail := func(_ context.Context, id string) { attempts <- id }
	for sweep := 0; sweep < 3; sweep++ {
		for i := 0; i < 12; i++ {
			m.dispatchBackgroundExpiry(ctx, fmt.Sprintf("agent-%02d", i), fail)
			m.startWG.Wait()
		}
		m.startWG.Wait()
		seen := make(map[string]bool)
		for i := 0; i < 12; i++ {
			seen[<-attempts] = true
		}
		require.Len(t, seen, 12)
	}
}

func TestBackgroundExpiryFairnessWithBlockedLanes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("a-blocked-%02d", i)
		subagentTestParentWithModel(t, db, id, store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
		lane := m.laneFor(id)
		lane.serial.Lock()
		defer lane.serial.Unlock()
	}
	subagentTestParentWithModel(t, db, "z-ready", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m.TerminateExpiredBackgroundWorkers(ctx)
		var count int
		err := raw.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id='z-ready' AND kind='terminate'`).Scan(&count)
		return err == nil && count == 1
	}, 5*time.Second, 100*time.Millisecond)
}

func TestBackgroundExpiryBusyClaudeInterruptsBeforeLaneDrains(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "busy-claude", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z' WHERE agent_id=?`, binding.AgentID)
	require.NoError(t, err)
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
	m.TerminateExpiredBackgroundWorkers(ctx)
	select {
	case <-active.interrupted:
	case <-time.After(time.Second):
		t.Fatal("expiry did not interrupt busy Claude")
	}
	agent, err := db.ManagedAgent(ctx, binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "terminated", agent.DesiredState)
}

func TestBackgroundExpiryBusyEmbeddedWaitsAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "busy-embedded", store.ManagedModel{Provider: "openai", ID: "test"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z' WHERE agent_id=?`, binding.AgentID)
	require.NoError(t, err)
	entered := make(chan struct{}, 2)
	m.embeddedTerminationGateTestHook = func() { entered <- struct{}{} }
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	locked := true
	defer func() {
		if locked {
			lane.serial.Unlock()
		}
	}()
	m.TerminateExpiredBackgroundWorkers(ctx)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("expiry did not enter busy embedded termination")
	}
	// Stay busy beyond the old 250ms deadline and run repeated sweeps.
	time.Sleep(600 * time.Millisecond)
	for i := 0; i < 10; i++ {
		m.TerminateExpiredBackgroundWorkers(ctx)
	}
	select {
	case <-entered:
		t.Fatal("duplicate termination entered while worker was busy")
	default:
	}
	lane.serial.Unlock()
	locked = false
	require.Eventually(t, func() bool {
		var count int
		err := raw.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate'`, binding.AgentID).Scan(&count)
		return err == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond)
}

func TestBackgroundExpiryRetriesAsynchronousCleanupFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParentWithModel(t, db, "retry-expiry", store.ManagedModel{Provider: "openai", ID: "test"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z' WHERE agent_id=?`, binding.AgentID)
	require.NoError(t, err)
	var attempts atomic.Int32
	m.beforeTerminationArtifactCleanup = func(context.Context, string) error {
		if attempts.Add(1) == 1 {
			return errors.New("injected cleanup failure")
		}
		return nil
	}
	m.TerminateExpiredBackgroundWorkers(ctx)
	require.Eventually(t, func() bool {
		var count int
		err := raw.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND kind='terminate' AND state='failed'`, binding.AgentID).Scan(&count)
		return err == nil && count == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		m.TerminateExpiredBackgroundWorkers(ctx)
		_, err := db.ManagedAgent(ctx, binding.AgentID)
		return errors.Is(err, sql.ErrNoRows)
	}, 5*time.Second, 10*time.Millisecond)
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
}

func TestBackgroundExpiryDefaultCleansWorktree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	subagentTestParentWithModel(t, db, "expiry-worktree", store.ManagedModel{Provider: "openai", ID: "test"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	base := t.TempDir()
	wt := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.Mkdir(wt, 0755))
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z', worktree=1, worktree_base_path=?, cwd=? WHERE agent_id='expiry-worktree'`, base, wt)
	require.NoError(t, err)
	m.terminateExpiredBackgroundWorker(ctx, "expiry-worktree")
	require.Eventually(t, func() bool { _, err := db.ManagedAgent(ctx, "expiry-worktree"); return errors.Is(err, sql.ErrNoRows) }, 5*time.Second, 10*time.Millisecond)
	_, err = os.Stat(wt)
	require.True(t, os.IsNotExist(err), "normal DELETE default removes worktree")
}

func TestBackgroundExpiryDoesNotResumeManualTermination(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	subagentTestParentWithModel(t, db, "manual-worktree", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	base := t.TempDir()
	wt := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.Mkdir(wt, 0755))
	_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z', worktree=1, worktree_base_path=?, cwd=? WHERE agent_id='manual-worktree'`, base, wt)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TRIGGER reject_termination_updates BEFORE UPDATE ON managed_lifecycle_actions WHEN OLD.kind='terminate' AND OLD.stage != NEW.stage BEGIN SELECT RAISE(ABORT, 'injected lifecycle failure'); END`)
	require.NoError(t, err)
	_, err = m.Terminate(ctx, TerminateRequest{AgentID: "manual-worktree", Method: "DELETE", CanonicalPath: "/v1/managed-agents/manual-worktree", RequestID: "a8eac401-75f5-4447-8d33-2a3009b790bb", CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", CleanupWorktree: true})
	require.NoError(t, err)
	m.startWG.Wait()
	action, err := db.ActiveManagedLifecycleAction(ctx, "manual-worktree")
	require.NoError(t, err)
	_, err = raw.Exec(`DROP TRIGGER reject_termination_updates`)
	require.NoError(t, err)
	m.terminateExpiredBackgroundWorker(ctx, "manual-worktree")
	m.startWG.Wait()
	after, err := db.ActiveManagedLifecycleAction(ctx, "manual-worktree")
	require.NoError(t, err)
	require.Equal(t, action.ActionID, after.ActionID)
	require.Equal(t, action.Stage, after.Stage)
	_, err = os.Stat(wt)
	require.NoError(t, err)
}

func TestBackgroundExpiryConcurrentManualWorktreePolicy(t *testing.T) {
	for _, cleanup := range []bool{true, false} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanup), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			m := makeSubagentTestManager(t, db, nil, 4)
			defer m.Close(ctx)
			subagentTestParentWithModel(t, db, "manual-concurrent", store.ManagedModel{Provider: claudeCodeProvider, ID: "sonnet"})
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer raw.Close()
			base := t.TempDir()
			wt := filepath.Join(t.TempDir(), "worktree")
			require.NoError(t, os.Mkdir(wt, 0755))
			_, err = raw.Exec(`UPDATE managed_agents SET is_background=1, created_at='2000-01-01T00:00:00Z', worktree=1, worktree_base_path=?, cwd=? WHERE agent_id='manual-concurrent'`, base, wt)
			require.NoError(t, err)
			entered := make(chan struct{})
			release := make(chan struct{})
			m.beforeTerminationArtifactCleanup = func(context.Context, string) error { close(entered); <-release; return nil }
			_, err = m.Terminate(ctx, TerminateRequest{AgentID: "manual-concurrent", Method: "DELETE", CanonicalPath: "/v1/managed-agents/manual-concurrent", RequestID: "a8eac401-75f5-4447-8d33-2a3009b790bb", CanonicalBodyHash: "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", CleanupWorktree: cleanup})
			require.NoError(t, err)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("manual cleanup did not start")
			}
			m.TerminateExpiredBackgroundWorkers(ctx)
			close(release)
			require.Eventually(t, func() bool { _, err := db.ManagedAgent(ctx, "manual-concurrent"); return errors.Is(err, sql.ErrNoRows) }, 5*time.Second, 10*time.Millisecond)
			m.startWG.Wait()
			_, err = os.Stat(wt)
			if cleanup {
				require.True(t, os.IsNotExist(err))
			} else {
				require.NoError(t, err)
			}
		})
	}
}

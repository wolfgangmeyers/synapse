package managed

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// 1. Tool parameter handling, defaults (cleanup_worktree: true), explicit args, and schema closure.
func TestWorkerSelfTerminateTool_ExecutionAndParameters(t *testing.T) {
	schema := embeddedWorkerTerminateSchema()
	require.NotNil(t, schema)
	require.Equal(t, "object", schema.Type)

	// Valid argument combinations
	require.True(t, schema.Check(map[string]any{}))
	require.True(t, schema.Check(map[string]any{"reason": "finished task"}))
	require.True(t, schema.Check(map[string]any{"cleanup_worktree": true}))
	require.True(t, schema.Check(map[string]any{"cleanup_worktree": false}))
	require.True(t, schema.Check(map[string]any{"reason": "finished task", "cleanup_worktree": false}))

	// Schema closure: rejection of unknown parameters / additional properties
	require.False(t, schema.Check(map[string]any{"unknown_param": "bad"}))
	require.False(t, schema.Check(map[string]any{"target_agent": "other"}))

	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-term-params"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}

	var capturedReq *workerSelfTerminateRequest
	tool := mgr.embeddedWorkerTerminateTool(owner, func(req workerSelfTerminateRequest) {
		capturedReq = &req
	})

	require.Equal(t, "mecha_worker_terminate", tool.Name)

	// Test 1: Default arguments (no reason, no cleanup_worktree specified)
	capturedReq = nil
	res, err := tool.Execute(ctx, "call-1", map[string]any{}, nil)
	require.NoError(t, err)
	require.True(t, res.Terminate, "must signal immediate turn loop halt via Terminate: true")
	require.NotNil(t, capturedReq)
	require.Equal(t, "worker self-terminated", capturedReq.reason)
	require.True(t, capturedReq.cleanupWorktree, "default cleanup_worktree must be true")
	require.Len(t, res.Content, 1)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "Worker self-termination scheduled")

	// Test 2: Explicit cleanup_worktree: false and custom reason
	capturedReq = nil
	res, err = tool.Execute(ctx, "call-2", map[string]any{
		"reason":           "task done, keeping tree",
		"cleanup_worktree": false,
	}, nil)
	require.NoError(t, err)
	require.True(t, res.Terminate)
	require.NotNil(t, capturedReq)
	require.Equal(t, "task done, keeping tree", capturedReq.reason)
	require.False(t, capturedReq.cleanupWorktree)

	// Test 3: Explicit cleanup_worktree: true
	capturedReq = nil
	res, err = tool.Execute(ctx, "call-3", map[string]any{
		"cleanup_worktree": true,
	}, nil)
	require.NoError(t, err)
	require.True(t, res.Terminate)
	require.NotNil(t, capturedReq)
	require.True(t, capturedReq.cleanupWorktree)
}

// 2. Role restrictions: rejection of non-worker / root callers.
func TestWorkerSelfTerminateTool_RoleRestrictions(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	mgr := &Manager{store: db}

	// Case 1: Role is "root"
	rootID := "root-agent"
	rootBinding := subagentTestParent(t, db, rootID)
	// Change role to root
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `UPDATE embedded_sessions SET role='root' WHERE agent_id=?`, rootID)
	require.NoError(t, err)

	rootOwner := embeddedOwner{binding: rootBinding, identity: "fixture:" + rootID}

	called := false
	rootTool := mgr.embeddedWorkerTerminateTool(rootOwner, func(req workerSelfTerminateRequest) {
		called = true
	})

	_, err = rootTool.Execute(ctx, "call-root", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "only permitted for worker agents")
	require.False(t, called, "callback must not be invoked on role restriction failure")

	// Case 2: Unregistered / unknown role
	unknownID := "unknown-agent"
	unknownBinding := store.EmbeddedBinding{AgentID: unknownID, SessionID: "session-unknown", Epoch: 1, Generation: "gen-1"}
	unknownOwner := embeddedOwner{binding: unknownBinding, identity: "fixture:" + unknownID}

	called = false
	unknownTool := mgr.embeddedWorkerTerminateTool(unknownOwner, func(req workerSelfTerminateRequest) {
		called = true
	})

	_, err = unknownTool.Execute(ctx, "call-unknown", map[string]any{}, nil)
	require.Error(t, err)
	require.False(t, called)

	// Case 3: Fail closed when store is nil
	nilStoreMgr := &Manager{store: nil}
	nilStoreTool := nilStoreMgr.embeddedWorkerTerminateTool(rootOwner, func(req workerSelfTerminateRequest) {
		called = true
	})
	_, err = nilStoreTool.Execute(ctx, "call-nil-store", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "store unavailable for worker termination")
}

// 3. Turn loop halt and full termination via runEmbeddedSessionPrompt.
func TestWorkerSelfTerminate_TurnLoopHalt(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "mock-term-halt",
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	var turnStep atomic.Int32
	var secondStepReached atomic.Bool

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			step := turnStep.Add(1)
			if step == 1 {
				// Turn 1: Model invokes mecha_worker_terminate
				return providers.FauxAssistantMessage(ai.ContentList{
					providers.FauxToolCall("mecha_worker_terminate", map[string]any{
						"reason": "finished task",
					}, "call-term-1"),
				}, ai.StopToolUse)
			}
			// If Terminate: true works, the SDK loop halts immediately and subsequent steps in the same turn are not reached
			secondStepReached.Store(true)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Unexpected continuation after termination"},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	workerBinding := subagentTestParent(t, db, "worker-halt-agent")
	registerParentOwner(mgr, workerBinding)

	savedAgent, err := db.ManagedAgent(ctx, workerBinding.AgentID)
	require.NoError(t, err)

	owner := embeddedOwner{binding: workerBinding, identity: "fixture:" + workerBinding.AgentID}
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Please finish your work and terminate.")
	require.NoError(t, err)

	require.False(t, secondStepReached.Load(), "second step in turn must not be reached when tool returns Terminate: true")
	require.Equal(t, int32(1), turnStep.Load())

	// Assert agent termination in store following the turn
	require.Eventually(t, func() bool {
		_, err := db.ManagedAgent(ctx, workerBinding.AgentID)
		return errors.Is(err, sql.ErrNoRows)
	}, 5*time.Second, 50*time.Millisecond, "agent should be terminated in store after invoking mecha_worker_terminate in turn")
}

// 4. End-to-end lifecycle with worktree cleanup.
func TestWorkerSelfTerminate_EndToEndLifecycle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-e2e-wt-cleanup"
	baseRepoDir := t.TempDir()
	wtDir := filepath.Join(t.TempDir(), "test-worktree")
	err = os.MkdirAll(wtDir, 0755)
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "mock-e2e",
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	workerBinding := subagentTestParent(t, db, agentID)
	registerParentOwner(mgr, workerBinding)

	// Set worktree columns and cwd on agent record
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_agents SET worktree=1, worktree_base_path=?, worktree_branch='feature/worker', cwd=? WHERE agent_id=?`, baseRepoDir, wtDir, agentID)
	require.NoError(t, err)

	// Request worker self termination
	mgr.requestWorkerSelfTermination(agentID, workerSelfTerminateRequest{
		reason:          "job done",
		cleanupWorktree: true,
	})

	require.True(t, mgr.isWorkerSelfTerminationPending(agentID))

	// Execute turn boundary post-processing
	dispatched := mgr.checkWorkerSelfTerminationAfterTurn(ctx, agentID)
	require.True(t, dispatched)

	// Await termination completion (agent row is deleted upon successful termination)
	require.Eventually(t, func() bool {
		_, err := db.ManagedAgent(ctx, agentID)
		return errors.Is(err, sql.ErrNoRows)
	}, 5*time.Second, 50*time.Millisecond)

	require.False(t, mgr.isWorkerSelfTerminationPending(agentID))

	// Verify worktree directory is removed from filesystem
	_, statErr := os.Stat(wtDir)
	require.True(t, os.IsNotExist(statErr), "worktree directory must be removed when cleanup_worktree: true")
}

// 5. Worktree cleanup disabled (cleanup_worktree: false).
func TestWorkerSelfTerminate_NoWorktreeCleanupWhenDisabled(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-no-wt-cleanup"
	baseRepoDir := t.TempDir()
	wtDir := filepath.Join(t.TempDir(), "test-worktree-kept")
	err = os.MkdirAll(wtDir, 0755)
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "mock-no-wt",
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	workerBinding := subagentTestParent(t, db, agentID)
	registerParentOwner(mgr, workerBinding)

	// Set worktree columns and cwd on agent record
	rawDB2, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB2.Close()
	_, err = rawDB2.ExecContext(ctx, `UPDATE managed_agents SET worktree=1, worktree_base_path=?, worktree_branch='feature/worker', cwd=? WHERE agent_id=?`, baseRepoDir, wtDir, agentID)
	require.NoError(t, err)

	mgr.requestWorkerSelfTermination(agentID, workerSelfTerminateRequest{
		reason:          "job done, preserve wt",
		cleanupWorktree: false,
	})

	dispatched := mgr.checkWorkerSelfTerminationAfterTurn(ctx, agentID)
	require.True(t, dispatched)

	require.Eventually(t, func() bool {
		_, err := db.ManagedAgent(ctx, agentID)
		return errors.Is(err, sql.ErrNoRows)
	}, 5*time.Second, 50*time.Millisecond)

	// Worktree directory must still exist on disk
	fi, statErr := os.Stat(wtDir)
	require.NoError(t, statErr, "worktree directory must NOT be removed when cleanup_worktree: false")
	require.True(t, fi.IsDir())
}

// 6. Deadlock prevention when invoked under lane.serial.Lock().
func TestWorkerSelfTerminate_DeadlockPrevention(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-deadlock-test"
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "mock-deadlock",
			ContextWindow: 128000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	workerBinding := subagentTestParent(t, db, agentID)
	registerParentOwner(mgr, workerBinding)

	lane := mgr.laneFor(agentID)

	// Simulate turn execution where lane.serial is locked during the turn
	lane.serial.Lock()

	mgr.requestWorkerSelfTermination(agentID, workerSelfTerminateRequest{
		reason:          "terminating under lane lock",
		cleanupWorktree: false,
	})

	// checkWorkerSelfTerminationAfterTurn is called at turn boundary
	dispatched := mgr.checkWorkerSelfTerminationAfterTurn(ctx, agentID)
	require.True(t, dispatched)

	// Release lane.serial lock at the end of the turn
	lane.serial.Unlock()

	// The background termination task must successfully acquire lane locks without deadlocking
	require.Eventually(t, func() bool {
		_, err := db.ManagedAgent(ctx, agentID)
		return errors.Is(err, sql.ErrNoRows)
	}, 5*time.Second, 50*time.Millisecond, "agent should terminate without deadlocking")
}

// 7. Goal continuation suppression when self-termination is pending.
func TestWorkerSelfTerminate_GoalContinuationSuppression(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-goal-suppress"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}

	mgr := &Manager{store: db}

	require.False(t, mgr.isWorkerSelfTerminationPending(agentID))

	// Request termination
	mgr.requestWorkerSelfTermination(agentID, workerSelfTerminateRequest{
		reason:          "suppress goal continuation",
		cleanupWorktree: false,
	})

	require.True(t, mgr.isWorkerSelfTerminationPending(agentID))

	// Check goal continuation suppression check
	require.True(t, mgr.isWorkerSelfTerminationPending(binding.AgentID))
}

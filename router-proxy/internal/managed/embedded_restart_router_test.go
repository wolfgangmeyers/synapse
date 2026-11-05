package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// TestRestartRouterTool_EmptyReason verifies that an empty or whitespace-only reason
// returns an explicit error/rejection without initiating a restart.
func TestRestartRouterTool_EmptyReason(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-restart-empty-reason"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	tool := mgr.embeddedRestartRouterTool(owner)

	for _, invalidReason := range []string{"", "   ", "\t\n"} {
		res, err := tool.Execute(ctx, "call-empty", map[string]any{
			"reason": invalidReason,
		}, nil)
		require.NoError(t, err)
		require.Len(t, res.Content, 1)
		text, ok := res.Content[0].(ai.TextContent)
		require.True(t, ok)
		require.Contains(t, text.Text, "Error: reason is required")

		select {
		case code := <-exitCalled:
			t.Fatalf("unexpected exit trigger called with code %d for empty reason %q", code, invalidReason)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestRestartRouterTool_StringForceParameter verifies that passing force="true" as a string
// (e.g. from JSON or CLI caller) functions identically to boolean force=true.
func TestRestartRouterTool_StringForceParameter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-string-force"
	siblingID := "worker-restart-sibling"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	mgr.markAgentTurnActive(siblingID, true)
	tool := mgr.embeddedRestartRouterTool(owner)

	// 1. Initial attempt without force -> rejected
	res1, err := tool.Execute(ctx, "call-step-1", map[string]any{
		"reason": "emergency schema fix",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res1.Content[0].(ai.TextContent).Text, "[RESTART REJECTED - ACTIVE PROCESSES RUNNING]")

	// 2. Second attempt within 60s with string force="true" -> succeeds
	res2, err := tool.Execute(ctx, "call-step-2", map[string]any{
		"reason": "emergency schema fix",
		"force":  "true",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res2.Content, 1)
	text, ok := res2.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART SCHEDULED - FORCED]")

	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forced restart exit trigger")
	}
}

// TestHasValidRestartRouterRejection tests the managedAgentLane's hasValidRestartRouterRejection helper.
func TestHasValidRestartRouterRejection(t *testing.T) {
	var nilLane *managedAgentLane
	require.False(t, nilLane.hasValidRestartRouterRejection(time.Now(), time.Minute))

	lane := &managedAgentLane{}
	now := time.Now()

	// Initially no rejection recorded
	require.False(t, lane.hasValidRestartRouterRejection(now, time.Minute))

	// Record rejection at now
	lane.recordRestartRouterRejection(now, "sibling turn active")
	require.True(t, lane.hasValidRestartRouterRejection(now, time.Minute))
	require.True(t, lane.hasValidRestartRouterRejection(now.Add(30*time.Second), time.Minute))
	require.True(t, lane.hasValidRestartRouterRejection(now.Add(60*time.Second), time.Minute))

	// Rejection beyond TTL is invalid
	require.False(t, lane.hasValidRestartRouterRejection(now.Add(61*time.Second), time.Minute))

	// Future rejection time relative to now (clock skew / negative diff) is invalid
	require.False(t, lane.hasValidRestartRouterRejection(now.Add(-10*time.Second), time.Minute))
}

// TestRestartRouterTool_ImmediateSuccessWhenClear verifies Scenario A:
// When no active sibling workers (turns/goals) and no queued/running subagents exist,
// calling mecha_restart_router succeeds immediately without requiring force=true.
// The restart trigger executes with exit code 75.
func TestRestartRouterTool_ImmediateSuccessWhenClear(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-restart-clear"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	var exitCode atomic.Int32
	exitCalled := make(chan int, 1)

	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCode.Store(int32(code))
			select {
			case exitCalled <- code:
			default:
			}
		},
	}

	tool := mgr.embeddedRestartRouterTool(owner)
	require.Equal(t, "mecha_restart_router", tool.Name)

	res, err := tool.Execute(ctx, "call-1", map[string]any{
		"reason": "applied schema migration",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART SCHEDULED]")
	require.Contains(t, text.Text, "Router restart initiated")
	require.Contains(t, text.Text, "applied schema migration")
	require.Contains(t, text.Text, "75")

	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for restart exit trigger")
	}
}

// TestRestartRouterTool_RejectedWhenOtherWorkersActive verifies Scenario B:
// When another sibling worker has active execution, mecha_restart_router is rejected.
// The caller's lane records the rejection timestamp, lists active conflicts,
// and instructs the caller regarding mecha_reminder or force=true.
func TestRestartRouterTool_RejectedWhenOtherWorkersActive(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-caller"
	siblingID := "worker-restart-sibling"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	// Simulate active sibling worker in Manager's active session or lane
	mgr.markAgentTurnActive(siblingID, true)

	tool := mgr.embeddedRestartRouterTool(owner)
	res, err := tool.Execute(ctx, "call-reject-sibling", map[string]any{
		"reason": "testing sibling conflict",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART REJECTED - ACTIVE PROCESSES RUNNING]")
	require.Contains(t, text.Text, siblingID)
	require.Contains(t, text.Text, "mecha_reminder")
	require.Contains(t, text.Text, "force=true")

	// Ensure exit was not triggered
	select {
	case code := <-exitCalled:
		t.Fatalf("unexpected exit trigger called with code %d", code)
	case <-time.After(50 * time.Millisecond):
	}

	// Verify lane recorded rejection timestamp
	lane := mgr.laneFor(callerID)
	lane.activeMu.RLock()
	rejectedAt := lane.restartRejectedAt
	lane.activeMu.RUnlock()
	require.False(t, rejectedAt.IsZero(), "lane must record rejection timestamp")
}

// TestRestartRouterTool_RejectedWhenActiveSubagentsExist verifies Scenario B for subagents:
// When queued or running subagents exist in memory or durable store,
// mecha_restart_router is rejected and lists the active subagents.
func TestRestartRouterTool_RejectedWhenActiveSubagentsExist(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-subagent"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	// Simulate active subagent
	mgr.markSubagentActiveForTest("subagent-abc", callerID, "running")

	tool := mgr.embeddedRestartRouterTool(owner)
	res, err := tool.Execute(ctx, "call-reject-subagent", map[string]any{
		"reason": "testing subagent conflict",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART REJECTED - ACTIVE PROCESSES RUNNING]")
	require.Contains(t, text.Text, "subagent-abc")
	require.Contains(t, text.Text, "mecha_reminder")
	require.Contains(t, text.Text, "force=true")

	// Ensure exit was not triggered
	select {
	case code := <-exitCalled:
		t.Fatalf("unexpected exit trigger called with code %d", code)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRestartRouterTool_ForceDisallowedWithoutPriorRejection verifies Scenario C:
// Calling mecha_restart_router with force=true on the initial attempt (no prior rejection)
// is rejected immediately with an explicit error explaining force cannot be used preemptively.
func TestRestartRouterTool_ForceDisallowedWithoutPriorRejection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-no-prior-force"
	siblingID := "worker-restart-sibling"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	mgr.markAgentTurnActive(siblingID, true)

	tool := mgr.embeddedRestartRouterTool(owner)
	res, err := tool.Execute(ctx, "call-force-preemptive", map[string]any{
		"reason": "emergency fix attempt",
		"force":  true,
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART REJECTED - INVALID FORCE USAGE]")
	require.Contains(t, text.Text, "force flag cannot be used until the request has been evaluated and rejected")

	select {
	case code := <-exitCalled:
		t.Fatalf("unexpected exit trigger called with code %d", code)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRestartRouterTool_ForceAllowedAfterPriorRejection verifies Scenario D:
// When a prior rejection occurred within the 60-second window, calling with force=true succeeds
// despite active conflicts, triggers exit code 75, and clears the lane's rejection state.
func TestRestartRouterTool_ForceAllowedAfterPriorRejection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-force-valid"
	siblingID := "worker-restart-sibling"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	mgr.markAgentTurnActive(siblingID, true)
	tool := mgr.embeddedRestartRouterTool(owner)

	// 1. Initial attempt without force -> rejected
	res1, err := tool.Execute(ctx, "call-step-1", map[string]any{
		"reason": "emergency update",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res1.Content[0].(ai.TextContent).Text, "[RESTART REJECTED - ACTIVE PROCESSES RUNNING]")

	// 2. Second attempt within 60s with force=true -> succeeds
	res2, err := tool.Execute(ctx, "call-step-2", map[string]any{
		"reason": "emergency update",
		"force":  true,
	}, nil)
	require.NoError(t, err)
	require.Len(t, res2.Content, 1)
	text, ok := res2.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART SCHEDULED - FORCED]")
	require.Contains(t, text.Text, "Router restart initiated with force override")

	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forced restart exit trigger")
	}

	// Verify lane rejection state was cleared
	lane := mgr.laneFor(callerID)
	lane.activeMu.RLock()
	rejectedAt := lane.restartRejectedAt
	lane.activeMu.RUnlock()
	require.True(t, rejectedAt.IsZero(), "lane rejection state must be cleared after successful restart")
}

// TestRestartRouterTool_ForceExpiredRejection verifies Scenario E:
// If prior rejection is older than 60 seconds, calling with force=true is rejected
// with an expired rejection message.
func TestRestartRouterTool_ForceExpiredRejection(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	callerID := "worker-restart-force-expired"
	siblingID := "worker-restart-sibling"
	binding := store.EmbeddedBinding{AgentID: callerID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			exitCalled <- code
		},
	}

	mgr.markAgentTurnActive(siblingID, true)
	tool := mgr.embeddedRestartRouterTool(owner)

	// Set lane rejection timestamp to 65 seconds ago
	lane := mgr.laneFor(callerID)
	lane.activeMu.Lock()
	lane.restartRejectedAt = time.Now().Add(-65 * time.Second)
	lane.activeMu.Unlock()

	res, err := tool.Execute(ctx, "call-force-expired", map[string]any{
		"reason": "late force attempt",
		"force":  true,
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART REJECTED - PRIOR ATTEMPT EXPIRED]")
	require.Contains(t, text.Text, "Prior rejection occurred more than 60 seconds ago")

	select {
	case code := <-exitCalled:
		t.Fatalf("unexpected exit trigger called with code %d", code)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestRestartRouterTool_CanonicalName verifies that only the canonical name `mecha_restart_router`
// is registered and returned.
func TestRestartRouterTool_CanonicalName(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-restart-canonical"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedRestartRouterTool(owner)

	require.Equal(t, "mecha_restart_router", tool.Name)
	require.NotEmpty(t, tool.Description)
	require.NotNil(t, tool.Parameters)
}

// TestRestartRouterTool_ExitCodeAndFile verifies exit code 75 protocol
// and ROUTER_EXIT_FILE update when configured.
func TestRestartRouterTool_ExitCodeAndFile(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	exitFile := filepath.Join(t.TempDir(), "router.exit_code")
	t.Setenv("ROUTER_EXIT_FILE", exitFile)

	agentID := "worker-restart-exit-file"
	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	exitCalled := make(chan int, 1)
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			// Simulate defaultProcessExitTrigger writing to ROUTER_EXIT_FILE
			defaultProcessExitTrigger(code)
			exitCalled <- code
		},
	}

	tool := mgr.embeddedRestartRouterTool(owner)
	res, err := tool.Execute(ctx, "call-exit-file", map[string]any{
		"reason": "testing exit file writing",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, res.Content[0].(ai.TextContent).Text, "[RESTART SCHEDULED]")

	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for exit trigger")
	}

	content, err := os.ReadFile(exitFile)
	require.NoError(t, err)
	require.Equal(t, "75", strings.TrimSpace(string(content)))
}

// TestRestartRouterTool_BehavioralLifecycle executes a full behavioral integration test
// where a session executes a turn with the model calling mecha_restart_router, proving end-to-end
// within-service integration:
// - Tool is executed by the session engine
// - Transcript / entry is persisted in store
// - Restart trigger is invoked with code 75
func TestRestartRouterTool_BehavioralLifecycle(t *testing.T) {
	ctx := context.Background()
	m, db, provider, caller, root := requiredToolsFixture(t, "worker")

	exitCalled := make(chan int, 1)
	m.restartExitTrigger = func(code int) {
		select {
		case exitCalled <- code:
		default:
		}
	}

	callID := "call-behavioral-restart"
	args := map[string]any{
		"reason": "applied schema migration 105",
	}

	result := requiredToolsCall(t, m, db, provider, caller, "mecha_restart_router", callID, args)
	require.Equal(t, "mecha_restart_router", result.ToolName)
	require.Equal(t, callID, result.ToolCallID)
	require.False(t, result.IsError)

	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "[RESTART SCHEDULED]")
	require.Contains(t, text.Text, "applied schema migration 105")

	// 1. Verify restart trigger was invoked with code 75
	select {
	case code := <-exitCalled:
		require.Equal(t, 75, code)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for restart exit trigger to be invoked with code 75")
	}

	// 2. Verify transcript / entry is persisted in store (kind='toolResult' containing the restart result)
	rawDB, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
	require.NoError(t, err)
	defer rawDB.Close()

	var toolResultCount int
	var toolResultBody string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT count(*), max(CAST(body AS TEXT)) FROM embedded_entries WHERE agent_id=? AND kind='toolResult' AND instr(body, ?) > 0`, caller, callID).Scan(&toolResultCount, &toolResultBody))
	require.GreaterOrEqual(t, toolResultCount, 1, "toolResult entry must be persisted in embedded_entries")
	require.Contains(t, toolResultBody, "[RESTART SCHEDULED]")
	require.Contains(t, toolResultBody, "applied schema migration 105")
}

// TestRestartRouter_RoleExclusion asserts mecha_restart_router and restart_router are absent
// from EmbeddedSubagentTools and EmbeddedSubagentPublicTools, and are only available for root and worker.
func TestRestartRouter_RoleExclusion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	mgr := &Manager{
		store: db,
	}

	// 1. EmbeddedSubagentTools (tools for child subagents) must NOT contain mecha_restart_router or restart_router
	childTools, cleanup, err := mgr.EmbeddedSubagentTools(t.TempDir())
	require.NoError(t, err)
	defer cleanup()

	for _, ct := range childTools {
		require.NotEqual(t, "mecha_restart_router", ct.Name, "subagent child tools must not contain mecha_restart_router")
		require.NotEqual(t, "restart_router", ct.Name, "subagent child tools must not contain restart_router")
	}

	// 2. EmbeddedSubagentPublicTools (public tools exposed to parent for managing subagents) must NOT contain mecha_restart_router or restart_router
	parentBinding := store.EmbeddedBinding{AgentID: "worker-parent", SessionID: "session-p", Epoch: 1, Generation: "gen-p"}
	subagentPublicTools := mgr.EmbeddedSubagentPublicTools(parentBinding)
	for _, pt := range subagentPublicTools {
		require.NotEqual(t, "mecha_restart_router", pt.Name, "subagent public tools must not contain mecha_restart_router")
		require.NotEqual(t, "restart_router", pt.Name, "subagent public tools must not contain restart_router")
	}

	// 3. EmbeddedSessionRole check: verify execution enforcement only permits root or worker.
	// For root and worker sessions, the tool is included in the session tools list, whereas
	// for non-root/non-worker sessions it is not registered at all.
	// Furthermore, verify via requiredToolsFixture that both "root" and "worker" register the tool.
	for _, role := range []string{"root", "worker"} {
		mRole, _, _, callerID, _ := requiredToolsFixture(t, role)
		lane := mRole.laneFor(callerID)
		require.NotNil(t, lane)
		baseTools, _ := lane.getTools()
		hasMechaRestartRouter := false
		hasLegacyRestartRouter := false
		for _, tool := range baseTools {
			if tool.Name == "mecha_restart_router" {
				hasMechaRestartRouter = true
			}
			if tool.Name == "restart_router" {
				hasLegacyRestartRouter = true
			}
		}
		require.True(t, hasMechaRestartRouter, "role %s must have mecha_restart_router in base tools", role)
		require.False(t, hasLegacyRestartRouter, "role %s must NOT have restart_router alias in base tools", role)
	}
}

// TestRestartRouter_Concurrency_AtomicSingleExit verifies that launching multiple
// concurrent goroutines attempting to restart executes TriggerRouterRestart and the
// exit trigger exactly once.
func TestRestartRouter_Concurrency_AtomicSingleExit(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	exitFile := filepath.Join(t.TempDir(), "router.exit_code")
	t.Setenv("ROUTER_EXIT_FILE", exitFile)

	var exitCallCount int64
	mgr := &Manager{
		store: db,
		restartExitTrigger: func(code int) {
			atomic.AddInt64(&exitCallCount, 1)
			require.Equal(t, 75, code)
		},
	}

	const concurrency = 20
	ready := make(chan struct{})
	done := make(chan struct{}, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			<-ready
			mgr.TriggerRouterRestart()
			done <- struct{}{}
		}()
	}

	close(ready)

	for i := 0; i < concurrency; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent restart triggers to complete")
		}
	}

	require.Equal(t, int64(1), atomic.LoadInt64(&exitCallCount), "exit trigger must be executed exactly once")

	content, err := os.ReadFile(exitFile)
	require.NoError(t, err)
	require.Equal(t, "75", strings.TrimSpace(string(content)))
}

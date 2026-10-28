package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestBlockedGoalContinuationSuppression(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openTestStore(t, ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-blocked-goal-test"
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		SlotHeld:         true,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		AcceptingPrompts: true,
		PiSessionID:      "session-" + agentID,
		SessionEpoch:     1,
		Runtime:          "embedded",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash",
		CreatedAt:        "2026-01-01T00:00:00Z",
		UpdatedAt:        "2026-01-01T00:00:00Z",
	}
	op := store.ManagedOperation{
		OperationID:    "op-" + agentID,
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-" + agentID,
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchRecord, op))

	binding := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-" + agentID, Epoch: 1, Generation: "gen-1"}

	// Set active goal policy directly in DB
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1, goal_text='Complete the critical task', applied_revision=1, desired_revision=1, projection_state='applied' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	mgr := &Manager{
		store:  db,
		runCtx: ctx,
	}

	var continuationTurns atomic.Int32
	// Test startEmbeddedGoalContinuationWithPrompt when blocked
	key := store.EmbeddedGoalEvaluationKey{
		UserSeq:      1,
		UserDigest:   "d1",
		GoalRevision: 1,
		FinalSeq:     2,
		FinalDigest:  "d2",
	}

	// 1. Mark agent as blocked
	require.NoError(t, db.SetAgentBlocked(ctx, agentID, true, "Missing credentials"))

	// Calling startEmbeddedGoalContinuationWithPrompt should be suppressed
	mgr.startEmbeddedGoalContinuationWithPrompt(binding, key, "Continuation test")
	require.Equal(t, int32(0), continuationTurns.Load(), "continuation must not run when agent is blocked")

	// Calling kickoffIdleGoalContinuation should be suppressed
	mgr.kickoffIdleGoalContinuation(binding)
	require.Equal(t, int32(0), continuationTurns.Load(), "idle continuation must not run when agent is blocked")

	// Calling evaluateEmbeddedTurn should also return early and not evaluate
	appliedReceipt := store.EmbeddedGoalAppliedReceipt{
		EmbeddedGoalAppliedReceiptRequest: store.EmbeddedGoalAppliedReceiptRequest{
			GoalRevision:    1,
			TurnEntrySeq:    1,
			TurnEntryDigest: "d1",
		},
		GoalEnabled: true,
	}
	p := &embeddedPersistence{
		turnSeq:    1,
		turnDigest: "d1",
	}
	evalErr := mgr.evaluateEmbeddedTurn(ctx, binding, p, &appliedReceipt, nil, nil)
	require.NoError(t, evalErr)

	// 2. Unblock agent
	require.NoError(t, db.SetAgentBlocked(ctx, agentID, false, ""))

	curAgent, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.False(t, curAgent.Blocked)
}

func TestEmbeddedBlockedToolRegistration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := openTestStore(t, ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-tool-reg-test"
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             agentID,
		SlotHeld:         true,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		AcceptingPrompts: true,
		PiSessionID:      "session-" + agentID,
		SessionEpoch:     1,
		Runtime:          "embedded",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash",
		CreatedAt:        "2026-01-01T00:00:00Z",
		UpdatedAt:        "2026-01-01T00:00:00Z",
	}
	op := store.ManagedOperation{
		OperationID:    "op-" + agentID,
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-" + agentID,
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchRecord, op))

	mgr := &Manager{store: db}
	owner := embeddedOwner{
		binding:  store.EmbeddedBinding{AgentID: agentID, SessionID: "session-" + agentID, Epoch: 1, Generation: "gen-1"},
		identity: "fixture:" + agentID,
	}
	tool := mgr.embeddedBlockedTool(owner)
	require.Equal(t, "mecha_blocked", tool.Name)
	require.NotNil(t, tool.Parameters)
	require.NotNil(t, tool.Execute)
	require.Contains(t, tool.Description, "NOT a workflow state")
}

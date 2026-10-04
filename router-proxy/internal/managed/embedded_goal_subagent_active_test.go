package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestGoalEvaluationSkippedWhileSubagentsActive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	var evaluatorCalled atomic.Int32
	mgr.embeddedGoalEvaluator = func(evalCtx context.Context, evalKey store.EmbeddedGoalEvaluationKey) (string, string, error) {
		evaluatorCalled.Add(1)
		return store.EmbeddedGoalEvaluationDone, "goal finished", nil
	}

	launched, err := mgr.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:      "parent-worker-goal",
		Cwd:       root,
		Runtime:   "embedded",
		RequestID: "req-1",
	})
	require.NoError(t, err)

	binding := store.EmbeddedBinding{
		AgentID:    launched.Agent.AgentID,
		SessionID:  launched.Agent.PiSessionID,
		Epoch:      launched.Agent.SessionEpoch,
		Generation: launched.Agent.RuntimeGeneration,
		OwnerToken: 1,
	}

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1, goal_text='Complete the assignment', applied_revision=1, desired_revision=1, projection_state='applied' WHERE agent_id=?`,
		binding.AgentID)
	require.NoError(t, err)

	receipt := &store.EmbeddedGoalAppliedReceipt{
		EmbeddedGoalAppliedReceiptRequest: store.EmbeddedGoalAppliedReceiptRequest{
			GoalRevision:    1,
			TurnEntrySeq:    1,
			TurnEntryDigest: "digest-turn-1",
		},
		GoalEnabled: true,
	}
	p := &embeddedPersistence{
		turnSeq:    1,
		turnDigest: "digest-turn-1",
		seq:        2,
		digest:     "digest-assistant-2",
	}

	// 1. In-memory subagent is active (running)
	mgr.subagentsMu.Lock()
	mgr.subagentsActive["child-running-1"] = &subagentTask{
		parent:  binding,
		childID: "child-running-1",
		status:  "running",
	}
	mgr.subagentsMu.Unlock()

	err = mgr.evaluateEmbeddedTurn(ctx, binding, p, receipt, nil, nil)
	require.NoError(t, err)
	require.Zero(t, evaluatorCalled.Load(), "goal evaluation must NOT run while in-memory subagent is active (running)")

	// 2. In-memory subagent is active (queued)
	mgr.subagentsMu.Lock()
	mgr.subagentsActive["child-running-1"].status = "queued"
	mgr.subagentsMu.Unlock()

	err = mgr.evaluateEmbeddedTurn(ctx, binding, p, receipt, nil, nil)
	require.NoError(t, err)
	require.Zero(t, evaluatorCalled.Load(), "goal evaluation must NOT run while in-memory subagent is active (queued)")

	// 3. Clear in-memory subagent, but store has an active subagent (running)
	mgr.subagentsMu.Lock()
	delete(mgr.subagentsActive, "child-running-1")
	mgr.subagentsMu.Unlock()

	err = db.CreateManagedSubagent(ctx, binding, store.ManagedSubagentRecord{
		AgentID:       "child-store-running-1",
		ParentAgentID: binding.AgentID,
		Description:   "subagent running in store",
		Status:        "running",
	})
	require.NoError(t, err)

	err = mgr.evaluateEmbeddedTurn(ctx, binding, p, receipt, nil, nil)
	require.NoError(t, err)
	require.Zero(t, evaluatorCalled.Load(), "goal evaluation must NOT run while store has an active subagent (running)")

	// 4. Store has an active subagent (queued)
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_subagents SET status='queued' WHERE agent_id='child-store-running-1'`)
	require.NoError(t, err)

	err = mgr.evaluateEmbeddedTurn(ctx, binding, p, receipt, nil, nil)
	require.NoError(t, err)
	require.Zero(t, evaluatorCalled.Load(), "goal evaluation must NOT run while store has an active subagent (queued)")
}

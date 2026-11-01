package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// 1. In `router-proxy/internal/managed/`: write a test verifying that when an agent calls `goal_snooze` alone
// (tool-only turn without assistant text), it does NOT count as an empty turn / re-nudge (`lane.consecutiveEmptyTurns` remains 0,
// no empty turn re-nudge prompt is sent).
func TestEmbeddedGoalSnoozeToolOnlyTurnDoesNotCountAsEmptyTurn(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return nil },
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	agentID := "worker-snooze-test-1"
	binding := subagentTestParent(t, db, agentID)

	// Set active goal on agent policy
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()

	_, err = rawDB.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1, goal_text='Work on task', applied_revision=1, desired_revision=1, projection_state='applied' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	// User turn entry
	userMsg, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "Please start"}}})
	require.NoError(t, err)
	userEntry, err := db.AppendEmbeddedEntry(ctx, binding, "user", userMsg)
	require.NoError(t, err)
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, userEntry.Sequence, userEntry.Digest))

	// Commit applied receipt so ClaimEmbeddedGoalEvaluation succeeds
	appliedReceipt, _, err := db.CommitEmbeddedGoalAppliedReceipt(ctx, binding, store.EmbeddedGoalAppliedReceiptRequest{
		TurnKey:         "turn-receipt-test-1",
		TurnEntrySeq:    userEntry.Sequence,
		TurnEntryDigest: userEntry.Digest,
		GoalRevision:    1,
	})
	require.NoError(t, err)

	// Assistant calls goal_snooze alone (no text content)
	asstMsg, err := json.Marshal(ai.AssistantMessage{
		Content: ai.ContentList{
			ai.ToolCall{
				ID:   "call-snooze-1",
				Name: "goal_snooze",
				Arguments: map[string]any{
					"duration_ms": int64(60000),
				},
			},
		},
		StopReason: ai.StopStop,
	})
	require.NoError(t, err)
	asstEntry, err := db.AppendEmbeddedEntry(ctx, binding, "assistant", asstMsg)
	require.NoError(t, err)

	p := &embeddedPersistence{
		turnSeq:    userEntry.Sequence,
		turnDigest: userEntry.Digest,
		seq:        asstEntry.Sequence,
		digest:     asstEntry.Digest,
	}

	lane := mgr.laneFor(agentID)
	lane.consecutiveEmptyTurns = 0

	err = mgr.evaluateEmbeddedTurn(ctx, binding, p, &appliedReceipt, nil, nil)
	require.NoError(t, err)

	// Verify that lane.consecutiveEmptyTurns remains 0 (not counted as an empty turn)
	require.Equal(t, 0, lane.consecutiveEmptyTurns, "goal_snooze tool call alone must not be counted as empty turn")
}

// 2. Write a test verifying that when `SetManagedAgentActiveWorkflowState` / `embeddedWorkflowTransitionTool` transitions
// to a new state with a goal, existing custom goal settings (e.g. `SnoozeDurationMS: 600000`) on the agent policy
// are preserved rather than overwritten by `DefaultManagedGoalSettings()`.
func TestEmbeddedWorkflowTransitionPreservesCustomGoalSettings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-custom-goal-settings"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedWorkflowTransitionTool(owner, "mecha_workflow_transition")

	// Create workflow with State 1 and State 2
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Custom Settings WF"})
	require.NoError(t, err)

	state2, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:       "State2",
		AllowExit:  ptr(true),
		WorkflowID: &wf.ID,
	})
	require.NoError(t, err)

	state1, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:       "State1",
		AllowExit:  ptr(true),
		WorkflowID: &wf.ID,
		Transitions: []store.WorkflowTransitionConfig{
			{TargetStateID: state2.ID, Description: "Go to state 2"},
		},
	})
	require.NoError(t, err)

	// Set active workflow state to State 1
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, &wf.ID, &state1.ID, nil))

	// Configure custom goal settings on agent policy: SnoozeDurationMS: 600000
	customSettings := store.DefaultManagedGoalSettings()
	customSettings.SnoozeDurationMS = 600000
	customSettings.GoalLockEnabled = true
	_, err = db.UpdateManagedGoalConfig(ctx, agentID, store.ManagedGoalConfig{
		Enabled:  true,
		Goal:     "Complete active workflow: " + wf.Name,
		Settings: customSettings,
	})
	require.NoError(t, err)

	// Verify custom settings are present before transition
	policyBefore, err := db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, int64(600000), policyBefore.Config.Settings.SnoozeDurationMS)

	// Transition to State 2
	res, err := tool.Execute(ctx, "call-trans-custom", map[string]any{
		"action":       "transition",
		"target_state": "State2",
	}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)

	// Verify policy after transition: Goal is preserved, GoalLockEnabled is true,
	// and custom SnoozeDurationMS (600000) is preserved rather than reset to DefaultManagedGoalSettings() (300000).
	policyAfter, err := db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.True(t, policyAfter.Config.Enabled)
	require.Equal(t, "Complete active workflow: "+wf.Name, policyAfter.Config.Goal)
	require.True(t, policyAfter.Config.Settings.GoalLockEnabled)
	require.Equal(t, int64(600000), policyAfter.Config.Settings.SnoozeDurationMS, "custom SnoozeDurationMS must be preserved across workflow state transitions")
}

// 3. Write a test verifying `MaybeResumeEmbeddedGoal` does not resume an idle agent when an active
// `CurrentEmbeddedGoalSnoozePreference` is present with `SnoozeUntil > now`.
func TestMaybeResumeEmbeddedGoalDoesNotResumeWhenSnoozed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	fixedNow := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(fixedNow)

	db, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()

	agentID := "worker-snooze-resume-check"
	binding := subagentTestParent(t, db, agentID)

	// Ensure agent is idle and desired running
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()

	nowStr := formatManagedTime(fixedNow)
	futureSnooze := formatManagedTime(fixedNow.Add(10 * time.Minute))

	// Enable goal
	_, err = rawDB.ExecContext(ctx, `UPDATE managed_agent_policies SET goal_enabled=1, goal_text='Test goal', desired_revision=1, applied_revision=1, evaluator_state='idle', snoozed_until='' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	// Record a user message for initiating turn
	userMsg, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "start turn"}}})
	require.NoError(t, err)
	userEntry, err := db.AppendEmbeddedEntry(ctx, binding, "user", userMsg)
	require.NoError(t, err)
	require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, userEntry.Sequence, userEntry.Digest))

	// Record an embedded goal snooze preference in store with due_at in the future
	callID := "call-snooze-test-active"
	intent, err := json.Marshal(map[string]any{"id": callID, "name": "goal_snooze", "arguments": map[string]any{"duration_ms": int64(600000)}})
	require.NoError(t, err)
	gen, err := db.MintEmbeddedProcessGeneration(ctx, "snooze-pref-process")
	require.NoError(t, err)
	invocation, err := db.RecordEmbeddedGoalInvocation(ctx, binding, gen, userEntry.Sequence, userEntry.Digest, "", callID, intent)
	require.NoError(t, err)
	require.NoError(t, db.CheckEmbeddedGoalInvocation(ctx, invocation))

	rawArgs, err := json.Marshal(map[string]any{"duration_ms": int64(600000)})
	require.NoError(t, err)
	turn := store.EmbeddedGoalSnoozeTurn{UserSeq: userEntry.Sequence, UserDigest: userEntry.Digest, GoalRevision: 1}
	pref, replay, err := db.SaveCurrentEmbeddedGoalSnoozePreference(ctx, binding, turn, callID, rawArgs, invocation)
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, futureSnooze, pref.DueAt)

	// Verify CurrentEmbeddedGoalSnoozePreference returns the preference with SnoozeUntil > now
	currentPref, err := db.CurrentEmbeddedGoalSnoozePreference(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, currentPref)
	require.True(t, currentPref.Active)
	require.Greater(t, currentPref.SnoozeUntil, nowStr)

	// Now construct Manager via NewManager and test MaybeResumeEmbeddedGoal
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	mgr, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Clock:                 clock,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return nil },
	})
	require.NoError(t, err)
	defer mgr.Close(ctx)

	// Calling MaybeResumeEmbeddedGoal directly must not start a continuation because SnoozeUntil > now
	mgr.MaybeResumeEmbeddedGoal(agentID)

	// Give any async tasks a moment to settle
	time.Sleep(100 * time.Millisecond)

	// Verify agent remains idle and no started continuation exists
	hasPending, err := db.HasPendingWakeOrStartedContinuation(ctx, binding)
	require.NoError(t, err)
	require.False(t, hasPending, "MaybeResumeEmbeddedGoal must not resume or mark started when active snooze preference is in the future")

	agent, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)
}

package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T {
	return &v
}

func TestFormatWorkflowStateGuidance(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "guidance_test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	targetState, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Review",
		Description: "Reviewing pull request",
		EntryText:   "Run robot-review tools",
		AllowExit:   ptr(true),
	})
	require.NoError(t, err)

	sourceState, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Plan",
		Description: "Planning phase",
		EntryText:   "Write SPEC.md and PLAN.md",
		AllowExit:   ptr(false),
		Transitions: []store.WorkflowTransitionConfig{
			{TargetStateID: targetState.ID, Description: "Proceed to review"},
		},
	})
	require.NoError(t, err)

	// Format guidance for sourceState (locked exit, has transition)
	guidanceSource := formatWorkflowStateGuidance(ctx, db, sourceState)
	require.Contains(t, guidanceSource, "## Active Workflow State: Plan")
	require.Contains(t, guidanceSource, "Planning phase")
	require.Contains(t, guidanceSource, "### Instructions\nWrite SPEC.md and PLAN.md")
	require.Contains(t, guidanceSource, "### Allowed Transitions\n- Review: Proceed to review")
	require.Contains(t, guidanceSource, "### Workflow Exit\nNot allowed from this state")

	// Format guidance for targetState (allowed exit, no forward transitions)
	guidanceTarget := formatWorkflowStateGuidance(ctx, db, targetState)
	require.Contains(t, guidanceTarget, "## Active Workflow State: Review")
	require.Contains(t, guidanceTarget, "### Allowed Transitions\nNone.")
	require.Contains(t, guidanceTarget, "### Workflow Exit\nAllowed: invoke `mecha_workflow_transition` with action=\"exit\"")
}

func TestEmbeddedWorkflowTransitionSchema(t *testing.T) {
	schema := embeddedWorkflowTransitionSchema()
	require.NotNil(t, schema)
	require.Equal(t, "object", schema.Type)

	// Valid action: transition
	require.True(t, schema.Check(map[string]any{"action": "transition", "target_state": "Implement"}))
	require.True(t, schema.Check(map[string]any{"action": "transition", "target_state": "Implement", "reason": "Spec reviewed"}))

	// Valid action: exit
	require.True(t, schema.Check(map[string]any{"action": "exit"}))
	require.True(t, schema.Check(map[string]any{"action": "exit", "reason": "All tasks completed"}))

	// Invalid action
	require.False(t, schema.Check(map[string]any{"action": "unknown"}))

	// Unrecognized extra field rejected
	require.False(t, schema.Check(map[string]any{"action": "transition", "target_state": "Implement", "extra": "field"}))
}

func TestEmbeddedWorkflowTransitionAllowedToolsBypass(t *testing.T) {
	// mecha_workflow_transition and workflow_transition must be admitted even when allowed_tools is restricted
	restricted := []string{"read", "edit"}
	require.True(t, store.IsToolAllowedInWorkflowState("mecha_workflow_transition", restricted))
	require.True(t, store.IsToolAllowedInWorkflowState("workflow_transition", restricted))
	require.True(t, store.IsToolAllowedInWorkflowState("read", restricted))
	require.False(t, store.IsToolAllowedInWorkflowState("bash", restricted))
}

func TestEmbeddedWorkflowTransitionExecution(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	// 1. Create three workflow states: Plan, Implement, Done
	// Plan: transitions to Implement, allow_exit = false
	// Implement: transitions to Done, allow_exit = false
	// Done: no transitions, allow_exit = true
	doneState, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Done",
		Description: "Work completed",
		EntryText:   "Clean up worktree and notify supervisor.",
		AllowExit:   ptr(true),
	})
	require.NoError(t, err)

	implementState, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Implement",
		Description: "Writing code",
		EntryText:   "Follow TDD and write unit tests.",
		Goal:        "Implement the planned feature",
		AllowExit:   ptr(false),
		Transitions: []store.WorkflowTransitionConfig{
			{TargetStateID: doneState.ID, Description: "Transition here once tests pass"},
		},
	})
	require.NoError(t, err)

	planState, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Plan",
		Description: "Planning phase",
		EntryText:   "Draft SPEC.md and PLAN.md",
		AllowExit:   ptr(false),
		Transitions: []store.WorkflowTransitionConfig{
			{TargetStateID: implementState.ID, Description: "Proceed to implementation"},
		},
	})
	require.NoError(t, err)

	// Register a managed worker agent using existing test helper
	agentID := "worker-transitions-1"
	binding := subagentTestParent(t, db, agentID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + agentID}

	mgr := &Manager{store: db}
	tool := mgr.embeddedWorkflowTransitionTool(owner, "mecha_workflow_transition")
	aliasTool := mgr.embeddedWorkflowTransitionTool(owner, "workflow_transition")

	// 1. Try transition with NO active workflow state -> Error
	_, err = tool.Execute(ctx, "call-0", map[string]any{
		"action":       "transition",
		"target_state": "Implement",
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no active workflow state")

	// 2. Set active workflow state to Plan
	require.NoError(t, db.SetManagedAgentActiveWorkflowState(ctx, agentID, &planState.ID))

	// 3. Try to exit from Plan -> Blocked because allow_exit == false
	_, err = tool.Execute(ctx, "call-1", map[string]any{
		"action": "exit",
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "workflow exit is not permitted")

	// 4. Try invalid transition target -> Blocked
	_, err = tool.Execute(ctx, "call-2", map[string]any{
		"action":       "transition",
		"target_state": "Done", // Direct skip to Done not permitted from Plan
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not permitted")

	// 5. Successful transition from Plan to Implement (case-insensitive name match: "implement")
	res, err := tool.Execute(ctx, "call-3", map[string]any{
		"action":       "transition",
		"target_state": "implement",
		"reason":       "Plan approved by supervisor",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	outText := res.Content[0].(ai.TextContent).Text
	require.Contains(t, outText, "Successfully transitioned to workflow state: Implement")
	require.Contains(t, outText, "Follow TDD and write unit tests.")
	require.Contains(t, outText, "Done") // Allowed transition listed

	// Verify active state in store is now Implement
	currState, err := db.GetManagedAgentActiveWorkflowState(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, currState)
	require.Equal(t, implementState.ID, currState.ID)

	// Verify goal was synchronized to target state's goal and locked
	goalPolicy, err := db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.True(t, goalPolicy.Config.Enabled)
	require.Equal(t, "Implement the planned feature", goalPolicy.Config.Goal)
	require.True(t, goalPolicy.Config.Settings.GoalLockEnabled)

	// 6. Transition from Implement to Done using aliasTool and TargetStateID
	resAlias, err := aliasTool.Execute(ctx, "call-4", map[string]any{
		"action":       "transition",
		"target_state": doneState.ID,
		"reason":       "Implementation verified",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resAlias.Content[0].(ai.TextContent).Text, "Successfully transitioned to workflow state: Done")

	// Verify active state in store is now Done
	currState, err = db.GetManagedAgentActiveWorkflowState(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, doneState.ID, currState.ID)

	// Done state has empty goal -> agent goal should be deactivated and unlocked
	goalPolicy, err = db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.False(t, goalPolicy.Config.Enabled)
	require.Empty(t, goalPolicy.Config.Goal)
	require.False(t, goalPolicy.Config.Settings.GoalLockEnabled)

	// 7. Done state has allow_exit == true -> Successful exit
	resExit, err := tool.Execute(ctx, "call-5", map[string]any{
		"action": "exit",
		"reason": "Feature shipped to main",
	}, nil)
	require.NoError(t, err)
	require.Contains(t, resExit.Content[0].(ai.TextContent).Text, "Successfully exited workflow state")

	// Verify active state in store is now nil (deactivated)
	currState, err = db.GetManagedAgentActiveWorkflowState(ctx, agentID)
	require.NoError(t, err)
	require.Nil(t, currState)

	// Verify goal remains deactivated and unlocked after exit
	goalPolicy, err = db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.False(t, goalPolicy.Config.Enabled)
	require.Empty(t, goalPolicy.Config.Goal)
	require.False(t, goalPolicy.Config.Settings.GoalLockEnabled)

	// 8. Test transition with variable substitution in goal, reminders, guidance
	wfWithVars, err := db.CreateWorkflow(ctx, store.WorkflowRequest{
		Name: "Variable WF",
		Variables: &[]store.WorkflowVariableDefinition{
			{Name: "COMPONENT", DefaultValue: "core"},
		},
	})
	require.NoError(t, err)

	stateWithVars, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Deploy",
		Description: "Deploying ${COMPONENT}",
		Goal:        "Deploy {{COMPONENT}} to prod",
		AllowExit:   ptr(true),
		WorkflowID:  &wfWithVars.ID,
	})
	require.NoError(t, err)

	// Connect Done -> Deploy
	doneWithTrans, err := db.UpdateWorkflowState(ctx, doneState.ID, store.WorkflowStateRequest{
		Name:        doneState.Name,
		Description: doneState.Description,
		AllowExit:   ptr(true),
		Transitions: []store.WorkflowTransitionConfig{
			{TargetStateID: stateWithVars.ID, Description: "Deploy component"},
		},
	})
	require.NoError(t, err)
	_ = doneWithTrans

	// Activate Done state with variables
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, agentID, &wfWithVars.ID, &doneState.ID, map[string]string{"COMPONENT": "auth-service"}))

	// Transition to Deploy
	resVar, err := tool.Execute(ctx, "call-6", map[string]any{
		"action":       "transition",
		"target_state": "Deploy",
	}, nil)
	require.NoError(t, err)
	resVarText := resVar.Content[0].(ai.TextContent).Text
	require.Contains(t, resVarText, "Deploying auth-service")

	// Verify goal was substituted
	goalPolicy, err = db.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "Deploy auth-service to prod", goalPolicy.Config.Goal)

	// 8. Test subagent invocation rejection
	subagentID := "subagent-transitions-1"
	require.NoError(t, db.CreateManagedSubagent(ctx, binding, store.ManagedSubagentRecord{
		AgentID:       subagentID,
		ParentAgentID: binding.AgentID,
		Description:   "Subagent test",
		Status:        "queued",
	}))
	subBinding := store.EmbeddedBinding{AgentID: subagentID, SessionID: "sub-1", Epoch: 1, Generation: "gen-1"}
	subOwner := embeddedOwner{binding: subBinding, identity: "fixture:" + subagentID}
	subTool := mgr.embeddedWorkflowTransitionTool(subOwner, "mecha_workflow_transition")

	_, err = subTool.Execute(ctx, "call-sub", map[string]any{
		"action": "exit",
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "subagents cannot invoke workflow state transitions")

	// 9. Test SyncEmbeddedWorkflowTools updates active tools dynamically
	lane := mgr.laneFor(agentID)
	lane.setTools([]agent.AgentTool{
		{Name: "read"},
		{Name: "edit"},
		{Name: "bash"},
	}, []agent.AgentTool{
		{Name: "read"},
		{Name: "edit"},
		{Name: "bash"},
	})
	// In Deploy state, AllowedTools is empty, so all tools should remain allowed
	require.NoError(t, mgr.SyncEmbeddedWorkflowTools(ctx, agentID))
	require.Len(t, lane.getActiveTools(), 3)

	// Now update Deploy state to restrict allowed tools to "read" only
	_, err = db.UpdateWorkflowState(ctx, stateWithVars.ID, store.WorkflowStateRequest{
		Name:         stateWithVars.Name,
		Description:  stateWithVars.Description,
		AllowedTools: []string{"read"},
		AllowExit:    ptr(true),
	})
	require.NoError(t, err)

	require.NoError(t, mgr.SyncEmbeddedWorkflowTools(ctx, agentID))
	activeNow := lane.getActiveTools()
	require.Len(t, activeNow, 1)
	require.Equal(t, "read", activeNow[0].Name)

	// Exit workflow and verify all tools are restored
	resExit2, err := tool.Execute(ctx, "call-exit-2", map[string]any{
		"action": "exit",
	}, nil)
	require.NoError(t, err)
	_ = resExit2
	require.Len(t, lane.getActiveTools(), 3)
}

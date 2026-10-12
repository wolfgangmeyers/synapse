package transport_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestManagedAgentWorkflowStateHTTP(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	router, err := transport.New(transport.Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	// Create a managed agent directly via raw DB
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	agentID := "agent-" + uuid.NewString()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, "workflow-agent")
	require.NoError(t, err)

	// Create a workflow state
	ws, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "review-mode",
		Description: "Code review mode",
		EntryText:   "Review the code thoroughly.",
	})
	require.NoError(t, err)

	client := server.Client()

	// 1. GET workflow state initially empty
	res, err := client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp))
	res.Body.Close()
	require.Equal(t, agentID, getResp["agent_id"])
	require.Equal(t, "", getResp["active_workflow_state_id"])
	require.Nil(t, getResp["active_workflow_state"])

	// 2. Activate workflow state
	activateBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": ws.ID,
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var activateResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&activateResp))
	res.Body.Close()
	require.Equal(t, ws.ID, activateResp["active_workflow_state_id"])
	activeMap, ok := activateResp["active_workflow_state"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "review-mode", activeMap["name"])

	// Verify goal is deactivated since review-mode has empty goal
	policy, err := durable.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	require.False(t, policy.Config.Settings.GoalLockEnabled)

	// Verify persistence via GET
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp2 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp2))
	res.Body.Close()
	require.Equal(t, ws.ID, getResp2["active_workflow_state_id"])

	// Activate a state with a goal -> verify agent goal updated and locked
	wsWithGoal, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "goal-workflow-state",
		Description: "State with locked goal",
		Goal:        "Implement all required functional tests",
	})
	require.NoError(t, err)
	activateGoalBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": wsWithGoal.ID,
	})
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateGoalBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())
	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	policy, err = durable.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.True(t, policy.Config.Enabled)
	require.Equal(t, "Implement all required functional tests", policy.Config.Goal)
	require.True(t, policy.Config.Settings.GoalLockEnabled)

	// 3. Deactivate workflow state
	deactivateBody, err := json.Marshal(map[string]any{
		"action": "deactivate",
	})
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(deactivateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var deactivateResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&deactivateResp))
	res.Body.Close()
	require.Equal(t, "", deactivateResp["active_workflow_state_id"])
	require.Nil(t, deactivateResp["active_workflow_state"])

	// Verify goal deactivated and unlocked on state deactivation
	policy, err = durable.ManagedGoalPolicyFor(ctx, agentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	require.Empty(t, policy.Config.Goal)
	require.False(t, policy.Config.Settings.GoalLockEnabled)

	// Verify persistence after deactivation via GET
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp3 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp3))
	res.Body.Close()
	require.Equal(t, "", getResp3["active_workflow_state_id"])

	// 4. Test state with reminders activation and cleanup upon deactivation
	subj := "Review Mode Subject"
	wsWithRem, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "review-mode-with-reminders",
		Description: "Review with automated reminders",
		EntryText:   "Review checklist entry",
		Reminders: []store.WorkflowReminderConfig{
			{
				ID:           "test-rem-oneshot",
				Type:         store.WorkflowReminderOneShot,
				Subject:      &subj,
				Body:         "Please complete the code review checklist.",
				DelaySeconds: 100,
			},
		},
	})
	require.NoError(t, err)

	activateWithRemBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": wsWithRem.ID,
	})
	require.NoError(t, err)
	req, err = http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateWithRemBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify reminder was created
	rems, err := durable.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, rems, 1)
	require.Equal(t, agentID, rems[0].Target)
	require.Equal(t, "Please complete the code review checklist.", rems[0].Body)

	// Deactivate and verify reminder was cleaned up
	deactReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(deactivateBody))
	require.NoError(t, err)
	deactReq.Header.Set("Content-Type", "application/json")
	deactReq.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(deactReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify reminder was cleaned up
	rems, err = durable.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, rems)
}

func TestManagedAgentWorkflowStateTransitionsIntegration(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)

	router, err := transport.New(transport.Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	agentID := "agent-" + uuid.NewString()
	_, err = rawDB.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,?,'/tmp','openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, "transition-agent")
	require.NoError(t, err)

	falseVal := false
	trueVal := true

	// Create Target State: Implement
	implState, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Implement",
		Description: "Write and test features",
		EntryText:   "Follow the plan step by step.",
		AllowExit:   &trueVal,
	})
	require.NoError(t, err)

	// Create Source State: Plan (Locked exit, transition to Implement)
	planState, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "Plan",
		Description: "Decompose requirements into steps",
		EntryText:   "Write SPEC.md and PLAN.md first.",
		AllowExit:   &falseVal,
		Transitions: []store.WorkflowTransitionConfig{
			{
				TargetStateID: implState.ID,
				Description:   "When plan is validated and approved",
			},
		},
	})
	require.NoError(t, err)

	client := server.Client()

	// 1. Verify DELETE on implState fails with 409 Conflict because planState targets it
	delReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/workflow-states/"+implState.ID, nil)
	require.NoError(t, err)
	res, err := client.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, res.StatusCode)
	res.Body.Close()

	// 2. Activate planState on the agent
	activateBody, err := json.Marshal(map[string]any{
		"action":            "activate",
		"workflow_state_id": planState.ID,
	})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/managed-agents/"+agentID+"/workflow-state", bytes.NewReader(activateBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", uuid.NewString())

	res, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// 3. GET workflow state confirms planState is active with allow_exit=false and 1 transition
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp))
	res.Body.Close()
	require.Equal(t, planState.ID, getResp["active_workflow_state_id"])
	activeMap := getResp["active_workflow_state"].(map[string]any)
	require.Equal(t, "Plan", activeMap["name"])
	require.Equal(t, false, activeMap["allow_exit"])
	transitionsList := activeMap["transitions"].([]any)
	require.Len(t, transitionsList, 1)

	// 4. Test Transition tool execution
	tool := manager.EmbeddedWorkflowTransitionToolForTest(agentID, "embedded-owner-1", "mecha_workflow_transition")

	// 4a. Attempt to exit from locked Plan state -> fails
	_, err = tool.Execute(ctx, "call-1", map[string]any{"action": "exit"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "workflow exit is not permitted")

	// 4b. Attempt transition to invalid target -> fails
	_, err = tool.Execute(ctx, "call-2", map[string]any{"action": "transition", "target_state": "Deploy"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "transition to \"Deploy\" is not permitted")

	// 4c. Transition to valid target by name (case-insensitive) -> succeeds
	out, err := tool.Execute(ctx, "call-3", map[string]any{"action": "transition", "target_state": "implement", "reason": "Plan approved"}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, out.Content)
	tc, ok := out.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, tc.Text, "Successfully transitioned to workflow state: Implement")
	require.Contains(t, tc.Text, "Follow the plan step by step.")

	// 5. GET workflow state confirms agent is now in Implement state
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp2 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp2))
	res.Body.Close()
	require.Equal(t, implState.ID, getResp2["active_workflow_state_id"])
	activeMap2 := getResp2["active_workflow_state"].(map[string]any)
	require.Equal(t, "Implement", activeMap2["name"])
	require.Equal(t, true, activeMap2["allow_exit"])

	// 6. Test exit from Implement state -> succeeds
	outExit, err := tool.Execute(ctx, "call-4", map[string]any{"action": "exit", "reason": "Implementation complete"}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, outExit.Content)
	tcExit, ok := outExit.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, tcExit.Text, "Successfully exited workflow state \"Implement\"")

	// 7. GET workflow state confirms agent has no active workflow state
	res, err = client.Get(server.URL + "/v1/managed-agents/" + agentID + "/workflow-state")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var getResp3 map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&getResp3))
	res.Body.Close()
	require.Equal(t, "", getResp3["active_workflow_state_id"])
	require.Nil(t, getResp3["active_workflow_state"])
}

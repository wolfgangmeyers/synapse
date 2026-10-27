package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func setupWorkflowAdminToolsFixture(t *testing.T) (*Manager, *store.Store, embeddedOwner, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "workflow_admin_tools_test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"}))
	mgr, err := NewManager(ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config: Config{
			SessionRoot:                  filepath.Join(root, "sessions"),
			AllowedWorkingDirectoryRoots: []string{root, t.TempDir(), os.TempDir()},
			Limits:                       DefaultLimits(),
		},
		CatalogSource: CatalogFromModels(
			Model{Provider: "google-vertex", ID: "gemini-3.8-flash", Available: true},
		),
	})
	require.NoError(t, err)

	callerID := "wf-admin-caller-worker"
	binding := subagentTestParent(t, db, callerID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	return mgr, db, owner, root
}

func TestWorkflowAdminTools_RegistrationAndFiltering(t *testing.T) {
	mgr, db, owner, _ := setupWorkflowAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	wfTools := mgr.embeddedWorkflowManagementTools(owner)
	require.Len(t, wfTools, 9)

	toolNames := make(map[string]bool)
	for _, tool := range wfTools {
		toolNames[tool.Name] = true
	}
	for _, expectedName := range WorkflowAdminToolNames {
		require.True(t, toolNames[expectedName], "expected tool %s to be present in embeddedWorkflowManagementTools", expectedName)
	}

	// Verify managed_tool_defaults: all 5 workflow admin tools must be DefaultEnabled == false
	defaults, err := db.GetToolDefaults(ctx)
	require.NoError(t, err)
	defaultMap := make(map[string]bool)
	for _, d := range defaults {
		defaultMap[d.ToolName] = d.Enabled
	}
	for _, expectedName := range WorkflowAdminToolNames {
		enabled, exists := defaultMap[expectedName]
		require.True(t, exists, "tool %s must exist in tool defaults", expectedName)
		require.False(t, enabled, "tool %s must be DefaultEnabled == false", expectedName)
	}

	// Verify filtering: by default, filterToolsForAgent strips all 5 workflow admin tools
	baseTools := append([]agent.AgentTool(nil), wfTools...)
	filtered, err := mgr.filterToolsForAgent(ctx, owner.binding.AgentID, baseTools)
	require.NoError(t, err)
	require.Empty(t, filtered, "all workflow admin tools should be filtered out by default")

	// Explicitly enable workflow_list
	err = db.SetAgentTool(ctx, owner.binding.AgentID, "workflow_list", true)
	require.NoError(t, err)

	filteredAfterOverride, err := mgr.filterToolsForAgent(ctx, owner.binding.AgentID, baseTools)
	require.NoError(t, err)
	require.Len(t, filteredAfterOverride, 1)
	require.Equal(t, "workflow_list", filteredAfterOverride[0].Name)
}

func TestWorkflowAdminTools_Lifecycle(t *testing.T) {
	mgr, db, owner, _ := setupWorkflowAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()

	listTool := mgr.embeddedWorkflowListTool(owner)
	getTool := mgr.embeddedWorkflowGetTool(owner)
	saveTool := mgr.embeddedWorkflowSaveTool(owner)
	deleteTool := mgr.embeddedWorkflowDeleteTool(owner)
	assignTool := mgr.embeddedWorkflowAssignTool(owner)

	// 1. Initial workflow_list: empty
	resList, err := listTool.Execute(ctx, "c1", map[string]any{}, nil)
	require.NoError(t, err)
	var listResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resList.Content[0].(ai.TextContent).Text), &listResp))
	require.Equal(t, "ok", listResp["status"])
	wfs, ok := listResp["workflows"].([]any)
	require.True(t, ok)
	require.Empty(t, wfs)

	// 2. workflow_save via fields
	resSaveFields, err := saveTool.Execute(ctx, "c2", map[string]any{
		"name":        "Release Loop",
		"description": "Orchestrates release validation",
	}, nil)
	require.NoError(t, err)
	var saveFieldsResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resSaveFields.Content[0].(ai.TextContent).Text), &saveFieldsResp))
	require.Equal(t, "ok", saveFieldsResp["status"])
	wfID := saveFieldsResp["workflow_id"].(string)
	require.NotEmpty(t, wfID)

	// 3. workflow_get by ID and by Name
	resGetID, err := getTool.Execute(ctx, "c3", map[string]any{"workflow_id": wfID}, nil)
	require.NoError(t, err)
	var getIDResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resGetID.Content[0].(ai.TextContent).Text), &getIDResp))
	require.Equal(t, "ok", getIDResp["status"])
	wfObj := getIDResp["workflow"].(map[string]any)
	require.Equal(t, "Release Loop", wfObj["name"])

	resGetName, err := getTool.Execute(ctx, "c4", map[string]any{"workflow_id": "Release Loop"}, nil)
	require.NoError(t, err)
	var getNameResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resGetName.Content[0].(ai.TextContent).Text), &getNameResp))
	require.Equal(t, "ok", getNameResp["status"])

	// 4. workflow_save with bundle
	bundle := store.WorkflowExportBundle{
		SchemaVersion: 1,
		ExportedAt:    "2026-09-01T12:00:00Z",
		Workflow: store.WorkflowInfoExport{
			Name:        "Test Bundle Workflow",
			Description: "Imported from bundle",
			Variables: []store.WorkflowVariableDefinition{
				{Name: "ENV", Description: "Target env", Required: true, DefaultValue: "staging"},
			},
		},
		EntryStateKey: "state_init",
		States: []store.WorkflowStateExport{
			{
				Key:          "state_init",
				Name:         "Init State",
				Description:  "Initialize work",
				Goal:         "Initialize env ${ENV}",
				IsEntryState: true,
				AllowExit:    false,
				AllowedTools: []string{"read", "bash"},
				Transitions: []store.WorkflowTransitionExport{
					{TargetStateKey: "state_finish", Description: "Proceed to finish"},
				},
			},
			{
				Key:          "state_finish",
				Name:         "Finish State",
				Description:  "Complete work",
				Goal:         "Done",
				IsEntryState: false,
				AllowExit:    true,
				AllowedTools: []string{"read"},
			},
		},
	}
	bundleBytes, err := json.Marshal(bundle)
	require.NoError(t, err)

	resSaveBundle, err := saveTool.Execute(ctx, "c5", map[string]any{
		"bundle": string(bundleBytes),
	}, nil)
	require.NoError(t, err)
	var saveBundleResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resSaveBundle.Content[0].(ai.TextContent).Text), &saveBundleResp))
	require.Equal(t, "ok", saveBundleResp["status"])
	importedWfID := saveBundleResp["workflow_id"].(string)
	require.NotEmpty(t, importedWfID)

	// List again: should have 2 workflows
	resList2, err := listTool.Execute(ctx, "c6", map[string]any{}, nil)
	require.NoError(t, err)
	var list2Resp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resList2.Content[0].(ai.TextContent).Text), &list2Resp))
	wfs2 := list2Resp["workflows"].([]any)
	require.Len(t, wfs2, 2)

	// 5. workflow_assign: activate on caller agent
	resAssignAct, err := assignTool.Execute(ctx, "c7", map[string]any{
		"agent_id":    owner.binding.AgentID,
		"action":      "activate",
		"workflow_id": importedWfID,
		"variables": map[string]any{
			"ENV": "production",
		},
	}, nil)
	require.NoError(t, err)
	var assignActResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resAssignAct.Content[0].(ai.TextContent).Text), &assignActResp))
	require.Equal(t, "ok", assignActResp["status"])
	require.Equal(t, owner.binding.AgentID, assignActResp["agent_id"])
	require.Equal(t, "Init State", assignActResp["active_workflow_state"])

	// Verify agent has active workflow state in DB
	rec, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, importedWfID, rec.ActiveWorkflowID)
	require.NotEmpty(t, rec.ActiveWorkflowStateID)

	// Verify goal synchronized with variable substitution (${ENV} -> production)
	pol, err := db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.True(t, pol.Config.Enabled)
	require.Equal(t, "Initialize env production", pol.Config.Goal)

	// 6. workflow_assign: deactivate
	resAssignDeact, err := assignTool.Execute(ctx, "c8", map[string]any{
		"agent_id": owner.binding.AgentID,
		"action":   "deactivate",
	}, nil)
	require.NoError(t, err)
	var assignDeactResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resAssignDeact.Content[0].(ai.TextContent).Text), &assignDeactResp))
	require.Equal(t, "ok", assignDeactResp["status"])

	recDeact, err := db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, recDeact.ActiveWorkflowID)
	require.Empty(t, recDeact.ActiveWorkflowStateID)

	polDeact, err := db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.False(t, polDeact.Config.Enabled)

	// 7. workflow_delete
	resDel, err := deleteTool.Execute(ctx, "c9", map[string]any{"workflow_id": wfID}, nil)
	require.NoError(t, err)
	var delResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resDel.Content[0].(ai.TextContent).Text), &delResp))
	require.Equal(t, "ok", delResp["status"])
	require.True(t, delResp["deleted"].(bool))

	// Verify deleted workflow is gone
	delWf, err := db.GetWorkflow(ctx, wfID)
	require.NoError(t, err)
	require.Nil(t, delWf)

	// 8. Workflow State Tools: state_list, state_save, state_get, state_delete
	stateListTool := mgr.embeddedWorkflowStateListTool(owner)
	stateGetTool := mgr.embeddedWorkflowStateGetTool(owner)
	stateSaveTool := mgr.embeddedWorkflowStateSaveTool(owner)
	stateDeleteTool := mgr.embeddedWorkflowStateDeleteTool(owner)

	// List states in importedWfID (should have state_init and state_finish)
	resStateList, err := stateListTool.Execute(ctx, "c10", map[string]any{"workflow_id": importedWfID}, nil)
	require.NoError(t, err)
	var stateListResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateList.Content[0].(ai.TextContent).Text), &stateListResp))
	require.Equal(t, "ok", stateListResp["status"])
	require.Equal(t, importedWfID, stateListResp["workflow_id"])
	statesList, ok := stateListResp["states"].([]any)
	require.True(t, ok)
	require.Len(t, statesList, 2)

	// Create a new state via workflow_state_save
	resStateSave, err := stateSaveTool.Execute(ctx, "c11", map[string]any{
		"workflow_id": importedWfID,
		"name":        "Intermediate State",
		"description": "Performs intermediate actions",
		"goal":        "Process items",
		"entry_text":  "Please process the items now.",
		"allow_exit":  false,
		"allowed_tools": []any{"read", "edit"},
	}, nil)
	require.NoError(t, err)
	var stateSaveResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateSave.Content[0].(ai.TextContent).Text), &stateSaveResp))
	require.Equal(t, "ok", stateSaveResp["status"])
	newStateObj := stateSaveResp["state"].(map[string]any)
	newStateID := newStateObj["id"].(string)
	require.NotEmpty(t, newStateID)
	require.Equal(t, "Intermediate State", newStateObj["name"])

	// Get by state_id
	resStateGetID, err := stateGetTool.Execute(ctx, "c12", map[string]any{"state_id": newStateID}, nil)
	require.NoError(t, err)
	var stateGetIDResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateGetID.Content[0].(ai.TextContent).Text), &stateGetIDResp))
	require.Equal(t, "ok", stateGetIDResp["status"])
	require.Equal(t, newStateID, stateGetIDResp["state"].(map[string]any)["id"])

	// Get by workflow_id and state_name
	resStateGetName, err := stateGetTool.Execute(ctx, "c13", map[string]any{
		"workflow_id": importedWfID,
		"state_name":  "Intermediate State",
	}, nil)
	require.NoError(t, err)
	var stateGetNameResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateGetName.Content[0].(ai.TextContent).Text), &stateGetNameResp))
	require.Equal(t, "ok", stateGetNameResp["status"])
	require.Equal(t, newStateID, stateGetNameResp["state"].(map[string]any)["id"])

	// Update the state via workflow_state_save
	resStateUpdate, err := stateSaveTool.Execute(ctx, "c14", map[string]any{
		"workflow_id": importedWfID,
		"state_id":    newStateID,
		"name":        "Intermediate State Updated",
		"goal":        "Process items thoroughly",
	}, nil)
	require.NoError(t, err)
	var stateUpdateResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateUpdate.Content[0].(ai.TextContent).Text), &stateUpdateResp))
	require.Equal(t, "ok", stateUpdateResp["status"])
	require.Equal(t, "Intermediate State Updated", stateUpdateResp["state"].(map[string]any)["name"])
	require.Equal(t, "Process items thoroughly", stateUpdateResp["state"].(map[string]any)["goal"])

	// Delete state via workflow_state_delete
	resStateDelete, err := stateDeleteTool.Execute(ctx, "c15", map[string]any{"state_id": newStateID}, nil)
	require.NoError(t, err)
	var stateDeleteResp map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStateDelete.Content[0].(ai.TextContent).Text), &stateDeleteResp))
	require.Equal(t, "ok", stateDeleteResp["status"])
	require.True(t, stateDeleteResp["deleted"].(bool))
	require.Equal(t, newStateID, stateDeleteResp["state_id"])

	// Verify state is gone
	delState, err := db.GetWorkflowState(ctx, newStateID)
	require.NoError(t, err)
	require.Nil(t, delState)
}

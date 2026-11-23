package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func setupAdminToolsFixture(t *testing.T) (*Manager, *store.Store, embeddedOwner, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "admin_tools_test.db")
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
			Model{Provider: "google-vertex", ID: "gemini-3.8-flash", Available: true},
		),
	})
	require.NoError(t, err)

	callerID := "admin-caller-worker"
	binding := subagentTestParent(t, db, callerID)
	owner := embeddedOwner{binding: binding, identity: "fixture:" + callerID}

	return mgr, db, owner, root
}

func TestAdminTools_RegistrationAndFiltering(t *testing.T) {
	mgr, db, owner, _ := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	adminTools := mgr.embeddedSessionTools(owner)
	require.Len(t, adminTools, 6)

	toolNames := make(map[string]bool)
	for _, tool := range adminTools {
		toolNames[tool.Name] = true
	}
	for _, expectedName := range AdminToolNames {
		require.True(t, toolNames[expectedName], "expected tool %s to be present in embeddedSessionTools", expectedName)
	}

	// 1. By default, with enabled = 0 in managed_tool_defaults, filterToolsForAgent strips all 6 admin tools
	baseTools := append([]agent.AgentTool(nil), adminTools...)
	filtered, err := mgr.filterToolsForAgent(ctx, owner.binding.AgentID, baseTools)
	require.NoError(t, err)
	require.Empty(t, filtered, "all admin tools should be filtered out by default")

	// 2. Explicitly enable worker_spawn for the caller agent
	err = db.SetAgentTool(ctx, owner.binding.AgentID, "worker_spawn", true)
	require.NoError(t, err)

	filteredAfterOverride, err := mgr.filterToolsForAgent(ctx, owner.binding.AgentID, baseTools)
	require.NoError(t, err)
	require.Len(t, filteredAfterOverride, 1)
	require.Equal(t, "worker_spawn", filteredAfterOverride[0].Name)

	// 3. Enable remaining tools one by one
	err = db.SetAgentTool(ctx, owner.binding.AgentID, "worker_terminate", true)
	require.NoError(t, err)
	filteredTwo, err := mgr.filterToolsForAgent(ctx, owner.binding.AgentID, baseTools)
	require.NoError(t, err)
	require.Len(t, filteredTwo, 2)
}

func TestAdminTools_WorkerSpawn(t *testing.T) {
	mgr, db, owner, root := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	tool := mgr.embeddedWorkerSpawnTool(owner)
	require.Equal(t, "worker_spawn", tool.Name)

	// 1. Validation: Missing required fields
	_, err := tool.Execute(ctx, "call-1", map[string]any{}, nil)
	require.Error(t, err)

	_, err = tool.Execute(ctx, "call-2", map[string]any{
		"name": "child-1",
		"cwd":  root,
	}, nil)
	require.Error(t, err, "missing instruction should fail")

	// 2. Spawn worker without admin flag
	res, err := tool.Execute(ctx, "call-spawn-1", map[string]any{
		"name":        "child-regular",
		"cwd":         root,
		"instruction": "Do routine work",
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(ai.TextContent).Text), &payload))
	require.Equal(t, "accepted", payload["status"])
	require.Equal(t, "child-regular", payload["worker_id"])

	// Verify child-regular does not have admin tools enabled
	childRegularTools, err := db.GetAgentTools(ctx, "child-regular")
	require.NoError(t, err)
	for _, ct := range childRegularTools {
		if ct.Category == AdminToolsCategory {
			require.False(t, ct.Enabled, "admin tool %s should be disabled on non-admin worker", ct.ToolName)
		}
	}

	// 3. Spawn worker with admin: true
	resAdmin, err := tool.Execute(ctx, "call-spawn-2", map[string]any{
		"name":        "child-admin",
		"cwd":         root,
		"instruction": "Supervise cluster",
		"model": map[string]any{
			"provider": "google-vertex",
			"id":       "gemini-3.8-flash",
		},
		"admin": true,
	}, nil)
	require.NoError(t, err)
	require.Len(t, resAdmin.Content, 1)

	var adminPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resAdmin.Content[0].(ai.TextContent).Text), &adminPayload))
	require.Equal(t, "accepted", adminPayload["status"])
	require.Equal(t, "child-admin", adminPayload["worker_id"])

	// Verify child-admin has all 5 admin tools enabled in managed_agent_tools
	childAdminTools, err := db.GetAgentTools(ctx, "child-admin")
	require.NoError(t, err)
	adminToolsMap := make(map[string]bool)
	for _, ct := range childAdminTools {
		if ct.Category == AdminToolsCategory {
			adminToolsMap[ct.ToolName] = ct.Enabled
		}
	}
	for _, expectedName := range AdminToolNames {
		require.True(t, adminToolsMap[expectedName], "admin tool %s must be enabled for admin worker", expectedName)
	}
}

func TestAdminTools_WorkerTerminate(t *testing.T) {
	mgr, db, owner, root := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	spawnTool := mgr.embeddedWorkerSpawnTool(owner)
	termTool := mgr.embeddedWorkerTerminateAdminTool(owner)
	require.Equal(t, "worker_terminate", termTool.Name)

	// Spawn target worker
	_, err := spawnTool.Execute(ctx, "spawn-target", map[string]any{
		"name":        "worker-to-kill",
		"cwd":         root,
		"instruction": "Work until stopped",
	}, nil)
	require.NoError(t, err)

	// 1. Self-termination guard: Calling lane target rejected
	_, err = termTool.Execute(ctx, "term-self", map[string]any{
		"worker_id": owner.binding.AgentID,
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot target calling lane")

	// 2. Missing worker_id rejected
	_, err = termTool.Execute(ctx, "term-empty", map[string]any{}, nil)
	require.Error(t, err)

	// 3. Terminate target worker
	res, err := termTool.Execute(ctx, "term-valid", map[string]any{
		"worker_id":        "worker-to-kill",
		"reason":           "task complete",
		"cleanup_worktree": true,
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(ai.TextContent).Text), &payload))
	require.Equal(t, "accepted", payload["status"])
	require.Equal(t, "worker-to-kill", payload["worker_id"])

	// Verify worker is marked terminated
	rec, err := db.ManagedAgent(ctx, "worker-to-kill")
	require.NoError(t, err)
	require.Equal(t, "terminated", rec.DesiredState)
}

func TestAdminTools_WorkerSetModel(t *testing.T) {
	mgr, db, owner, _ := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	setModelTool := mgr.embeddedWorkerSetModelTool(owner)
	require.Equal(t, "worker_set_model", setModelTool.Name)

	targetID := "worker-model-target"
	targetBinding := subagentTestParent(t, db, targetID)
	mgr.mu.Lock()
	if mgr.embeddedOwners == nil {
		mgr.embeddedOwners = make(map[string]embeddedOwner)
	}
	mgr.embeddedOwners[targetID] = embeddedOwner{binding: targetBinding, identity: "fixture:" + targetID}
	mgr.mu.Unlock()

	// 1. Target worker does not exist
	_, err := setModelTool.Execute(ctx, "call-no-worker", map[string]any{
		"worker_id": "non-existent-worker",
		"model": map[string]any{
			"provider": "openai-codex",
			"id":       "gpt-5.6-terra",
		},
	}, nil)
	require.Error(t, err)

	// 2. Invalid model format
	_, err = setModelTool.Execute(ctx, "call-bad-model", map[string]any{
		"worker_id": targetID,
		"model": map[string]any{
			"provider": "",
			"id":       "",
		},
	}, nil)
	require.Error(t, err)

	// 3. Valid set_model
	res, err := setModelTool.Execute(ctx, "call-set-model", map[string]any{
		"worker_id": targetID,
		"model": map[string]any{
			"provider": "google-vertex",
			"id":       "gemini-3.8-flash",
		},
	}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(ai.TextContent).Text), &payload))
	require.Equal(t, "accepted", payload["status"])
	require.Equal(t, targetID, payload["worker_id"])
	modelData, ok := payload["model"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "google-vertex", modelData["provider"])
	require.Equal(t, "gemini-3.8-flash", modelData["id"])
}

func TestAdminTools_WorkerSetGoal(t *testing.T) {
	mgr, db, owner, root := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	spawnTool := mgr.embeddedWorkerSpawnTool(owner)
	setGoalTool := mgr.embeddedWorkerSetGoalTool(owner)
	require.Equal(t, "worker_set_goal", setGoalTool.Name)

	targetID := "worker-goal-target"
	_, err := spawnTool.Execute(ctx, "spawn-goal-target", map[string]any{
		"name":        targetID,
		"cwd":         root,
		"instruction": "Work on goal tests",
	}, nil)
	require.NoError(t, err)

	// 1. Invalid action
	_, err = setGoalTool.Execute(ctx, "call-invalid-act", map[string]any{
		"worker_id": targetID,
		"action":    "invalid",
	}, nil)
	require.Error(t, err)

	// 2. Action "set" missing goal
	_, err = setGoalTool.Execute(ctx, "call-empty-goal", map[string]any{
		"worker_id": targetID,
		"action":    "set",
	}, nil)
	require.Error(t, err)

	// 3. Action "set" with goal
	resSet, err := setGoalTool.Execute(ctx, "call-set-goal", map[string]any{
		"worker_id": targetID,
		"action":    "set",
		"goal":      "Ship the admin tools",
	}, nil)
	require.NoError(t, err)
	var setPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resSet.Content[0].(ai.TextContent).Text), &setPayload))
	require.Equal(t, "accepted", setPayload["status"])
	require.Equal(t, targetID, setPayload["worker_id"])
	require.Equal(t, "set", setPayload["action"])

	// Verify policy in DB
	pol, err := db.ManagedGoalPolicyFor(ctx, targetID)
	require.NoError(t, err)
	require.True(t, pol.Config.Enabled)
	require.Equal(t, "Ship the admin tools", pol.Config.Goal)

	// 4. Action "status"
	resStatus, err := setGoalTool.Execute(ctx, "call-status-goal", map[string]any{
		"worker_id": targetID,
		"action":    "status",
	}, nil)
	require.NoError(t, err)
	var statusPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resStatus.Content[0].(ai.TextContent).Text), &statusPayload))
	require.Equal(t, "accepted", statusPayload["status"])
	require.Equal(t, targetID, statusPayload["worker_id"])
	require.Equal(t, "status", statusPayload["action"])
	goalState, ok := statusPayload["goal_state"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, goalState["enabled"])
	require.Equal(t, "Ship the admin tools", goalState["goal"])

	// 5. Action "off"
	resOff, err := setGoalTool.Execute(ctx, "call-off-goal", map[string]any{
		"worker_id": targetID,
		"action":    "off",
	}, nil)
	require.NoError(t, err)
	var offPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resOff.Content[0].(ai.TextContent).Text), &offPayload))
	require.Equal(t, "accepted", offPayload["status"])
	require.Equal(t, targetID, offPayload["worker_id"])
	require.Equal(t, "off", offPayload["action"])

	// Verify goal disabled in DB
	polOff, err := db.ManagedGoalPolicyFor(ctx, targetID)
	require.NoError(t, err)
	require.False(t, polOff.Config.Enabled)
}

func TestAdminTools_WorkerStatus(t *testing.T) {
	mgr, db, owner, root := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	spawnTool := mgr.embeddedWorkerSpawnTool(owner)
	statusTool := mgr.embeddedWorkerStatusTool(owner)
	require.Equal(t, "worker_status", statusTool.Name)

	targetID := "worker-status-target"
	_, err := spawnTool.Execute(ctx, "spawn-status-target", map[string]any{
		"name":        targetID,
		"cwd":         root,
		"instruction": "Work on status tests",
	}, nil)
	require.NoError(t, err)

	// Set a goal on target worker
	_, err = db.SetManagedGoalDesiredState(ctx, targetID, true, "Keep inspecting status")
	require.NoError(t, err)

	// 1. Status for specific worker_id
	resOne, err := statusTool.Execute(ctx, "call-stat-one", map[string]any{
		"worker_id": targetID,
	}, nil)
	require.NoError(t, err)
	var onePayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resOne.Content[0].(ai.TextContent).Text), &onePayload))
	require.Equal(t, "ok", onePayload["status"])
	require.Equal(t, targetID, onePayload["worker_id"])
	workerMap, ok := onePayload["worker"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, targetID, workerMap["agent_id"])
	require.Equal(t, "running", workerMap["desired_state"])
	goalMap, ok := workerMap["goal"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, goalMap["enabled"])
	require.Equal(t, "Keep inspecting status", goalMap["goal"])

	// 2. Status for all workers (omitted worker_id)
	resAll, err := statusTool.Execute(ctx, "call-stat-all", map[string]any{}, nil)
	require.NoError(t, err)
	var allPayload map[string]any
	require.NoError(t, json.Unmarshal([]byte(resAll.Content[0].(ai.TextContent).Text), &allPayload))
	require.Equal(t, "ok", allPayload["status"])
	workersList, ok := allPayload["workers"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(workersList), 2) // caller and targetID

	// 3. Status for non-existent worker returns error
	_, err = statusTool.Execute(ctx, "call-stat-ghost", map[string]any{
		"worker_id": "ghost-worker",
	}, nil)
	require.Error(t, err)
}

func TestAdminTools_WorkerList(t *testing.T) {
	mgr, db, owner, root := setupAdminToolsFixture(t)
	defer db.Close()
	defer mgr.Close(context.Background())

	ctx := context.Background()
	listTool := mgr.embeddedWorkerListTool(owner)
	require.Equal(t, "worker_list", listTool.Name)
	require.NotNil(t, listTool.Parameters)

	// 1. Worker A: Running worker with goal set and started_at in the past
	now := mgr.now()
	launchA := store.ManagedAgentRecord{
		AgentID:          "worker-list-a",
		Name:             "worker-list-a",
		Cwd:              root,
		SelectedModel:    store.ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "active",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash-a",
		CreatedAt:        now.Add(-200 * time.Second).UTC().Format(time.RFC3339),
		StartedAt:        now.Add(-120 * time.Second).UTC().Format(time.RFC3339),
		UpdatedAt:        now.UTC().Format(time.RFC3339),
	}
	opA := store.ManagedOperation{
		OperationID:    "op-list-a",
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-list-a",
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchA, opA))
	_, err := db.SetManagedGoalDesiredState(ctx, "worker-list-a", true, "Complete milestone A")
	require.NoError(t, err)

	// 2. Worker B: Running worker with worktree metadata
	launchB := store.ManagedAgentRecord{
		AgentID:          "worker-list-b",
		Name:             "worker-list-b",
		Cwd:              root,
		SelectedModel:    store.ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		Worktree:         true,
		WorktreeBasePath: "/tmp/repo",
		WorktreeBranch:   "feature/worker-b",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash-b",
		CreatedAt:        now.Add(-100 * time.Second).UTC().Format(time.RFC3339),
		StartedAt:        now.Add(-60 * time.Second).UTC().Format(time.RFC3339),
		UpdatedAt:        now.UTC().Format(time.RFC3339),
	}
	opB := store.ManagedOperation{
		OperationID:    "op-list-b",
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-list-b",
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchB, opB))

	// 3. Worker C: Terminated / stopped worker
	launchC := store.ManagedAgentRecord{
		AgentID:          "worker-list-c",
		Name:             "worker-list-c",
		Cwd:              root,
		SelectedModel:    store.ManagedModel{Provider: "google-vertex", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "terminated",
		LifecycleState:   "failed",
		ActivityState:    "stopped",
		ManifestRevision: 1,
		ManifestHash:     "manifest-hash-c",
		CreatedAt:        now.Add(-50 * time.Second).UTC().Format(time.RFC3339),
		StartedAt:        now.Add(-40 * time.Second).UTC().Format(time.RFC3339),
		UpdatedAt:        now.UTC().Format(time.RFC3339),
	}
	opC := store.ManagedOperation{
		OperationID:    "op-list-c",
		Method:         "POST",
		CanonicalPath:  "/v1/managed-agents",
		RequestID:      "req-list-c",
		ResponseStatus: 202,
		ResponseBody:   []byte(`{}`),
	}
	require.NoError(t, db.ReserveManagedLaunch(ctx, launchC, opC))

	// Helper to parse payload
	parsePayload := func(res agent.AgentToolResult) map[string]any {
		require.Len(t, res.Content, 1)
		var p map[string]any
		require.NoError(t, json.Unmarshal([]byte(res.Content[0].(ai.TextContent).Text), &p))
		return p
	}

	// 4. Default / omitted status defaults to "all"
	resOmitted, err := listTool.Execute(ctx, "call-omitted", map[string]any{}, nil)
	require.NoError(t, err)
	pOmitted := parsePayload(resOmitted)
	require.Equal(t, "ok", pOmitted["status"])
	require.Equal(t, "all", pOmitted["filter"])
	workersOmitted := pOmitted["workers"].([]any)
	require.GreaterOrEqual(t, len(workersOmitted), 4)

	// 5. Explicit status: "all"
	resAll, err := listTool.Execute(ctx, "call-all", map[string]any{"status": "all"}, nil)
	require.NoError(t, err)
	pAll := parsePayload(resAll)
	require.Equal(t, "ok", pAll["status"])
	require.Equal(t, "all", pAll["filter"])
	workersAll := pAll["workers"].([]any)
	require.Equal(t, len(workersOmitted), len(workersAll))

	// 6. Status: "running"
	resRunning, err := listTool.Execute(ctx, "call-running", map[string]any{"status": "running"}, nil)
	require.NoError(t, err)
	pRunning := parsePayload(resRunning)
	require.Equal(t, "ok", pRunning["status"])
	require.Equal(t, "running", pRunning["filter"])
	workersRunning := pRunning["workers"].([]any)
	runningIDs := make(map[string]bool)
	for _, w := range workersRunning {
		wMap := w.(map[string]any)
		require.Equal(t, "running", wMap["lifecycle_state"])
		runningIDs[wMap["agent_id"].(string)] = true
	}
	require.True(t, runningIDs["worker-list-a"], "worker-list-a must be in running filter")
	require.True(t, runningIDs["worker-list-b"], "worker-list-b must be in running filter")
	require.False(t, runningIDs["worker-list-c"], "worker-list-c must not be in running filter")

	// 7. Status: "active"
	resActive, err := listTool.Execute(ctx, "call-active", map[string]any{"status": "active"}, nil)
	require.NoError(t, err)
	pActive := parsePayload(resActive)
	require.Equal(t, "ok", pActive["status"])
	require.Equal(t, "active", pActive["filter"])
	workersActive := pActive["workers"].([]any)
	activeIDs := make(map[string]bool)
	for _, w := range workersActive {
		wMap := w.(map[string]any)
		activeIDs[wMap["agent_id"].(string)] = true
	}
	require.True(t, activeIDs["worker-list-a"], "worker-list-a must be in active filter")
	require.True(t, activeIDs["worker-list-b"], "worker-list-b must be in active filter")
	require.False(t, activeIDs["worker-list-c"], "worker-list-c must not be in active filter")

	// 8. Status: "stopped"
	resStopped, err := listTool.Execute(ctx, "call-stopped", map[string]any{"status": "stopped"}, nil)
	require.NoError(t, err)
	pStopped := parsePayload(resStopped)
	require.Equal(t, "ok", pStopped["status"])
	require.Equal(t, "stopped", pStopped["filter"])
	workersStopped := pStopped["workers"].([]any)
	stoppedIDs := make(map[string]bool)
	for _, w := range workersStopped {
		wMap := w.(map[string]any)
		stoppedIDs[wMap["agent_id"].(string)] = true
	}
	require.True(t, stoppedIDs["worker-list-c"], "worker-list-c must be in stopped filter")
	require.False(t, stoppedIDs["worker-list-a"], "worker-list-a must not be in stopped filter")
	require.False(t, stoppedIDs["worker-list-b"], "worker-list-b must not be in stopped filter")

	// 9. Verify record hydration details
	workersMap := make(map[string]map[string]any)
	for _, w := range workersAll {
		wMap := w.(map[string]any)
		require.NotEmpty(t, wMap["runtime"])
		require.NotEmpty(t, wMap["updated_at"])
		workersMap[wMap["agent_id"].(string)] = wMap
	}

	// Worker A hydration (goal, uptime)
	wA := workersMap["worker-list-a"]
	require.NotNil(t, wA)
	require.Equal(t, "worker-list-a", wA["name"])
	require.Equal(t, "embedded", wA["runtime"])
	require.NotEmpty(t, wA["updated_at"])
	require.Equal(t, "running", wA["lifecycle_state"])
	require.Equal(t, "active", wA["activity_state"])
	require.Equal(t, "running", wA["desired_state"])
	require.Equal(t, root, wA["cwd"])
	modelA, ok := wA["model"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "google-vertex", modelA["provider"])
	require.Equal(t, "gemini-3.8-flash", modelA["id"])
	goalA, ok := wA["goal"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, goalA["enabled"])
	require.Equal(t, "Complete milestone A", goalA["goal"])
	require.False(t, wA["worktree"].(bool))
	require.Empty(t, wA["worktree_base_path"])
	require.Empty(t, wA["worktree_branch"])
	uptimeA := int64(wA["uptime_seconds"].(float64))
	require.GreaterOrEqual(t, uptimeA, int64(120), "worker A uptime must be >= 120s")

	// Worker B hydration (worktree metadata)
	wB := workersMap["worker-list-b"]
	require.NotNil(t, wB)
	require.Equal(t, "embedded", wB["runtime"])
	require.NotEmpty(t, wB["updated_at"])
	require.True(t, wB["worktree"].(bool))
	require.Equal(t, "/tmp/repo", wB["worktree_base_path"])
	require.Equal(t, "feature/worker-b", wB["worktree_branch"])
	require.Nil(t, wB["goal"])
	uptimeB := int64(wB["uptime_seconds"].(float64))
	require.GreaterOrEqual(t, uptimeB, int64(60), "worker B uptime must be >= 60s")

	// Worker C hydration (stopped worker uptime == 0)
	wC := workersMap["worker-list-c"]
	require.NotNil(t, wC)
	require.Equal(t, "embedded", wC["runtime"])
	require.NotEmpty(t, wC["updated_at"])
	uptimeC := int64(wC["uptime_seconds"].(float64))
	require.Equal(t, int64(0), uptimeC, "stopped worker uptime must be 0")

	// 10. Invalid status error handling
	_, err = listTool.Execute(ctx, "call-invalid-status", map[string]any{
		"status": "bogus",
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid status filter")

	_, err = listTool.Execute(ctx, "call-non-string-status", map[string]any{
		"status": 12345,
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "status must be a string")
}

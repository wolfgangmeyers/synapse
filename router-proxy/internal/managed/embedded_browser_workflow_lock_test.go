package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedBrowserWorkflowGoalLock(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	binding := subagentTestParent(t, db, "browser-workflow")
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Locked workflow"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Work", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	settings := store.DefaultManagedGoalSettings()
	settings.GoalLockEnabled = true
	_, err = db.UpdateManagedGoalConfig(ctx, binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "Complete active workflow: Locked workflow", Settings: settings})
	require.NoError(t, err)
	before, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
	require.NoError(t, err)
	mgr, err := NewManager(ManagerOptions{Store: db, Manifest: manifestManagedTools(t), Config: Config{SessionRoot: t.TempDir()}})
	require.NoError(t, err)
	defer mgr.Close(ctx)
	mgr.embeddedOwners = map[string]embeddedOwner{binding.AgentID: {binding: binding, identity: "fixture:" + binding.AgentID}}
	for _, action := range []string{"set", "off"} {
		args := map[string]any{"action": action}
		if action == "set" {
			args["goal"] = "replacement"
		}
		_, err = mgr.directEmbeddedBrowserGoal(ctx, binding.AgentID, "goal.control", args, nil)
		require.ErrorContains(t, err, "manual goal control is disabled while workflow")
		after, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
		require.NoError(t, err)
		require.Equal(t, before.DesiredRevision, after.DesiredRevision)
		require.Equal(t, before.Config, after.Config)
	}
	_, err = mgr.directEmbeddedBrowserGoal(ctx, binding.AgentID, "goal.control", map[string]any{"action": "status"}, nil)
	require.NoError(t, err)
	mgr.mu.Lock()
	owner := mgr.embeddedOwners[binding.AgentID]
	owner.binding.Generation = "stale-generation"
	mgr.embeddedOwners[binding.AgentID] = owner
	mgr.mu.Unlock()
	_, err = mgr.directEmbeddedBrowserGoal(ctx, binding.AgentID, "goal.control", map[string]any{"action": "off"}, nil)
	require.ErrorIs(t, err, errManagedControlsUnavailable)
}

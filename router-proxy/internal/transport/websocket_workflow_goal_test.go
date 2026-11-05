package transport

import (
	"context"
	"testing"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Supporting transport proof. Public registration/proxy coverage belongs to the
// workflow Playwright suite; the managed target here has no legacy WS session.
func TestGoalControlCurrentManagedWorkflowRejectsWithoutMutation(t *testing.T) {
	for _, action := range []string{"set", "off"} {
		t.Run(action, func(t *testing.T) {
			durable, _, binding := setupTestStoreWithParent(t, "workflow-target")
			ctx := context.Background()
			state, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "active"})
			require.NoError(t, err)
			require.NoError(t, durable.SetManagedAgentActiveWorkflowState(ctx, binding.AgentID, &state.ID))
			settings := store.DefaultManagedGoalSettings()
			settings.GoalLockEnabled = true
			_, err = durable.UpdateManagedGoalConfig(ctx, binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "Complete active workflow: active", Settings: settings})
			require.NoError(t, err)
			require.NoError(t, durable.SetManagedGoalSnoozeUntil(ctx, binding.AgentID, "2099-01-01T00:00:00Z"))
			before, err := durable.ManagedGoalPolicyFor(ctx, binding.AgentID)
			require.NoError(t, err)
			router, err := New(Options{Store: durable, GoalControlAvailable: true})
			require.NoError(t, err)
			var frames []map[string]any
			source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", "user_interaction", func(frame map[string]any) error { frames = append(frames, frame); return nil })
			request := controlRequestFrame("goal", "source", "source-instance", "source-lease", binding.AgentID, binding.SessionID, "workflow-"+action)
			request["action"] = rawJSON(action)
			require.NoError(t, router.goalControl(source, request))
			require.Len(t, frames, 1)
			require.Equal(t, "error", frames[0]["type"])
			require.Equal(t, "WORKFLOW_ACTIVE", frames[0]["code"])
			require.Equal(t, "request-workflow-"+action, frames[0]["request_id"])
			after, err := durable.ManagedGoalPolicyFor(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, before, after, "denial must preserve goal/revision/lock/snooze")
			current, err := durable.ManagedAgent(ctx, binding.AgentID)
			require.NoError(t, err)
			require.Equal(t, state.ID, current.ActiveWorkflowStateID)
			desired, err := durable.GoalControlDesiredFor(ctx, binding.AgentID, binding.SessionID)
			require.NoError(t, err)
			require.Nil(t, desired)
			require.Empty(t, router.pendingGoal)
			require.NotContains(t, router.sessions, binding.AgentID)
		})
	}
}

func TestGoalControlManagedWorkflowPreservesSourceAndTargetChecks(t *testing.T) {
	for _, tc := range []struct {
		name, role, targetInstance, lease, code string
		workflow                                bool
	}{
		{"stale target", "user_interaction", "old-session", "source-lease", "target_not_connected", true},
		{"agent ID is not session", "user_interaction", "workflow-target", "source-lease", "target_not_connected", true},
		{"empty target", "user_interaction", "", "source-lease", "target_not_connected", true},
		{"non UI", "agent", "workflow-target-session", "source-lease", "goal_control_not_allowed", true},
		{"bad source lease", "user_interaction", "workflow-target-session", "wrong-lease", "lease_not_active", true},
		{"no workflow", "user_interaction", "workflow-target-session", "source-lease", "target_not_connected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			durable, _, binding := setupTestStoreWithParent(t, "workflow-target")
			if tc.workflow {
				state, err := durable.CreateWorkflowState(context.Background(), store.WorkflowStateRequest{Name: "active"})
				require.NoError(t, err)
				require.NoError(t, durable.SetManagedAgentActiveWorkflowState(context.Background(), binding.AgentID, &state.ID))
			}
			router, err := New(Options{Store: durable, GoalControlAvailable: true})
			require.NoError(t, err)
			var frames []map[string]any
			source := newTransportSession(t, router, durable, "source", "source-instance", "source-lease", tc.role, func(frame map[string]any) error { frames = append(frames, frame); return nil })
			source.closeOverride = func(websocket.StatusCode, string) error { return nil }
			request := controlRequestFrame("goal", "source", "source-instance", tc.lease, binding.AgentID, tc.targetInstance, "checks")
			require.NoError(t, router.goalControl(source, request))
			require.Len(t, frames, 1)
			require.Equal(t, tc.code, frames[0]["code"])
			require.Equal(t, "request-checks", frames[0]["request_id"])
			require.Empty(t, router.pendingGoal)
		})
	}
}

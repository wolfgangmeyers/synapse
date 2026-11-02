package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestWorkflowLifecycleRetiresTimerAfterDurableSuccess(t *testing.T) {
	for _, action := range []string{"transition", "exit", "deactivate"} {
		for _, fail := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/success", true: "/rollback"}[fail], func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "router.db")
				clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
				db, err := store.Open(ctx, path, store.Options{Clock: clock})
				require.NoError(t, err)
				defer db.Close()
				binding := subagentTestParent(t, db, "retirement")
				owner := embeddedOwner{binding: binding, identity: "fixture:retirement"}
				wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Retirement"})
				require.NoError(t, err)
				next, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Next", WorkflowID: &wf.ID})
				require.NoError(t, err)
				state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Current", WorkflowID: &wf.ID, AllowExit: ptr(true), Transitions: []store.WorkflowTransitionConfig{{TargetStateID: next.ID}}})
				require.NoError(t, err)
				require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
				settings := store.DefaultManagedGoalSettings()
				settings.GoalLockEnabled = true
				_, err = db.UpdateManagedGoalConfig(ctx, binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "Complete active workflow: Retirement", Settings: settings})
				require.NoError(t, err)
				before, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
				require.NoError(t, err)
				require.NoError(t, db.SetManagedGoalSnoozeUntil(ctx, binding.AgentID, "2030-01-01T00:05:00.000Z"))
				raw, err := sql.Open("sqlite", path)
				require.NoError(t, err)
				defer raw.Close()
				if fail {
					_, err = raw.ExecContext(ctx, `CREATE TRIGGER fail_retirement BEFORE UPDATE OF snoozed_until ON managed_agent_policies BEGIN SELECT RAISE(ABORT,'retirement failed'); END`)
					require.NoError(t, err)
				}
				mgr := &Manager{store: db, clock: clock}
				mgr.scheduleEmbeddedGoalContinuation(binding, store.EmbeddedGoalEvaluationKey{}, clock.Now().Add(5*time.Minute))
				if action == "deactivate" {
					_, err = mgr.embeddedWorkflowAssignTool(owner).Execute(ctx, "deactivate", map[string]any{"agent_id": binding.AgentID, "action": "deactivate"}, nil)
				} else {
					_, err = mgr.embeddedWorkflowTransitionTool(owner, "mecha_workflow_transition").Execute(ctx, "lifecycle", map[string]any{"action": action, "target_state": "Next"}, nil)
				}
				if fail {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				mgr.mu.Lock()
				timer := mgr.embeddedGoalTimers[binding.AgentID]
				mgr.mu.Unlock()
				if fail {
					require.NotNil(t, timer)
					timer.cancel()
				} else {
					require.Nil(t, timer, "successful durable retirement must cancel old timer")
				}
				policy, err := db.ManagedGoalPolicyFor(ctx, binding.AgentID)
				require.NoError(t, err)
				agent, err := db.ManagedAgent(ctx, binding.AgentID)
				require.NoError(t, err)
				if fail {
					require.Equal(t, state.ID, agent.ActiveWorkflowStateID)
					require.Equal(t, "2030-01-01T00:05:00.000Z", policy.SnoozedUntil)
				} else {
					require.Empty(t, policy.SnoozedUntil)
					if action == "transition" {
						require.Equal(t, next.ID, agent.ActiveWorkflowStateID)
						require.Equal(t, before.DesiredRevision, policy.DesiredRevision)
						require.True(t, policy.Config.Settings.GoalLockEnabled)
					} else {
						require.Empty(t, agent.ActiveWorkflowStateID)
						require.False(t, policy.Config.Enabled)
						require.False(t, policy.Config.Settings.GoalLockEnabled)
						require.Empty(t, policy.Config.Goal)
					}
				}
			})
		}
	}
}

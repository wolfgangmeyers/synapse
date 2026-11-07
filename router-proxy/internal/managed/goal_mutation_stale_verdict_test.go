package managed

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestGoalMutationDoesNotCancelButRejectsStaleVerdict(t *testing.T) {
	for _, mutation := range []string{"browser-set", "workflow-assign", "worker-set", "worker-off", "worker-fallback-set", "worker-fallback-off"} {
		for _, verdict := range []string{store.EmbeddedGoalEvaluationDone, store.EmbeddedGoalEvaluationNotDone} {
			t.Run(mutation+"/"+verdict, func(t *testing.T) {
				f := newFailureFixture(t)
				entered, release, observed := make(chan struct{}), make(chan struct{}), make(chan error, 1)
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				f.m.embeddedGoalEvaluator = func(ctx context.Context, _ store.EmbeddedGoalEvaluationKey) (string, string, error) {
					close(entered)
					<-release
					observed <- ctx.Err()
					return verdict, "stale", nil
				}
				owner := f.launch(t, "")
				f.m.startWG.Wait()
				ctx := context.Background()
				_, err := f.db.UpdateManagedGoalConfig(ctx, owner.binding.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "original", Settings: store.DefaultManagedGoalSettings()})
				require.NoError(t, err)
				evaluateClaudeFixture(t, f, owner)
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("evaluator did not start")
				}
				switch mutation {
				case "browser-set":
					_, err = f.m.directEmbeddedBrowserGoal(ctx, owner.binding.AgentID, "goal.control", map[string]any{"action": "set", "goal": "revised"}, nil)
				case "workflow-assign":
					wf, e := f.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "replacement"})
					require.NoError(t, e)
					state, e := f.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "entry", WorkflowID: &wf.ID})
					require.NoError(t, e)
					_, err = f.m.embeddedWorkflowAssignTool(owner).Execute(ctx, "assign", map[string]any{"agent_id": owner.binding.AgentID, "action": "activate", "workflow_id": wf.ID, "workflow_state_id": state.ID}, nil)
				case "worker-set", "worker-off", "worker-fallback-set", "worker-fallback-off":
					action := "set"
					if mutation == "worker-off" || mutation == "worker-fallback-off" {
						action = "off"
					}
					// Removing only the in-memory control binding forces the worker tool's
					// store fallback; restore it before the evaluator is released.
					fallback := mutation == "worker-fallback-set" || mutation == "worker-fallback-off"
					if fallback {
						f.m.mu.Lock()
						delete(f.m.embeddedOwners, owner.binding.AgentID)
						f.m.mu.Unlock()
					}
					_, err = f.m.embeddedWorkerSetGoalTool(owner).Execute(ctx, "worker-"+action, map[string]any{"worker_id": owner.binding.AgentID, "action": action, "goal": "revised"}, nil)
					if fallback {
						f.m.mu.Lock()
						f.m.embeddedOwners[owner.binding.AgentID] = owner
						f.m.mu.Unlock()
					}
				}
				require.NoError(t, err)
				policy, err := f.db.ManagedGoalPolicyFor(ctx, owner.binding.AgentID)
				require.NoError(t, err)
				require.NotNil(t, policy)
				switch mutation {
				case "worker-off", "worker-fallback-off":
					require.False(t, policy.Config.Enabled)
				case "workflow-assign":
					require.True(t, policy.Config.Enabled)
					require.Equal(t, "Complete active workflow: replacement", policy.Config.Goal)
				default:
					require.True(t, policy.Config.Enabled)
					require.Equal(t, "revised", policy.Config.Goal)
				}
				unblock()
				select {
				case err := <-observed:
					require.NoError(t, err, "mutation must not cancel the evaluator")
				case <-time.After(5 * time.Second):
					t.Fatal("evaluator did not return")
				}
				done := make(chan struct{})
				go func() { f.m.startWG.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("evaluator did not settle")
				}
				require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, owner.binding.AgentID))
				require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state IN ('DONE','NOT_DONE')`, owner.binding.AgentID))
				require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, owner.binding.AgentID))
				require.Zero(t, f.count(t, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, owner.binding.AgentID))
			})
		}
	}
}

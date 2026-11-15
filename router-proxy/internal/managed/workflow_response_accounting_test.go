package managed

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// SCN-GWS-008: exercise a workflow turn through the real Manager, Pi SDK and
// SQLite. A checked response renews the workflow attempt allowance; the same
// response must not change standalone-goal accounting.
func TestWorkflowCheckedResponseResetsAutomaticAttempts(t *testing.T) {
	for _, workflow := range []bool{true, false} {
		name := "standalone"
		if workflow {
			name = "workflow"
		}
		t.Run(name, func(t *testing.T) {
			f := newFailureFixture(t)
			f.reg.SetResponses([]providers.FauxResponseStep{f.response("startup"), f.response("progress")})
			owner := f.launch(t, "start")
			ctx := context.Background()
			if workflow {
				wf, err := f.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Accounting"})
				require.NoError(t, err)
				state, err := f.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "Working", WorkflowID: &wf.ID})
				require.NoError(t, err)
				require.NoError(t, f.db.SetManagedAgentActiveWorkflowWithVariables(ctx, owner.binding.AgentID, &wf.ID, &state.ID, nil))
			}
			saved, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
			require.NoError(t, err)
			lane := f.m.laneFor(owner.binding.AgentID)
			lane.serial.Lock()
			lane.consecutiveContinuations.Store(19)
			turnCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)
			turnCtx = context.WithValue(turnCtx, embeddedAutomaticGoalKey{}, true)
			err = f.m.runEmbeddedSessionTurn(turnCtx, *saved, "continue", nil)
			got := lane.consecutiveContinuations.Load()
			lane.serial.Unlock()
			require.NoError(t, err)
			if workflow {
				require.Zero(t, got, "checked workflow response must reset the chain")
			} else {
				require.EqualValues(t, 19, got)
			}
		})
	}
}

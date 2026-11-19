package managed

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestClaudeWorkflowCrashRecoveryNudge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := newFailureFixture(t)

	var requests atomic.Int32
	f.reg.SetResponses([]providers.FauxResponseStep{
		// Turn 1: Initial response
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Initial response"},
			}, ai.StopStop)
		},
		// Turn 2: Workflow continuation turn
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Continuing workflow"},
			}, ai.StopStop)
		},
	})

	owner := f.launch(t, "start work")
	require.Eventually(t, func() bool {
		return requests.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)

	// Create workflow and state in the database
	wf, err := f.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "Crash Recovery Workflow"})
	require.NoError(t, err)
	st, err := f.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "State A", WorkflowID: &wf.ID})
	require.NoError(t, err)

	// Update agent to Claude Code WITH active workflow
	_, err = f.raw.ExecContext(ctx, `UPDATE managed_agents SET selected_model_provider=?, selected_model_id=?, active_workflow_id=?, active_workflow_state_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", wf.ID, st.ID, owner.binding.AgentID)
	require.NoError(t, err)

	// Update policy: applied_revision == desired_revision
	_, err = f.raw.ExecContext(ctx, `UPDATE managed_agent_policies SET applied_revision=1, desired_revision=1, projection_state='applied', goal_enabled=1, goal_text='Complete active workflow' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)

	// Reset lane throttle
	lane := f.m.laneFor(owner.binding.AgentID)
	lane.serial.Lock()
	lane.goalNextAttempt = time.Time{}
	lane.serial.Unlock()

	// Call kickoffIdleGoalContinuation: must NOT be suppressed because active workflow exists
	f.m.kickoffIdleGoalContinuation(owner.binding)

	// Assert that the continuation turn was executed (seq advanced past Turn 1's seq of 3)
	require.Eventually(t, func() bool {
		var maxSeq int64
		err := f.raw.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM embedded_entries WHERE agent_id=?`, owner.binding.AgentID).Scan(&maxSeq)
		return err == nil && maxSeq > 3
	}, 15*time.Second, 50*time.Millisecond)
}

func TestClaudeNonWorkflowAppliedRevisionStillSuppressed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	f := newFailureFixture(t)

	var requests atomic.Int32
	f.reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			requests.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "initial work"},
			}, ai.StopStop)
		},
	})

	owner := f.launch(t, "start work")

	require.Eventually(t, func() bool {
		return requests.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)

	// Verify agent is idle
	require.Eventually(t, func() bool {
		a, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
		return err == nil && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Set model to Claude Code
	_, err := f.raw.ExecContext(ctx, `UPDATE managed_agents SET selected_model_provider=?, selected_model_id=? WHERE agent_id=?`, claudeCodeProvider, "sonnet", owner.binding.AgentID)
	require.NoError(t, err)

	// Set policy to AppliedRevision == DesiredRevision with NO active workflow
	_, err = f.raw.ExecContext(ctx, `UPDATE managed_agent_policies SET applied_revision=1, desired_revision=1, projection_state='applied', goal_enabled=1, goal_text='non-workflow goal' WHERE agent_id=?`, owner.binding.AgentID)
	require.NoError(t, err)

	// Ensure agent is idle and has NO active workflow
	a, err := f.db.ManagedAgent(ctx, owner.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, a.ActiveWorkflowID)
	require.Empty(t, a.ActiveWorkflowStateID)

	// Calling kickoffIdleGoalContinuation should be SUPPRESSED for Claude Code when not in a workflow
	f.m.kickoffIdleGoalContinuation(owner.binding)

	time.Sleep(100 * time.Millisecond)
	// Requests must remain 1 (no continuation turn was dispatched)
	require.Equal(t, int32(1), requests.Load(), "non-workflow Claude Code goal continuation must remain suppressed when AppliedRevision == DesiredRevision")
}

package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestManagerRecoversInterruptedWorkflowCheck(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	b := owner.binding
	keys, err := f.m.recoverInterruptedWorkflowChecks(b)
	require.NoError(t, err)
	require.Empty(t, keys, "no candidate is not a recovery error")
	wf, err := f.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "recover"})
	require.NoError(t, err)
	st, err := f.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, f.db.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
	_, err = f.db.UpdateManagedGoalConfig(ctx, b.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "work", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	u, err := f.db.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, b, u.Sequence, u.Digest))
	_, _, err = f.db.CommitEmbeddedGoalAppliedReceipt(ctx, b, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "recover", TurnEntrySeq: u.Sequence, TurnEntryDigest: u.Digest, GoalRevision: policy.DesiredRevision})
	require.NoError(t, err)
	body, err = json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
	require.NoError(t, err)
	last, err := f.db.AppendEmbeddedEntry(ctx, b, "assistant", body)
	require.NoError(t, err)
	k := store.EmbeddedGoalEvaluationKey{UserSeq: u.Sequence, UserDigest: u.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: last.Sequence, FinalDigest: last.Digest}
	_, claimed, err := f.db.ClaimEmbeddedGoalEvaluation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, claimed)
	b.OwnerToken, err = f.db.AcquireEmbeddedOwner(ctx, b, "successor", 0)
	require.NoError(t, err)
	_, err = f.m.recoverInterruptedWorkflowChecks(b)
	require.NoError(t, err)
	c, err := f.db.EmbeddedGoalContinuationFor(ctx, b, k)
	require.NoError(t, err)
	require.Equal(t, "reserved", c.State)
	// A second owner arrives after settlement but before a timer was installed.
	b.OwnerToken, err = f.db.AcquireEmbeddedOwner(ctx, b, "second-successor", 0)
	require.NoError(t, err)
	keys, err = f.m.recoverUnstartedWorkflowReservations(b)
	require.NoError(t, err)
	require.Equal(t, []store.EmbeddedGoalEvaluationKey{k}, keys)
	f.m.dispatchEmbeddedGoalContinuation(b, k)
	f.m.mu.Lock()
	timer := f.m.embeddedGoalTimers[b.AgentID]
	require.NotNil(t, timer)
	require.Equal(t, k, timer.key)
	timer.cancel()
	delete(f.m.embeddedGoalTimers, b.AgentID)
	f.m.mu.Unlock()
	_, started, err := f.db.StartEmbeddedGoalContinuation(ctx, b, k)
	require.NoError(t, err)
	require.True(t, started)
	keys, err = f.m.recoverUnstartedWorkflowReservations(b)
	require.NoError(t, err)
	require.Empty(t, keys, "started unknown must not be replayed")
	require.NoError(t, f.db.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, nil, nil, nil))
	prompt := f.m.formatGoalContinuationPrompt(ctx, b, &k)
	require.Contains(t, prompt, "workflow recover is currently active in state work")
	token, err := f.db.EnterEmbeddedSDKExecution(ctx, b, &k)
	require.NoError(t, err)
	p := &embeddedPersistence{db: f.db, binding: b, executionToken: token, continuationKey: &k}
	lane := &managedAgentLane{}
	observation := &workflowNudgeObservation{p: p, lane: lane, limit: 20}
	require.NoError(t, observation.dispatch(ctx))
	require.True(t, observation.workflow)
	require.Equal(t, wf.ID, observation.workflowID)
	_, err = f.db.AcquireEmbeddedOwner(ctx, b, "stale-prompt-owner", 0)
	require.NoError(t, err)
	require.Empty(t, f.m.formatGoalContinuationPrompt(ctx, b, &k))
	require.Error(t, observation.dispatch(ctx))
}

func TestInterruptedRecoveryDistinguishesListingError(t *testing.T) {
	f := newFailureFixture(t)
	_, err := f.m.recoverInterruptedWorkflowChecks(store.EmbeddedBinding{})
	require.Error(t, err)
}

func TestManagerRecoversPreclaimWorkflowFinal(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	b := owner.binding
	wf, err := f.db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "preclaim"})
	require.NoError(t, err)
	st, err := f.db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "work", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, f.db.SetManagedAgentActiveWorkflowWithVariables(ctx, b.AgentID, &wf.ID, &st.ID, nil))
	_, err = f.db.UpdateManagedGoalConfig(ctx, b.AgentID, store.ManagedGoalConfig{Enabled: true, Goal: "work", Settings: store.DefaultManagedGoalSettings()})
	require.NoError(t, err)
	policy, err := f.db.ManagedGoalPolicyFor(ctx, b.AgentID)
	require.NoError(t, err)
	token, err := f.db.EnterEmbeddedSDKExecution(ctx, b, nil)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
	require.NoError(t, err)
	u, err := f.db.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	require.NoError(t, f.db.SetEmbeddedSDKExecutionUser(ctx, b, token, u.Sequence, u.Digest))
	require.NoError(t, f.db.EnterEmbeddedNonMailProviderTurn(ctx, b, u.Sequence, u.Digest))
	_, _, err = f.db.CommitEmbeddedGoalAppliedReceipt(ctx, b, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "preclaim", TurnEntrySeq: u.Sequence, TurnEntryDigest: u.Digest, GoalRevision: policy.DesiredRevision})
	require.NoError(t, err)
	body, err = json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
	require.NoError(t, err)
	last, err := f.db.AppendEmbeddedEntry(ctx, b, "assistant", body)
	require.NoError(t, err)
	key := store.EmbeddedGoalEvaluationKey{UserSeq: u.Sequence, UserDigest: u.Digest, GoalRevision: policy.DesiredRevision, FinalSeq: last.Sequence, FinalDigest: last.Digest}
	b.OwnerToken, err = f.db.AcquireEmbeddedOwner(ctx, b, "successor", 0)
	require.NoError(t, err)
	keys, err := f.m.recoverPreclaimWorkflowCheck(b)
	require.NoError(t, err)
	require.Equal(t, []store.EmbeddedGoalEvaluationKey{key}, keys)
	b.OwnerToken, err = f.db.AcquireEmbeddedOwner(ctx, b, "second-successor", 0)
	require.NoError(t, err)
	keys, err = f.m.recoverUnstartedWorkflowReservations(b)
	require.NoError(t, err)
	require.Equal(t, []store.EmbeddedGoalEvaluationKey{key}, keys)
}

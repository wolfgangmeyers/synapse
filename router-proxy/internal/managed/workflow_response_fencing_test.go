package managed

import (
	"context"
	"database/sql"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Real SQLite owner and execution fences guard both runtime observation paths.
func TestWorkflowResponseObservationChecksCurrentTurn(t *testing.T) {
	ctx := context.Background()
	db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agent_policies(agent_id,history_scope_id,desired_revision,goal_enabled,goal_text,settings_json,created_at,updated_at) VALUES(?,?,1,1,'workflow','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, binding.AgentID, "goal-history-"+binding.AgentID)
	require.NoError(t, err)
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "response"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	token, err := db.EnterEmbeddedSDKExecution(ctx, binding, nil)
	require.NoError(t, err)
	p := &embeddedPersistence{db: db, binding: binding, executionToken: token, digest: "genesis"}
	require.NoError(t, p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "nudge"}}}))
	lane := &managedAgentLane{}
	lane.consecutiveContinuations.Store(19)
	o := &workflowNudgeObservation{lane: lane, p: p, limit: 20}
	require.NoError(t, o.dispatch(ctx))
	o.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventTextDelta, Delta: " \t"})
	o.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventToolCallStart})
	o.observeClaude(ctx, &ai.AssistantMessage{Content: ai.ContentList{ai.ThinkingContent{Thinking: "thinking"}}})
	require.EqualValues(t, 19, lane.consecutiveContinuations.Load())
	require.EqualValues(t, 1, lane.workflowUnanswered.Load())
	// A newer checked user makes the old nudge's output stale.
	require.NoError(t, p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "new user"}}}))
	o.observeClaude(ctx, &ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "late"}}})
	require.EqualValues(t, 19, lane.consecutiveContinuations.Load())
	require.EqualValues(t, 1, lane.workflowUnanswered.Load())
	fresh := &workflowNudgeObservation{lane: lane, p: p, limit: 20}
	require.NoError(t, fresh.dispatch(ctx))
	fresh.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventToolCallEnd, ToolCall: &ai.ToolCall{ID: "id", Name: "read"}})
	require.Zero(t, lane.consecutiveContinuations.Load())
	require.Zero(t, lane.workflowUnanswered.Load())
}

func TestWorkflowResponseObservationClaudePointerVariants(t *testing.T) {
	ctx := context.Background()
	db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agent_policies(agent_id,history_scope_id,desired_revision,goal_enabled,goal_text,settings_json,created_at,updated_at) VALUES(?,?,1,1,'workflow','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, binding.AgentID, "goal-history-"+binding.AgentID)
	require.NoError(t, err)
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "pointer-response"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	token, err := db.EnterEmbeddedSDKExecution(ctx, binding, nil)
	require.NoError(t, err)
	p := &embeddedPersistence{db: db, binding: binding, executionToken: token, digest: "genesis"}
	require.NoError(t, p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "nudge"}}}))

	// Test *ai.TextContent pointer variant
	lane := &managedAgentLane{}
	lane.consecutiveContinuations.Store(15)
	o := &workflowNudgeObservation{lane: lane, p: p, limit: 20}
	require.NoError(t, o.dispatch(ctx))
	o.observeClaude(ctx, &ai.AssistantMessage{Content: ai.ContentList{&ai.TextContent{Text: "valid pointer text"}}})
	require.Zero(t, lane.consecutiveContinuations.Load(), "*ai.TextContent pointer variant must be recognized as eligible response")
	require.Zero(t, lane.workflowUnanswered.Load())

	// Test *ai.ToolCall pointer variant
	lane2 := &managedAgentLane{}
	lane2.consecutiveContinuations.Store(12)
	o2 := &workflowNudgeObservation{lane: lane2, p: p, limit: 20}
	require.NoError(t, o2.dispatch(ctx))
	o2.observeClaude(ctx, &ai.AssistantMessage{Content: ai.ContentList{&ai.ToolCall{ID: "call-1", Name: "bash"}}})
	require.Zero(t, lane2.consecutiveContinuations.Load(), "*ai.ToolCall pointer variant must be recognized as eligible response")
	require.Zero(t, lane2.workflowUnanswered.Load())
}

func TestWorkflowResponseObservationPiThinkingIgnored(t *testing.T) {
	ctx := context.Background()
	db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, "2026-01-01T00:00:00Z")
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO managed_agent_policies(agent_id,history_scope_id,desired_revision,goal_enabled,goal_text,settings_json,created_at,updated_at) VALUES(?,?,1,1,'workflow','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, binding.AgentID, "goal-history-"+binding.AgentID)
	require.NoError(t, err)
	wf, err := db.CreateWorkflow(ctx, store.WorkflowRequest{Name: "pi-thinking"})
	require.NoError(t, err)
	state, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "working", WorkflowID: &wf.ID})
	require.NoError(t, err)
	require.NoError(t, db.SetManagedAgentActiveWorkflowWithVariables(ctx, binding.AgentID, &wf.ID, &state.ID, nil))
	token, err := db.EnterEmbeddedSDKExecution(ctx, binding, nil)
	require.NoError(t, err)
	p := &embeddedPersistence{db: db, binding: binding, executionToken: token, digest: "genesis"}
	require.NoError(t, p.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "nudge"}}}))

	lane := &managedAgentLane{}
	lane.consecutiveContinuations.Store(14)
	o := &workflowNudgeObservation{lane: lane, p: p, limit: 20}
	require.NoError(t, o.dispatch(ctx))

	// observePi must ignore EventThinkingDelta and EventThinkingEnd so thinking does not reset workflow nudges.
	o.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventThinkingDelta, Delta: "thinking thoughts"})
	require.EqualValues(t, 14, lane.consecutiveContinuations.Load(), "EventThinkingDelta must not reset consecutiveContinuations")
	require.EqualValues(t, 1, lane.workflowUnanswered.Load(), "EventThinkingDelta must not reset workflowUnanswered")

	o.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventThinkingEnd, Content: "finished thinking"})
	require.EqualValues(t, 14, lane.consecutiveContinuations.Load(), "EventThinkingEnd must not reset consecutiveContinuations")
	require.EqualValues(t, 1, lane.workflowUnanswered.Load(), "EventThinkingEnd must not reset workflowUnanswered")

	// Verify that subsequent eligible content (e.g., text) does reset workflow nudges.
	o.observePi(ctx, ai.AssistantMessageEvent{Type: ai.EventTextDelta, Delta: "visible action taken"})
	require.Zero(t, lane.consecutiveContinuations.Load(), "visible text must reset consecutiveContinuations")
	require.Zero(t, lane.workflowUnanswered.Load(), "visible text must reset workflowUnanswered")
}


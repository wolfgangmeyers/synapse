package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestSetEmbeddedActivityStateTogglesAndBumpsProjection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b := redBind(t, s, "activity-worker", "activity-session", 1, "activity-generation", "worker")

	initialVersion, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)

	// Transition from idle to active
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))
	agent, err := s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", agent.ActivityState)

	versionAfterActive, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)
	// One bump from SetEmbeddedActivityState + one bump from our query
	require.Equal(t, initialVersion+2, versionAfterActive)

	// Setting to active again should be a no-op (no extra projection bump)
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))
	versionNoChange, err := s.bumpManagedProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, versionAfterActive+1, versionNoChange)

	// Transition from active to idle
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "idle"))
	agent, err = s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", agent.ActivityState)

	// Invalid state returns error
	require.Error(t, s.SetEmbeddedActivityState(ctx, b, "unknown_state"))
}

func TestResetActiveEmbeddedAgentsStartupRecovery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b1 := redBind(t, s, "active-agent-1", "session-1", 1, "gen-1", "worker")
	b2 := redBind(t, s, "active-agent-2", "session-2", 1, "gen-1", "root")

	// Set both to active
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b1, "active"))
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b2, "active"))

	a1, err := s.ManagedAgent(ctx, b1.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", a1.ActivityState)

	a2, err := s.ManagedAgent(ctx, b2.AgentID)
	require.NoError(t, err)
	require.Equal(t, "active", a2.ActivityState)

	// Insert dangling active history entry and completed entry
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('active-agent-1', 'managed_tool', 'info', 'Running bash', '2026-10-08T14:40:34.372Z', '{"kind":"tool","tool_name":"bash","outcome":"active","summary":"Running"}')`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id, category, level, message, occurred_at, presentation_json)
		VALUES ('active-agent-1', 'managed_tool', 'info', 'Ran echo', '2026-10-08T14:40:30.000Z', '{"kind":"tool","tool_name":"bash","outcome":"completed","summary":"Succeeded"}')`)
	require.NoError(t, err)

	// Simulate router startup resetting active agents to idle
	require.NoError(t, s.ResetActiveEmbeddedAgents(ctx))

	a1, err = s.ManagedAgent(ctx, b1.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a1.ActivityState)

	a2, err = s.ManagedAgent(ctx, b2.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a2.ActivityState)

	var activeOutcome, activeSummary string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Running bash'`).Scan(&activeOutcome, &activeSummary))
	require.Equal(t, "failed", activeOutcome)
	require.Equal(t, "Interrupted by restart", activeSummary)

	var completedOutcome, completedSummary string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT json_extract(presentation_json, '$.outcome'), json_extract(presentation_json, '$.summary') FROM managed_complete_history WHERE message='Ran echo'`).Scan(&completedOutcome, &completedSummary))
	require.Equal(t, "completed", completedOutcome)
	require.Equal(t, "Succeeded", completedSummary)
}

func TestResetActiveEmbeddedAgentsDanglingToolCallsReconciliation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b := redBind(t, s, "dangling-worker", "session-dangling", 1, "gen-dangling", "worker")

	// 1. Initial user turn
	userMsg := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "do something"}}}
	userBody, err := json.Marshal(userMsg)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "user", userBody)
	require.NoError(t, err)

	// 2. Assistant message with two tool calls: call_1 and call_2
	assistantMsg := ai.AssistantMessage{
		Content: ai.ContentList{
			ai.TextContent{Text: "Running tools"},
			ai.ToolCall{
				ID:        "call_1",
				Name:      "bash",
				Arguments: map[string]any{"command": "echo hello"},
			},
			ai.ToolCall{
				ID:        "call_2",
				Name:      "bash",
				Arguments: map[string]any{"command": "sleep 10"},
			},
		},
		StopReason: ai.StopToolUse,
	}
	assistantBody, err := json.Marshal(assistantMsg)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", assistantBody)
	require.NoError(t, err)

	// 3. Only call_1 completes before crash/restart
	tool1Result := ai.ToolResultMessage{
		ToolCallID: "call_1",
		ToolName:   "bash",
		Content:    ai.ContentList{ai.TextContent{Text: "hello"}},
		IsError:    false,
	}
	tool1Body, err := json.Marshal(tool1Result)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "toolResult", tool1Body)
	require.NoError(t, err)

	// Verify head before recovery is toolResult for call_1
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 3, count) // user, assistant, toolResult(call_1)

	// Set activity to active to simulate turn in progress
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))

	// Trigger restart recovery
	require.NoError(t, s.ResetActiveEmbeddedAgents(ctx))

	// call_2 should have received a synthetic toolResult
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 4, count) // user, assistant, toolResult(call_1), synthetic toolResult(call_2)

	var lastKind, lastBody string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT kind, CAST(body AS TEXT) FROM embedded_entries WHERE agent_id=? ORDER BY seq DESC LIMIT 1`, b.AgentID).Scan(&lastKind, &lastBody))
	require.Equal(t, "toolResult", lastKind)

	var recoveredResult ai.ToolResultMessage
	require.NoError(t, json.Unmarshal([]byte(lastBody), &recoveredResult))
	require.Equal(t, "call_2", recoveredResult.ToolCallID)
	require.Equal(t, "bash", recoveredResult.ToolName)
	require.True(t, recoveredResult.IsError)
	require.Len(t, recoveredResult.Content, 1)
	require.Equal(t, "Command interrupted by router restart", recoveredResult.Content[0].(ai.TextContent).Text)

	// Idempotency: running recovery again should not inject another result
	require.NoError(t, s.ResetActiveEmbeddedAgents(ctx))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 4, count)
}

func TestResetActiveEmbeddedAgentsAllToolCallsDangling(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	b := redBind(t, s, "dangling-worker-all", "session-dangling-all", 1, "gen-dangling-all", "worker")

	// 1. Initial user turn
	userMsg := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "run something"}}}
	userBody, err := json.Marshal(userMsg)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "user", userBody)
	require.NoError(t, err)

	// 2. Assistant message with two tool calls, NEITHER has a result before restart
	assistantMsg := ai.AssistantMessage{
		Content: ai.ContentList{
			ai.TextContent{Text: "Running tools"},
			ai.ToolCall{
				ID:        "call_a",
				Name:      "bash",
				Arguments: map[string]any{"command": "echo a"},
			},
			ai.ToolCall{
				ID:        "call_b",
				Name:      "read",
				Arguments: map[string]any{"path": "file.txt"},
			},
		},
		StopReason: ai.StopToolUse,
	}
	assistantBody, err := json.Marshal(assistantMsg)
	require.NoError(t, err)
	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", assistantBody)
	require.NoError(t, err)

	// Set activity to active
	require.NoError(t, s.SetEmbeddedActivityState(ctx, b, "active"))

	// Trigger restart recovery
	require.NoError(t, s.ResetActiveEmbeddedAgents(ctx))

	// Both call_a and call_b should have received synthetic toolResults in order
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&count))
	require.Equal(t, 4, count) // user, assistant, synthetic(call_a), synthetic(call_b)

	rows, err := s.db.QueryContext(ctx, `SELECT seq, kind, CAST(body AS TEXT) FROM embedded_entries WHERE agent_id=? AND seq > 2 ORDER BY seq ASC`, b.AgentID)
	require.NoError(t, err)
	defer rows.Close()

	var results []ai.ToolResultMessage
	for rows.Next() {
		var seq int64
		var kind, body string
		require.NoError(t, rows.Scan(&seq, &kind, &body))
		require.Equal(t, "toolResult", kind)
		var res ai.ToolResultMessage
		require.NoError(t, json.Unmarshal([]byte(body), &res))
		results = append(results, res)
	}
	require.NoError(t, rows.Err())
	require.Len(t, results, 2)
	require.Equal(t, "call_a", results[0].ToolCallID)
	require.Equal(t, "bash", results[0].ToolName)
	require.True(t, results[0].IsError)
	require.Equal(t, "Command interrupted by router restart", results[0].Content[0].(ai.TextContent).Text)

	require.Equal(t, "call_b", results[1].ToolCallID)
	require.Equal(t, "read", results[1].ToolName)
	require.True(t, results[1].IsError)
	require.Equal(t, "Command interrupted by router restart", results[1].Content[0].(ai.TextContent).Text)
}


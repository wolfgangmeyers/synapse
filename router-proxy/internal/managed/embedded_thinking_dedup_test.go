package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedThinking_MultiStepToolTurns_DeduplicateIdenticalCumulativeThinking(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "thinking_dedup.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()

	agentID := "worker-thinking-dedup"
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,'worker',?,'openai-codex','gpt-5','running','running','idle','embedded','session-1',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, t.TempDir())
	require.NoError(t, err)

	b := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-1", Epoch: 1, Generation: "gen-1"}
	require.NoError(t, db.BindEmbeddedSession(ctx, b, "root"))
	token, err := db.AcquireEmbeddedOwner(ctx, b, "test-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token

	p := &embeddedPersistence{
		db:         db,
		binding:    b,
		digest:     "genesis",
		toolInputs: make(map[string]any),
	}

	cumulativeThinking := "Analyzing codebase to plan multi-step refactoring operations."

	// 1. User message arrives
	require.NoError(t, p.AppendMessage(ctx, ai.UserMessage{
		Content: ai.ContentList{ai.TextContent{Text: "Perform multi-step task"}},
	}))

	// 2. Assistant Step 1: Outputs thinking and Tool Call 1
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: cumulativeThinking},
		providers.FauxToolCall("bash", map[string]any{"command": "echo step1"}, "call-1"),
	}, ai.StopToolUse)))

	// 3. Tool Result 1 returns
	require.NoError(t, p.AppendMessage(ctx, ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "bash",
		Content:    ai.ContentList{ai.TextContent{Text: "step1"}},
	}))

	// 4. Assistant Step 2: Outputs IDENTICAL cumulative thinking and Tool Call 2
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: cumulativeThinking},
		providers.FauxToolCall("bash", map[string]any{"command": "echo step2"}, "call-2"),
	}, ai.StopToolUse)))

	// 5. Tool Result 2 returns
	require.NoError(t, p.AppendMessage(ctx, ai.ToolResultMessage{
		ToolCallID: "call-2",
		ToolName:   "bash",
		Content:    ai.ContentList{ai.TextContent{Text: "step2"}},
	}))

	// 6. Assistant Step 3: Outputs IDENTICAL cumulative thinking and final answer (no tool calls)
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: cumulativeThinking},
		ai.TextContent{Text: "All steps completed successfully."},
	}, ai.StopStop)))

	// Verify only a single managed_thinking row was created in managed_complete_history
	var thinkingCount int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_thinking'`, agentID).Scan(&thinkingCount))
	require.Equal(t, 1, thinkingCount, "multi-step tool turns with identical cumulative thinking must only produce a single managed_thinking record")

	var recordedContent string
	require.NoError(t, raw.QueryRow(`SELECT message_content FROM managed_complete_history WHERE agent_id=? AND category='managed_thinking'`, agentID).Scan(&recordedContent))
	require.Equal(t, cumulativeThinking, recordedContent)

	// 7. A subsequent assistant message in the same turn/session with identical thinking must NOT insert another managed_thinking row
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: cumulativeThinking},
		ai.TextContent{Text: "Acknowledged."},
	}, ai.StopStop)))

	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_thinking'`, agentID).Scan(&thinkingCount))
	require.Equal(t, 1, thinkingCount, "duplicate thinking in same session must not produce additional records")

	// 8. New, distinct thinking must be inserted
	newThinking := "Now analyzing next phase of verification."
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: newThinking},
		ai.TextContent{Text: "Proceeding to next phase."},
	}, ai.StopStop)))

	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_thinking'`, agentID).Scan(&thinkingCount))
	require.Equal(t, 2, thinkingCount, "distinct thinking must produce a new record")
}

package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedThinking_ThinkingOnlyTurnFlags(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "thinking_flags.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()

	agentID := "worker-thinking-flags"
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,'worker-flags',?,'openai-codex','gpt-5','running','running','idle','embedded','session-flags',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, t.TempDir())
	require.NoError(t, err)

	b := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-flags", Epoch: 1, Generation: "gen-1"}
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

	// 1. User message arrives
	require.NoError(t, p.AppendMessage(ctx, ai.UserMessage{
		Content: ai.ContentList{ai.TextContent{Text: "Wake up and do something"}},
	}))

	// 2. Assistant returns ONLY thinking (0 tools, 0 text)
	require.NoError(t, p.AppendMessage(ctx, providers.FauxAssistantMessage(ai.ContentList{
		ai.ThinkingContent{Thinking: "I am contemplating what to do but will emit no text or tool calls."},
	}, ai.StopStop)))

	// Check flags
	require.True(t, p.thinkingGenerated, "thinkingGenerated must be true when thinking was generated")
	require.False(t, p.toolCallReceived, "toolCallReceived must be false when no tools were called")
	require.False(t, p.toolExecuted, "toolExecuted must be false when no tools executed")
}

func TestEmbeddedThinking_NudgeSessionWhenThinkingOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "thinking_nudge.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()

	agentID := "worker-thinking-nudge"
	cwd := t.TempDir()
	_, err = raw.Exec(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at) VALUES(?,'worker-nudge',?,'faux','model-1','running','running','idle','embedded','session-nudge',1,'gen-1',1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, agentID, cwd)
	require.NoError(t, err)

	b := store.EmbeddedBinding{AgentID: agentID, SessionID: "session-nudge", Epoch: 1, Generation: "gen-1"}
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

	var promptLog []string
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	reg.SetResponses([]providers.FauxResponseStep{
		// 1. Initial turn: thinking only, 0 text, 0 tool calls
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for i := len(hist.Messages) - 1; i >= 0; i-- {
				if u, ok := hist.Messages[i].(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok {
							promptLog = append(promptLog, tc.Text)
						}
					}
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ThinkingContent{Thinking: "Just passive thoughts"},
			}, ai.StopStop)
		},
		// 2. Second turn (triggered by continuation prompt): returns text response!
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for i := len(hist.Messages) - 1; i >= 0; i-- {
				if u, ok := hist.Messages[i].(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok {
							promptLog = append(promptLog, tc.Text)
						}
					}
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Now I am taking action and providing a response."},
			}, ai.StopStop)
		},
	})

	model := reg.GetModel()
	stream := func(ctx context.Context, m *ai.Model, t ai.TranscriptContext, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		return ai.StreamSimple(ctx, m, ai.Context{Messages: t.Messages}, o)
	}

	session := coding.NewSession(coding.SessionOptions{
		SessionID:          b.SessionID,
		Model:              model,
		Cwd:                cwd,
		OrderedPersistence: p,
		StreamFn:           stream,
	})

	// Initial user turn produces thinking-only
	_, err = session.Run(ctx, "Hello subagent completed")
	require.NoError(t, err)

	// Verify state before nudge: thinking generated, no tool executed, empty text
	require.True(t, p.thinkingGenerated)
	require.False(t, p.toolExecuted)
	require.Empty(t, strings.TrimSpace(session.LastAssistantText()))

	mgr := &Manager{}
	mgr.nudgeEmbeddedSessionIfNeeded(ctx, session, p)

	// Continuation prompt must have been sent
	require.Len(t, promptLog, 2)
	require.Contains(t, promptLog[1], "You generated internal thinking without taking action or responding. Please execute your next tool call or provide your response to proceed.")
	// And model responded to the continuation prompt
	require.Equal(t, "Now I am taking action and providing a response.", session.LastAssistantText())
}

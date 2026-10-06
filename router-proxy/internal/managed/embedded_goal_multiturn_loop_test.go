package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestEmbeddedGoalMultiTurnContinuationLoop tests a multi-turn goal continuation loop:
// - Turn 1: worker does work, evaluator returns NOT_DONE.
// - Turn 2: continuation turn automatically runs with "Continue working on the current goal.", evaluator returns NOT_DONE.
// - Turn 3: continuation turn automatically runs, evaluator returns DONE.
// - Verify worker completes and settles cleanly.
func TestEmbeddedGoalMultiTurnContinuationLoop(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var ordinary, evaluations, continuations atomic.Int32
	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			n := evaluations.Add(1)
			require.NotNil(t, opts.MaxTokens)
			require.Equal(t, embeddedGoalEvaluationMaxTokens, *opts.MaxTokens)
			switch n {
			case 1:
				// Turn 1 evaluation -> NOT_DONE
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.ThinkingContent{Thinking: "Turn 1 work is incomplete. Still need to finish."},
					ai.TextContent{Text: "NOT_DONE"},
				}, ai.StopStop)
			case 2:
				// Turn 2 evaluation -> NOT_DONE
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.ThinkingContent{Thinking: "Turn 2 made progress, but more work remains."},
					ai.TextContent{Text: "Status: NOT_DONE yet."},
				}, ai.StopStop)
			default:
				// Turn 3 evaluation -> DONE
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.ThinkingContent{Thinking: "All goal requirements have been satisfied."},
					ai.TextContent{Text: "DONE"},
				}, ai.StopStop)
			}
		}

		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ")) {
					continuations.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation work completed"}}, ai.StopStop)
				}
			}
		}

		n := ordinary.Add(1)
		if n == 1 {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "multi-turn loop goal"}, "loop-set"),
			}, ai.StopToolUse)
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial work final"}}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "multiturn-goal-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start multi-turn work",
		RequestID:          "c5555555-5555-4555-8555-555555555591",
	})
	require.NoError(t, err)

	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()

	// Wait for Turn 1 to complete and first evaluation to commit NOT_DONE
	require.Eventually(t, func() bool {
		return evaluations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for Turn 2 (first continuation turn) to run automatically
	require.Eventually(t, func() bool {
		return continuations.Load() >= 1 && evaluations.Load() >= 2
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for Turn 3 (second continuation turn) to run automatically and evaluation to return DONE
	require.Eventually(t, func() bool {
		return continuations.Load() >= 2 && evaluations.Load() >= 3
	}, 5*time.Second, 10*time.Millisecond)

	// Verify worker settles cleanly to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	// Verify the final evaluation state is DONE in durable store
	var finalEvalState string
	require.NoError(t, read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY completed_at DESC LIMIT 1`, launched.Agent.AgentID).Scan(&finalEvalState))
	require.Equal(t, store.EmbeddedGoalEvaluationDone, finalEvalState)

	// Verify evaluations count in store
	var evalCount int
	require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND completed_at IS NOT NULL`, launched.Agent.AgentID).Scan(&evalCount))
	require.Equal(t, 3, evalCount)
}

// TestEmbeddedGoalThinkingBlockVerdictParsing tests that Gemini-style thinking blocks
// (ThinkingContent + TextContent) and lenient verdict formats parse reliably.
func TestEmbeddedGoalThinkingBlockVerdictParsing(t *testing.T) {
	tests := []struct {
		name        string
		content     ai.ContentList
		wantVerdict string
		wantErr     bool
	}{
		{
			name: "thinking followed by clean DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "The worker accomplished all tasks. Evaluating outcome..."},
				ai.TextContent{Text: "DONE"},
			},
			wantVerdict: store.EmbeddedGoalEvaluationDone,
		},
		{
			name: "thinking followed by clean NOT_DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Tasks are incomplete. Subagents still in flight."},
				ai.TextContent{Text: "NOT_DONE"},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
		},
		{
			name: "thinking followed by verbose sentence containing NOT_DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Analyzing logs..."},
				ai.TextContent{Text: "The active goal is currently NOT_DONE."},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
		},
		{
			name: "thinking followed by verbose sentence containing DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Everything looks finished."},
				ai.TextContent{Text: "The goal evaluation verdict is DONE."},
			},
			wantVerdict: store.EmbeddedGoalEvaluationDone,
		},
		{
			name: "whitespace padded verdict",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Thinking..."},
				ai.TextContent{Text: "  \n  DONE  \n\t "},
			},
			wantVerdict: store.EmbeddedGoalEvaluationDone,
		},
		{
			name: "lowercase not_done",
			content: ai.ContentList{
				ai.TextContent{Text: "not_done"},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
		},
		{
			name: "lowercase done",
			content: ai.ContentList{
				ai.TextContent{Text: "done"},
			},
			wantVerdict: store.EmbeddedGoalEvaluationDone,
		},
		{
			name: "multiple thinking blocks before text",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Step 1 thinking..."},
				ai.ThinkingContent{Thinking: "Step 2 thinking..."},
				ai.TextContent{Text: "NOT_DONE"},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
		},
		{
			name: "no text content produces error",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Only thinking, no text output."},
			},
			wantErr: true,
		},
		{
			name: "unparseable text produces error",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Thinking..."},
				ai.TextContent{Text: "I am undecided and cannot tell."},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()

			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()

			var ordinary atomic.Int32
			respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if embeddedIsGoalEvaluation(h) {
					return providers.FauxAssistantMessage(tc.content, ai.StopStop)
				}
				for _, msg := range h.Messages {
					if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
						if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ")) {
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation final"}}, ai.StopStop)
						}
					}
				}
				n := ordinary.Add(1)
				if n == 1 {
					return providers.FauxAssistantMessage(ai.ContentList{
						providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "test parsing goal"}, "parse-set"),
					}, ai.StopToolUse)
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial work final"}}, ai.StopStop)
			}
			steps := make([]providers.FauxResponseStep, 10)
			for i := range steps {
				steps[i] = respond
			}
			reg.SetResponses(steps)

			m := productionPolicyManager(t, db, root, reg)
			defer m.Close(ctx)

			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
				Name:               "thinking-parse-agent",
				Cwd:                root,
				Runtime:            "embedded",
				Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
				InitialInstruction: "set goal to test thinking parsing",
				RequestID:          "d5555555-5555-4555-8555-555555555592",
			})
			require.NoError(t, err)

			read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer read.Close()

			if tc.wantErr {
				// Evaluation fails or remains UNKNOWN
				require.Eventually(t, func() bool {
					var state string
					err := read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&state)
					return err == nil && (state == store.EmbeddedGoalEvaluationUnknown || state == "")
				}, 4*time.Second, 10*time.Millisecond)
			} else {
				require.Eventually(t, func() bool {
					var state string
					err := read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&state)
					return err == nil && state == tc.wantVerdict
				}, 4*time.Second, 10*time.Millisecond)
			}
		})
	}
}

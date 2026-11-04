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
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
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
			name: "no text content falls back to NOT_DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Only thinking, no text output."},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
		},
		{
			name: "unparseable text falls back to NOT_DONE",
			content: ai.ContentList{
				ai.ThinkingContent{Thinking: "Thinking..."},
				ai.TextContent{Text: "I am undecided and cannot tell."},
			},
			wantVerdict: store.EmbeddedGoalEvaluationNotDone,
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
						if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
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

// TestEmbeddedGoalAutonomousLoopWithToolExecutions tests that a multi-step turn
// where the assistant executes a tool (bash) and concludes with an empty message
// (content: [] and stopReason: "stop", the exact Gemini pattern from workflows)
// does NOT trip the empty-turn circuit breaker, completes goal evaluation with NOT_DONE,
// and automatically dispatches continuation for the next turn.
func TestEmbeddedGoalAutonomousLoopWithToolExecutions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var evaluations, continuations atomic.Int32
	var turnStep atomic.Int32

	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			evaluations.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ThinkingContent{Thinking: "Tool execution happened, but goal is not yet finished."},
				ai.TextContent{Text: "NOT_DONE"},
			}, ai.StopStop)
		}

		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
					continuations.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation step executed"}}, ai.StopStop)
				}
			}
		}

		step := turnStep.Add(1)
		switch step {
		case 1:
			// Turn 1 step 1: set standing goal
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "autonomous tool execution goal"}, "loop-goal-set"),
			}, ai.StopToolUse)
		case 2:
			// Turn 1 step 2: call bash tool
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo 'workflow tool execution'"}, "bash-call-1"),
			}, ai.StopToolUse)
		case 3:
			// Turn 1 step 3: concluding assistant message with empty content and stopReason: "stop" (Gemini pattern)
			return providers.FauxAssistantMessage(ai.ContentList{}, ai.StopStop)
		default:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "fallback final"}}, ai.StopStop)
		}
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "autonomous-tools-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start autonomous loop with tools",
		RequestID:          "e5555555-5555-4555-8555-555555555593",
	})
	require.NoError(t, err)

	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()

	// Wait for Turn 1 to complete: circuit breaker must NOT trip, and goal evaluation must complete with NOT_DONE
	require.Eventually(t, func() bool {
		return evaluations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Verify that goal evaluation completed with NOT_DONE in durable store
	var evalState string
	require.NoError(t, read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY completed_at DESC LIMIT 1`, launched.Agent.AgentID).Scan(&evalState))
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, evalState)

	// Verify that continuation automatically dispatches the next turn
	require.Eventually(t, func() bool {
		return continuations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)
}

// TestEmbeddedGoalReNudgesOnSingleEmptyResponse tests that when an assistant emits
// an empty response on turn 1 with a standing goal:
// - Turn 2 receives the actionable re-nudge prompt instead of the continuation loop dying.
// - Turn 2 executes tools and completes.
// - The empty counter resets and the goal continues to completion.
func TestEmbeddedGoalReNudgesOnSingleEmptyResponse(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var evaluations atomic.Int32
	var turnStep atomic.Int32
	var receivedReNudge atomic.Bool
	var promptText atomic.Value

	expectedGoal := "test empty response re-nudge goal"
	expectedActionablePrefix := "You emitted an empty response without taking action. You must take concrete action now on this goal: " + expectedGoal + ". Execute a bash command, read a file, or edit code to make progress."

	respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		if embeddedIsGoalEvaluation(h) {
			evaluations.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ThinkingContent{Thinking: "Tool execution happened, goal is now complete."},
				ai.TextContent{Text: "DONE"},
			}, ai.StopStop)
		}

		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok {
					if strings.Contains(text.Text, "You emitted an empty response without taking action.") {
						receivedReNudge.Store(true)
						promptText.Store(text.Text)
					}
				}
			}
		}

		step := turnStep.Add(1)
		switch step {
		case 1:
			// Turn 1 step 1: set standing goal
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": expectedGoal}, "renudge-goal-set"),
			}, ai.StopToolUse)
		case 2:
			// Turn 1 step 2: empty assistant response (0 tools, 0 text)
			return providers.FauxAssistantMessage(ai.ContentList{}, ai.StopStop)
		case 3:
			// Turn 2 step 1: re-nudged turn executes bash tool
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo 'making progress on goal'"}, "bash-call-1"),
			}, ai.StopToolUse)
		case 4:
			// Turn 2 step 2: concludes with text
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Turn 2 completed actionable tool work."},
			}, ai.StopStop)
		default:
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "fallback final"}}, ai.StopStop)
		}
	}

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = respond
	}
	reg.SetResponses(steps)

	m := productionPolicyManager(t, db, root, reg)
	defer m.Close(ctx)

	launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
		Name:               "renudge-empty-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start work on empty response re-nudge",
		RequestID:          "f5555555-5555-4555-8555-555555555594",
	})
	require.NoError(t, err)

	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()

	// Wait for Turn 2 to receive the actionable re-nudge prompt
	require.Eventually(t, func() bool {
		return receivedReNudge.Load()
	}, 5*time.Second, 10*time.Millisecond, "turn 2 must receive actionable re-nudge prompt")

	// Verify the re-nudge prompt contains the expected actionable directive
	actualPrompt, _ := promptText.Load().(string)
	require.Contains(t, actualPrompt, expectedActionablePrefix)

	// Wait for Turn 2 to complete tool execution and goal evaluation to return DONE
	require.Eventually(t, func() bool {
		return evaluations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Verify the final evaluation state is DONE in durable store
	var finalEvalState string
	require.NoError(t, read.QueryRow(`SELECT state FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY completed_at DESC LIMIT 1`, launched.Agent.AgentID).Scan(&finalEvalState))
	require.Equal(t, store.EmbeddedGoalEvaluationDone, finalEvalState)

	// Verify the lane empty counter was reset to 0 after tool execution
	lane := m.laneFor(launched.Agent.AgentID)
	require.Zero(t, lane.consecutiveEmptyTurns.Load(), "consecutiveEmptyTurns must be reset to 0 after tool execution")

	// Verify agent settles cleanly to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)
}

// TestEmbeddedGoalEvaluatorErrorFallsBackToNotDoneAndContinues tests that:
//   - Turn 1: Evaluator returns an error (ai.StopError). The system does NOT stall in UNKNOWN;
//     it logs the error, falls back to NOT_DONE, and dispatches a continuation turn.
//   - Turn 2: Evaluator returns malformed/unparseable text. The system fails open to NOT_DONE
//     and dispatches another continuation turn.
//   - Turn 3: Evaluator returns DONE. The agent completes and settles cleanly.
//   - Durable assertions: No rows remain in UNKNOWN, fallback rows are NOT_DONE, and continuations ran.
func TestEmbeddedGoalEvaluatorErrorFallsBackToNotDoneAndContinues(t *testing.T) {
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
			switch n {
			case 1:
				// Turn 1 evaluation fails with StopError (simulating model/stream error)
				return providers.FauxAssistantMessage(nil, ai.StopError)
			case 2:
				// Turn 2 evaluation returns unparseable/malformed text (simulating hallucinated/broken output)
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "I am unable to determine completion status from the transcript."},
				}, ai.StopStop)
			default:
				// Turn 3 evaluation completes with DONE
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "DONE"},
				}, ai.StopStop)
			}
		}

		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
				if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
					continuations.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation progress made"}}, ai.StopStop)
				}
			}
		}

		n := ordinary.Add(1)
		if n == 1 {
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "resilient evaluator goal"}, "resilient-set"),
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
		Name:               "resilient-evaluator-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start work on resilient goal",
		RequestID:          "e7777777-7777-4777-8777-777777777797",
	})
	require.NoError(t, err)

	read, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	require.NoError(t, err)
	defer read.Close()

	// Wait for Turn 1 to complete, evaluator error to occur, fall back to NOT_DONE, and Turn 2 continuation to run
	require.Eventually(t, func() bool {
		return continuations.Load() >= 1 && evaluations.Load() >= 1
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for Turn 2 to complete, unparseable output to fall back to NOT_DONE, and Turn 3 continuation to run
	require.Eventually(t, func() bool {
		return continuations.Load() >= 2 && evaluations.Load() >= 2
	}, 5*time.Second, 10*time.Millisecond)

	// Wait for Turn 3 to complete and final evaluation to return DONE
	require.Eventually(t, func() bool {
		return evaluations.Load() >= 3
	}, 5*time.Second, 10*time.Millisecond)

	// Verify worker settles cleanly to idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && agent.LifecycleState == "running"
	}, 5*time.Second, 10*time.Millisecond)

	// Assert that NO evaluation row stalled in UNKNOWN
	var unknownCount int
	require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND state='UNKNOWN'`, launched.Agent.AgentID).Scan(&unknownCount))
	require.Equal(t, 0, unknownCount, "no evaluation must stall in UNKNOWN")

	// Verify all 3 evaluations completed
	var completedCount int
	require.NoError(t, read.QueryRow(`SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND completed_at IS NOT NULL`, launched.Agent.AgentID).Scan(&completedCount))
	require.Equal(t, 3, completedCount)

	// Verify specific states and reasons
	type evalRow struct {
		state  string
		reason string
	}
	rows, err := read.Query(`SELECT state, reason FROM embedded_goal_evaluations WHERE agent_id=? ORDER BY user_seq ASC`, launched.Agent.AgentID)
	require.NoError(t, err)
	defer rows.Close()

	var evals []evalRow
	for rows.Next() {
		var r evalRow
		require.NoError(t, rows.Scan(&r.state, &r.reason))
		evals = append(evals, r)
	}
	require.Len(t, evals, 3)

	// Turn 1 evaluation: fell back from eval error
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, evals[0].state)
	require.Contains(t, evals[0].reason, "Evaluator inconclusive:")

	// Turn 2 evaluation: fell back from unparseable text
	require.Equal(t, store.EmbeddedGoalEvaluationNotDone, evals[1].state)
	require.Equal(t, "Inconclusive evaluator output; continuing progress on goal", evals[1].reason)

	// Turn 3 evaluation: completed normally with DONE
	require.Equal(t, store.EmbeddedGoalEvaluationDone, evals[2].state)
}

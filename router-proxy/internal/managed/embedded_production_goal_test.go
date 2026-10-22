package managed

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedGoalEvaluatorRawOutputAndPresentation verifies that when a goal evaluation
// produces raw unparseable output or an inconclusive evaluation, managed_complete_history
// stores the raw evaluator output in message_content and the concise status in presentation.summary.
func TestEmbeddedGoalEvaluatorRawOutputAndPresentation(t *testing.T) {
	t.Run("unparseable output", func(t *testing.T) {
		ctx := context.Background()
		root := t.TempDir()
		path := filepath.Join(root, "router.db")
		db, err := store.Open(ctx, path, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		defer reg.Unregister()

		rawUnparseableOutput := "Here is my evaluation analysis:\nThe worker has performed some tasks, but I cannot determine whether it is completely finished.\nNo clear VERDICT is provided."

		var evaluations atomic.Int32
		var turnStep atomic.Int32

		respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			if embeddedIsGoalEvaluation(h) {
				evaluations.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: rawUnparseableOutput},
				}, ai.StopStop)
			}

			for _, msg := range h.Messages {
				if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
					if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation step"}}, ai.StopStop)
					}
				}
			}

			n := turnStep.Add(1)
			if n == 1 {
				return providers.FauxAssistantMessage(ai.ContentList{
					providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "test raw evaluator output persistence"}, "call-goal-raw"),
				}, ai.StopToolUse)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "turn work done"}}, ai.StopStop)
		}

		steps := make([]providers.FauxResponseStep, 10)
		for i := range steps {
			steps[i] = respond
		}
		reg.SetResponses(steps)

		m := productionPolicyManager(t, db, root, reg)
		defer m.Close(ctx)

		launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
			Name:               "evaluator-raw-agent",
			Cwd:                root,
			Runtime:            "embedded",
			Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			InitialInstruction: "start goal task",
			RequestID:          "e5555555-5555-4555-8555-555555555599",
		})
		require.NoError(t, err)

		// Wait for at least one evaluation to complete
		require.Eventually(t, func() bool {
			return evaluations.Load() >= 1
		}, 5*time.Second, 10*time.Millisecond)

		// Wait for the history entry to be recorded in managed_complete_history
		var historyEntries []store.ManagedHistoryEntry
		require.Eventually(t, func() bool {
			entries, err := db.GetManagedCompleteHistory(ctx, launched.Agent.AgentID)
			if err == nil {
				for _, entry := range entries {
					if entry.Presentation != nil && entry.Presentation.Kind == "goal_evaluation" {
						historyEntries = entries
						return true
					}
				}
			}
			return false
		}, 5*time.Second, 10*time.Millisecond)

		var evalEntry *store.ManagedHistoryEntry
		for i := range historyEntries {
			if historyEntries[i].Presentation != nil && historyEntries[i].Presentation.Kind == "goal_evaluation" {
				evalEntry = &historyEntries[i]
				break
			}
		}
		require.NotNil(t, evalEntry, "expected a managed_goal_evaluation history entry")

		// Verify presentation:
		// - presentation.summary holds the concise status/preview
		// - presentation.companion_text is empty/absent
		require.NotNil(t, evalEntry.Presentation)
		require.Equal(t, "goal_evaluation", evalEntry.Presentation.Kind)
		require.Equal(t, "not_done", evalEntry.Presentation.Outcome)
		require.Equal(t, "Inconclusive evaluator output; continuing progress on goal", evalEntry.Presentation.Summary)
		require.Empty(t, evalEntry.Presentation.CompanionText, "companion_text must be empty to avoid unwanted chat bubble in timeline")

		// Verify message_content holds the raw unparseable evaluator output
		require.NotNil(t, evalEntry.MessageContent, "message_content must not be nil")
		require.Equal(t, rawUnparseableOutput, *evalEntry.MessageContent, "message_content must store the raw unparseable evaluator output")
	})

	t.Run("inconclusive empty evaluation", func(t *testing.T) {
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

		respond := func(h ai.TranscriptContext, opts *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			if embeddedIsGoalEvaluation(h) {
				evaluations.Add(1)
				return providers.FauxAssistantMessage(ai.ContentList{}, ai.StopStop)
			}

			for _, msg := range h.Messages {
				if u, ok := msg.(ai.UserMessage); ok && len(u.Content) == 1 {
					if text, ok := u.Content[0].(ai.TextContent); ok && (text.Text == "Continue working on the current goal." || strings.HasPrefix(text.Text, "NOT_DONE: Continue ") || strings.HasPrefix(text.Text, "NOT_DONE: ")) {
						return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "continuation step"}}, ai.StopStop)
					}
				}
			}

			n := turnStep.Add(1)
			if n == 1 {
				return providers.FauxAssistantMessage(ai.ContentList{
					providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "test empty evaluator output persistence"}, "call-goal-empty"),
				}, ai.StopToolUse)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "turn work done"}}, ai.StopStop)
		}

		steps := make([]providers.FauxResponseStep, 10)
		for i := range steps {
			steps[i] = respond
		}
		reg.SetResponses(steps)

		m := productionPolicyManager(t, db, root, reg)
		defer m.Close(ctx)

		launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{
			Name:               "evaluator-empty-agent",
			Cwd:                root,
			Runtime:            "embedded",
			Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
			InitialInstruction: "start goal task",
			RequestID:          "e5555555-5555-4555-8555-555555555598",
		})
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			return evaluations.Load() >= 1
		}, 5*time.Second, 10*time.Millisecond)

		var historyEntries []store.ManagedHistoryEntry
		require.Eventually(t, func() bool {
			entries, err := db.GetManagedCompleteHistory(ctx, launched.Agent.AgentID)
			if err == nil {
				for _, entry := range entries {
					if entry.Presentation != nil && entry.Presentation.Kind == "goal_evaluation" {
						historyEntries = entries
						return true
					}
				}
			}
			return false
		}, 5*time.Second, 10*time.Millisecond)

		var evalEntry *store.ManagedHistoryEntry
		for i := range historyEntries {
			if historyEntries[i].Presentation != nil && historyEntries[i].Presentation.Kind == "goal_evaluation" {
				evalEntry = &historyEntries[i]
				break
			}
		}
		require.NotNil(t, evalEntry)
		require.Equal(t, "goal_evaluation", evalEntry.Presentation.Kind)
		require.Equal(t, "not_done", evalEntry.Presentation.Outcome)
		require.Equal(t, "Inconclusive evaluator output; continuing progress on goal", evalEntry.Presentation.Summary)
		require.NotNil(t, evalEntry.MessageContent)
		require.Equal(t, "Empty evaluator output", *evalEntry.MessageContent)
	})
}

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

// TestContextThresholdLifecycle runs end-to-end behavioral tests for:
// 1. Warning threshold (10%): Warning reminder is delivered to mailbox, epoch unchanged.
// 2. Fallback Auto-Reset threshold (20%): Auto-reset executed, epoch incremented, wake turn sent, scratchpad notes preserved.
// 3. Per-model threshold override: Custom percentages respected (e.g. 30% warning, 40% reset).
// 4. Zero compaction references in prompts or agent responses.
func subagentTestParentWithModel(t *testing.T, s *store.Store, id string, model store.ManagedModel) store.EmbeddedBinding {
	t.Helper()
	ctx := context.Background()
	runtime := "embedded"
	agent := store.ManagedAgentRecord{
		AgentID: id, Name: id, SlotHeld: true, Cwd: t.TempDir(),
		SelectedModel: model,
		Runtime:       runtime, DesiredState: "running", PiSessionID: id + "-session", SessionEpoch: 1,
		ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64),
	}
	op := store.ManagedOperation{
		OperationID: id + "-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: id + "-launch", CanonicalBodyHash: strings.Repeat("a", 64), ResponseStatus: 202,
		ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, id, agent.PiSessionID, store.ProcessIdentityRecord{PID: 123, StartMarker: "start", Incarnation: "inc"}, "generation-1"))
	require.NoError(t, s.MarkManagedReady(ctx, id))
	b := store.EmbeddedBinding{AgentID: id, SessionID: agent.PiSessionID, Epoch: 1, Generation: "generation-1"}
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "worker"))
	token, err := s.AcquireEmbeddedOwner(ctx, b, "fixture:"+b.AgentID, time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	return b
}

func TestContextThresholdLifecycle(t *testing.T) {
	ctx := context.Background()

	t.Run("Test1_WarningThresholdDelivery", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_warning_threshold.db")
		s, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer s.Close()

		// Configure global thresholds: 10% warning, 20% reset
		require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{
			WarningPercent:  10,
			ResetPercent:    20,
			ModelThresholds: map[string]store.ModelContextThreshold{},
		}))

		const window = 100_000 // 10% warning = 10,000; 20% reset = 20,000
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "warning-lifecycle-model",
				ContextWindow: window,
				MaxTokens:     4096,
				Input:         []string{"text"},
			}},
		})
		defer reg.Unregister()

		var reminderReceived atomic.Bool
		var reminderBody atomic.Value
		steps := make([]providers.FauxResponseStep, 20)
		for i := range steps {
			steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if len(h.Messages) > 0 {
					lastMsg := h.Messages[len(h.Messages)-1]
					if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
						if txt, ok := u.Content[0].(ai.TextContent); ok {
							if strings.Contains(txt.Text, "Context threshold reached:") || strings.Contains(txt.Text, "Context usage") || strings.Contains(txt.Text, "warning threshold") {
								reminderReceived.Store(true)
								reminderBody.Store(txt.Text)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, s, reg, 4)
		defer mgr.Close(ctx)

		agentID := "warn-worker"
		parent := subagentTestParent(t, s, agentID)
		registerParentOwner(mgr, parent)

		savedAgent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + agentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Generate ~12,000 tokens (between 10% = 10,000 and 20% = 20,000)
		prompt := strings.Repeat("data ", 9600)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt)
		require.NoError(t, err)

		// 1. Verify warning reminder was delivered
		require.Eventually(t, func() bool {
			return reminderReceived.Load()
		}, 3*time.Second, 20*time.Millisecond, "warning reminder should be delivered when crossing 10% threshold")

		bodyStr, _ := reminderBody.Load().(string)
		require.Contains(t, bodyStr, "scratchpad", "Reminder should instruct agent to use scratchpad")
		require.Contains(t, bodyStr, "context_reset", "Reminder should instruct agent to call context_reset")
		require.NotContains(t, strings.ToLower(bodyStr), "compaction", "Reminder must not mention compaction")

		// 2. Verify context was NOT reset: epoch remains 1
		currentAgent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		require.Equal(t, int64(1), currentAgent.SessionEpoch, "Session epoch must remain 1 after warning")
	})

	t.Run("Test2_FallbackAutoResetExecution", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_auto_reset.db")
		s, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer s.Close()

		require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{
			WarningPercent:  10,
			ResetPercent:    20,
			ModelThresholds: map[string]store.ModelContextThreshold{},
		}))

		const window = 100_000 // 20% reset = 20,000 tokens
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "reset-lifecycle-model",
				ContextWindow: window,
				MaxTokens:     4096,
				Input:         []string{"text"},
			}},
		})
		defer reg.Unregister()

		var wakePromptReceived atomic.Bool
		steps := make([]providers.FauxResponseStep, 20)
		for i := range steps {
			steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if len(h.Messages) > 0 {
					lastMsg := h.Messages[len(h.Messages)-1]
					if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
						if txt, ok := u.Content[0].(ai.TextContent); ok {
							if strings.Contains(txt.Text, ContextResetWakePrompt) || strings.Contains(txt.Text, "Context has been reset") {
								wakePromptReceived.Store(true)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, s, reg, 4)
		defer mgr.Close(ctx)

		agentID := "reset-worker"
		parent := subagentTestParent(t, s, agentID)
		registerParentOwner(mgr, parent)

		// Record a scratchpad note before turn
		_, err = s.AppendScratchpadEntry(ctx, agentID, "Important progress note that must survive reset", false)
		require.NoError(t, err)

		savedAgent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + agentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Generate ~25,000 tokens (> 20,000 tokens = 20%) -> triggers automatic fallback context reset
		prompt := strings.Repeat("data ", 20000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt)
		require.NoError(t, err)

		// 1. Verify session epoch incremented from 1 to 2
		require.Eventually(t, func() bool {
			a, err := s.ManagedAgent(ctx, agentID)
			return err == nil && a.SessionEpoch == 2
		}, 3*time.Second, 20*time.Millisecond, "Session epoch should advance to 2 on auto-reset")

		// 2. Verify wake turn prompt was received
		require.Eventually(t, func() bool {
			return wakePromptReceived.Load()
		}, 3*time.Second, 20*time.Millisecond, "Wake prompt should be received in epoch 2")

		// 3. Verify scratchpad note is preserved in epoch 2
		scratchpadEntries, total, err := s.FetchScratchpadRecent(ctx, agentID, 10)
		require.NoError(t, err)
		require.GreaterOrEqual(t, total, 1)
		require.Equal(t, "Important progress note that must survive reset", scratchpadEntries[0].Content)
	})

	t.Run("Test3_PerModelThresholdOverride", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_model_override.db")
		s, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer s.Close()

		// Global: 10% warn, 20% reset
		// Model Override: 30% warn, 40% reset for "antigravity/custom-override-model"
		require.NoError(t, s.SetContextThresholdConfig(ctx, store.ContextThresholdConfig{
			WarningPercent: 10,
			ResetPercent:   20,
			ModelThresholds: map[string]store.ModelContextThreshold{
				"antigravity/custom-override-model": {
					WarningPercent: 30,
					ResetPercent:   40,
				},
			},
		}))

		const window = 100_000 // 30% warn = 30,000 tokens, 40% reset = 40,000 tokens
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "custom-override-model",
				ContextWindow: window,
				MaxTokens:     4096,
				Input:         []string{"text"},
			}},
		})
		defer reg.Unregister()

		var reminderReceived atomic.Bool
		steps := make([]providers.FauxResponseStep, 20)
		for i := range steps {
			steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if len(h.Messages) > 0 {
					lastMsg := h.Messages[len(h.Messages)-1]
					if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
						if txt, ok := u.Content[0].(ai.TextContent); ok {
							if strings.Contains(txt.Text, "Context usage warning:") || strings.Contains(txt.Text, "Context threshold reached:") || strings.Contains(txt.Text, "Context usage threshold warning") {
								reminderReceived.Store(true)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, s, reg, 4)
		defer mgr.Close(ctx)

		agentID := "override-worker"
		parent := subagentTestParentWithModel(t, s, agentID, store.ManagedModel{
			Provider: "antigravity",
			ID:       "custom-override-model",
		})
		registerParentOwner(mgr, parent)

		savedAgent, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + agentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Step 3a: Turn 1 with 15% tokens (~12,000 words * 1.25 = ~15,000 tokens).
		// Under global (10%), this would trigger warning.
		// Under override (30%), 15% should NOT trigger warning.
		prompt1 := strings.Repeat("data ", 12000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt1)
		require.NoError(t, err)

		// Wait briefly and verify NO reminder was received
		time.Sleep(100 * time.Millisecond)
		require.False(t, reminderReceived.Load(), "15% should not trigger warning reminder under 30% override")

		// Step 3b: Turn 2 crossing 30% warning (cumulative tokens reaching ~28,000-32,000 tokens < 40,000).
		// Should trigger warning reminder, but NOT auto-reset (reset is 40%).
		prompt2 := strings.Repeat("more ", 10000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt2)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			return reminderReceived.Load()
		}, 3*time.Second, 20*time.Millisecond, "Crossing 30% must trigger warning reminder")

		agentAfterTurn2, err := s.ManagedAgent(ctx, agentID)
		require.NoError(t, err)
		require.Equal(t, int64(1), agentAfterTurn2.SessionEpoch, "Epoch must remain 1 below 40% reset threshold")

		// Step 3c: Turn 3 crossing 40% reset (cumulative tokens reaching ~45,000 tokens).
		// Should trigger auto-reset: epoch increments to 2.
		prompt3 := strings.Repeat("extra ", 10000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt3)
		require.NoError(t, err)

		require.Eventually(t, func() bool {
			a, err := s.ManagedAgent(ctx, agentID)
			return err == nil && a.SessionEpoch == 2
		}, 3*time.Second, 20*time.Millisecond, "Crossing 40% reset threshold must trigger auto-reset and epoch increment to 2")
	})

	t.Run("Test4_ZeroCompactionReferencesInWorkerPromptsAndResponses", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_zero_compaction.db")
		s, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer s.Close()

		mgr := makeSubagentTestManager(t, s, providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{}), 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, s, "zero-worker")
		registerParentOwner(mgr, parent)

		owner := embeddedOwner{binding: parent, identity: "fixture:zero-worker"}

		// Inspect mecha_context tool description and execution guidance
		tool := mgr.embeddedCompactionTool(owner)
		require.NotContains(t, strings.ToLower(tool.Description), "compaction", "mecha_context tool description must not mention compaction")

		res, err := tool.Execute(ctx, "call_1", map[string]any{"action": "status"}, nil)
		require.NoError(t, err)
		toolText := res.Content[0].(ai.TextContent).Text
		require.NotContains(t, strings.ToLower(toolText), "compaction", "mecha_context output must not mention compaction")

		// Check warning reminder wording
		reminderText := FormatContextResetWarningReminder(1500, 10000, 10, 20)
		require.NotContains(t, strings.ToLower(reminderText), "compaction", "Warning reminder must not mention compaction")

		// Check wake turn prompt wording
		require.NotContains(t, strings.ToLower(ContextResetWakePrompt), "compaction", "Wake turn prompt must not mention compaction")
	})
}

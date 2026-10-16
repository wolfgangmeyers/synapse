package managed

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestContextThresholdExceeded tests auto-reset threshold calculation:
// - 50% of context window for 1M+ models
// - 80% of context window for <1M models
func TestContextThresholdExceeded(t *testing.T) {
	// Models with >= 1,000,000 context window (threshold = 50%)
	model1M := Model{
		Provider:      "google",
		ID:            "gemini-2.5-flash",
		ContextWindow: 1_000_000,
	}
	require.False(t, contextThresholdExceeded(model1M, 499_999), "49.9999%% should not exceed 50%% threshold")
	require.True(t, contextThresholdExceeded(model1M, 500_000), "exact 50%% should exceed threshold")
	require.True(t, contextThresholdExceeded(model1M, 600_000), "60%% should exceed 50%% threshold")
	require.True(t, ContextThresholdExceeded(model1M, 500_000), "exported ContextThresholdExceeded should match")

	model2M := Model{
		Provider:      "google",
		ID:            "gemini-2.5-pro",
		ContextWindow: 2_000_000,
	}
	require.False(t, contextThresholdExceeded(model2M, 999_999), "< 50%% should not exceed")
	require.True(t, contextThresholdExceeded(model2M, 1_000_000), "1M / 2M = 50%% should exceed")
	require.True(t, contextThresholdExceeded(model2M, 1_500_000), "75%% should exceed 50%% threshold")

	// Models with < 1,000,000 context window (threshold = 80%)
	model200k := Model{
		Provider:      "anthropic",
		ID:            "claude-3-7-sonnet",
		ContextWindow: 200_000,
	}
	require.False(t, contextThresholdExceeded(model200k, 159_999), "79.99%% should not exceed 80%% threshold")
	require.True(t, contextThresholdExceeded(model200k, 160_000), "exact 80%% should exceed threshold")
	require.True(t, contextThresholdExceeded(model200k, 180_000), "90%% should exceed 80%% threshold")

	model128k := Model{
		Provider:      "openai",
		ID:            "gpt-4o",
		ContextWindow: 128_000,
	}
	require.False(t, contextThresholdExceeded(model128k, 102_399), "< 80%% should not exceed")
	require.True(t, contextThresholdExceeded(model128k, 102_400), "exact 80%% (102400) should exceed")
	require.True(t, contextThresholdExceeded(model128k, 110_000), "> 80%% should exceed")

	// Edge case: ContextWindow <= 0
	modelZero := Model{Provider: "test", ID: "zero", ContextWindow: 0}
	require.False(t, contextThresholdExceeded(modelZero, 100))
	modelNegative := Model{Provider: "test", ID: "negative", ContextWindow: -100}
	require.False(t, contextThresholdExceeded(modelNegative, 100))
}

// TestContextResetPortsLastFewMessages verifies that executing context reset:
// 1. Starts a new epoch in embedded_sessions (epoch 2)
// 2. Marks the old epoch current=0
// 3. Ports the last 4-6 messages from epoch 1 (no synthetic genesis prompt added when PendingInitialInstruction is empty)
// 4. Appends the wake prompt ContextResetWakePrompt
// 5. Executes the assistant wake turn response
func TestContextResetPortsLastFewMessages(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	var turnCount atomic.Int32
	var lastReceivedUserPrompt atomic.Value

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "port-msg-test",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turn := turnCount.Add(1)
			if len(h.Messages) > 0 {
				lastMsg := h.Messages[len(h.Messages)-1]
				if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
					if txt, ok := u.Content[0].(ai.TextContent); ok {
						lastReceivedUserPrompt.Store(txt.Text)
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: fmt.Sprintf("Response for turn %d", turn)}}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	parent := subagentTestParent(t, db, "port-agent")
	registerParentOwner(mgr, parent)

	savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)

	owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	// Run 4 turns in epoch 1 to build up history (9 entries total: 1 system + 4 user + 4 assistant)
	for i := 1; i <= 4; i++ {
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, fmt.Sprintf("Turn %d work message", i))
		require.NoError(t, err)
	}

	snap1, err := db.ReopenEmbedded(ctx, parent)
	require.NoError(t, err)
	require.Equal(t, 9, len(snap1.Entries), "epoch 1 should have 9 entries (1 system + 4 user + 4 assistant)")

	// Execute Context Reset explicitly
	err = mgr.ExecuteContextReset(ownerCtx, parent, "test_context_reset")
	require.NoError(t, err)

	// Verify old session epoch is no longer current
	var oldCurrent int
	err = raw.QueryRowContext(ctx, `SELECT current FROM embedded_sessions WHERE agent_id=? AND epoch=1`, parent.AgentID).Scan(&oldCurrent)
	require.NoError(t, err)
	require.Equal(t, 0, oldCurrent, "old session epoch must be marked current=0")

	// Verify new session epoch (epoch 2) exists and is current
	var newEpoch int64
	var newSessionID string
	err = raw.QueryRowContext(ctx, `SELECT epoch, session_id FROM embedded_sessions WHERE agent_id=? AND current=1`, parent.AgentID).Scan(&newEpoch, &newSessionID)
	require.NoError(t, err)
	require.Equal(t, int64(2), newEpoch, "new session epoch must be 2")

	// Verify managed_agents table was updated to epoch 2
	updatedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.Equal(t, int64(2), updatedAgent.SessionEpoch)
	require.Equal(t, newSessionID, updatedAgent.PiSessionID)

	// Inspect the new session snapshot (epoch 2)
	nextBinding := store.EmbeddedBinding{
		AgentID:    parent.AgentID,
		SessionID:  newSessionID,
		Epoch:      2,
		Generation: parent.Generation,
		OwnerToken: 1,
	}
	snap2, err := db.ReopenEmbedded(ctx, nextBinding)
	require.NoError(t, err)

	require.Equal(t, 9, len(snap2.Entries), "epoch 2 should have 6 ported + system + wake user + wake assistant = 9 entries")

	// Verify the ported messages match the tail of epoch 1 (entries 3..8 of snap1 correspond to entries 0..5 of snap2)
	for i := 0; i < 6; i++ {
		origMsg, err := ai.UnmarshalMessage(snap1.Entries[3+i].Body)
		require.NoError(t, err)
		portedMsg, err := ai.UnmarshalMessage(snap2.Entries[0+i].Body)
		require.NoError(t, err)
		require.Equal(t, origMsg.MessageRole(), portedMsg.MessageRole(), "ported message role must match original")
	}

	// Verify Entry 7 is the system prompt injected for the new session
	sysMsg, err := ai.UnmarshalMessage(snap2.Entries[6].Body)
	require.NoError(t, err)
	require.Equal(t, ai.RoleSystem, sysMsg.MessageRole())

	// Verify Entry 8 is the post-reset wake prompt
	wakeMsg, err := ai.UnmarshalMessage(snap2.Entries[7].Body)
	require.NoError(t, err)
	require.Equal(t, ai.RoleUser, wakeMsg.MessageRole())
	require.Equal(t, ContextResetWakePrompt, messageContentText(wakeMsg))

	// Verify Entry 9 is the assistant response
	wakeResp, err := ai.UnmarshalMessage(snap2.Entries[8].Body)
	require.NoError(t, err)
	require.Equal(t, ai.RoleAssistant, wakeResp.MessageRole())

	// Verify that the assistant actually received the wake prompt during the wake turn
	require.Equal(t, ContextResetWakePrompt, lastReceivedUserPrompt.Load())
}

// TestPostResetAutoWakeContinuesWorkerLoop verifies that after context reset,
// the agent wake turn executes cleanly and continues the worker loop for subsequent turns.
func TestPostResetAutoWakeContinuesWorkerLoop(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	var wakePromptReceived atomic.Bool
	var followUpReceived atomic.Bool

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "loop-continue-test",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	loopSteps := make([]providers.FauxResponseStep, 20)
	for i := range loopSteps {
		loopSteps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			if len(h.Messages) > 0 {
				lastMsg := h.Messages[len(h.Messages)-1]
				if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
					if txt, ok := u.Content[0].(ai.TextContent); ok {
						if txt.Text == ContextResetWakePrompt {
							wakePromptReceived.Store(true)
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Acknowledged wake prompt, continuing."}}, ai.StopStop)
						}
						if strings.Contains(txt.Text, "follow-up work") {
							followUpReceived.Store(true)
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Follow-up completed."}}, ai.StopStop)
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Initial response."}}, ai.StopStop)
		}
	}
	reg.SetResponses(loopSteps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	parent := subagentTestParent(t, db, "loop-agent")
	registerParentOwner(mgr, parent)

	savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)

	owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	// Turn 1
	err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Initial task")
	require.NoError(t, err)

	// Execute context reset
	err = mgr.ExecuteContextReset(ownerCtx, parent, "test_loop_continue")
	require.NoError(t, err)

	require.True(t, wakePromptReceived.Load(), "wake turn must execute during ExecuteContextReset")

	// Verify agent lifecycle state is running and idle (ready for next work)
	curAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", curAgent.LifecycleState)
	require.Equal(t, "idle", curAgent.ActivityState)

	// Continue the worker loop: send another turn to the reset session
	mgr.mu.Lock()
	newOwner := mgr.embeddedOwners[parent.AgentID]
	mgr.mu.Unlock()

	newOwnerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, newOwner)
	err = mgr.runEmbeddedSessionPrompt(newOwnerCtx, *curAgent, "Perform follow-up work")
	require.NoError(t, err)
	require.True(t, followUpReceived.Load(), "worker loop must accept follow-up turns in the new session")
}

// TestPostResetMailDispatchAfterLeaseExpiry verifies that after context reset,
// if the owner lease expires while the agent is idle, inbound mail can still be admitted
// and dispatched successfully rather than being stranded in the queued state.
func TestPostResetMailDispatchAfterLeaseExpiry(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 7, 20, 30, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()

	var mailTurnReceived atomic.Bool

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "mail-expiry-test",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	loopSteps := make([]providers.FauxResponseStep, 20)
	for i := range loopSteps {
		loopSteps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			if len(h.Messages) > 0 {
				lastMsg := h.Messages[len(h.Messages)-1]
				if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
					if txt, ok := u.Content[0].(ai.TextContent); ok {
						if strings.Contains(txt.Text, "scratchpad tools") {
							mailTurnReceived.Store(true)
							return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Here are the scratchpad tools."}}, ai.StopStop)
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Initial response."}}, ai.StopStop)
		}
	}
	reg.SetResponses(loopSteps)

	mgr := makeSubagentTestManagerWithClock(t, db, reg, 4, clock)
	defer mgr.Close(ctx)

	parent := subagentTestParent(t, db, "reset-mail-agent")
	registerParentOwner(mgr, parent)

	savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)

	owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	// Turn 1
	err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Initial task")
	require.NoError(t, err)

	// Execute context reset (advancing epoch from 1 to 2)
	err = mgr.ExecuteContextReset(ownerCtx, parent, "user_request")
	require.NoError(t, err)

	// Advance clock by 40s (exceeding the 30s embeddedOwnerTTL so the owner lease in SQLite expires)
	require.NoError(t, clock.Advance(40*time.Second))

	// Enqueue a mail message
	msgID := "test-scratchpad-mail-id"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        msgID,
		From:      "wolfgang",
		To:        parent.AgentID,
		Body:      "list out the scratchpad tools",
		CreatedAt: clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, msgID)
	require.NoError(t, err)

	// Dispatch embedded mail
	mgr.dispatchEmbeddedMail(parent.AgentID)

	// Verify the mail turn was executed and message acknowledged
	require.True(t, mailTurnReceived.Load(), "mail turn must execute even if owner lease had expired while idle")
	mail, err := db.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, mail.State)
}

// TestExplicitContextResetToolInvocation verifies that when a worker agent calls
// the context_reset tool, the reset is triggered at the turn boundary.
func TestExplicitContextResetToolInvocation(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	var turnStep atomic.Int32
	var wakePromptReceived atomic.Bool

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "tool-reset-test",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	toolSteps := make([]providers.FauxResponseStep, 20)
	for i := range toolSteps {
		toolSteps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			step := turnStep.Add(1)
			if step == 1 {
				// Turn 1: Worker decides to invoke context_reset tool
				return providers.FauxAssistantMessage(ai.ContentList{
					providers.FauxToolCall("context_reset", map[string]any{
						"reason": "clean_context_checkpoint",
					}, "call-reset-1"),
				}, ai.StopToolUse)
			}
			if step == 2 {
				// Turn 1 tool result continuation: worker acknowledges and stops
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Context reset tool call completed."}}, ai.StopStop)
			}
			// Subsequent turns: check for wake prompt
			if len(h.Messages) > 0 {
				lastMsg := h.Messages[len(h.Messages)-1]
				if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
					if txt, ok := u.Content[0].(ai.TextContent); ok && txt.Text == ContextResetWakePrompt {
						wakePromptReceived.Store(true)
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Post-reset turn response."}}, ai.StopStop)
		}
	}
	reg.SetResponses(toolSteps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	parent := subagentTestParent(t, db, "tool-agent")
	registerParentOwner(mgr, parent)

	savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)

	owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	// Run turn that triggers the context_reset tool call
	err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Start task and check context")
	require.NoError(t, err)

	// Verify context reset executed and wake prompt was received
	require.True(t, wakePromptReceived.Load(), "wake prompt must be received after explicit context_reset tool invocation")

	// Verify agent has advanced to epoch 2
	curAgent, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.Equal(t, int64(2), curAgent.SessionEpoch, "agent must be at session epoch 2")
}

// TestAutoResetAtThreshold verifies auto-reset triggering:
// - At 80% for <1M models
// - At 50% for 1M+ models
func TestAutoResetAtThreshold(t *testing.T) {
	ctx := context.Background()

	t.Run("LessThan1MModelTriggersAt80Percent", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_80.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		const window = 100_000 // < 1M model, 80% threshold = 80,000 tokens
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "threshold-80-test",
				ContextWindow: window,
				MaxTokens:     4096,
				Input:         []string{"text"},
			}},
		})
		defer reg.Unregister()

		var wakeReceived atomic.Bool
		steps := make([]providers.FauxResponseStep, 20)
		for i := range steps {
			steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if len(h.Messages) > 0 {
					lastMsg := h.Messages[len(h.Messages)-1]
					if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
						if txt, ok := u.Content[0].(ai.TextContent); ok {
							if txt.Text == ContextResetWakePrompt {
								wakeReceived.Store(true)
								return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Wake response."}}, ai.StopStop)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Turn response."}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, db, "thresh-80-agent")
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Turn 1: Large initial task analysis (~70,000 tokens = ~280,000 chars)
		// Fits within 100,000 - 16,384 headroom. Does not reach 80% (80,000).
		prompt1 := strings.Repeat("data ", 56000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt1)
		require.NoError(t, err)
		require.False(t, wakeReceived.Load(), "turn 1 at ~70% should not trigger 80%% reset")

		// Wait for any background warning reminder turn to complete before sending next prompt
		require.Eventually(t, func() bool {
			a, e := db.ManagedAgent(ctx, parent.AgentID)
			return e == nil && a.ActivityState == "idle"
		}, 3*time.Second, 10*time.Millisecond)

		// Turns 2-4: Small follow-up turns
		for i := 2; i <= 4; i++ {
			err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, fmt.Sprintf("Follow up step %d", i))
			require.NoError(t, err)
		}
		require.False(t, wakeReceived.Load(), "turns 2-4 should not exceed 80%% threshold yet")

		// Turn 5: Pushes context past 80,000 tokens (e.g. ~11,000 tokens = 44,000 chars)
		// Total context exceeds 80,000 tokens, triggering auto-reset!
		prompt5 := strings.Repeat("more ", 9000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt5)
		require.NoError(t, err)

		// Assert that auto-reset triggered and wake was received
		require.True(t, wakeReceived.Load(), "auto-reset at 80%% threshold must trigger wake turn")

		curAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Equal(t, int64(2), curAgent.SessionEpoch, "agent must be advanced to epoch 2")
	})

	t.Run("1MPlusModelTriggersAt50Percent", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_50.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		const window = 1_000_000 // 1M model, 50% threshold = 500,000 tokens
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "threshold-50-test",
				ContextWindow: window,
				MaxTokens:     4096,
				Input:         []string{"text"},
			}},
		})
		defer reg.Unregister()

		var wakeReceived atomic.Bool
		steps := make([]providers.FauxResponseStep, 20)
		for i := range steps {
			steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				if len(h.Messages) > 0 {
					lastMsg := h.Messages[len(h.Messages)-1]
					if u, ok := lastMsg.(ai.UserMessage); ok && len(u.Content) > 0 {
						if txt, ok := u.Content[0].(ai.TextContent); ok {
							if txt.Text == ContextResetWakePrompt {
								wakeReceived.Store(true)
								return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Wake response."}}, ai.StopStop)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Initial response."}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, db, "thresh-50-agent")
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Initial turn to establish session
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Initial task")
		require.NoError(t, err)

		// Verify 400,000 tokens does NOT exceed 50% of 1M
		model1M := Model{Provider: "test", ID: "1m", ContextWindow: window}
		require.False(t, contextThresholdExceeded(model1M, 400_000))

		// Verify 500,000 tokens DOES exceed 50% of 1M
		require.True(t, contextThresholdExceeded(model1M, 500_000))

		// Execute threshold reset with 500,000 tokens
		// We verify ExecuteContextReset executes cleanly for the 1M+ model
		err = mgr.ExecuteContextReset(ownerCtx, parent, "threshold_exceeded_50_percent")
		require.NoError(t, err)

		require.True(t, wakeReceived.Load(), "auto-reset at 50%% threshold must trigger wake turn for 1M+ model")

		curAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Equal(t, int64(2), curAgent.SessionEpoch, "agent must be advanced to epoch 2")
	})
}

// TestPreResetScratchpadWarningReminder verifies:
// 1. contextWarningThresholdExceeded calculation:
//   - 40% for 1M+ models
//   - 70% for <1M models
//
// 2. Scratchpad warning reminder prompt is emitted when threshold is reached
// 3. Warning reminder is tracked per epoch and not repeatedly emitted in the same epoch
func TestPreResetScratchpadWarningReminder(t *testing.T) {
	ctx := context.Background()

	t.Run("WarningThresholdCalculations", func(t *testing.T) {
		// 1M+ models with 40% warning
		model1M := Model{Provider: "google", ID: "gemini-flash", ContextWindow: 1_000_000}
		require.False(t, contextWarningThresholdExceeded(model1M, 399_999, 40), "39.9999% should not exceed 40% warning threshold")
		require.True(t, contextWarningThresholdExceeded(model1M, 400_000, 40), "exact 40% should exceed warning threshold")
		require.True(t, contextWarningThresholdExceeded(model1M, 450_000, 40), "45% should exceed 40% warning threshold")
		require.True(t, ContextWarningThresholdExceeded(model1M, 400_000, 40), "exported ContextWarningThresholdExceeded must match")

		// 2M model: 40% = 800,000
		model2M := Model{Provider: "google", ID: "gemini-pro", ContextWindow: 2_000_000}
		require.False(t, contextWarningThresholdExceeded(model2M, 799_999, 40))
		require.True(t, contextWarningThresholdExceeded(model2M, 800_000, 40))

		// <1M models: 70%
		model100k := Model{Provider: "anthropic", ID: "claude-haiku", ContextWindow: 100_000}
		require.False(t, contextWarningThresholdExceeded(model100k, 69_999, 70), "69.999% should not exceed 70% warning threshold")
		require.True(t, contextWarningThresholdExceeded(model100k, 70_000, 70), "exact 70% should exceed warning threshold")
		require.True(t, contextWarningThresholdExceeded(model100k, 75_000, 70), "75% should exceed 70% warning threshold")
		require.True(t, ContextWarningThresholdExceeded(model100k, 70_000, 70), "exported ContextWarningThresholdExceeded must match")

		// 200k model: 70% = 140,000
		model200k := Model{Provider: "anthropic", ID: "claude-sonnet", ContextWindow: 200_000}
		require.False(t, contextWarningThresholdExceeded(model200k, 139_999, 70))
		require.True(t, contextWarningThresholdExceeded(model200k, 140_000, 70))

		// <= 0
		require.False(t, contextWarningThresholdExceeded(Model{ContextWindow: 0}, 100, 70))
		require.False(t, contextWarningThresholdExceeded(Model{ContextWindow: -10}, 100, 70))
	})

	t.Run("WarningReminderEmittedWhenExceedingThreshold", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_warning.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		const window = 100_000 // <1M model: 70% = 70,000 warning, 80% = 80,000 auto-reset
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "warning-test-model",
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
							if strings.Contains(txt.Text, "Context threshold reached:") || strings.Contains(txt.Text, "Context usage threshold warning") {
								reminderReceived.Store(true)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, db, "warning-agent")
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Turn 1: Large prompt that reaches ~71,250 tokens (57,000 words * 5 chars)
		// Context window = 100,000. 70% warning threshold is 70,000 tokens. 80% reset threshold is 80,000 tokens.
		prompt1 := strings.Repeat("data ", 57000)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, prompt1)
		require.NoError(t, err)

		// Verify warning reminder was emitted
		require.Eventually(t, func() bool {
			return reminderReceived.Load()
		}, 3*time.Second, 20*time.Millisecond, "warning reminder should be emitted when crossing 70%% threshold")

		// Wait for the warning reminder turn to complete and agent to return to idle
		require.Eventually(t, func() bool {
			a, err := db.ManagedAgent(ctx, parent.AgentID)
			return err == nil && a.ActivityState == "idle"
		}, 3*time.Second, 20*time.Millisecond)

		lane := mgr.laneFor(parent.AgentID)
		require.True(t, lane.warningEmitted, "lane.warningEmitted must be true after reminder emitted")

		// Reset reminderReceived flag to test de-duplication
		reminderReceived.Store(false)

		// Turn 2: Follow-up turn still in warning zone (>70% but <80%)
		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "short follow-up")
		require.NoError(t, err)

		// Verify reminder is NOT re-emitted in the same epoch
		time.Sleep(100 * time.Millisecond)
		require.False(t, reminderReceived.Load(), "warning reminder must NOT be re-emitted in same epoch")
	})

	t.Run("1MPlusModelWarningReminderEmittedAt40Percent", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_warning_1m.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		const window = 1_000_000 // 1M model: 40% = 400,000 warning, 50% = 500,000 auto-reset
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
			Models: []providers.FauxModelDefinition{{
				ID:            "warning-1m-test-model",
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
							if strings.Contains(txt.Text, "Context threshold reached:") || strings.Contains(txt.Text, "Context usage threshold warning") {
								reminderReceived.Store(true)
							}
						}
					}
				}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
			}
		}
		reg.SetResponses(steps)

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, db, "warning-1m-agent")
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		// Run turns that accumulate history to cross 400,000 tokens without exceeding max single-message body size (1MB).
		// Each turn adds ~85,000 tokens (~65,000 words = 325KB).
		// 5 turns = ~425,000 tokens, which crosses the 40% (400,000) warning threshold.
		for i := 1; i <= 5; i++ {
			err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, strings.Repeat("data ", 65000))
			require.NoError(t, err)
			if reminderReceived.Load() {
				break
			}
		}

		// Verify warning reminder was emitted
		require.Eventually(t, func() bool {
			return reminderReceived.Load()
		}, 3*time.Second, 20*time.Millisecond, "warning reminder should be emitted when crossing 40%% threshold on 1M+ model")

		lane := mgr.laneFor(parent.AgentID)
		require.True(t, lane.warningEmitted, "lane.warningEmitted must be true after reminder emitted")
	})
}

// TestManagerResetEmbeddedAgent verifies Manager.Reset on an embedded agent:
// 1. Returns 202 accepted with operation_id and agent
// 2. Replays identically (idempotent)
// 3. Executes context reset asynchronously (epoch advances, old epoch closed, wake turn dispatched)
func TestManagerResetEmbeddedAgent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer raw.Close()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "port-msg-test",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	parent := subagentTestParent(t, db, "reset-agent")
	registerParentOwner(mgr, parent)

	req := ResetRequest{
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents/reset-agent/reset",
		RequestID:         "req-reset-1",
		CanonicalBodyHash: "hash-reset-1",
		AgentID:           "reset-agent",
	}

	// 1. First call: initial reservation and reset trigger
	res, err := mgr.Reset(ctx, req)
	require.NoError(t, err)
	require.False(t, res.Replayed)
	require.Equal(t, 202, res.Operation.ResponseStatus)
	require.Equal(t, "reset-agent", res.Agent.AgentID)

	// 2. Second call: idempotent replay
	res2, err := mgr.Reset(ctx, req)
	require.NoError(t, err)
	require.True(t, res2.Replayed)
	require.Equal(t, res.Operation.OperationID, res2.Operation.OperationID)

	// 3. Wait for context reset to complete asynchronously
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, "reset-agent")
		if err != nil {
			return false
		}
		return agent.SessionEpoch == 2
	}, 5*time.Second, 50*time.Millisecond, "expected session epoch to advance to 2")

	// 4. Verify old session epoch is closed (current = 0)
	var oldCurrent int
	err = raw.QueryRowContext(ctx, `SELECT current FROM embedded_sessions WHERE agent_id=? AND epoch=1`, "reset-agent").Scan(&oldCurrent)
	require.NoError(t, err)
	require.Equal(t, 0, oldCurrent)
}

func TestMechaContextToolStatusAndRejectCompact(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "context_tool_test.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	mgr := &Manager{store: db}

	// 1. Create a 1M+ agent and verify mecha_context status
	b1M := subagentTestParent(t, db, "agent-1m")
	tok1M := 350_000
	pct1M := 35.0
	require.NoError(t, db.UpdateManagedAgentContextUsage(ctx, "agent-1m", &tok1M, 1_000_000, &pct1M))

	owner1M := embeddedOwner{binding: b1M}
	tool1M := mgr.embeddedCompactionTool(owner1M)
	require.Equal(t, "mecha_context", tool1M.Name)

	// Call status on 1M+ agent
	resStatus1M, err := tool1M.Execute(ctx, "call-1", map[string]any{"action": "status"}, nil)
	require.NoError(t, err)
	require.Len(t, resStatus1M.Content, 1)
	statusText1M := resStatus1M.Content[0].(ai.TextContent).Text
	require.Contains(t, statusText1M, `"session_id":"agent-1m-session"`)
	require.Contains(t, statusText1M, `"epoch":1`)
	require.Contains(t, statusText1M, `"warning_threshold_percent":40`)
	require.Contains(t, statusText1M, `"reset_threshold_percent":50`)
	require.Contains(t, statusText1M, `"system":"context_reset+scratchpad"`)
	require.Contains(t, statusText1M, `"current_tokens":350000`)
	require.Contains(t, statusText1M, `"context_window":1000000`)
	require.NotContains(t, strings.ToLower(statusText1M), "compaction is active")

	// 2. Create a <1M agent and verify mecha_context status
	bSmall := subagentTestParent(t, db, "agent-small")
	tokSmall := 120_000
	pctSmall := 60.0
	require.NoError(t, db.UpdateManagedAgentContextUsage(ctx, "agent-small", &tokSmall, 200_000, &pctSmall))

	ownerSmall := embeddedOwner{binding: bSmall}
	toolSmall := mgr.embeddedCompactionTool(ownerSmall)

	resStatusSmall, err := toolSmall.Execute(ctx, "call-2", map[string]any{"action": "status"}, nil)
	require.NoError(t, err)
	statusTextSmall := resStatusSmall.Content[0].(ai.TextContent).Text
	require.Contains(t, statusTextSmall, `"session_id":"agent-small-session"`)
	require.Contains(t, statusTextSmall, `"epoch":1`)
	require.Contains(t, statusTextSmall, `"warning_threshold_percent":10`)
	require.Contains(t, statusTextSmall, `"reset_threshold_percent":20`)

	// 3. Verify action=compact is REJECTED
	_, errCompact := tool1M.Execute(ctx, "call-3", map[string]any{"action": "compact"}, nil)
	require.Error(t, errCompact)
	require.Contains(t, errCompact.Error(), "unsupported mecha_context action")
	require.Contains(t, errCompact.Error(), "only action=status is supported")

	// 4. Verify invalid/missing action is rejected
	_, errEmpty := tool1M.Execute(ctx, "call-4", map[string]any{}, nil)
	require.Error(t, errEmpty)
	_, errInvalid := tool1M.Execute(ctx, "call-5", map[string]any{"action": "set_policy"}, nil)
	require.Error(t, errInvalid)

	// 5. Verify FormatContextResetWarningReminder wording has NO reference to compaction
	reminder1M := FormatContextResetWarningReminder(400_000, 1_000_000, 40, 50)
	require.Contains(t, reminder1M, "400000 / 1000000 tokens (40.0%)")
	require.Contains(t, reminder1M, "crossing the 40% warning threshold")
	require.Contains(t, reminder1M, "automatic reset occurs at 50%")
	require.Contains(t, reminder1M, "scratchpad")
	require.NotContains(t, strings.ToLower(reminder1M), "compact")

	reminderSmall := FormatContextResetWarningReminder(140_000, 200_000, 70, 80)
	require.Contains(t, reminderSmall, "140000 / 200000 tokens (70.0%)")
	require.Contains(t, reminderSmall, "crossing the 70% warning threshold")
	require.Contains(t, reminderSmall, "automatic reset occurs at 80%")
	require.Contains(t, reminderSmall, "scratchpad")
	require.NotContains(t, strings.ToLower(reminderSmall), "compact")
}

// TestContextResetGenesisPromptPreservedOnlyWhenExplicitlySet verifies:
// 1. When saved.PendingInitialInstruction is set, it is appended to epoch 2.
// 2. When saved.PendingInitialInstruction is empty, no genesis prompt is added at all.
func TestContextResetGenesisPromptPreservedOnlyWhenExplicitlySet(t *testing.T) {
	ctx := context.Background()

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{
		Models: []providers.FauxModelDefinition{{
			ID:            "genesis-test-model",
			ContextWindow: 1_000_000,
			MaxTokens:     4096,
			Input:         []string{"text"},
		}},
	})
	defer reg.Unregister()

	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "OK"}}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	t.Run("PendingInitialInstructionIsPreserved", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_preserved.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		explicitInstruction := "Follow these strict requirements for the task."

		// Reserve agent with PendingInitialInstruction set on launch
		id := "instruction-agent"
		agent := store.ManagedAgentRecord{
			AgentID: id, Name: id, SlotHeld: true, Cwd: t.TempDir(),
			SelectedModel:             store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-sol"},
			Runtime:                   "embedded",
			DesiredState:              "running",
			PiSessionID:               id + "-session",
			SessionEpoch:              1,
			ManifestRevision:          1,
			ManifestHash:              strings.Repeat("a", 64),
			PendingInitialInstruction: explicitInstruction,
		}
		op := store.ManagedOperation{
			OperationID: id + "-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
			RequestID: id + "-launch", CanonicalBodyHash: strings.Repeat("a", 64), ResponseStatus: 202,
			ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
		}
		require.NoError(t, db.ReserveManagedLaunch(ctx, agent, op))
		require.NoError(t, db.UpdateManagedAgentStarting(ctx, id, agent.PiSessionID, store.ProcessIdentityRecord{PID: 123, StartMarker: "start", Incarnation: "inc"}, "generation-1"))
		require.NoError(t, db.MarkManagedReady(ctx, id))
		parent := store.EmbeddedBinding{AgentID: id, SessionID: agent.PiSessionID, Epoch: 1, Generation: "generation-1"}
		require.NoError(t, db.BindEmbeddedSession(ctx, parent, "worker"))
		token, err := db.AcquireEmbeddedOwner(ctx, parent, "fixture:"+parent.AgentID, time.Minute)
		require.NoError(t, err)
		parent.OwnerToken = token
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Equal(t, explicitInstruction, savedAgent.PendingInitialInstruction)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "Initial message")
		require.NoError(t, err)

		err = mgr.ExecuteContextReset(ownerCtx, parent, "test_instruction_preserved")
		require.NoError(t, err)

		updatedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Equal(t, int64(2), updatedAgent.SessionEpoch)

		nextBinding := store.EmbeddedBinding{
			AgentID:    parent.AgentID,
			SessionID:  updatedAgent.PiSessionID,
			Epoch:      2,
			Generation: parent.Generation,
			OwnerToken: 1,
		}
		snap2, err := db.ReopenEmbedded(ctx, nextBinding)
		require.NoError(t, err)

		// Entry 0 must be the preserved initial instruction
		msg0, err := ai.UnmarshalMessage(snap2.Entries[0].Body)
		require.NoError(t, err)
		require.Equal(t, ai.RoleUser, msg0.MessageRole())
		require.Contains(t, messageContentText(msg0), explicitInstruction)
	})

	t.Run("EmptyInitialInstructionLeavesNoGenesisPrompt", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "test_no_genesis.db")
		db, err := store.Open(ctx, dbPath, store.Options{})
		require.NoError(t, err)
		defer db.Close()

		mgr := makeSubagentTestManager(t, db, reg, 4)
		defer mgr.Close(ctx)

		parent := subagentTestParent(t, db, "no-instruction-agent")
		registerParentOwner(mgr, parent)

		savedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Empty(t, savedAgent.PendingInitialInstruction)

		owner := embeddedOwner{binding: parent, identity: "fixture:" + parent.AgentID}
		ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

		err = mgr.runEmbeddedSessionPrompt(ownerCtx, *savedAgent, "First turn prompt")
		require.NoError(t, err)

		err = mgr.ExecuteContextReset(ownerCtx, parent, "test_no_genesis")
		require.NoError(t, err)

		updatedAgent, err := db.ManagedAgent(ctx, parent.AgentID)
		require.NoError(t, err)
		require.Equal(t, int64(2), updatedAgent.SessionEpoch)

		nextBinding := store.EmbeddedBinding{
			AgentID:    parent.AgentID,
			SessionID:  updatedAgent.PiSessionID,
			Epoch:      2,
			Generation: parent.Generation,
			OwnerToken: 1,
		}
		snap2, err := db.ReopenEmbedded(ctx, nextBinding)
		require.NoError(t, err)

		// Check all entries in snap2 to ensure NO synthetic genesis prompt exists
		for _, entry := range snap2.Entries {
			msg, err := ai.UnmarshalMessage(entry.Body)
			require.NoError(t, err)
			content := messageContentText(msg)
			require.NotContains(t, content, "You are the 'no-instruction-agent' agent. Proceed with your assigned task.")
			require.NotContains(t, content, "You are a mecha agent.")
		}
	})
}

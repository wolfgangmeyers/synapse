package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedMailSubagentCompletionDoesNotInterruptMultiTool tests that an active
// multi-tool batch is NOT interrupted mid-turn by an incoming subagent completion notice,
// so that both tools run to completion, and the subagent completion notice is processed
// only after the turn finishes and the agent settles to idle.
func TestEmbeddedMailSubagentCompletionDoesNotInterruptMultiTool(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	step1ActiveFile := filepath.Join(root, "step1_active.txt")
	step1FinishFile := filepath.Join(root, "step1_finish.txt")
	step2MarkerFile := filepath.Join(root, "step2_marker.txt")

	var sawSubagentNoticeInTurn1 atomic.Bool
	var sawTool2Ran atomic.Bool
	var sawSubagentTurn atomic.Bool

	// Initial step: worker responds to genesis
	initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}

	// Turn 1, Step 1: Model issues TWO tool calls in the same turn
	multiStep1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		step1Cmd := fmt.Sprintf("touch '%s' && while [ ! -f '%s' ]; do sleep 0.02; done && echo 'step1 complete'", step1ActiveFile, step1FinishFile)
		step2Cmd := fmt.Sprintf("touch '%s' && echo 'step2 ran'", step2MarkerFile)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": step1Cmd}, "call-step1"),
			providers.FauxToolCall("bash", map[string]any{"command": step2Cmd}, "call-step2"),
		}, ai.StopToolUse)
	}

	// Turn 1, Step 2: Model finishes the multi-tool turn after tool results
	multiStep2 := func(hist ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		for _, msg := range hist.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok {
						if strings.Contains(tc.Text, "subagent-completed-sub1-1") || strings.Contains(tc.Text, "subagent task finished") {
							sawSubagentNoticeInTurn1.Store(true)
						}
					}
				}
			}
			if tr, ok := msg.(ai.ToolResultMessage); ok {
				if tr.ToolCallID == "call-step2" {
					sawTool2Ran.Store(true)
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "both tools finished successfully"}}, ai.StopStop)
	}

	// Turn 2: Subagent notice turn after turn 1 settles to idle
	subagentTurnStep := func(hist ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		for _, msg := range hist.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok {
						if strings.Contains(tc.Text, "subagent task finished") {
							sawSubagentTurn.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "acknowledged subagent completion"}}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{initialStep, initialStep, initialStep})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "subagent-multitool-lane",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "d4444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	reg.SetResponses([]providers.FauxResponseStep{multiStep1, multiStep2, subagentTurnStep})

	// Enqueue first message to start the multi-step turn
	turnMsgID := "d5555555-5555-4555-8555-555555555555"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turnMsgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "begin long multi-tool turn",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turnMsgID)
	require.NoError(t, err)

	manager.OfferDirect(launched.Agent.AgentID)

	// Wait for tool 1 to start executing (it touches step1ActiveFile)
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(step1ActiveFile)
		return statErr == nil
	}, 3*time.Second, 10*time.Millisecond)

	// Verify activeSession is set on the lane mid-turn
	lane := manager.laneFor(launched.Agent.AgentID)
	lane.activeMu.RLock()
	active := lane.activeSession
	lane.activeMu.RUnlock()
	require.NotNil(t, active, "activeSession must be set mid-turn")

	// While tool 1 is running, enqueue a subagent completion notice
	subagentMsgID := "subagent-completed-sub1-1"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        subagentMsgID,
		From:      "system",
		To:        launched.Agent.AgentID,
		Body:      "subagent task finished",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Source: &protocol.MessageSource{
				Kind: "system",
			},
		},
	})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, subagentMsgID)
	require.NoError(t, err)

	// Offer the message
	manager.OfferDirect(launched.Agent.AgentID)

	// Allow tool 1 to complete so execution advances to tool 2
	require.NoError(t, os.WriteFile(step1FinishFile, []byte("ok"), 0644))

	// Wait for tool 2 to run and complete
	require.Eventually(t, func() bool {
		return sawTool2Ran.Load()
	}, 3*time.Second, 10*time.Millisecond)

	// Verify tool 1 finished and tool 2 ran to completion
	require.True(t, sawTool2Ran.Load(), "tool 2 must have executed to completion")
	require.False(t, sawSubagentNoticeInTurn1.Load(), "subagent notice must NOT have interrupted turn 1")

	// Wait for subagent completion message to be picked up as a subsequent turn and finish
	require.Eventually(t, func() bool {
		msgRecord, e := db.MessageFor(ctx, subagentMsgID)
		return e == nil && msgRecord.State == domain.MessageAcknowledged
	}, 5*time.Second, 20*time.Millisecond)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	require.True(t, sawSubagentTurn.Load(), "subagent message must be admitted as a subsequent turn")
}

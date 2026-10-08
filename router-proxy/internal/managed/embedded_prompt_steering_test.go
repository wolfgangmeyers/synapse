package managed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedPromptMidTurnSteering(t *testing.T) {
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

	initialStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ready"}}, ai.StopStop)
	}

	step1Started := make(chan struct{})
	allowStep1Finish := make(chan struct{})
	var sawSteering atomic.Bool

	multiStep1 := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		select {
		case step1Started <- struct{}{}:
		default:
		}
		<-allowStep1Finish
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": "echo 'step1 complete'"}, "call-step1"),
		}, ai.StopToolUse)
	}

	multiStep2 := func(hist ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		for _, msg := range hist.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok {
						if strings.Contains(tc.Text, "urgent prompt steer directive") {
							sawSteering.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "handled prompt steering and finished turn"}}, ai.StopStop)
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
		Name:      "prompt-steer-lane",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "c4444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	reg.SetResponses([]providers.FauxResponseStep{multiStep1, multiStep2})

	// Enqueue first message to start the multi-step turn
	turnMsgID := "c5555555-5555-4555-8555-555555555555"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turnMsgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "begin long multi-step turn",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turnMsgID)
	require.NoError(t, err)

	manager.OfferDirect(launched.Agent.AgentID)

	// Wait for step 1 to start
	select {
	case <-step1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for step 1 to start")
	}

	// Mid-turn: AdmitPromptWithRequestID must steer the active session immediately without blocking!
	promptDone := make(chan error, 1)
	go func() {
		promptDone <- manager.AdmitPromptWithRequestID(ctx, launched.Agent.AgentID, "urgent prompt steer directive: please prioritize this", "c6666666-6666-4666-8666-666666666666")
	}()

	select {
	case err := <-promptDone:
		require.NoError(t, err, "AdmitPromptWithRequestID should steer immediately while mid-turn")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("AdmitPromptWithRequestID blocked on mutex instead of steering active session mid-turn")
	}

	// Allow step 1 to complete so turn advances
	close(allowStep1Finish)

	// Wait for turn to finish and agent to return to idle
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	require.True(t, sawSteering.Load(), "steering prompt must be delivered to active session")
}

package managed

import (
	"context"
	"os"
	"path/filepath"
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

func TestInterruptEmbeddedMailPreservesRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := t.TempDir()
	require.NoError(t, os.Chmod(base, 0700))

	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	interrupted := make(chan struct{})
	var turnStarted atomic.Bool

	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			turnStarted.Store(true)
			// Wait until interrupted by Abort
			select {
			case <-interrupted:
			case <-time.After(5 * time.Second):
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "completed after wait"},
			}, ai.StopStop)
		},
	})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "test-interrupt-agent",
		Cwd:       base,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "a1111111-1111-4111-8111-111111111111",
	})
	require.NoError(t, err)
	agentID := launched.Agent.AgentID

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Send a mail to trigger a turn
	mailID := "m1111111-1111-4111-8111-111111111111"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        mailID,
		From:      "sender",
		To:        agentID,
		Body:      "test mail to interrupt",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)

	_, err = db.ResolveMessageTarget(ctx, mailID)
	require.NoError(t, err)

	manager.OfferDirect(agentID)

	// Wait for turn to start
	require.Eventually(t, func() bool {
		return turnStarted.Load()
	}, 5*time.Second, 20*time.Millisecond)

	// Interrupt the agent
	require.NoError(t, manager.InterruptAgent(ctx, agentID))
	close(interrupted)

	// Verify agent remains running, activity becomes idle, prompts accepted, slot held, no error
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		if e != nil {
			return false
		}
		return a.LifecycleState == "running" &&
			a.ActivityState == "idle" &&
			a.AcceptingPrompts &&
			a.SlotHeld &&
			a.LastErrorCode == ""
	}, 5*time.Second, 20*time.Millisecond)

	agent, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "running", agent.LifecycleState)
	require.Equal(t, "idle", agent.ActivityState)
	require.True(t, agent.AcceptingPrompts)
	require.True(t, agent.SlotHeld)
	require.Empty(t, agent.LastErrorCode)
	require.Empty(t, agent.LastErrorMessage)
}

func TestInterruptEmbeddedPromptPreservesRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	base := t.TempDir()
	require.NoError(t, os.Chmod(base, 0700))

	dbPath := filepath.Join(base, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	interrupted := make(chan struct{})
	var turnStarted atomic.Bool

	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
			turnStarted.Store(true)
			select {
			case <-interrupted:
			case <-time.After(5 * time.Second):
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "completed after wait"},
			}, ai.StopStop)
		},
	})

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{base}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:      "test-interrupt-prompt-agent",
		Cwd:       base,
		Runtime:   "embedded",
		Model:     &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "p1111111-1111-4111-8111-111111111111",
	})
	require.NoError(t, err)
	agentID := launched.Agent.AgentID

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	promptErrCh := make(chan error, 1)
	go func() {
		promptErrCh <- manager.AdmitPromptWithRequestID(ctx, agentID, "test prompt to interrupt", "req-111")
	}()

	require.Eventually(t, func() bool {
		return turnStarted.Load()
	}, 5*time.Second, 20*time.Millisecond)

	require.NoError(t, manager.InterruptAgent(ctx, agentID))
	close(interrupted)

	promptErr := <-promptErrCh
	require.Error(t, promptErr)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		if e != nil {
			return false
		}
		return a.LifecycleState == "running" &&
			a.ActivityState == "idle" &&
			a.AcceptingPrompts &&
			a.SlotHeld &&
			a.LastErrorCode == ""
	}, 5*time.Second, 20*time.Millisecond)

	agent, err := db.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, "running", agent.LifecycleState)
	require.Equal(t, "idle", agent.ActivityState)
	require.True(t, agent.AcceptingPrompts)
	require.True(t, agent.SlotHeld)
	require.Empty(t, agent.LastErrorCode)
	require.Empty(t, agent.LastErrorMessage)
}

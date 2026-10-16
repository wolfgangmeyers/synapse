package managed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedWorkerSystemPrompt(t *testing.T) {
	t.Run("injects agent ID and working directory", func(t *testing.T) {
		agentID := "Worktrees"
		cwd := "/home/wolfgang/code/mecha-developer/worktrees"
		prompt := resolveEmbeddedWorkerSystemPrompt("worker", agentID, cwd)

		// Base instructions must be present
		require.Contains(t, prompt, "You are an expert coding assistant operating inside pi")
		require.Contains(t, prompt, "### Responsiveness & Communication")

		// Agent Identity section must be present with backtick formatting
		expectedIdentity := `# Agent Identity
- You are an embedded worker running in the mecha router harness.
- Your agent name / mailbox address is: ` + "`" + agentID + "`" + `
- When sending or receiving mailbox messages, your identity is ` + "`" + agentID + "`." + `
- Your working directory is: ` + "`" + cwd + "`"

		require.Contains(t, prompt, expectedIdentity)
	})

	t.Run("buildEmbeddedWorkerSystemPrompt matches resolveEmbeddedWorkerSystemPrompt", func(t *testing.T) {
		agentID := "Worktrees"
		cwd := "/home/wolfgang/code/mecha-developer/worktrees"
		resolved := resolveEmbeddedWorkerSystemPrompt("worker", agentID, cwd)
		built := buildEmbeddedWorkerSystemPrompt("worker", agentID, cwd)
		require.Equal(t, resolved, built)
	})

	t.Run("empty agent ID returns base prompt without identity", func(t *testing.T) {
		prompt := resolveEmbeddedWorkerSystemPrompt("worker", "", "/tmp")
		require.Contains(t, prompt, "You are an expert coding assistant operating inside pi")
		require.NotContains(t, prompt, "# Agent Identity")
	})

	t.Run("formatGenesisPrompt tells agent its name", func(t *testing.T) {
		agentID := "Worktrees"
		initialInstruction := "Implement the requested feature in worktree."

		genesis := formatGenesisPrompt(agentID, initialInstruction)
		require.True(t, strings.HasPrefix(genesis, "You are the 'Worktrees' agent."))
		require.Contains(t, genesis, initialInstruction)

		// Idempotency: if already contains agent name, do not duplicate
		repeated := formatGenesisPrompt(agentID, genesis)
		require.Equal(t, genesis, repeated)

		// Empty instruction gets default task continuation with name
		emptyGenesis := formatGenesisPrompt(agentID, "")
		require.Equal(t, "You are the 'Worktrees' agent. Proceed with your assigned task.", emptyGenesis)
	})
}

func TestEmbeddedWorkerSystemPromptAndGenesisInSession(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var sawSystemPromptIdentity atomic.Bool
	var sawGenesisAgentName atomic.Bool
	var capturedUserContent string

	reg.SetResponses([]providers.FauxResponseStep{
		// First response for root-supervisor initial instruction
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "root ready"}}, ai.StopStop)
		},
		// Second response for Worktrees worker initial instruction
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			sys := ai.GetCurrentSystemPrompt(h.Messages)
			if strings.Contains(sys, "# Agent Identity") &&
				strings.Contains(sys, "- Your agent name / mailbox address is: `Worktrees`") &&
				strings.Contains(sys, "- When sending or receiving mailbox messages, your identity is `Worktrees`.") &&
				strings.Contains(sys, "- Your working directory is: `"+root+"`") {
				sawSystemPromptIdentity.Store(true)
			}
			if len(h.Messages) > 0 {
				if user, ok := h.Messages[len(h.Messages)-1].(ai.UserMessage); ok {
					if len(user.Content) > 0 {
						if text, ok := user.Content[0].(ai.TextContent); ok {
							capturedUserContent = text.Text
							if strings.HasPrefix(text.Text, "You are the 'Worktrees' agent.") {
								sawGenesisAgentName.Store(true)
							}
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "worktrees worker ready"}}, ai.StopStop)
		},
	})

	var processes, rpcs atomic.Int32
	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessFactory:        &rootRedProcessFactory{calls: &processes},
		RPCFactory:            rootRedRPCFactory{calls: &rpcs},
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)

	// Launch root owner to authoritatively launch the worker
	rootReq := LaunchRequest{
		Name:               "root-supervisor",
		Cwd:                root,
		InitialInstruction: "supervisor init",
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:          "11111111-1111-4111-8111-111111111111",
	}
	launchedRoot, err := manager.LaunchEmbeddedRoot(ctx, rootReq)
	require.NoError(t, err)

	rootBinding := store.EmbeddedBinding{
		AgentID:    launchedRoot.Agent.AgentID,
		SessionID:  launchedRoot.Agent.PiSessionID,
		Epoch:      launchedRoot.Agent.SessionEpoch,
		Generation: launchedRoot.Agent.RuntimeGeneration,
	}
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, rootBinding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 10*time.Second, 10*time.Millisecond)

	manager.mu.Lock()
	rootOwner, ok := manager.embeddedOwners[rootBinding.AgentID]
	manager.mu.Unlock()
	require.True(t, ok)

	// Launch worker agent "Worktrees"
	workerReq := LaunchRequest{
		Name:               "Worktrees",
		Cwd:                root,
		InitialInstruction: "Check git worktrees status and clean up stale branches.",
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:          "22222222-2222-4222-8222-222222222222",
	}
	launchedWorker, err := manager.launchEmbeddedWorkerFromOwner(ctx, workerReq, rootOwner)
	require.NoError(t, err)

	workerBinding := store.EmbeddedBinding{
		AgentID:    launchedWorker.Agent.AgentID,
		SessionID:  launchedWorker.Agent.PiSessionID,
		Epoch:      launchedWorker.Agent.SessionEpoch,
		Generation: launchedWorker.Agent.RuntimeGeneration,
	}
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, workerBinding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 10*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		return sawSystemPromptIdentity.Load() && sawGenesisAgentName.Load()
	}, 10*time.Second, 10*time.Millisecond, "expected worker to receive system prompt identity and genesis agent name")

	require.True(t, sawSystemPromptIdentity.Load(), "worker must receive system prompt with agent name and working directory")
	require.True(t, sawGenesisAgentName.Load(), "worker must be told its name in genesis prompt")
	require.Contains(t, capturedUserContent, "You are the 'Worktrees' agent.")
	require.Contains(t, capturedUserContent, "Check git worktrees status and clean up stale branches.")
}

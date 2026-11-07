package managed

import (
	"context"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPiRootProviderPromptIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_session_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(root, 0700))
	info, statErr = os.Stat(root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", root, info.Mode().Perm())
	dbPath := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls atomic.Int32
	var sawResponsivenessPrompt, sawPiRootIdentity atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		sys := ai.GetCurrentSystemPrompt(h.Messages)
		if strings.Contains(sys, "embedded worker running") && !strings.Contains(sys, "embedded root running") {
			sawPiRootIdentity.Store(true)
		}
		if strings.Contains(sys, "### Responsiveness & Communication") &&
			strings.Contains(sys, "always acknowledge the user with a brief conversational response first") {
			sawResponsivenessPrompt.Store(true)
		}
		user, ok := h.Messages[len(h.Messages)-1].(ai.UserMessage)
		require.True(t, ok)
		require.Equal(t, ai.ContentList{ai.TextContent{Text: "durable prompt"}}, user.Content)
		calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "persisted answer"}}, ai.StopStop)
	}})
	manager, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer func() {
		if t.Failed() {
			manager.mu.Lock()
			_, published := manager.embeddedOwners["production-root"]
			manager.mu.Unlock()
			t.Logf("failure snapshot owner_published=%t providers=%d", published, calls.Load())
			cwdPrivacyManagedSnapshot(t, dbPath)
		}
	}()
	request := LaunchRequest{Name: "production-root", Cwd: root, InitialInstruction: "durable prompt", Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "22222222-2222-4222-8222-222222222222"}
	launched, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && saved.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	binding = inspectManagedOwnerBinding(t, dbPath, binding)
	require.Eventually(t, func() bool {
		snap, e := db.ReopenEmbedded(ctx, binding)
		return e == nil && calls.Load() == 1 && len(snap.Entries) == 3 && snap.Entries[1].Kind == "user" && snap.Entries[2].Kind == "assistant"
	}, 5*time.Second, 10*time.Millisecond, "provider response and persisted user/assistant entries")
	require.EqualValues(t, 1, calls.Load())
	require.True(t, sawPiRootIdentity.Load(), "Pi root provider must receive original worker identity")
	require.True(t, sawResponsivenessPrompt.Load(), "embedded worker must receive responsiveness instruction in system prompt")
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, db.Close())
}

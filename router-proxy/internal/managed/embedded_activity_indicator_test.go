package managed

import (
	"context"
	"os"
	"path/filepath"
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

func TestEmbeddedSessionTogglesActivityStateDuringTurn(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))

	dbPath := filepath.Join(t.TempDir(), "activity.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var observedActive atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			// During provider call, check that activity_state is "active"
			saved, e := db.ManagedAgent(context.Background(), "activity-indicator-root")
			if e == nil && saved.ActivityState == "active" {
				observedActive.Store(true)
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "turn completed"}}, ai.StopStop)
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

	var publishedProjections atomic.Int32
	manager.SetProjectionPublisher(func() {
		publishedProjections.Add(1)
	})

	request := LaunchRequest{
		Name:               "activity-indicator-root",
		Cwd:                root,
		InitialInstruction: "prompt to run",
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:          "11111111-1111-4111-8111-111111111111",
	}

	launched, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		saved, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && saved.LifecycleState == "running" && saved.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Verify that the provider observed "active" state during execution
	require.True(t, observedActive.Load(), "activity_state must be 'active' while the turn executes")

	// Verify that projections were published
	require.Greater(t, publishedProjections.Load(), int32(0), "publishProjection must be invoked on activity changes")

	// After completion, activity_state must be "idle"
	saved, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", saved.ActivityState)
}

package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestSelectModelUsesExplicitSavedThenFirstAvailableFallback(t *testing.T) {
	catalog := CatalogFromModels(Model{Provider: "saved", ID: "model", Available: true}, Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true})
	current, err := catalog.Catalog(context.Background())
	require.NoError(t, err)
	selected, err := SelectModel(&Model{Provider: "saved", ID: "model"}, nil, current)
	require.NoError(t, err)
	require.Equal(t, "saved", selected.Provider)
	selected, err = SelectModel(nil, &store.ManagedModel{Provider: "saved", ID: "model"}, current)
	require.NoError(t, err)
	require.Equal(t, "saved", selected.Provider)
	require.True(t, selected.Available)
	selected, err = SelectModel(nil, &store.ManagedModel{Provider: "missing", ID: "model"}, current)
	require.NoError(t, err)
	require.Equal(t, "missing", selected.Provider)
	require.Equal(t, "model", selected.ID)
	require.False(t, selected.Available)
	selected, err = SelectModel(nil, nil, current)
	require.NoError(t, err)
	require.Equal(t, "model", selected.ID)
}

func TestProductionModelSourceReadsPersistedDefault(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	require.NoError(t, durable.SetDefaultModel(ctx, &store.ManagedModel{Provider: "saved", ID: "persisted"}))
	source := StoreCatalogSource{Store: durable, Helper: CatalogFromModels(
		Model{Provider: "saved", ID: "persisted", Available: true},
		Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true},
	)}
	selected, err := source.Select(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, Model{Provider: "saved", ID: "persisted", Available: true}, selected)
	require.NoError(t, durable.SetDefaultModel(ctx, &store.ManagedModel{Provider: "missing", ID: "persisted"}))
	selected, err = source.Select(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, Model{Provider: "missing", ID: "persisted", Name: "persisted", Available: false}, selected)
}

func TestSelectModelRejectsUnavailableExplicitButUsesServerFallbackWhenOmitted(t *testing.T) {
	_, err := SelectModel(&Model{Provider: "missing", ID: "model"}, nil, Catalog{Models: []Model{}})
	require.Error(t, err)
	require.True(t, IsModelUnavailable(err))

	selected, err := SelectModel(nil, nil, Catalog{Models: []Model{}})
	require.NoError(t, err)
	require.Equal(t, Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", Available: true}, selected)

	// When an available model exists in catalog (e.g. Google), select it instead of Sol fallback
	googleCatalog := Catalog{Models: []Model{
		{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: false},
		{Provider: "google", ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Available: true},
	}}
	selected, err = SelectModel(nil, nil, googleCatalog)
	require.NoError(t, err)
	require.Equal(t, Model{Provider: "google", ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash", Available: true}, selected)
}

func TestPublicAgentOmitsPrivateProcessAndSessionIdentity(t *testing.T) {
	agent := &store.ManagedAgentRecord{
		AgentID: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"},
		DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
		PiSessionID: "private-session", ProcessIncarnation: "private-process", RuntimeGeneration: "private-generation",
	}
	projection := PublicAgent(agent)
	_, hasSession := projection["pi_session_id"]
	_, hasProcess := projection["process_incarnation"]
	_, hasGeneration := projection["runtime_generation"]
	_, hasEpoch := projection["session_epoch"]
	require.False(t, hasSession)
	require.False(t, hasProcess)
	require.False(t, hasGeneration)
	require.False(t, hasEpoch)
}

func TestResolveModelCanonical(t *testing.T) {
	// Baseline: the catalog has "gemini-3.8-flash" under "google".
	catalogGoogle := ai.GetModel("google", "gemini-3.8-flash")
	require.NotNil(t, catalogGoogle)
	require.Equal(t, "google", catalogGoogle.Provider)

	// 1. Direct lookup for a standard catalog model
	direct, err := ResolveModel(store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"})
	require.NoError(t, err)
	require.NotNil(t, direct)
	require.Equal(t, "google", direct.Provider)
	require.Equal(t, "gemini-3.8-flash", direct.ID)

	// 2. Antigravity alias to Google catalog model: must preserve "antigravity" provider
	agResolved, err := ResolveModel(store.ManagedModel{Provider: "antigravity", ID: "gemini-3.8-flash"})
	require.NoError(t, err)
	require.NotNil(t, agResolved)
	require.Equal(t, "antigravity", agResolved.Provider)
	require.Equal(t, "gemini-3.8-flash", agResolved.ID)
	// Must not have mutated the catalog model
	require.Equal(t, "google", catalogGoogle.Provider)

	// 3. Antigravity model not in catalog: fallback metadata provided
	agFallback, err := ResolveModel(store.ManagedModel{Provider: "antigravity", ID: "gemini-custom-future"})
	require.NoError(t, err)
	require.NotNil(t, agFallback)
	require.Equal(t, "antigravity", agFallback.Provider)
	require.Equal(t, "gemini-custom-future", agFallback.ID)
	require.Equal(t, "google-generative-ai", agFallback.Api)
	require.Equal(t, 1048576, agFallback.ContextWindow)
	require.Equal(t, 8192, agFallback.MaxTokens)

	// 3b. Gemini-4-argon synthesized metadata
	argonGoogle, err := ResolveModel(store.ManagedModel{Provider: "google", ID: "gemini-4-argon"})
	require.NoError(t, err)
	require.NotNil(t, argonGoogle)
	require.Equal(t, "google", argonGoogle.Provider)
	require.Equal(t, "gemini-4-argon", argonGoogle.ID)
	require.Equal(t, "google-generative-ai", argonGoogle.Api)
	require.Equal(t, 1048576, argonGoogle.ContextWindow)
	require.Equal(t, 65536, argonGoogle.MaxTokens)

	argonVertex, err := ResolveModel(store.ManagedModel{Provider: "google-vertex", ID: "gemini-4-argon"})
	require.NoError(t, err)
	require.NotNil(t, argonVertex)
	require.Equal(t, "google-vertex", argonVertex.Provider)
	require.Equal(t, "gemini-4-argon", argonVertex.ID)
	require.Equal(t, "google-vertex", argonVertex.Api)
	require.Equal(t, 1048576, argonVertex.ContextWindow)
	require.Equal(t, 65536, argonVertex.MaxTokens)

	argonAG, err := ResolveModel(store.ManagedModel{Provider: "antigravity", ID: "gemini-4-argon"})
	require.NoError(t, err)
	require.NotNil(t, argonAG)
	require.Equal(t, "antigravity", argonAG.Provider)
	require.Equal(t, "gemini-4-argon", argonAG.ID)
	require.Equal(t, "google-generative-ai", argonAG.Api)
	require.Equal(t, 1048576, argonAG.ContextWindow)
	require.Equal(t, 65536, argonAG.MaxTokens)

	// 4. Missing model under non-antigravity provider: returns error, does NOT fall back to openai-codex
	missingGoogle, err := ResolveModel(store.ManagedModel{Provider: "google", ID: "nonexistent-model"})
	require.Error(t, err)
	require.Nil(t, missingGoogle)
	require.True(t, IsModelUnavailable(err))

	missingOther, err := ResolveModel(store.ManagedModel{Provider: "other-provider", ID: "custom-model"})
	require.Error(t, err)
	require.Nil(t, missingOther)

	// 5. Empty provider or ID
	empty, err := ResolveModel(store.ManagedModel{})
	require.Error(t, err)
	require.Nil(t, empty)
}

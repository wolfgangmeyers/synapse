package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestCustomModels_CatalogAndResolve(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "custom_models_test.db")
	s, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer s.Close()

	// 1. Initially catalog has no custom models
	catSource := StoreCatalogSource{Store: s}
	cat, err := catSource.Catalog(ctx)
	require.NoError(t, err)
	for _, m := range cat.Models {
		require.False(t, IsCustomProvider(m.Provider))
	}

	// 2. Insert custom provider with encrypted key
	key, err := crypto.DeriveKey("")
	require.NoError(t, err)
	encKey, err := crypto.Encrypt([]byte("test-secret-key-123"), key)
	require.NoError(t, err)

	provider := store.CustomProviderRecord{
		ID:               "local-ollama",
		Label:            "Local Ollama",
		BaseURL:          "http://127.0.0.1:11434/v1",
		Protocol:         "openai-compatible",
		APIKeyCiphertext: &encKey,
		Enabled:          true,
	}
	models := []store.CustomProviderModelRecord{
		{
			ID:            "llama3.2",
			DisplayName:   "Llama 3.2 3B",
			ContextWindow: 131072,
			MaxTokens:     4096,
			SupportsTools: true,
		},
		{
			ID:            "qwen2.5-coder",
			DisplayName:   "Qwen 2.5 Coder 7B",
			ContextWindow: 65536,
			MaxTokens:     8192,
			SupportsTools: true,
		},
	}
	err = s.CreateCustomProvider(ctx, provider, models)
	require.NoError(t, err)

	// 3. Check Catalog() contains custom models with canonical provider "custom-local-ollama"
	cat, err = catSource.Catalog(ctx)
	require.NoError(t, err)

	var foundLlama, foundQwen bool
	for _, m := range cat.Models {
		if m.Provider == "custom-local-ollama" && m.ID == "llama3.2" {
			foundLlama = true
			require.Equal(t, "Llama 3.2 3B", m.Name)
			require.True(t, m.Available)
			require.Equal(t, 131072, m.ContextWindow)
		}
		if m.Provider == "custom-local-ollama" && m.ID == "qwen2.5-coder" {
			foundQwen = true
			require.Equal(t, "Qwen 2.5 Coder 7B", m.Name)
			require.True(t, m.Available)
			require.Equal(t, 65536, m.ContextWindow)
		}
	}
	require.True(t, foundLlama, "llama3.2 must be in catalog")
	require.True(t, foundQwen, "qwen2.5-coder must be in catalog")

	// 4. Sync custom models to ai registry
	err = SyncCustomModelsToAI(ctx, s)
	require.NoError(t, err)

	// 5. Test ResolveModel
	resolved, err := ResolveModel(store.ManagedModel{
		Provider: "custom-local-ollama",
		ID:       "llama3.2",
	})
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, "custom-local-ollama", resolved.Provider)
	require.Equal(t, "llama3.2", resolved.ID)
	require.Equal(t, "Llama 3.2 3B", resolved.Name)
	require.Equal(t, ai.APIOpenAICompletions, resolved.Api)
	require.Equal(t, "http://127.0.0.1:11434/v1", resolved.BaseURL)
	require.Equal(t, 131072, resolved.ContextWindow)
	require.Equal(t, 4096, resolved.MaxTokens)

	authHeader := resolved.Headers["Authorization"]
	require.NotNil(t, authHeader)
	require.Equal(t, "Bearer test-secret-key-123", *authHeader)

	// 6. Test ResolveModel for non-existent model in custom provider
	_, err = ResolveModel(store.ManagedModel{
		Provider: "custom-local-ollama",
		ID:       "nonexistent",
	})
	require.Error(t, err)
	require.True(t, IsModelUnavailable(err))

	// 7. Test admitEmbeddedModel
	mgr := &Manager{store: s}
	admitted, err := mgr.admitEmbeddedModel(ctx, Model{
		Provider: "custom-local-ollama",
		ID:       "llama3.2",
	})
	require.NoError(t, err)
	require.Equal(t, "custom-local-ollama", admitted.Provider)
	require.Equal(t, "llama3.2", admitted.ID)

	// Disabling provider removes models from active catalog
	provider.Enabled = false
	err = s.UpdateCustomProvider(ctx, provider, models)
	require.NoError(t, err)

	cat, err = catSource.Catalog(ctx)
	require.NoError(t, err)
	for _, m := range cat.Models {
		require.False(t, m.Provider == "custom-local-ollama")
	}

	// 8. Custom provider without API key sets Authorization: Bearer none
	providerKeyless := store.CustomProviderRecord{
		ID:       "keyless-ollama",
		Label:    "Keyless Ollama",
		BaseURL:  "http://127.0.0.1:11434/v1",
		Protocol: "openai-compatible",
		Enabled:  true,
	}
	modelsKeyless := []store.CustomProviderModelRecord{
		{
			ID:          "deepseek-r1",
			DisplayName: "DeepSeek R1",
		},
	}
	err = s.CreateCustomProvider(ctx, providerKeyless, modelsKeyless)
	require.NoError(t, err)

	err = SyncCustomModelsToAI(ctx, s)
	require.NoError(t, err)

	resolvedKeyless, err := ResolveModel(store.ManagedModel{
		Provider: "custom-keyless-ollama",
		ID:       "deepseek-r1",
	})
	require.NoError(t, err)
	require.NotNil(t, resolvedKeyless)
	keylessAuthHeader := resolvedKeyless.Headers["Authorization"]
	require.NotNil(t, keylessAuthHeader)
	require.Equal(t, "Bearer none", *keylessAuthHeader)
}

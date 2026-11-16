package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomProviders_CRUD(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test_custom_providers.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()

	// 1. Initial list should be empty
	providers, err := s.ListCustomProviders(ctx)
	require.NoError(t, err)
	require.Empty(t, providers)

	// 2. Create custom provider
	apiKey := "enc-secret-key-123"
	p := CustomProviderRecord{
		ID:               "local-ollama",
		Label:            "Local Ollama",
		BaseURL:          "http://127.0.0.1:11434/v1",
		Protocol:         "openai-compatible",
		APIKeyCiphertext: &apiKey,
		Enabled:          true,
	}
	models := []CustomProviderModelRecord{
		{
			ID:            "qwen2.5-coder:7b",
			DisplayName:   "Qwen 2.5 Coder 7B",
			ContextWindow: 32768,
			MaxTokens:     4096,
			SupportsTools: true,
		},
		{
			ID:            "llama3.2:latest",
			DisplayName:   "Llama 3.2 Latest",
			ContextWindow: 131072,
			MaxTokens:     8192,
			SupportsTools: false,
		},
	}

	err = s.CreateCustomProvider(ctx, p, models)
	require.NoError(t, err)

	// 3. Get custom provider
	got, err := s.GetCustomProvider(ctx, "local-ollama")
	require.NoError(t, err)
	require.Equal(t, "local-ollama", got.Provider.ID)
	require.Equal(t, "Local Ollama", got.Provider.Label)
	require.Equal(t, "http://127.0.0.1:11434/v1", got.Provider.BaseURL)
	require.Equal(t, "openai-compatible", got.Provider.Protocol)
	require.True(t, got.Provider.Enabled)
	require.NotNil(t, got.Provider.APIKeyCiphertext)
	require.Equal(t, apiKey, *got.Provider.APIKeyCiphertext)
	require.Len(t, got.Models, 2)
	require.Equal(t, "llama3.2:latest", got.Models[0].ID) // Ordered by id ASC
	require.False(t, got.Models[0].SupportsTools)
	require.Equal(t, "qwen2.5-coder:7b", got.Models[1].ID)
	require.True(t, got.Models[1].SupportsTools)

	// 4. List active custom models
	activeModels, err := s.ListActiveCustomModels(ctx)
	require.NoError(t, err)
	require.Len(t, activeModels, 2)
	require.Equal(t, "local-ollama", activeModels[0].ProviderID)

	// 5. Update custom provider (disable, change label, replace models)
	p.Label = "Renamed Ollama"
	p.Enabled = false
	newModels := []CustomProviderModelRecord{
		{
			ID:            "deepseek-r1:14b",
			DisplayName:   "DeepSeek R1 14B",
			ContextWindow: 65536,
			MaxTokens:     8192,
			SupportsTools: true,
		},
	}
	err = s.UpdateCustomProvider(ctx, p, newModels)
	require.NoError(t, err)

	updated, err := s.GetCustomProvider(ctx, "local-ollama")
	require.NoError(t, err)
	require.Equal(t, "Renamed Ollama", updated.Provider.Label)
	require.False(t, updated.Provider.Enabled)
	require.Len(t, updated.Models, 1)
	require.Equal(t, "deepseek-r1:14b", updated.Models[0].ID)

	// Since provider is disabled, ListActiveCustomModels should now return empty
	activeModels, err = s.ListActiveCustomModels(ctx)
	require.NoError(t, err)
	require.Empty(t, activeModels)

	// Re-enable provider
	p.Enabled = true
	err = s.UpdateCustomProvider(ctx, p, newModels)
	require.NoError(t, err)

	activeModels, err = s.ListActiveCustomModels(ctx)
	require.NoError(t, err)
	require.Len(t, activeModels, 1)
	require.Equal(t, "deepseek-r1:14b", activeModels[0].ID)

	// 6. Delete custom provider cascades to models
	err = s.DeleteCustomProvider(ctx, "local-ollama")
	require.NoError(t, err)

	_, err = s.GetCustomProvider(ctx, "local-ollama")
	require.ErrorIs(t, err, ErrCustomProviderNotFound)

	activeModels, err = s.ListActiveCustomModels(ctx)
	require.NoError(t, err)
	require.Empty(t, activeModels)

	// Verify child table is clean via foreign key cascade
	var childCount int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custom_provider_models WHERE provider_id = ?`, "local-ollama").Scan(&childCount)
	require.NoError(t, err)
	require.Equal(t, 0, childCount)
}

func TestCustomProviders_ValidationAndErrors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test_custom_providers_val.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()

	// Empty ID
	err = s.CreateCustomProvider(ctx, CustomProviderRecord{Label: "Test", BaseURL: "http://localhost"}, nil)
	require.Error(t, err)

	// Empty Label
	err = s.CreateCustomProvider(ctx, CustomProviderRecord{ID: "test", BaseURL: "http://localhost"}, nil)
	require.Error(t, err)

	// Empty BaseURL
	err = s.CreateCustomProvider(ctx, CustomProviderRecord{ID: "test", Label: "Test"}, nil)
	require.Error(t, err)

	// Get non-existent
	_, err = s.GetCustomProvider(ctx, "non-existent")
	require.ErrorIs(t, err, ErrCustomProviderNotFound)

	// Update non-existent
	err = s.UpdateCustomProvider(ctx, CustomProviderRecord{ID: "non-existent", Label: "X", BaseURL: "http://localhost"}, nil)
	require.ErrorIs(t, err, ErrCustomProviderNotFound)

	// Delete non-existent
	err = s.DeleteCustomProvider(ctx, "non-existent")
	require.ErrorIs(t, err, ErrCustomProviderNotFound)
}

func TestCustomProviders_SubagentAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test_custom_providers_subagent.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()

	// Provider "custom-ollama" should initially not be admitted
	admitted, err := s.isAdmittedSubagentProviderTx(ctx, "custom-ollama")
	require.NoError(t, err)
	require.False(t, admitted)

	// Insert "ollama" into custom_providers
	err = s.CreateCustomProvider(ctx, CustomProviderRecord{
		ID:      "ollama",
		Label:   "Ollama",
		BaseURL: "http://127.0.0.1:11434/v1",
	}, []CustomProviderModelRecord{
		{ID: "qwen2.5:7b", DisplayName: "Qwen"},
	})
	require.NoError(t, err)

	// Now "custom-ollama" must be admitted
	admitted, err = s.isAdmittedSubagentProviderTx(ctx, "custom-ollama")
	require.NoError(t, err)
	require.True(t, admitted)

	// Built-in providers still admitted
	admitted, err = s.isAdmittedSubagentProviderTx(ctx, "google-vertex")
	require.NoError(t, err)
	require.True(t, admitted)

	// Unrelated custom provider not admitted
	admitted, err = s.isAdmittedSubagentProviderTx(ctx, "custom-unknown")
	require.NoError(t, err)
	require.False(t, admitted)

	// Delete provider, admission should be revoked
	err = s.DeleteCustomProvider(ctx, "ollama")
	require.NoError(t, err)

	admitted, err = s.isAdmittedSubagentProviderTx(ctx, "custom-ollama")
	require.NoError(t, err)
	require.False(t, admitted)
}

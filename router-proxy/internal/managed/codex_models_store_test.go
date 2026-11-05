package managed

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestLoadCodexModelsStoreAddsSupportedMissingModels(t *testing.T) {
	if !isolatedCodexCatalogTest(t) {
		return
	}
	dir := t.TempDir()
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	model.ID, model.Name = "store-fixture-model", "Store Fixture Model"
	require.Nil(t, ai.GetModel("openai-codex", model.ID), "fixture must not already be registered")
	invalid := model
	invalid.ID, invalid.BaseURL = "wrong-endpoint", "https://example.org"
	existing := model
	existing.ID, existing.Name = "gpt-6-sol", "must not replace SDK"
	data, err := json.Marshal(map[string]any{"openai-codex": map[string]any{"models": []ai.Model{model, invalid, existing}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models-store.json"), data, 0600))
	require.NoError(t, LoadCodexModelsStore(dir))
	resolved := ai.GetModel("openai-codex", model.ID)
	require.NotNil(t, resolved)
	require.Equal(t, model.Name, resolved.Name)
	require.Equal(t, model.ContextWindow, resolved.ContextWindow)
	require.Equal(t, "GPT-6 Sol", ai.GetModel("openai-codex", "gpt-6-sol").Name)
	require.Nil(t, ai.GetModel("openai-codex", invalid.ID))
}

func TestLoadCodexModelsStoreMissingAndMalformed(t *testing.T) {
	if !isolatedCodexCatalogTest(t) {
		return
	}
	dir := t.TempDir()
	require.NoError(t, LoadCodexModelsStore(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models-store.json"), []byte("{"), 0600))
	require.Error(t, LoadCodexModelsStore(dir))
}

// The SDK has no unregister API. A fresh test process gives every catalog test
// the real builtins without leaking registration into other package tests.
func isolatedCodexCatalogTest(t *testing.T) bool {
	t.Helper()
	if os.Getenv("MECHA_CODEX_CATALOG_TEST") == t.Name() {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=30s")
	// Instrumented test binaries need an explicit coverage directory on exit.
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		cmd.Args = append(cmd.Args, "-test.gocoverdir="+dir)
	}
	cmd.Env = append(os.Environ(), "MECHA_CODEX_CATALOG_TEST="+t.Name())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return false
}

func TestLoadCodexModelsStoreTerra(t *testing.T) {
	for _, scenario := range []string{"empty-path", "missing", "empty-store", "malformed", "read-error", "valid-supplement", "invalid-supplement", "existing"} {
		t.Run(scenario, func(t *testing.T) {
			if !isolatedCodexCatalogTest(t) {
				return
			}
			require.Nil(t, ai.GetModel("openai-codex", "gpt-6.1-terra"), "test requires SDK without Terra")
			dir := t.TempDir()
			path := filepath.Join(dir, "models-store.json")
			var wantErr error
			var authoritative *ai.Model
			switch scenario {
			case "empty-path":
				dir = ""
			case "empty-store":
				require.NoError(t, os.WriteFile(path, []byte(`{}`), 0600))
			case "malformed":
				data := []byte(`{"openai-codex":{"models":[]},`)
				require.NoError(t, os.WriteFile(path, data, 0600))
				var decoded any
				wantErr = json.Unmarshal(data, &decoded)
			case "read-error":
				require.NoError(t, os.Mkdir(path, 0700))
				_, wantErr = os.ReadFile(path)
			case "valid-supplement", "invalid-supplement", "existing":
				model := *ai.GetModel("openai-codex", "gpt-6-sol")
				model.ID, model.Name = "gpt-6.1-terra", "authoritative catalog entry"
				model.ContextWindow = 42
				if scenario == "invalid-supplement" {
					model.BaseURL = "https://example.org"
				}
				data, err := json.Marshal(map[string]any{"openai-codex": map[string]any{"models": []*ai.Model{nil, &model}}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, data, 0600))
				if scenario == "existing" {
					model.Name = "SDK entry wins"
					ai.RegisterModel(&model)
					authoritative = &model
				} else if scenario == "valid-supplement" {
					authoritative = &model
				}
			}
			err := LoadCodexModelsStore(dir)
			require.Equal(t, wantErr, err, "original store error must survive fallback")
			resolved, err := ResolveModel(store.ManagedModel{Provider: "openai-codex", ID: "gpt-6.1-terra"})
			require.NoError(t, err)
			require.NoError(t, validateEmbeddedCodexModel(resolved))
			require.True(t, ai.IsModelType(resolved, ai.ModelTypeChat))
			if authoritative != nil {
				require.Equal(t, authoritative, resolved)
			} else {
				require.Equal(t, "gpt-6.1-terra", resolved.ID)
				require.Equal(t, "openai-codex", resolved.Provider)
				require.Equal(t, ai.APIOpenAICodexResponses, resolved.Api)
				require.Equal(t, "https://chatgpt.com/backend-api", resolved.BaseURL)
				require.Empty(t, resolved.Headers)
				require.Equal(t, []string{"text", "image"}, resolved.Input)
				require.Equal(t, 1050000, resolved.ContextWindow)
				require.Equal(t, 128000, resolved.MaxTokens)
				require.Equal(t, ai.ModelCost{Input: 1, CacheRead: 0.05, Output: 5}, resolved.Cost)
				require.Empty(t, resolved.ThinkingLevelMap)
				require.Empty(t, resolved.Compat)
			}
			require.Equal(t, wantErr, LoadCodexModelsStore(dir))
			require.Same(t, resolved, ai.GetModel("openai-codex", "gpt-6.1-terra"))
			count := 0
			for _, model := range ai.GetModels("openai-codex") {
				if model.ID == "gpt-6.1-terra" {
					count++
				}
			}
			require.Equal(t, 1, count)
		})
	}
}

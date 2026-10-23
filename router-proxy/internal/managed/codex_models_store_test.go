package managed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestLoadCodexModelsStoreAddsSupportedMissingModels(t *testing.T) {
	dir := t.TempDir()
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	model.ID, model.Name = "gpt-6.1-sol", "GPT-6.1 Sol"
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
	dir := t.TempDir()
	require.NoError(t, LoadCodexModelsStore(dir))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "models-store.json"), []byte("{"), 0600))
	require.Error(t, LoadCodexModelsStore(dir))
}

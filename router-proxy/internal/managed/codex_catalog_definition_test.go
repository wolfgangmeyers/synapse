package managed

import (
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestFreshCodexSol61Definition(t *testing.T) {
	require.NoError(t, LoadCodexModelsStore(t.TempDir()))
	model, err := ResolveModel(store.ManagedModel{Provider: "openai-codex", ID: "gpt-6.1-sol"})
	require.NoError(t, err)
	require.NotNil(t, model)
	require.NoError(t, validateEmbeddedCodexModel(model))
	require.True(t, ai.IsModelType(model, ai.ModelTypeChat))
	require.Equal(t, "GPT-6.1 Sol", model.Name)
	require.Equal(t, 500000, model.ContextWindow)
	require.Equal(t, 128000, model.MaxTokens)
	require.Equal(t, []string{"text", "image"}, model.Input)
	require.True(t, model.Reasoning)
	require.Equal(t, 0.1, model.Cost.CacheRead)
	require.Len(t, model.Cost.Tiers, 1)
	_, hasOff := model.ThinkingLevelMap["off"]
	require.True(t, hasOff)
	require.Nil(t, model.ThinkingLevelMap["off"])
	require.NotNil(t, model.InputLimits)
	require.JSONEq(t, `{"supportsOpenAIGrammarTools":true,"supportsAdditionalTools":true,"supportsToolSearch":true,"supportsMidConvoSystemMessages":true}`, string(model.Compat))
	require.Equal(t, "GPT-6 Sol", ai.GetModel("openai-codex", "gpt-6-sol").Name)
}

package managed

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sky-valley/pi/ai"
)

// LoadCodexModelsStore adds Codex models learned by ordinary Pi to the SDK
// catalog, then fills missing shipped Codex entries. The SDK's built-in entries remain authoritative for existing IDs.
func LoadCodexModelsStore(piAgentDir string) error {
	// Apply even when the supplemental store is absent or invalid, without
	// changing its error. Deferring keeps validated supplements authoritative.
	defer registerMissingCodexModels()
	if piAgentDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(piAgentDir, "models-store.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var stored struct {
		Codex struct {
			Models []*ai.Model `json:"models"`
		} `json:"openai-codex"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	for _, model := range stored.Codex.Models {
		if model == nil || ValidateSelectedModel(Model{Provider: model.Provider, ID: model.ID}) != nil || model.Name == "" || model.ContextWindow <= 0 || model.MaxTokens <= 0 ||
			ai.GetModel("openai-codex", model.ID) != nil || validateEmbeddedCodexModel(model) != nil ||
			!ai.IsModelType(model, ai.ModelTypeChat) {
			continue
		}
		// ponytail: preserve the SDK catalog and only fill missing Codex entries.
		ai.RegisterModel(model)
	}
	return nil
}

// registerMissingCodexModels fills release gaps in the pinned SDK catalog.
// Selection and execution both use this central registry, not a picker overlay.
func registerMissingCodexModels() {
	if ai.GetModel("openai-codex", "gpt-6.1-terra") != nil {
		return
	}
	ai.RegisterModel(&ai.Model{
		Type:          ai.ModelTypeChat,
		ID:            "gpt-6.1-terra",
		Name:          "GPT-6.1 Terra",
		Provider:      "openai-codex",
		Api:           ai.APIOpenAICodexResponses,
		BaseURL:       "https://chatgpt.com/backend-api",
		Input:         []string{"text", "image"},
		ContextWindow: 1050000,
		MaxTokens:     128000,
		Cost:          ai.ModelCost{Input: 1, CacheRead: 0.05, Output: 5},
	})
}

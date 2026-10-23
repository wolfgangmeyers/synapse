package managed

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sky-valley/pi/ai"
)

// LoadCodexModelsStore adds Codex models learned by ordinary Pi to the SDK
// catalog. The SDK's built-in entries remain authoritative for existing IDs.
func LoadCodexModelsStore(piAgentDir string) error {
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

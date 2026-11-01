package managed

import (
	_ "embed"
	"encoding/json"

	"github.com/sky-valley/pi/ai"
)

// The pinned Go SDK predates this entry. Preserve the complete definition from
// @earendil-works/pi-ai 1.4.0, providers/data/openai-codex.json,
// openai-codex-responses/chat:gpt-6.1-sol. Bundle it so a fresh installation
// needs neither an ordinary Pi models-store nor network discovery to launch.
//
//go:embed codex-sol61.json
var codexSol61Definition []byte

func init() {
	// GetModel loads SDK built-ins first; a future SDK definition takes precedence.
	if ai.GetModel("openai-codex", "gpt-6.1-sol") != nil {
		return
	}
	var model ai.Model
	if json.Unmarshal(codexSol61Definition, &model) != nil ||
		model.ID != "gpt-6.1-sol" || model.Name == "" ||
		model.ContextWindow <= 0 || model.MaxTokens <= 0 ||
		!ai.IsModelType(&model, ai.ModelTypeChat) || validateEmbeddedCodexModel(&model) != nil {
		panic("invalid bundled Codex model definition")
	}
	ai.RegisterModel(&model)
}

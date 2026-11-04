package managed

import (
	"fmt"
)

const claudeCodeProvider = "claude-code"

// These are selectable CLI aliases, not a provider-auth or sign-in snapshot.
func claudeCodeModels() []Model {
	return []Model{
		{Provider: claudeCodeProvider, ID: "fable", Name: "Claude Fable", Available: true, ContextWindow: 1000000},
		{Provider: claudeCodeProvider, ID: "opus", Name: "Claude Opus", Available: true, ContextWindow: 1000000},
		{Provider: claudeCodeProvider, ID: "sonnet", Name: "Claude Sonnet", Available: true, ContextWindow: 1000000},
		{Provider: claudeCodeProvider, ID: "haiku", Name: "Claude Haiku", Available: true, ContextWindow: 1000000},
	}
}

func claudeCodeModel(id string) (Model, error) {
	for _, model := range claudeCodeModels() {
		if model.ID == id {
			return model, nil
		}
	}
	return Model{}, &modelUnavailableError{claudeCodeProvider, id}
}

func withClaudeCodeModels(catalog Catalog) Catalog {
	models := make([]Model, 0, len(catalog.Models)+4)
	for _, model := range catalog.Models {
		// Only the static alias table defines this engine; never persist auth state.
		if model.Provider != claudeCodeProvider {
			models = append(models, model)
		}
	}
	catalog.Models = append(models, claudeCodeModels()...)
	return catalog
}

// EngineSelectionError is a public selection restriction, not a CLI failure.
type EngineSelectionError struct{ Reason string }

func (e *EngineSelectionError) Error() string { return e.Reason }

// ClaudeUnavailableError preserves actionable local sign-in guidance at HTTP boundaries.
type ClaudeUnavailableError struct{ Cause error }

func (e *ClaudeUnavailableError) Error() string {
	return fmt.Sprintf("Claude CLI unavailable; install Claude and sign in on the router host with claude: %v", e.Cause)
}
func (e *ClaudeUnavailableError) Unwrap() error { return e.Cause }

package managed

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/claudemodels"
)

const claudeCodeProvider = claudemodels.Provider

// These are selectable CLI aliases, not a provider-auth or sign-in snapshot.
func claudeCodeModels() []Model {
	var models []Model
	for _, alias := range claudemodels.Catalog() {
		models = append(models, Model{Provider: claudeCodeProvider, ID: alias.ID, Name: alias.Name, Available: true, ContextWindow: 1000000})
	}
	return models
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

// ClaudeAdmissionFencedError reports that Claude process safety blocks launch.
type ClaudeAdmissionFencedError struct{ Cause error }

func (e *ClaudeAdmissionFencedError) Error() string {
	message := "Claude process admission is fenced"
	if e.Cause == nil {
		return message + "; process pool closed"
	}
	var recovery *claudeSafeRecoveryError
	if errors.As(e.Cause, &recovery) {
		return message + "; " + recovery.Error()
	}
	var errno syscall.Errno
	if errors.As(e.Cause, &errno) {
		return fmt.Sprintf("%s; cleanup failed (operating system error: %s)", message, errno.Error())
	}
	if errors.Is(e.Cause, errClaudeCodeGroupUnconfirmed) {
		return message + "; Claude group absence unconfirmed"
	}
	return message + "; cleanup or recovery failed"
}
func (e *ClaudeAdmissionFencedError) Unwrap() error { return e.Cause }

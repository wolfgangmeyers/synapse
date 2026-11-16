package managed

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/antigravitymodels"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const antigravityProvider = antigravitymodels.Provider

// These are selectable CLI models, not an OAuth helper or credential snapshot.
func antigravityModels() []Model {
	var models []Model
	for _, m := range antigravitymodels.Catalog() {
		models = append(models, Model{
			Provider:      antigravityProvider,
			ID:            m.ID,
			Name:          m.Name,
			Available:     true,
			ContextWindow: m.ContextWindow,
		})
	}
	return models
}

func antigravityModel(id string) (Model, error) {
	for _, model := range antigravityModels() {
		if model.ID == id {
			return model, nil
		}
	}
	return Model{}, &modelUnavailableError{antigravityProvider, id}
}

func isAntigravityProvider(provider string) bool {
	return provider == antigravityProvider
}

func isAntigravityModel(model store.ManagedModel) bool {
	return model.Provider == antigravityProvider
}

func isClaudeCodeProvider(provider string) bool {
	return provider == claudeCodeProvider
}

func isCLIProvider(provider string) bool {
	return provider == claudeCodeProvider || provider == antigravityProvider
}

func isCLIModel(model store.ManagedModel) bool {
	return isCLIProvider(model.Provider)
}

func engineOf(provider string) string {
	switch provider {
	case claudeCodeProvider:
		return claudeCodeProvider
	case antigravityProvider:
		return antigravityProvider
	default:
		return "pi"
	}
}

func withAntigravityModels(catalog Catalog) Catalog {
	models := make([]Model, 0, len(catalog.Models)+4)
	for _, model := range catalog.Models {
		if model.Provider != antigravityProvider {
			models = append(models, model)
		}
	}
	catalog.Models = append(models, antigravityModels()...)
	return catalog
}

// AntigravityUnavailableError preserves actionable local sign-in guidance at HTTP boundaries.
type AntigravityUnavailableError struct{ Cause error }

func (e *AntigravityUnavailableError) Error() string {
	return fmt.Sprintf("Antigravity CLI unavailable; install agy and sign in on the router host with agy: %v", e.Cause)
}
func (e *AntigravityUnavailableError) Unwrap() error { return e.Cause }

// AntigravityAdmissionFencedError reports that Antigravity process safety blocks launch.
type AntigravityAdmissionFencedError struct{ Cause error }

func (e *AntigravityAdmissionFencedError) Error() string {
	message := "Antigravity process admission is fenced"
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
	return fmt.Sprintf("%s (%v)", message, e.Cause)
}
func (e *AntigravityAdmissionFencedError) Unwrap() error { return e.Cause }

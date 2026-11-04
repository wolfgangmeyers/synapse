package managed

import (
	"context"
	"errors"
	"time"

	"github.com/sky-valley/pi/ai"
)

// Claude never chooses an evaluator by searching the catalog. Verify the one
// configured Pi default can construct an authenticated stream before claiming
// evaluation. Token acquisition may fail; that is unavailability, not NOT_DONE.
func (m *Manager) claudeCodeEvaluatorModel(ctx context.Context) (*ai.Model, error) {
	unavailable := errors.New("Claude goal evaluation unavailable: configure an authenticated Pi router default")
	selected, err := m.store.GetDefaultModel(ctx)
	if err != nil || selected == nil || selected.Provider == claudeCodeProvider {
		return nil, unavailable
	}
	if _, err = m.admitEmbeddedModel(ctx, Model{Provider: selected.Provider, ID: selected.ID}); err != nil {
		return nil, unavailable
	}
	model, err := ResolveModel(*selected)
	if err != nil || model == nil || model.ContextWindow <= embeddedContextHeadroom {
		return nil, unavailable
	}
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch model.Provider {
	case "google":
		_, err = m.resolveGoogleAPIKey(authCtx)
	case "google-vertex":
		_, _, _, err = m.embeddedVertexAuth(authCtx, false, model.ID)
	case "openai-codex":
		_, err = m.embeddedCodexAuth(authCtx)
	default:
		return nil, unavailable
	}
	if err != nil {
		return nil, unavailable
	}
	return model, nil
}

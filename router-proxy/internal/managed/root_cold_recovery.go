package managed

import (
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// managedLaunchRecipe records the fixed, non-secret inputs used by a managed
// launch. Listener URL, runtime generation, and session epoch are per-runtime
// values and are supplied when a process starts.
func (m *Manager) managedLaunchRecipe(agent store.ManagedAgentRecord) (store.ManagedLaunchRecipe, error) {
	environment := ChildEnvironmentFor(m.config, "", agent.SessionEpoch)
	stable := make([]string, 0, len(environment))
	for _, value := range environment {
		key, _, _ := strings.Cut(value, "=")
		// These values are derived from the current router instance. Persisting
		// them would make a recipe fail after a normal listener restart.
		if key == "MECHA_ROUTER_URL" || key == "MECHA_RPC_RUNTIME_GENERATION" || key == "MECHA_RPC_SESSION_EPOCH" {
			continue
		}
		// Only the fixed launch environment is durable. Unknown test or host
		// variables may contain credentials and are intentionally excluded.
		if !store.IsManagedLaunchEnvironmentKey(key) {
			continue
		}
		stable = append(stable, value)
	}
	recipe := store.ManagedLaunchRecipe{
		PiExecutable:         m.config.PiExecutable,
		SessionRoot:          m.config.SessionRoot,
		Cwd:                  agent.Cwd,
		Provider:             agent.SelectedModel.Provider,
		ModelID:              agent.SelectedModel.ID,
		MechaRPCExtension:    m.config.MechaRPCExtension,
		PiSubagentsExtension: m.config.PiSubagentsExtension,
		CursorExtension:      m.config.CursorExtension,
		TestOnlyExtensions:   append([]string(nil), m.config.TestOnlyExtensions...),
		Environment:          stable,
	}
	return store.ManagedLaunchRecipeDigest(recipe)
}

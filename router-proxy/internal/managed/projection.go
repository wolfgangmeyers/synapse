package managed

import (
	"encoding/json"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// PublicAgent is the safe first-slice projection. It contains no PID, session
// identity, auth path, environment, or raw process output.
func PublicAgent(agent *store.ManagedAgentRecord) map[string]any {
	if agent == nil {
		return nil
	}
	result := map[string]any{
		"agent_id": agent.AgentID, "source": "router-managed", "cwd": agent.Cwd,
		"model":         map[string]any{"provider": agent.SelectedModel.Provider, "id": agent.SelectedModel.ID},
		"desired_state": agent.DesiredState, "lifecycle_state": agent.LifecycleState, "activity_state": agent.ActivityState,
		"accepting_prompts": agent.AcceptingPrompts,
		"manifest":          map[string]any{"revision": agent.ManifestRevision, "sha256": agent.ManifestHash, "installed_tool_count": 16},
		"created_at":        agent.CreatedAt, "started_at": nil, "updated_at": agent.UpdatedAt, "last_error": nil,
	}
	if agent.StartedAt != "" {
		result["started_at"] = agent.StartedAt
	}
	if agent.LastErrorCode != "" {
		// The browser needs the stable code, not raw process diagnostics. Older
		// durable rows may predate current redaction rules, so do not make their
		// text a browser-visible field at all.
		result["last_error"] = map[string]any{"code": agent.LastErrorCode, "message": "Managed worker reported an operational error", "at": agent.LastErrorAt}
	}
	return result
}
func ProjectionEnvelope(snapshot store.ManagedAgentSnapshot) map[string]any {
	return map[string]any{"generated_at": snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), "projection_version": snapshot.ProjectionVersion, "agent": PublicAgent(snapshot.Agent)}
}

// ProjectionListEnvelope is the direct managed-list counterpart of
// ProjectionEnvelope. It includes failed and stopped durable rows so the UI
// can show lifecycle recovery state without learning private runtime fields.
func ProjectionListEnvelope(snapshot store.ManagedAgentsSnapshot) map[string]any {
	agents := make([]map[string]any, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		agents = append(agents, PublicAgent(agent))
	}
	return map[string]any{"generated_at": snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), "projection_version": snapshot.ProjectionVersion, "agents": agents}
}
func MarshalProjectionEnvelope(snapshot store.ManagedAgentSnapshot) ([]byte, error) {
	return json.Marshal(ProjectionEnvelope(snapshot))
}

package managed

import (
	"encoding/json"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func installedToolCount(manifestRevision int) int {
	if manifestRevision == managedManifestRevision {
		return len(managedManifestOperations)
	}
	// Durable rows from the immutable first manifest can survive a router
	// update. Keep their public projection truthful during replacement/recovery.
	return len(baselineManagedNames)
}

// PublicAgent is the safe first-slice projection. It contains no PID, session
// identity, auth path, environment, or raw process output.
func PublicAgent(agent *store.ManagedAgentRecord) map[string]any {
	if agent == nil {
		return nil
	}
	result := map[string]any{
		"agent_id": agent.AgentID, "source": "router-managed", "cwd": agent.Cwd, "runtime": agent.Runtime,
		"model":         map[string]any{"provider": agent.SelectedModel.Provider, "id": agent.SelectedModel.ID},
		"desired_state": agent.DesiredState, "lifecycle_state": agent.LifecycleState, "activity_state": agent.ActivityState,
		"accepting_prompts": agent.AcceptingPrompts,
		"manifest":          map[string]any{"revision": agent.ManifestRevision, "sha256": agent.ManifestHash, "installed_tool_count": installedToolCount(agent.ManifestRevision)},
		"created_at":        agent.CreatedAt, "started_at": nil, "updated_at": agent.UpdatedAt, "last_error": nil,
	}
	if agent.StartedAt != "" {
		result["started_at"] = agent.StartedAt
	}
	if agent.LastErrorCode != "" {
		// Durable rows can outlive a redaction change. Sanitize the bounded local
		// cause again at the public boundary before the Web UI can render it.
		result["last_error"] = map[string]any{"code": agent.LastErrorCode, "message": store.RedactedManagedDiagnostic(agent.LastErrorMessage), "at": agent.LastErrorAt}
	}
	return result
}

// PublicAgentWithGoal adds only the embedded agent's own durable desired
// policy. Applied revision and projection state remain separate from SDK set.
func PublicAgentWithGoal(agent *store.ManagedAgentRecord, policy *store.ManagedGoalPolicy) map[string]any {
	result := PublicAgent(agent)
	if result == nil || agent.Runtime != "embedded" || policy == nil || policy.AgentID != agent.AgentID {
		return result
	}
	settings := policy.Config.Settings
	goal := policy.Config.Goal
	if !policy.Config.Enabled {
		goal = ""
	}
	result["goal_state"] = map[string]any{
		"enabled": policy.Config.Enabled, "goal": goal,
		"desired_revision": policy.DesiredRevision, "applied_revision": policy.AppliedRevision,
		"projection_state":  policy.ProjectionState,
		"goal_lock_enabled": settings.GoalLockEnabled, "snooze_enabled": settings.SnoozeEnabled,
		"snooze_duration_ms": settings.SnoozeDurationMS, "snooze_turn_limit": settings.SnoozeTurnLimit,
		"snooze_budget_cap_ms":   settings.SnoozeBudgetCapMS,
		"snooze_refill_yield_ms": settings.SnoozeRefillYieldMS, "snooze_refill_elapsed_ms": settings.SnoozeRefillElapsedMS,
	}
	return result
}

func PublicArchive(archive *store.ManagedAgentArchive) map[string]any {
	if archive == nil {
		return nil
	}
	return map[string]any{
		"record_kind": archive.RecordKind, "lifecycle_state": archive.LifecycleState,
		"agent_id": archive.AgentID, "name": archive.Name, "archive_key": archive.ArchiveKey,
		"terminal_outcome": archive.TerminalOutcome, "completed_at": archive.CompletedAt,
		"delivery": map[string]any{
			"disposition":            archive.Delivery.Disposition,
			"total":                  archive.Delivery.Total,
			"queued_never_attempted": archive.Delivery.QueuedNeverAttempted,
			"in_flight_or_unknown":   archive.Delivery.InFlightOrUnknown,
			"completed":              archive.Delivery.Completed,
			"released_or_failed":     archive.Delivery.ReleasedOrFailed,
			"overflow": map[string]any{
				"total":                  archive.Delivery.TotalOverflow,
				"queued_never_attempted": archive.Delivery.QueuedNeverAttemptedOverflow,
				"in_flight_or_unknown":   archive.Delivery.InFlightOrUnknownOverflow,
				"completed":              archive.Delivery.CompletedOverflow,
				"released_or_failed":     archive.Delivery.ReleasedOrFailedOverflow,
			},
		},
	}
}

// ManagedRecordKey is the stable identity used by the managed projection. A
// reusable agent ID cannot identify both a current worker and old archives.
func ManagedRecordKey(record map[string]any) string {
	if record == nil {
		return ""
	}
	if archiveKey, ok := record["archive_key"].(string); ok && record["record_kind"] == "archived" {
		return "archive:" + archiveKey
	}
	if agentID, ok := record["agent_id"].(string); ok {
		return "live:" + agentID
	}
	return ""
}

func ProjectionEnvelope(snapshot store.ManagedAgentSnapshot) map[string]any {
	return map[string]any{"generated_at": snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), "projection_version": snapshot.ProjectionVersion, "agent": PublicAgentWithGoal(snapshot.Agent, snapshot.GoalPolicy)}
}

func ProjectionArchiveEnvelope(archive *store.ManagedAgentArchive, generatedAt time.Time, projectionVersion int64) map[string]any {
	return map[string]any{"generated_at": generatedAt.UTC().Format(time.RFC3339Nano), "projection_version": projectionVersion, "agent": PublicArchive(archive)}
}

// ProjectionListEnvelope is the direct managed-list counterpart of
// ProjectionEnvelope. It includes failed and stopped durable rows so the UI
// can show lifecycle recovery state without learning private runtime fields.
func ProjectionListEnvelope(snapshot store.ManagedAgentsSnapshot) map[string]any {
	agents := make([]map[string]any, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		agents = append(agents, PublicAgentWithGoal(agent, snapshot.GoalPolicies[agent.AgentID]))
	}
	return map[string]any{"generated_at": snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), "projection_version": snapshot.ProjectionVersion, "agents": agents}
}
func MarshalProjectionEnvelope(snapshot store.ManagedAgentSnapshot) ([]byte, error) {
	return json.Marshal(ProjectionEnvelope(snapshot))
}

package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// managedChildrenProjection is deliberately separate from managed.PublicAgent.
// The readiness source remains private; this boundary exposes only an
// allowlisted child activity view after proving it belongs to the current
// parent runtime fence.
func (r *Router) managedChildrenProjection(ctx context.Context, agentID string) (map[string]any, error) {
	snapshot, err := r.store.ManagedAgentsProjectionSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, agent := range snapshot.Agents {
		if agent.AgentID == agentID {
			return managedChildrenProjectionForAgent(agent, snapshot.ReadinessSources[agentID]), nil
		}
	}
	return nil, sql.ErrNoRows
}

type managedChildSourceSnapshot struct {
	Runs []struct {
		TaskID        string `json:"task_id"`
		RunID         string `json:"run_id"`
		ParentEntryID string `json:"parent_entry_id"`
		Description   string `json:"description"`
		Model         *struct {
			Provider string `json:"provider"`
			ID       string `json:"id"`
			Name     string `json:"name"`
		} `json:"model"`
		LifecycleState string  `json:"lifecycle_state"`
		StartedAt      *string `json:"started_at"`
		FinishedAt     *string `json:"finished_at"`
		DurationMS     *int64  `json:"duration_ms"`
		Compaction     struct {
			State       string  `json:"state"`
			Count       int64   `json:"count"`
			StartedAt   *string `json:"started_at"`
			LastOutcome *string `json:"last_outcome"`
		} `json:"compaction"`
		Sequence int64 `json:"sequence"`
	} `json:"runs"`
}

func managedChildrenProjectionForAgent(agent *store.ManagedAgentRecord, source *store.ManagedReadinessSource) map[string]any {
	agentID := ""
	if agent != nil {
		agentID = agent.AgentID
	}
	result := map[string]any{"agent_id": agentID, "source": "router-managed", "lifecycle_version": int64(0), "recovery_state": "reconstructing", "children": []map[string]any{}}
	if !managedChildSourceCurrent(agent, source) {
		return result
	}
	result["lifecycle_version"] = source.Snapshot.Sequence
	var snapshot managedChildSourceSnapshot
	if err := json.Unmarshal(source.Snapshot.Canonical, &snapshot); err != nil {
		return result
	}
	children := make([]map[string]any, 0, len(snapshot.Runs))
	for _, run := range snapshot.Runs {
		if !managedChildIdentityFits(run.ParentEntryID, run.TaskID, run.RunID) {
			continue
		}
		// The snapshot was strictly normalized before storage. This second
		// allowlist is still necessary: it is the sole browser projection and
		// deliberately does not expose a Pi session, process identity, fence, or
		// unbounded source value.
		child := map[string]any{
			"id":               managedChildID(agent, run.ParentEntryID, run.TaskID, run.RunID),
			"parent_entry_id":  managedChildDisplayID(run.ParentEntryID),
			"task_id":          managedChildDisplayID(run.TaskID),
			"run_id":           managedChildDisplayID(run.RunID),
			"status":           managedChildStatus(run.LifecycleState),
			"sequence":         run.Sequence,
			"recovery_state":   "current",
			"compaction_state": managedChildCompaction(run.Compaction.State, run.Compaction.LastOutcome),
			"compaction":       managedChildCompactionProjection(run.Compaction.State, run.Compaction.Count, run.Compaction.StartedAt, run.Compaction.LastOutcome),
		}
		if run.Description != "" {
			child["description"] = boundedManagedDisplay(managed.RedactedStderr(run.Description), 512)
		}
		if model := managedChildModel(run.Model); model != nil {
			child["model"] = model
		}
		if at, ok := managedChildTimestamp(run.StartedAt); ok {
			child["started_at"] = at
		}
		if at, ok := managedChildTimestamp(run.FinishedAt); ok {
			child["ended_at"] = at
		}
		// Never run a browser-side elapsed clock. The runtime's explicit duration
		// or terminal timestamp is the only timing value that survives recovery.
		if run.DurationMS != nil {
			child["duration_ms"] = *run.DurationMS
		}
		children = append(children, child)
	}
	sort.Slice(children, func(left, right int) bool { return children[left]["id"].(string) < children[right]["id"].(string) })
	result["recovery_state"] = "current"
	result["children"] = children
	return result
}

func managedChildSourceCurrent(agent *store.ManagedAgentRecord, source *store.ManagedReadinessSource) bool {
	if agent == nil || source == nil || source.Fence == nil || source.Snapshot == nil || source.AgentID != agent.AgentID || source.Snapshot.OwnerSessionID != agent.PiSessionID {
		return false
	}
	fence := source.Fence
	return fence.AgentID == agent.AgentID && fence.PiSessionID == agent.PiSessionID && fence.SessionEpoch == agent.SessionEpoch && fence.ProcessPID == agent.ProcessPID && fence.ProcessStartMarker == agent.ProcessStartMarker && fence.ProcessIncarnation == agent.ProcessIncarnation && fence.RuntimeGeneration == agent.RuntimeGeneration && fence.ManifestRevision == agent.ManifestRevision && fence.ManifestHash == agent.ManifestHash
}

func managedChildIdentityFits(parts ...string) bool {
	for _, part := range parts {
		if part == "" || len([]byte(part)) > 128 {
			return false
		}
	}
	return true
}

// The child ID is a stable public digest of the complete parent/session/process
// fence plus the source parent entry/task/run tuple. It preserves composite
// identity across frames without leaking session or process identifiers.
func managedChildID(agent *store.ManagedAgentRecord, parentEntryID, taskID, runID string) string {
	parts := []string{agent.AgentID, agent.PiSessionID, agent.ProcessStartMarker, agent.ProcessIncarnation, agent.RuntimeGeneration, parentEntryID, taskID, runID}
	joined := ""
	for _, part := range parts {
		joined += part + "\x00"
	}
	sum := sha256.Sum256([]byte(joined))
	return "child-" + hex.EncodeToString(sum[:16])
}

func managedChildDisplayID(value string) string {
	return boundedManagedDisplay(managed.RedactedStderr(value), 128)
}

func managedChildModel(model *struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}) map[string]any {
	if model == nil || model.Provider == "" || model.ID == "" || len([]byte(model.Provider)) > 128 || len([]byte(model.ID)) > 256 || len([]byte(model.Name)) > 256 {
		return nil
	}
	result := map[string]any{"provider": model.Provider, "id": model.ID}
	if model.Name != "" {
		result["name"] = model.Name
	}
	return result
}

func managedChildCompactionProjection(state string, count int64, startedAt, outcome *string) map[string]any {
	result := map[string]any{"state": state, "count": count}
	if at, ok := managedChildTimestamp(startedAt); ok {
		result["started_at"] = at
	}
	if outcome != nil && *outcome != "" {
		result["last_outcome"] = *outcome
	}
	return result
}

func managedChildrenSnapshot(ctx context.Context, durable *store.Store) (int64, string, []map[string]any, error) {
	snapshot, err := durable.ManagedAgentsProjectionSnapshot(ctx)
	if err != nil {
		return 0, "", nil, err
	}
	return managedChildrenSnapshotFromStored(snapshot)
}

func managedChildrenSnapshotFromStored(snapshot store.ManagedAgentsSnapshot) (int64, string, []map[string]any, error) {
	children := make([]map[string]any, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		children = append(children, managedChildrenProjectionForAgent(agent, snapshot.ReadinessSources[agent.AgentID]))
	}
	// ProjectionVersion is the only global, committed watermark shared by every
	// parent. A max per-child source sequence can regress when another parent
	// changes, which would make reconnect recovery ambiguous.
	return snapshot.ProjectionVersion, snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), children, nil
}

func managedChildStatus(value string) string {
	switch value {
	case "queued", "running", "completed", "steered", "aborted", "stopped", "error":
		return value
	default:
		return "unknown"
	}
}
func managedChildCompaction(state string, outcome *string) string {
	if state == "compacting" {
		return "compacting"
	}
	if outcome != nil && *outcome == "completed" {
		return "completed"
	}
	if state == "idle" {
		return "none"
	}
	return "unknown"
}
func managedChildTimestamp(value *string) (string, bool) {
	if value == nil || *value == "" {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), true
}
func boundedManagedDisplay(value string, limit int) string {
	bytes := []byte(value)
	if len(bytes) > limit {
		bytes = bytes[:limit]
		for len(bytes) > 0 && bytes[len(bytes)-1]&0xc0 == 0x80 {
			bytes = bytes[:len(bytes)-1]
		}
	}
	return string(bytes)
}

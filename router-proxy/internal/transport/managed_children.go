package transport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
			base := managedChildrenProjectionForAgent(agent, snapshot.ReadinessSources[agentID])
			return mergeNativeSubagentsForAgent(ctx, r.store, agent, base)
		}
	}
	return nil, sql.ErrNoRows
}

type managedChildSourceSnapshot struct {
	Runs []managedChildSourceRun `json:"runs"`
}

type managedChildSourceRun struct {
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
}

func managedChildrenProjectionForAgent(agent *store.ManagedAgentRecord, source *store.ManagedReadinessSource) map[string]any {
	agentID := ""
	if agent != nil {
		agentID = agent.AgentID
	}
	result := map[string]any{"agent_id": agentID, "source": "router-managed", "lifecycle_version": int64(0), "recovery_state": "reconstructing", "children": []map[string]any{}}
	recovered := managedRecoveredChildren(agent, source)
	if !managedChildSourceCurrent(agent, source) {
		result["children"] = recovered
		return result
	}
	result["lifecycle_version"] = source.Snapshot.Sequence
	var snapshot managedChildSourceSnapshot
	if err := json.Unmarshal(source.Snapshot.Canonical, &snapshot); err != nil {
		result["children"] = recovered
		return result
	}
	children := make([]map[string]any, 0, len(snapshot.Runs)+len(recovered))
	for _, run := range snapshot.Runs {
		if !managedChildIdentityFits(run.ParentEntryID, run.TaskID, run.RunID) {
			continue
		}
		children = append(children, managedChildProjectionForRun(managedChildID(agent, run.ParentEntryID, run.TaskID, run.RunID), run, map[string]any{"state": "confirmed", "cause": nil, "observed_at": source.SnapshotConfirmedAt}, false))
	}
	children = append(children, recovered...)
	sort.Slice(children, func(left, right int) bool { return children[left]["id"].(string) < children[right]["id"].(string) })
	result["recovery_state"] = "current"
	result["children"] = children
	return result
}

func managedChildProjectionForRun(id string, run managedChildSourceRun, recovery map[string]any, reconcileCompaction bool) map[string]any {
	compaction := managedChildCompactionProjection(run.Compaction.State, run.Compaction.Count, run.Compaction.StartedAt, run.Compaction.LastOutcome)
	if reconcileCompaction && run.Compaction.State == "compacting" {
		compaction = map[string]any{"state": "idle", "count": run.Compaction.Count, "last_outcome": "reconciled_unknown"}
	}
	child := map[string]any{
		"id":               id,
		"parent_entry_id":  managedChildDisplayID(run.ParentEntryID),
		"task_id":          managedChildDisplayID(run.TaskID),
		"run_id":           managedChildDisplayID(run.RunID),
		"status":           managedChildStatus(run.LifecycleState),
		"sequence":         run.Sequence,
		"recovery":         recovery,
		"compaction_state": managedChildCompaction(compaction["state"].(string), managedChildOutcome(compaction)),
		"compaction":       compaction,
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
	return child
}

func managedChildOutcome(compaction map[string]any) *string {
	outcome, _ := compaction["last_outcome"].(string)
	if outcome == "" {
		return nil
	}
	return &outcome
}

func managedRecoveredChildren(agent *store.ManagedAgentRecord, source *store.ManagedReadinessSource) []map[string]any {
	if agent == nil || source == nil || len(source.Recoveries) == 0 {
		return []map[string]any{}
	}
	children := make([]map[string]any, 0, len(source.Recoveries))
	for _, recovery := range source.Recoveries {
		var run managedChildSourceRun
		if json.Unmarshal(recovery.RunCanonical, &run) != nil || !managedChildIdentityFits(run.ParentEntryID, run.TaskID, run.RunID) {
			continue
		}
		id := managedRecoveredChildID(agent.AgentID, recovery, run.ParentEntryID, run.TaskID, run.RunID)
		children = append(children, managedChildProjectionForRun(id, run, map[string]any{"state": "interrupted_unknown", "cause": recovery.Cause, "observed_at": recovery.ObservedAt}, true))
	}
	sort.Slice(children, func(left, right int) bool { return children[left]["id"].(string) < children[right]["id"].(string) })
	return children
}

func managedRecoveredChildID(agentID string, recovery store.ManagedChildRecovery, parentEntryID, taskID, runID string) string {
	joined := agentID + "\x00" + recovery.PriorSessionID + "\x00" + fmt.Sprint(recovery.PriorSessionEpoch) + "\x00" + recovery.PriorProcessIncarnation + "\x00" + recovery.PriorRuntimeGeneration + "\x00" + parentEntryID + "\x00" + taskID + "\x00" + runID
	sum := sha256.Sum256([]byte(joined))
	return "child-" + hex.EncodeToString(sum[:16])
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
	return managedChildrenSnapshotFromStoredWithStore(ctx, durable, snapshot)
}

func managedChildStatus(value string) string {
	switch value {
	case "queued", "running", "completed", "failed", "steered", "aborted", "stopped", "error", "cancelled":
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

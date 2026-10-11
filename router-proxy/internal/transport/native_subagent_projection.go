package transport

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type nativeSubagentStore interface {
	ManagedSubagents(ctx context.Context, parent store.EmbeddedBinding) ([]*store.ManagedSubagentRecord, error)
}

func isStaleParentBinding(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "stale subagent parent binding") || strings.Contains(msg, "invalid embedded binding")
}

func nativeSubagentBinding(agent *store.ManagedAgentRecord) (store.EmbeddedBinding, bool) {
	if agent == nil || agent.Runtime != "embedded" || agent.AgentID == "" {
		return store.EmbeddedBinding{}, false
	}
	return store.EmbeddedBinding{
		AgentID:    agent.AgentID,
		SessionID:  agent.PiSessionID,
		Epoch:      agent.SessionEpoch,
		Generation: agent.RuntimeGeneration,
	}, true
}

func projectNativeSubagentRecord(record *store.ManagedSubagentRecord) map[string]any {
	if record == nil {
		return nil
	}
	observedAt := record.UpdatedAt
	if observedAt == "" {
		observedAt = record.CreatedAt
	}
	if parsed, ok := managedChildTimestamp(&observedAt); ok {
		observedAt = parsed
	} else {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}

	recovery := map[string]any{
		"state":       "confirmed",
		"cause":       nil,
		"observed_at": observedAt,
	}
	if record.Status == "interrupted" {
		recovery["state"] = "interrupted_unknown"
		recovery["cause"] = "parent_process_lost"
	}

	parentEntryID := record.ParentSessionID
	if parentEntryID == "" {
		parentEntryID = record.AgentID
	}

	desc := record.Description
	if desc == "" {
		desc = "Subagent"
	}

	child := map[string]any{
		"id":               record.AgentID,
		"parent_entry_id":  managedChildDisplayID(parentEntryID),
		"task_id":          managedChildDisplayID(record.AgentID),
		"run_id":           managedChildDisplayID(record.AgentID),
		"status":           managedChildStatus(record.Status),
		"sequence":         int64(record.TurnCount),
		"recovery":         recovery,
		"compaction_state": "none",
		"compaction": map[string]any{
			"state": "idle",
			"count": int64(0),
		},
		"description": boundedManagedDisplay(managed.RedactedStderr(desc), 512),
	}
	if at, ok := managedChildTimestamp(&record.CreatedAt); ok {
		child["started_at"] = at
	}
	if at, ok := managedChildTimestamp(&record.TerminalAt); ok {
		child["ended_at"] = at
	}
	return child
}

func loadNativeChildrenForAgent(ctx context.Context, s nativeSubagentStore, agent *store.ManagedAgentRecord) ([]map[string]any, error) {
	if s == nil || agent == nil {
		return nil, nil
	}
	binding, ok := nativeSubagentBinding(agent)
	if !ok {
		return nil, nil
	}
	records, err := s.ManagedSubagents(ctx, binding)
	if err != nil {
		if isStaleParentBinding(err) {
			return nil, nil
		}
		return nil, err
	}
	children := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		if rec == nil {
			continue
		}
		if projected := projectNativeSubagentRecord(rec); projected != nil {
			children = append(children, projected)
		}
	}
	return children, nil
}

func mergeNativeSubagentsForAgent(ctx context.Context, s nativeSubagentStore, agent *store.ManagedAgentRecord, base map[string]any) (map[string]any, error) {
	if base == nil {
		agentID := ""
		if agent != nil {
			agentID = agent.AgentID
		}
		base = map[string]any{"agent_id": agentID, "source": "router-managed", "lifecycle_version": int64(0), "recovery_state": "reconstructing", "children": []map[string]any{}}
	}
	nativeChildren, err := loadNativeChildrenForAgent(ctx, s, agent)
	if err != nil {
		return nil, err
	}
	if agent != nil && agent.Runtime == "embedded" && base["recovery_state"] == "reconstructing" {
		base["recovery_state"] = "current"
	}
	if len(nativeChildren) == 0 {
		return base, nil
	}
	existing, _ := base["children"].([]map[string]any)
	combined := make([]map[string]any, 0, len(existing)+len(nativeChildren))
	combined = append(combined, existing...)
	combined = append(combined, nativeChildren...)
	sort.Slice(combined, func(i, j int) bool {
		return combined[i]["id"].(string) < combined[j]["id"].(string)
	})
	base["children"] = combined
	return base, nil
}

func managedChildrenSnapshotFromStoredWithStore(ctx context.Context, s nativeSubagentStore, snapshot store.ManagedAgentsSnapshot) (int64, string, []map[string]any, error) {
	children := make([]map[string]any, 0, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		base := managedChildrenProjectionForAgent(agent, snapshot.ReadinessSources[agent.AgentID])
		merged, err := mergeNativeSubagentsForAgent(ctx, s, agent, base)
		if err != nil {
			return 0, "", nil, err
		}
		children = append(children, merged)
	}
	return snapshot.ProjectionVersion, snapshot.GeneratedAt.UTC().Format(time.RFC3339Nano), children, nil
}

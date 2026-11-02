package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
)

// ConnectedAgentStoreSnapshot is the durable portion of one connected-agent
// response. The transport joins it with connection-local live state.
type ConnectedAgentStoreSnapshot struct {
	Urgency   map[string]bool
	Leases    map[string]domain.Lease
	Lifecycle map[string]ConnectedAgentLifecycle
}

// ConnectedAgentLifecycle contains the producer authority and durable
// projection needed when the transport has no newer in-memory projection.
type ConnectedAgentLifecycle struct {
	Producer LifecycleProducerInfo
	Agent    LifecycleAgent
}

// ConnectedAgentSnapshot reads the durable rows needed by /v1/agents in at most
// three batched statements, regardless of the number of connected agents. Keeping the
// lease and lifecycle reads under the store mutex gives the response one
// serialized SQLite snapshot while avoiding one lookup per agent.
func (s *Store) ConnectedAgentSnapshot(ctx context.Context, agentIDs []string, includeLifecycle bool) (ConnectedAgentStoreSnapshot, error) {
	if ctx == nil {
		return ConnectedAgentStoreSnapshot{}, errors.New("context is required")
	}
	result := ConnectedAgentStoreSnapshot{
		Urgency:   make(map[string]bool),
		Leases:    make(map[string]domain.Lease),
		Lifecycle: make(map[string]ConnectedAgentLifecycle),
	}
	ids := uniqueAgentIDs(agentIDs)
	if len(ids) == 0 {
		return result, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ConnectedAgentStoreSnapshot{}, errors.New("store is closed")
	}

	placeholders := sqlitePlaceholders(len(ids))
	args := stringArgs(ids)
	rows, err := s.db.QueryContext(ctx, `SELECT lease_id, agent_id, instance_id, acquired_at, expires_at
		FROM agent_leases WHERE agent_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return ConnectedAgentStoreSnapshot{}, err
	}
	for rows.Next() {
		var lease domain.Lease
		if err := rows.Scan(&lease.LeaseID, &lease.AgentID, &lease.InstanceID, &lease.AcquiredAt, &lease.ExpiresAt); err != nil {
			_ = rows.Close()
			return ConnectedAgentStoreSnapshot{}, err
		}
		result.Leases[lease.AgentID] = lease
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ConnectedAgentStoreSnapshot{}, err
	}
	if err := rows.Close(); err != nil {
		return ConnectedAgentStoreSnapshot{}, err
	}

	urgencyRows, err := s.db.QueryContext(ctx, `SELECT agent_id, urgent FROM agents WHERE agent_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return ConnectedAgentStoreSnapshot{}, err
	}
	for urgencyRows.Next() {
		var id string
		var urgent bool
		if err := urgencyRows.Scan(&id, &urgent); err != nil {
			_ = urgencyRows.Close()
			return ConnectedAgentStoreSnapshot{}, err
		}
		result.Urgency[id] = urgent
	}
	if err := urgencyRows.Err(); err != nil {
		_ = urgencyRows.Close()
		return ConnectedAgentStoreSnapshot{}, err
	}
	if err := urgencyRows.Close(); err != nil {
		return ConnectedAgentStoreSnapshot{}, err
	}
	if !includeLifecycle {
		return result, nil
	}

	lifecycleArgs := stringArgs(ids)
	lifecycleRows, err := s.db.QueryContext(ctx, `SELECT producer.parent_agent_id, producer.instance_id,
		producer.parent_session_id, producer.producer_epoch, producer.lease_id, producer.authority,
		COALESCE(versions.max_router_version, global_version), running.subagent_id,
		running.type, running.description, running.status
		FROM subagent_lifecycle_producers producer
		LEFT JOIN (
			SELECT parent_agent_id, MAX(router_version) AS max_router_version
			FROM subagent_lifecycle_state GROUP BY parent_agent_id
		) versions ON versions.parent_agent_id = producer.parent_agent_id
		CROSS JOIN (
			SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'subagent_lifecycle_events'), 0) AS global_version
		) lifecycle_sequence
		LEFT JOIN subagent_lifecycle_state running
			ON running.parent_agent_id = producer.parent_agent_id AND running.state = 'running'
		WHERE producer.parent_agent_id IN (`+placeholders+`)
		ORDER BY producer.parent_agent_id ASC, running.subagent_id ASC`, lifecycleArgs...)
	if err != nil {
		return result, err
	}
	for lifecycleRows.Next() {
		var producer LifecycleProducerInfo
		var version int64
		var childID, childType, childDescription, childStatus sql.NullString
		if err := lifecycleRows.Scan(
			&producer.ParentAgentID, &producer.InstanceID, &producer.ParentSessionID,
			&producer.ProducerEpoch, &producer.LeaseID, &producer.Authority,
			&version, &childID, &childType, &childDescription, &childStatus,
		); err != nil {
			_ = lifecycleRows.Close()
			return result, err
		}
		entry, ok := result.Lifecycle[producer.ParentAgentID]
		if !ok {
			entry = ConnectedAgentLifecycle{
				Producer: producer,
				Agent: LifecycleAgent{
					AgentID:   producer.ParentAgentID,
					Subagents: []LifecycleChild{},
				},
			}
		}
		if version > entry.Agent.SubagentVersion {
			entry.Agent.SubagentVersion = version
		}
		if childID.Valid {
			child := LifecycleChild{ID: childID.String}
			if childType.Valid {
				child.Type = childType.String
			}
			if childDescription.Valid {
				child.Description = childDescription.String
			}
			if childStatus.Valid {
				child.Status = childStatus.String
			}
			entry.Agent.Subagents = append(entry.Agent.Subagents, child)
		}
		result.Lifecycle[producer.ParentAgentID] = entry
	}
	if err := lifecycleRows.Err(); err != nil {
		_ = lifecycleRows.Close()
		result.Lifecycle = make(map[string]ConnectedAgentLifecycle)
		return result, err
	}
	if err := lifecycleRows.Close(); err != nil {
		result.Lifecycle = make(map[string]ConnectedAgentLifecycle)
		return result, err
	}
	return result, nil
}

func uniqueAgentIDs(agentIDs []string) []string {
	seen := make(map[string]struct{}, len(agentIDs))
	ids := make([]string, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		if agentID == "" {
			continue
		}
		if _, ok := seen[agentID]; ok {
			continue
		}
		seen[agentID] = struct{}{}
		ids = append(ids, agentID)
	}
	return ids
}

func sqlitePlaceholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

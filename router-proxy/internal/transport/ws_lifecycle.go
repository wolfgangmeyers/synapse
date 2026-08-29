package transport

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) syncEnable(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	s.lifecycleEnabled = true
	return r.sendLifecycleSnapshot(s)
}
func (r *Router) syncResync(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	return r.sendLifecycleSnapshot(s)
}
func (r *Router) sendLifecycleSnapshot(s *session) error {
	r.mu.Lock()
	agents := make([]map[string]any, 0, len(r.lifecycleState))
	for id, state := range r.lifecycleState {
		agents = append(agents, map[string]any{"agent_id": id, "subagent_version": state.version, "subagent_count": len(state.subagents), "subagents": state.subagents})
	}
	version := r.nextVersion
	r.mu.Unlock()
	sort.Slice(agents, func(i, j int) bool { return agents[i]["agent_id"].(string) < agents[j]["agent_id"].(string) })
	return s.send(map[string]any{"type": "subagent_state_snapshot", "router_version": version, "agents": agents})
}
func (r *Router) lifecycleFrame(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	var children []map[string]any
	if frameString(frame, "type") == "subagent_lifecycle_snapshot" {
		_ = json.Unmarshal(frame["subagents"], &children)
	} else {
		var child map[string]any
		_ = json.Unmarshal(frame["subagent"], &child)
		children = []map[string]any{child}
	}
	r.mu.Lock()
	r.nextVersion++
	version := r.nextVersion
	r.lifecycleState[s.agentID] = lifecycleAgent{version: version, subagents: children}
	r.mu.Unlock()
	if err := s.send(map[string]any{"type": "subagent_lifecycle_acknowledged", "request_id": frameString(frame, "frame_id"), "event_id": frameString(frame, "event_id"), "client_sequence": mustInt(frame, "client_sequence"), "producer_epoch": frameString(frame, "producer_epoch"), "agent_id": frameString(frame, "agent_id"), "instance_id": frameString(frame, "instance_id"), "parent_session_id": frameString(frame, "parent_session_id"), "lease_id": frameString(frame, "lease_id"), "disposition": "applied", "router_version": version}); err != nil {
		return err
	}
	return r.broadcastLifecycle(s.agentID, version)
}
func (r *Router) broadcastLifecycle(agentID string, version int64) error {
	r.mu.Lock()
	state := r.lifecycleState[agentID]
	recipients := make([]*session, 0)
	for _, subs := range r.convos {
		for _, candidate := range subs {
			if candidate.lifecycleEnabled {
				recipients = append(recipients, candidate)
			}
		}
	}
	r.mu.Unlock()
	projection := map[string]any{"agent_id": agentID, "subagent_version": state.version, "subagent_count": len(state.subagents), "subagents": state.subagents}
	for _, recipient := range recipients {
		_ = recipient.send(map[string]any{"type": "subagent_state_update", "router_version": version, "agent": projection})
	}
	return nil
}

func (r *Router) sessionClosed(s *session, registered bool) {
	if !registered || s.agentID == "" {
		return
	}
	r.mu.Lock()
	if r.sessions[s.agentID] == s {
		delete(r.sessions, s.agentID)
		delete(r.models, s.agentID)
		delete(r.activities, s.agentID)
		delete(r.goals, s.agentID)
	}
	if subscribers := r.convos[s.agentID]; subscribers != nil {
		delete(subscribers, s.instanceID)
		if len(subscribers) == 0 {
			delete(r.convos, s.agentID)
		}
	}
	for key, pending := range r.pendingMod {
		if pending.source == s || pending.target == s {
			delete(r.pendingMod, key)
		}
	}
	for key, pending := range r.pendingGoal {
		if pending.source == s || pending.target == s {
			delete(r.pendingGoal, key)
		}
	}
	for key, pending := range r.pendingLock {
		if pending.source == s || pending.target == s {
			delete(r.pendingLock, key)
		}
	}
	r.mu.Unlock()
	_, _ = r.store.ReleaseAgentLease(context.Background(), store.LeaseRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID})
}

func (r *Router) broadcastActivity(agentID string) {
	r.mu.Lock()
	a, ok := r.activities[agentID]
	if !ok {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	fields := map[string]any{"type": "agent_activity", "agent_id": agentID, "instance_id": a.instanceID, "activity_state": a.state, "subagent_count": a.count, "subagents": a.subagents, "generated_at": timestamp(r.now())}
	if a.goal != nil {
		fields["goal"] = a.goal
	}
	if a.model != nil {
		fields["current_model"] = a.model
	}
	r.broadcast(fields)
}
func (r *Router) broadcastModel(agentID string) {
	r.mu.Lock()
	model, ok := r.models[agentID]
	r.mu.Unlock()
	if !ok {
		return
	}
	fields := map[string]any{"type": "agent_model_catalog", "agent_id": agentID, "instance_id": model.instanceID, "model_summaries": model.available, "total_count": model.total, "truncated": model.truncated, "generated_at": timestamp(r.now())}
	if model.current != nil {
		fields["current_model"] = model.current
	}
	r.broadcast(fields)
}
func (r *Router) broadcastGoal(agentID string) {
	r.mu.Lock()
	goal, ok := r.goals[agentID]
	r.mu.Unlock()
	if !ok {
		return
	}
	r.broadcast(map[string]any{"type": "agent_goal_state", "agent_id": agentID, "instance_id": goal.instanceID, "goal_state": goal.state, "generated_at": timestamp(r.now())})
}

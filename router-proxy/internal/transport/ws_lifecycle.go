package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) syncEnable(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	s.setLifecycleEnabled(true)
	if afterVersion, ok := frameInt(frame, "after_version"); ok && afterVersion > 0 {
		replay, err := r.store.ReplaySubagentLifecycle(context.Background(), afterVersion)
		if err != nil {
			return err
		}
		if replay.Mode == "replay" {
			return r.sendLifecycleReplay(s, replay)
		}
	}
	return r.sendLifecycleSnapshot(s)
}

func (r *Router) syncResync(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	afterVersion, _ := frameInt(frame, "after_version")
	replay, err := r.store.ReplaySubagentLifecycle(context.Background(), afterVersion)
	if err != nil {
		return err
	}
	if replay.Mode == "replay" {
		return r.sendLifecycleReplay(s, replay)
	}
	return r.sendLifecycleSnapshot(s)
}

func (r *Router) sendLifecycleSnapshot(s *session) error {
	projection, err := r.store.LifecycleProjection(context.Background())
	if err != nil {
		return err
	}
	r.hydrateLifecycleProjection(projection)
	agents := make([]map[string]any, 0, len(projection.Agents))
	for _, agent := range projection.Agents {
		agents = append(agents, lifecycleAgentFrame(agent, nil))
	}
	return s.send(map[string]any{"type": "subagent_state_snapshot", "router_version": projection.RouterVersion, "agents": agents})
}

func (r *Router) sendLifecycleReplay(s *session, replay store.LifecycleReplay) error {
	agents := make([]map[string]any, 0, len(replay.Updates))
	byParent := make(map[string]store.LifecycleAgent, len(replay.Projection.Agents))
	for _, agent := range replay.Projection.Agents {
		byParent[agent.AgentID] = agent
	}
	for _, update := range replay.Updates {
		agent, ok := byParent[update.ParentAgentID]
		if !ok {
			var err error
			agent, err = r.store.LifecycleAgentProjection(context.Background(), update.ParentAgentID)
			if err != nil {
				return err
			}
		}
		version := update.RouterVersion
		agents = append(agents, lifecycleAgentFrame(agent, &version))
	}
	return s.send(map[string]any{"type": "subagent_state_replay", "router_version": replay.RouterVersion, "updates": agents})
}

func lifecycleAgentFrame(agent store.LifecycleAgent, version *int64) map[string]any {
	children := make([]map[string]any, 0, len(agent.Subagents))
	for _, child := range agent.Subagents {
		item := map[string]any{"id": child.ID}
		if child.Type != "" {
			item["type"] = child.Type
		}
		if child.Description != "" {
			item["description"] = child.Description
		}
		if child.Status != "" {
			item["status"] = child.Status
		}
		children = append(children, item)
	}
	if version == nil {
		version = &agent.SubagentVersion
	}
	return map[string]any{"agent_id": agent.AgentID, "subagent_version": *version, "subagent_count": len(children), "subagents": children}
}

func (r *Router) lifecycleFrame(s *session, frame map[string]json.RawMessage) error {
	if !r.lifecycleAvailable {
		return s.sendError("migration_017_required", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	candidate, err := lifecycleCandidateFromFrame(frame)
	if err != nil {
		r.emitEvent(lifecycleIngestFailedEvent(frame))
		return err
	}
	r.emitEvent(lifecycleIngestedEvent(candidate))
	result, err := r.store.ApplySubagentLifecycle(context.Background(), candidate, candidate.Snapshot)
	if err != nil {
		r.emitEvent(lifecyclePersistFailedEvent(candidate))
		return err
	}
	r.emitEvent(lifecyclePersistedEvent(candidate, result.Disposition, result.RouterVersion))
	if result.Disposition == "applied" {
		r.setLifecycleProjection(candidate.ParentAgentID, result.RouterVersion, result.Snapshot.Subagents)
	}
	ack := map[string]any{
		"type":              "subagent_lifecycle_acknowledged",
		"request_id":        frameString(frame, "frame_id"),
		"event_id":          frameString(frame, "event_id"),
		"client_sequence":   mustInt(frame, "client_sequence"),
		"producer_epoch":    frameString(frame, "producer_epoch"),
		"agent_id":          frameString(frame, "agent_id"),
		"instance_id":       frameString(frame, "instance_id"),
		"parent_session_id": frameString(frame, "parent_session_id"),
		"lease_id":          frameString(frame, "lease_id"),
		"disposition":       result.Disposition,
		"router_version":    result.RouterVersion,
	}
	if result.HasAppliedVersion {
		ack["applied_version"] = result.AppliedVersion
	}
	if err := s.send(ack); err != nil {
		r.emitEvent(lifecycleAckFailedEvent(candidate))
		return err
	}
	r.emitEvent(lifecycleAcknowledgedEvent(candidate, result.Disposition, result.RouterVersion))
	if result.Disposition == "applied" {
		return r.broadcastLifecycle(candidate.ParentAgentID, result.RouterVersion)
	}
	return nil
}

func lifecycleCandidateFromFrame(frame map[string]json.RawMessage) (store.LifecycleCandidate, error) {
	sequence, ok := frameInt(frame, "client_sequence")
	if !ok {
		return store.LifecycleCandidate{}, errors.New("invalid lifecycle client_sequence")
	}
	candidate := store.LifecycleCandidate{
		ParentAgentID:   frameString(frame, "agent_id"),
		InstanceID:      frameString(frame, "instance_id"),
		ParentSessionID: frameString(frame, "parent_session_id"),
		LeaseID:         frameString(frame, "lease_id"),
		ProducerEpoch:   frameString(frame, "producer_epoch"),
		ClientSequence:  sequence,
		EventID:         frameString(frame, "event_id"),
		EventKind:       frameString(frame, "lifecycle_event"),
	}
	if candidate.EventKind == "" {
		candidate.Snapshot = true
		if err := json.Unmarshal(frame["subagents"], &candidate.Subagents); err != nil {
			return store.LifecycleCandidate{}, err
		}
		return candidate, nil
	}
	if err := json.Unmarshal(frame["subagent"], &candidate.Subagent); err != nil {
		return store.LifecycleCandidate{}, err
	}
	if candidate.EventKind == "terminal" {
		candidate.TerminalReason = frameString(frame, "terminal_reason")
	}
	return candidate, nil
}

func (r *Router) setLifecycleProjection(parent string, version int64, children []map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.lifecycleState[parent]
	if ok && current.version > version {
		return
	}
	r.lifecycleState[parent] = lifecycleAgent{version: version, subagents: children}
	if version > r.nextVersion {
		r.nextVersion = version
	}
}

func (r *Router) hydrateLifecycleProjection(projection store.LifecycleProjection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if projection.RouterVersion > r.nextVersion {
		r.nextVersion = projection.RouterVersion
	}
	seen := make(map[string]struct{}, len(projection.Agents))
	for _, agent := range projection.Agents {
		seen[agent.AgentID] = struct{}{}
		children := make([]map[string]any, 0, len(agent.Subagents))
		for _, child := range agent.Subagents {
			children = append(children, lifecycleChildFrame(child))
		}
		current, ok := r.lifecycleState[agent.AgentID]
		if !ok || current.version <= agent.SubagentVersion {
			r.lifecycleState[agent.AgentID] = lifecycleAgent{version: agent.SubagentVersion, subagents: children}
		}
	}
	for parent, current := range r.lifecycleState {
		if _, ok := seen[parent]; !ok && current.version <= projection.RouterVersion {
			delete(r.lifecycleState, parent)
		}
	}
}

func lifecycleChildFrame(child store.LifecycleChild) map[string]any {
	item := map[string]any{"id": child.ID}
	if child.Type != "" {
		item["type"] = child.Type
	}
	if child.Description != "" {
		item["description"] = child.Description
	}
	if child.Status != "" {
		item["status"] = child.Status
	}
	return item
}
func (r *Router) conversationRecipients(agentIDs ...string) []*session {
	r.mu.Lock()
	defer r.mu.Unlock()
	recipients := make([]*session, 0)
	seen := map[*session]bool{}
	addSession := func(candidate *session) {
		if !seen[candidate] {
			seen[candidate] = true
			recipients = append(recipients, candidate)
		}
	}
	add := func(subscribers map[string]*session) {
		for _, candidate := range subscribers {
			addSession(candidate)
		}
	}
	if len(agentIDs) == 0 {
		for _, subscribers := range r.convos {
			add(subscribers)
		}
		for candidate := range r.uiSessions {
			if candidate.conversationSync == "v1" {
				addSession(candidate)
			}
		}
	} else {
		for _, agentID := range agentIDs {
			add(r.convos[agentID])
		}
		// UI agent IDs are display labels only. Retain the old conversation
		// filtering semantics without putting browsers in the durable map.
		for candidate := range r.uiSessions {
			if candidate.conversationSync == "v1" {
				addSession(candidate)
			}
		}
	}
	return recipients
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneValue(item)
	}
	return result
}
func cloneValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneMap(item)
	case []map[string]any:
		result := make([]map[string]any, len(item))
		for index, child := range item {
			result[index] = cloneMap(child)
		}
		return result
	case []any:
		result := make([]any, len(item))
		for index, child := range item {
			result[index] = cloneValue(child)
		}
		return result
	default:
		return value
	}
}

func (r *Router) broadcastLifecycle(agentID string, version int64) error {
	r.mu.RLock()
	state := r.lifecycleState[agentID]
	subs := cloneValue(state.subagents)
	r.mu.RUnlock()
	var children []map[string]any
	if subs != nil {
		children, _ = subs.([]map[string]any)
	}
	if children == nil {
		children = []map[string]any{}
	}
	projection := map[string]any{"agent_id": agentID, "subagent_version": version, "subagent_count": len(children), "subagents": children}
	recipientCount, failureCount := 0, 0
	for _, recipient := range r.conversationRecipients() {
		if !recipient.lifecycleEnabledNow() {
			continue
		}
		recipientCount++
		if err := recipient.send(map[string]any{"type": "subagent_state_update", "router_version": version, "agent": projection}); err != nil {
			failureCount++
		}
	}
	if failureCount > 0 {
		r.emitEvent(LifecycleBroadcastFailedEvent(agentID, version, recipientCount, failureCount, "send_failed"))
	}
	return nil
}

func lifecycleFrameFields(frame map[string]json.RawMessage, extra ...map[string]any) map[string]any {
	fields := map[string]any{}
	for _, key := range []string{"agent_id", "event_id", "lifecycle_event"} {
		if value := frameString(frame, key); value != "" {
			fields[key] = value
		}
	}
	if sequence, ok := frameInt(frame, "client_sequence"); ok {
		fields["client_sequence"] = sequence
	}
	if raw, ok := frame["subagents"]; ok {
		var children []store.LifecycleChild
		if json.Unmarshal(raw, &children) == nil {
			fields["subagent_count"] = len(children)
		}
	}
	for _, values := range extra {
		for key, value := range values {
			fields[key] = value
		}
	}
	return fields
}

func lifecycleCandidateFields(candidate store.LifecycleCandidate, extra ...map[string]any) map[string]any {
	fields := map[string]any{
		"agent_id":        candidate.ParentAgentID,
		"event_id":        candidate.EventID,
		"client_sequence": candidate.ClientSequence,
		"subagent_count":  lifecycleCandidateCount(candidate),
	}
	if candidate.EventKind != "" {
		fields["lifecycle_event"] = candidate.EventKind
	}
	for _, values := range extra {
		for key, value := range values {
			fields[key] = value
		}
	}
	return fields
}

func lifecycleIngestFailedEvent(frame map[string]json.RawMessage) Event {
	fields := lifecycleFrameFields(frame)
	fields["reason"] = "invalid_candidate"
	return newEvent("lifecycle_ingest_failed", fields)
}
func lifecycleIngestedEvent(candidate store.LifecycleCandidate) Event {
	return newEvent("lifecycle_ingested", lifecycleCandidateFields(candidate))
}
func lifecyclePersistFailedEvent(candidate store.LifecycleCandidate) Event {
	fields := lifecycleCandidateFields(candidate)
	fields["reason"] = "store_error"
	return newEvent("lifecycle_persist_failed", fields)
}
func lifecyclePersistedEvent(candidate store.LifecycleCandidate, disposition string, version int64) Event {
	fields := lifecycleCandidateFields(candidate)
	fields["disposition"], fields["router_version"] = disposition, version
	return newEvent("lifecycle_persisted", fields)
}
func lifecycleAckFailedEvent(candidate store.LifecycleCandidate) Event {
	fields := lifecycleCandidateFields(candidate)
	fields["reason"] = "send_failed"
	return newEvent("lifecycle_ack_failed", fields)
}
func lifecycleAcknowledgedEvent(candidate store.LifecycleCandidate, disposition string, version int64) Event {
	fields := lifecycleCandidateFields(candidate)
	fields["disposition"], fields["router_version"] = disposition, version
	return newEvent("lifecycle_acknowledged", fields)
}
func LifecycleBroadcastFailedEvent(agentID string, version int64, recipients, failures int, reason string) Event {
	return newEvent("lifecycle_broadcast_failed", map[string]any{"agent_id": agentID, "router_version": version, "recipient_count": recipients, "failure_count": failures, "reason": reason})
}

func lifecycleCandidateCount(candidate store.LifecycleCandidate) int {
	if candidate.Snapshot {
		return len(candidate.Subagents)
	}
	return 1
}

// beginOwnership reserves one agent for an ownership transition. The mutex
// protects only this table; callers release it before touching SQLite or a
// socket. Waiting uses the transition's completion signal, so unrelated agents
// continue without polling or a global ownership bottleneck.
func (r *Router) beginOwnership(agentID string) *ownershipTransition {
	for {
		r.ownershipMu.Lock()
		if transition := r.ownership[agentID]; transition != nil {
			done := transition.done
			r.ownershipMu.Unlock()
			<-done
			continue
		}
		r.ownershipGeneration++
		transition := &ownershipTransition{agentID: agentID, generation: r.ownershipGeneration, done: make(chan struct{})}
		r.ownership[agentID] = transition
		r.ownershipMu.Unlock()
		return transition
	}
}

func (r *Router) ownershipCurrent(transition *ownershipTransition) bool {
	r.ownershipMu.Lock()
	defer r.ownershipMu.Unlock()
	current := r.ownership[transition.agentID]
	return current == transition && current.generation == transition.generation
}

func (r *Router) finishOwnership(agentID string, transition *ownershipTransition) {
	r.ownershipMu.Lock()
	if r.ownership[agentID] == transition {
		delete(r.ownership, agentID)
		close(transition.done)
	}
	r.ownershipMu.Unlock()
}

func (r *Router) sessionClosed(s *session, registered bool) {
	if !registered || s.agentID == "" {
		return
	}
	if s.hasUILease() {
		var replacement *session
		r.mu.Lock()
		delete(r.uiSessions, s)
		if r.uiDeliveryOwners[s.agentID] == s {
			delete(r.uiDeliveryOwners, s.agentID)
			replacement = r.promoteUIDeliveryOwnerLocked(s.agentID)
		}
		r.removePendingForSessionLocked(s)
		r.mu.Unlock()
		if replacement != nil {
			r.offer(replacement)
		}
		return
	}
	transition := r.beginOwnership(s.agentID)
	defer r.finishOwnership(s.agentID, transition)
	if !r.ownershipCurrent(transition) {
		return
	}
	r.sessionClosedSerialized(s)
}

func (r *Router) sessionClosedSerialized(s *session) {
	// The directory owns tmux source lifetime. A socket/lease replacement only
	// changes live delivery; it must not retire s.tmuxSourceID.
	sharedPaused := s.isPaused()
	var promoted *session
	r.mu.Lock()
	primary := r.sessions[s.agentID] == s
	if subscribers := r.convos[s.agentID]; subscribers != nil {
		// A reconnect can replace the same agent and instance before the old
		// reader observes its close. Only remove the exact old subscription.
		if subscribers[s.instanceID] == s {
			delete(subscribers, s.instanceID)
			r.removeConversationOrderLocked(s.agentID, s.instanceID)
		}
		if primary && len(subscribers) > 0 {
			// A conversation-sync secondary owns the shared lease after the
			// primary closes. Delivery remains on exactly one socket.
			for _, instanceID := range r.conversationOrder[s.agentID] {
				replacement := subscribers[instanceID]
				if replacement == nil {
					continue
				}
				r.sessions[s.agentID] = replacement
				promoted = replacement
				primary = false
				break
			}
		} else if len(subscribers) == 0 {
			delete(r.convos, s.agentID)
			delete(r.conversationOrder, s.agentID)
		}
	}
	if primary {
		delete(r.sessions, s.agentID)
		delete(r.conversationOrder, s.agentID)
		delete(r.models, s.agentID)
		delete(r.goals, s.agentID)
		delete(r.inlineToolStates, s.agentID)
		for key := range r.latestGoalRevision {
			if strings.HasPrefix(key, s.agentID+"\x00") {
				delete(r.latestGoalRevision, key)
			}
		}
		if activity, ok := r.activities[s.agentID]; !ok || activity.state != "compacting" {
			delete(r.activities, s.agentID)
		}
	}
	r.removePendingForSessionLocked(s)
	r.mu.Unlock()
	if primary {
		r.cancelPendingDeliveryOffer(s.agentID)
	}
	if promoted != nil {
		r.setAgentPaused(s.agentID, sharedPaused || r.agentCompacting(s.agentID))
		r.offer(promoted)
	}
	if primary {
		_, _ = r.store.ReleaseAgentLease(context.Background(), store.LeaseRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID})
		r.mu.Lock()
		fallback := r.promoteUIDeliveryOwnerLocked(s.agentID)
		r.mu.Unlock()
		if fallback != nil {
			r.offer(fallback)
		}
	}
}

func (r *Router) removeConversationOrderLocked(agentID, instanceID string) {
	order := r.conversationOrder[agentID]
	for index, value := range order {
		if value == instanceID {
			r.conversationOrder[agentID] = append(order[:index], order[index+1:]...)
			return
		}
	}
}

func (r *Router) removePendingForSession(s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removePendingForSessionLocked(s)
}

// removePendingForSessionLocked is called while r.mu is held. Registration
// invokes the unlocked wrapper before replacing a session; close invokes this
// form while it removes the exact owner.
func (r *Router) removePendingForSessionLocked(s *session) {
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
	for key, pending := range r.pendingInlineTool {
		if pending.source == s || pending.target == s {
			delete(r.pendingInlineTool, key)
		}
	}
}

func (r *Router) broadcastActivity(agentID string) {
	r.mu.Lock()
	a, ok := r.activities[agentID]
	if ok {
		a.subagents = cloneValue(a.subagents).([]map[string]any)
		a.goal = cloneMap(a.goal)
		a.model = cloneMap(a.model)
	}
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
	if ok {
		model.available = cloneValue(model.available).([]map[string]any)
		model.current = cloneMap(model.current)
	}
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
	if ok {
		goal.state = cloneMap(goal.state)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	r.broadcast(map[string]any{"type": "agent_goal_state", "agent_id": agentID, "instance_id": goal.instanceID, "goal_state": goal.state, "generated_at": timestamp(r.now())})
}

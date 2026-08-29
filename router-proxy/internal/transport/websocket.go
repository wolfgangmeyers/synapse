package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var clientFrameTypes = map[string][]string{
	"register":                     {"agent_id", "instance_id"},
	"lease_renew":                  {"agent_id", "instance_id", "lease_id"},
	"activity":                     {"agent_id", "instance_id", "lease_id", "activity_state"},
	"model_catalog":                {"agent_id", "instance_id", "lease_id", "model_summaries"},
	"model_control":                {"agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "model"},
	"model_control_result":         {"agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted"},
	"goal_control":                 {"agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "goal", "settings"},
	"goal_control_result":          {"agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "revision", "accepted", "goal_state"},
	"reminder_lock_control":        {"agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "reminder_id", "action"},
	"reminder_lock_control_result": {"agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "reminder_id", "accepted"},
	"goal_state":                   {"agent_id", "instance_id", "lease_id", "goal_state"},
	"message_arrival":              {"agent_id", "instance_id", "lease_id", "from", "source", "preview", "body", "created_at", "message_id"},
	"injection_requested":          {"agent_id", "instance_id", "lease_id", "message_id", "attempt_number"},
	"ack":                          {"agent_id", "instance_id", "lease_id", "message_id", "attempt_number"},
	"nack":                         {"agent_id", "instance_id", "lease_id", "message_id", "attempt_number", "reason"},
	"ping":                         nil,
	"subagent_sync_enable":         {"agent_id", "instance_id", "lease_id"},
	"subagent_lifecycle_snapshot":  {"agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "client_sequence", "event_id", "subagents"},
	"subagent_lifecycle_event":     {"agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "client_sequence", "event_id", "lifecycle_event", "subagent"},
	"subagent_state_resync":        {"agent_id", "instance_id", "lease_id", "after_version"},
}

func websocketUpgrade(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Connection"), "upgrade") && strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}

func (r *Router) serveWebSocket(w http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn.SetReadLimit(int64(r.maxFrame))
	r.readLoop(&session{router: r, conn: conn})
}

func (r *Router) readLoop(s *session) {
	registered := false
	defer func() { r.sessionClosed(s, registered) }()
	for {
		kind, payload, err := s.conn.Read(context.Background())
		if err != nil {
			return
		}
		if kind != websocket.MessageText || !utf8.Valid(payload) {
			_ = s.sendError("invalid_frame", "")
			continue
		}
		frame, err := decodeFrame(payload)
		if err != nil {
			_ = s.sendError("invalid_frame", "")
			continue
		}
		frame = protocol.SanitizeClientFrame(frame)
		sanitizedPayload, err := json.Marshal(frame)
		if err != nil || containsUnpairedSurrogate(sanitizedPayload) {
			_ = s.sendError("invalid_frame", "")
			continue
		}
		if err := validateClientFrame(frame); err != nil {
			// Node does not echo a request id for a frame that failed schema validation.
			_ = s.sendError("invalid_frame", "")
			continue
		}
		if frameString(frame, "type") == "register" {
			if registered {
				_ = s.sendError("already_registered", frameString(frame, "frame_id"))
				continue
			}
			err := r.register(s, frame)
			if err != nil {
				return
			}
			registered = true
			continue
		}
		if !registered || s.agentID == "" {
			_ = s.sendError("lease_not_active", frameString(frame, "frame_id"))
			_ = s.close(4003, "lease not active")
			return
		}
		err = r.dispatch(s, frame)
		if err != nil {
			r.emit("router_error", map[string]any{"error_code": "internal_error"})
			_ = s.sendError("internal_error", frameString(frame, "frame_id"))
			_ = s.close(websocket.StatusInternalError, "internal server error")
			return
		}
	}
}

func decodeFrame(payload []byte) (map[string]json.RawMessage, error) {
	var frame map[string]json.RawMessage
	if err := json.Unmarshal(payload, &frame); err != nil || frame == nil {
		return nil, errors.New("invalid frame")
	}
	return frame, nil
}

func containsUnpairedSurrogate(payload []byte) bool {
	inString := false
	for index := 0; index < len(payload); index++ {
		if !inString {
			if payload[index] == '"' {
				inString = true
			}
			continue
		}
		if payload[index] == '"' {
			inString = false
			continue
		}
		if payload[index] != '\\' || index+1 >= len(payload) {
			continue
		}
		if payload[index+1] != 'u' {
			index++
			continue
		}
		if index+6 > len(payload) {
			return false
		}
		value, ok := hex16(string(payload[index+2 : index+6]))
		if !ok {
			index += 5
			continue
		}
		if value >= 0xd800 && value <= 0xdbff {
			if index+12 > len(payload) || payload[index+6] != '\\' || payload[index+7] != 'u' {
				return true
			}
			low, ok := hex16(string(payload[index+8 : index+12]))
			if !ok || low < 0xdc00 || low > 0xdfff {
				return true
			}
			index += 11
		} else if value >= 0xdc00 && value <= 0xdfff {
			return true
		} else {
			index += 5
		}
	}
	return false
}
func hex16(value string) (uint16, bool) {
	var result uint16
	if len(value) != 4 {
		return 0, false
	}
	for _, digit := range []byte(value) {
		result <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			result += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			result += uint16(digit - 'a' + 10)
		case digit >= 'A' && digit <= 'F':
			result += uint16(digit - 'A' + 10)
		default:
			return 0, false
		}
	}
	return result, true
}
func validateClientFrame(frame map[string]json.RawMessage) error {
	return protocol.ValidateClientFrame(frame)
}
func frameString(frame map[string]json.RawMessage, key string) string {
	var result string
	if json.Unmarshal(frame[key], &result) != nil {
		return ""
	}
	return result
}
func frameInt(frame map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := frame[key]
	if !ok {
		return 0, false
	}
	return protocol.SafeInteger(raw)
}
func frameBool(frame map[string]json.RawMessage, key string) (bool, bool) {
	var result bool
	raw, ok := frame[key]
	if !ok || json.Unmarshal(raw, &result) != nil {
		return false, false
	}
	return result, true
}
func frameObject(frame map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	var result map[string]json.RawMessage
	raw, ok := frame[key]
	if !ok || json.Unmarshal(raw, &result) != nil || result == nil {
		return nil, false
	}
	return result, true
}
func frameAny(frame map[string]json.RawMessage, key string) any {
	var result any
	_ = json.Unmarshal(frame[key], &result)
	return result
}
func (s *session) sendError(code, requestID string) error {
	fields := map[string]any{"type": "error", "code": code}
	if requestID != "" {
		fields["request_id"] = requestID
	}
	return s.send(fields)
}
func (s *session) send(fields map[string]any) error {
	if s.sendOverride != nil {
		return s.sendOverride(fields)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	id, err := s.router.nextID()
	if err != nil {
		return err
	}
	frame := map[string]any{"version": "v1", "frame_id": id}
	for key, value := range fields {
		frame[key] = value
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, payload)
}
func (s *session) close(code websocket.StatusCode, reason string) error {
	var err error
	s.closeOnce.Do(func() {
		if s.closeOverride != nil {
			err = s.closeOverride(code, reason)
			return
		}
		err = s.conn.Close(code, reason)
	})
	return err
}

func (r *Router) register(s *session, frame map[string]json.RawMessage) error {
	agentID, instanceID := frameString(frame, "agent_id"), frameString(frame, "instance_id")
	transition := r.beginOwnership(agentID)
	defer r.finishOwnership(agentID, transition)

	if !r.ownershipCurrent(transition) {
		return errors.New("registration ownership transition superseded")
	}
	r.mu.RLock()
	owner := r.sessions[agentID]
	subscribers := r.convos[agentID]
	var reconnecting *session
	var conversationOrder []string
	if subscribers != nil {
		reconnecting = subscribers[instanceID]
		conversationOrder = append([]string(nil), r.conversationOrder[agentID]...)
	}
	r.mu.RUnlock()

	// conversation_sync sockets share one lease. Publish the replacement before
	// closing the old socket, and undo that publication if its welcome frame fails.
	if frameString(frame, "conversation_sync") == "v1" && subscribers != nil {
		if registered, err := r.registerConversationSync(s, frame, owner, reconnecting, subscribers, conversationOrder, transition); registered {
			return err
		}
	}

	oldLease, _ := r.store.LeaseFor(context.Background(), agentID)
	leaseID, err := r.nextID()
	if err != nil {
		return err
	}
	lease, err := r.store.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: agentID, InstanceID: instanceID, LeaseID: leaseID, LeaseDuration: r.lease})
	if err != nil {
		var duplicate *store.DuplicateAgentLeaseError
		if errors.As(err, &duplicate) {
			if frameString(frame, "conversation_sync") == "v1" && owner != nil && subscribers != nil {
				if registered, sharedErr := r.registerConversationSync(s, frame, owner, reconnecting, subscribers, conversationOrder, transition); registered {
					return sharedErr
				}
			}
			_ = s.send(map[string]any{"type": "lease_rejected", "request_id": frameString(frame, "frame_id"), "code": "duplicate_identity", "agent_id": agentID, "instance_id": instanceID, "active_instance_id": duplicate.ActiveInstanceID})
			_ = s.close(4009, "duplicate identity")
			return errors.New("duplicate registration")
		}
		return err
	}

	if r.goalAvailable {
		if _, err := r.store.DeleteGoalControlDesiredExcept(context.Background(), agentID, instanceID); err != nil {
			_, _ = r.store.ReleaseAgentLease(context.Background(), store.LeaseRequest{AgentID: agentID, InstanceID: instanceID, LeaseID: lease.LeaseID})
			return err
		}
	}
	if !r.ownershipCurrent(transition) {
		_, _ = r.store.ReleaseAgentLease(context.Background(), store.LeaseRequest{AgentID: agentID, InstanceID: instanceID, LeaseID: lease.LeaseID})
		return errors.New("registration ownership transition superseded")
	}
	r.mu.Lock()
	owner = r.sessions[agentID]
	subscribers = r.convos[agentID]
	oldSessions := make([]*session, 0, 1)
	if owner != nil {
		oldSessions = append(oldSessions, owner)
	}
	for _, candidate := range subscribers {
		if !containsSession(oldSessions, candidate) {
			oldSessions = append(oldSessions, candidate)
		}
	}
	r.mu.Unlock()
	for _, candidate := range oldSessions {
		r.removePendingForSession(candidate)
	}
	// Drop activity from a different instance before deriving the new
	// session's pause state. Same-instance reconnects retain their pause until
	// the client reports that compaction is complete.
	r.clearStaleActivityGeneration(agentID, instanceID)
	r.configureSession(s, frame, lease.LeaseID, lease.InstanceID)
	s.setPaused(r.agentCompacting(agentID))
	r.cancelPendingDeliveryOffer(agentID)
	r.mu.Lock()
	r.sessions[agentID] = s
	if s.conversationSync == "v1" {
		r.convos[agentID] = map[string]*session{instanceID: s}
		r.conversationOrder[agentID] = []string{instanceID}
	} else {
		delete(r.convos, agentID)
		delete(r.conversationOrder, agentID)
	}
	r.mu.Unlock()
	if err := r.sendRegistered(s, frame, lease); err != nil {
		r.rollbackRegisteredLease(s, owner, subscribers, conversationOrder, oldLease, agentID)
		_ = s.close(websocket.StatusInternalError, "registration failed")
		return err
	}
	r.replayGoalControlDesired(s)
	r.clearStaleGeneration(agentID, instanceID, lease.LeaseID, owner != nil && owner.instanceID != instanceID)
	for _, replacement := range oldSessions {
		if replacement != s {
			_ = replacement.sendError("lease_expired", "")
			_ = replacement.close(4001, "lease expired")
		}
	}
	r.offer(s)
	return nil
}

func (r *Router) registerConversationSync(s *session, frame map[string]json.RawMessage, owner, reconnecting *session, subscribers map[string]*session, conversationOrder []string, transition *ownershipTransition) (bool, error) {
	agentID, instanceID := frameString(frame, "agent_id"), frameString(frame, "instance_id")
	lease, leaseErr := r.store.LeaseFor(context.Background(), agentID)
	if owner == nil || leaseErr != nil || lease == nil || lease.ExpiresAt <= timestamp(r.now()) {
		return false, nil
	}
	if r.goalAvailable {
		if _, err := r.store.DeleteGoalControlDesiredExcept(context.Background(), agentID, instanceID); err != nil {
			return true, err
		}
	}
	if !r.ownershipCurrent(transition) {
		return true, errors.New("registration ownership transition superseded")
	}
	if reconnecting != nil {
		r.removePendingForSession(reconnecting)
	}
	r.configureSession(s, frame, lease.LeaseID, lease.InstanceID)
	s.setPaused(r.agentCompacting(agentID) || owner.isPaused())
	if !r.ownershipCurrent(transition) {
		return true, errors.New("registration ownership transition superseded")
	}
	r.mu.Lock()
	subscribers[instanceID] = s
	// Replacing an instance keeps its original connection-order position.
	if !containsString(r.conversationOrder[agentID], instanceID) {
		r.conversationOrder[agentID] = append(r.conversationOrder[agentID], instanceID)
	}
	if owner == reconnecting {
		r.sessions[agentID] = s
	}
	r.mu.Unlock()
	r.clearStaleGeneration(agentID, instanceID, lease.LeaseID, owner.instanceID != instanceID)
	if err := r.sendRegistered(s, frame, *lease); err != nil {
		r.rollbackSharedRegistration(s, owner, reconnecting, subscribers, conversationOrder, agentID, instanceID)
		_ = s.close(websocket.StatusInternalError, "registration failed")
		return true, err
	}
	r.replayGoalControlDesired(s)
	if reconnecting != nil && reconnecting != s {
		_ = reconnecting.sendError("lease_expired", "")
		_ = reconnecting.close(4001, "lease expired")
	}
	if r.isPrimarySession(s) {
		r.offer(s)
	}
	return true, nil
}

func (r *Router) rollbackSharedRegistration(s, owner, reconnecting *session, subscribers map[string]*session, conversationOrder []string, agentID, instanceID string) {
	r.mu.Lock()
	if subscribers[instanceID] == s {
		delete(subscribers, instanceID)
	}
	if owner == reconnecting && r.sessions[agentID] == s {
		r.sessions[agentID] = owner
	}
	if subscribers != nil {
		r.conversationOrder[agentID] = append([]string(nil), conversationOrder...)
	}
	r.mu.Unlock()
}

func (r *Router) rollbackRegisteredLease(s, owner *session, subscribers map[string]*session, conversationOrder []string, oldLease *domain.Lease, agentID string) {
	r.mu.Lock()
	if r.sessions[agentID] == s {
		if owner != nil {
			r.sessions[agentID] = owner
		} else {
			delete(r.sessions, agentID)
		}
	}
	if current := r.convos[agentID]; current != nil {
		if current[s.instanceID] == s {
			if subscribers != nil {
				r.convos[agentID] = subscribers
				r.conversationOrder[agentID] = append([]string(nil), conversationOrder...)
			} else {
				delete(r.convos, agentID)
				delete(r.conversationOrder, agentID)
			}
		}
	}
	r.mu.Unlock()
	_, _ = r.store.ReleaseAgentLease(context.Background(), store.LeaseRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID})
	if oldLease != nil {
		_, _ = r.store.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: oldLease.AgentID, InstanceID: oldLease.InstanceID, LeaseID: oldLease.LeaseID, LeaseDuration: r.lease})
	}
}

func (r *Router) configureSession(s *session, frame map[string]json.RawMessage, leaseID, deliveryInstance string) {
	s.agentID, s.instanceID, s.deliveryInstance, s.leaseID = frameString(frame, "agent_id"), frameString(frame, "instance_id"), deliveryInstance, leaseID
	s.role = frameString(frame, "agent_role")
	if s.role == "" {
		s.role = "agent"
	}
	s.clientType = frameString(frame, "client_type")
	s.ackConfirmation = frameString(frame, "ack_confirmation")
	s.conversationSync = frameString(frame, "conversation_sync")
	s.deliveryReceipts = frameString(frame, "delivery_receipts")
}

func (r *Router) sendRegistered(s *session, frame map[string]json.RawMessage, lease domain.Lease) error {
	fields := map[string]any{"type": "registered", "request_id": frameString(frame, "frame_id"), "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": lease.LeaseID, "expires_at": lease.ExpiresAt, "recovered": lease.Recovered, "agent_role": s.role}
	if s.ackConfirmation != "" {
		fields["ack_confirmation"] = s.ackConfirmation
	}
	if s.conversationSync != "" {
		fields["conversation_sync"] = s.conversationSync
	}
	return s.send(fields)
}

func (r *Router) replayGoalControlDesired(s *session) {
	if !r.goalAvailable || !r.sessionMatchesIdentity(s) {
		return
	}
	desired, err := r.store.GoalControlDesiredFor(context.Background(), s.agentID, s.instanceID)
	if err != nil || desired == nil {
		return
	}
	sourceAgent, sourceOK := desired.Config["sourceAgentId"].(string)
	controlID, controlOK := desired.Config["controlId"].(string)
	action, actionOK := desired.Config["action"].(string)
	goal, goalOK := desired.Config["goal"].(string)
	settings, settingsOK := desired.Config["settings"].(map[string]any)
	if !sourceOK || !controlOK || !actionOK || !goalOK || !settingsOK {
		return
	}
	_ = s.send(map[string]any{"type": "goal_control", "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "source_agent_id": sourceAgent, "control_id": controlID, "revision": desired.Revision, "action": action, "goal": goal, "settings": settings, "expires_at": desired.ExpiresAt})
}

func (r *Router) clearStaleActivityGeneration(agentID, instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.activities[agentID]; ok && value.instanceID != instanceID {
		delete(r.activities, agentID)
	}
}

func (r *Router) clearStaleGeneration(agentID, instanceID, leaseID string, ownerChanged bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if value, ok := r.activities[agentID]; ok && (ownerChanged || value.instanceID != instanceID || value.leaseID != leaseID) {
		delete(r.activities, agentID)
	}
	// Node retains model state across a same-instance reconnect. The lease
	// authenticates frames, but the model projection belongs to the instance.
	if value, ok := r.models[agentID]; ok && (ownerChanged || value.instanceID != instanceID) {
		delete(r.models, agentID)
	}
	if value, ok := r.goals[agentID]; ok && (ownerChanged || value.instanceID != instanceID || value.leaseID != leaseID) {
		delete(r.goals, agentID)
	}
	for key := range r.latestGoalRevision {
		if strings.HasPrefix(key, agentID+"\x00") && (ownerChanged || key != agentID+"\x00"+instanceID) {
			delete(r.latestGoalRevision, key)
		}
	}
}

func (r *Router) agentCompacting(agentID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	activity, ok := r.activities[agentID]
	return ok && activity.state == "compacting" && activity.expires.After(r.now())
}

func (r *Router) setAgentPaused(agentID string, paused bool) {
	r.mu.RLock()
	primary := r.sessions[agentID]
	sessions := make([]*session, 0, len(r.convos[agentID])+1)
	if primary != nil {
		sessions = append(sessions, primary)
	}
	for _, candidate := range r.convos[agentID] {
		if !containsSession(sessions, candidate) {
			sessions = append(sessions, candidate)
		}
	}
	r.mu.RUnlock()
	for _, candidate := range sessions {
		candidate.setPaused(paused)
	}
}

func (r *Router) isPrimarySession(s *session) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[s.agentID] == s
}
func (r *Router) dispatch(s *session, frame map[string]json.RawMessage) error {
	switch frameString(frame, "type") {
	case "lease_renew":
		return r.renew(s, frame)
	case "activity":
		return r.activity(s, frame)
	case "model_catalog":
		return r.modelCatalog(s, frame)
	case "model_control":
		return r.modelControl(s, frame)
	case "model_control_result":
		return r.modelControlResult(s, frame)
	case "goal_control":
		return r.goalControl(s, frame)
	case "goal_control_result":
		return r.goalControlResult(s, frame)
	case "reminder_lock_control":
		return r.reminderLockControl(s, frame)
	case "reminder_lock_control_result":
		return r.reminderLockResult(s, frame)
	case "goal_state":
		return r.goalStateFrame(s, frame)
	case "message_arrival":
		return r.messageArrival(s, frame)
	case "injection_requested", "ack", "nack":
		return r.receipt(s, frame)
	case "subagent_sync_enable":
		return r.syncEnable(s, frame)
	case "subagent_state_resync":
		return r.syncResync(s, frame)
	case "subagent_lifecycle_snapshot", "subagent_lifecycle_event":
		return r.lifecycleFrame(s, frame)
	case "ping":
		return s.send(map[string]any{"type": "pong", "request_id": frameString(frame, "frame_id")})
	}
	return nil
}
func (r *Router) activeSession(s *session) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeSessionLocked(s)
}

func (r *Router) activeSessionLocked(s *session) bool {
	if s.conversationSync == "v1" {
		return r.convos[s.agentID][s.instanceID] == s
	}
	return r.sessions[s.agentID] == s
}
func (r *Router) activeLease(s *session) bool {
	return r.activeLeaseContext(context.Background(), s)
}
func (r *Router) activeLeaseContext(ctx context.Context, s *session) bool {
	lease, err := r.store.LeaseFor(ctx, s.agentID)
	return err == nil && lease != nil && lease.LeaseID == s.leaseID && lease.InstanceID == s.deliveryInstance && lease.ExpiresAt > timestamp(r.now())
}
func (r *Router) sessionMatchesIdentity(s *session) bool {
	return r.activeSession(s) && r.activeLease(s)
}
func (r *Router) sessionMatches(s *session, frame map[string]json.RawMessage) bool {
	return r.sessionMatchesIdentity(s) && s.agentID == frameString(frame, "agent_id") && s.instanceID == frameString(frame, "instance_id") && s.leaseID == frameString(frame, "lease_id")
}
func (r *Router) leaseError(s *session, frame map[string]json.RawMessage) error {
	_ = s.sendError("lease_not_active", frameString(frame, "frame_id"))
	return s.close(4003, "lease not active")
}
func (r *Router) renew(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	lease, err := r.store.RenewAgentLease(context.Background(), store.LeaseRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID, LeaseDuration: r.lease})
	if err != nil {
		return r.leaseError(s, frame)
	}
	fields := map[string]any{"type": "registered", "request_id": frameString(frame, "frame_id"), "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "expires_at": lease.ExpiresAt, "recovered": false, "agent_role": s.role}
	if s.ackConfirmation != "" {
		fields["ack_confirmation"] = s.ackConfirmation
	}
	if s.conversationSync != "" {
		fields["conversation_sync"] = s.conversationSync
	}
	return s.send(fields)
}
func (r *Router) findTarget(agent, instance string) *session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	candidate := r.sessions[agent]
	if candidate == nil || candidate.instanceID != instance {
		return nil
	}
	return candidate
}

func (r *Router) findControlTarget(agent, instance string) (*session, string) {
	r.mu.RLock()
	candidate := r.sessions[agent]
	r.mu.RUnlock()
	if candidate == nil {
		return nil, "target_not_connected"
	}
	if candidate.instanceID != instance {
		return nil, "target_instance_changed"
	}
	if !r.sessionMatchesIdentity(candidate) {
		return nil, "target_not_connected"
	}
	return candidate, ""
}

func (r *Router) pruneExpiredPendingControls(now time.Time) {
	r.mu.Lock()
	expired := make([]struct {
		pending *pendingControl
		code    string
	}, 0)
	prune := func(pending map[string]*pendingControl, code string) {
		for key, item := range pending {
			if item.expires.After(now) {
				continue
			}
			delete(pending, key)
			expired = append(expired, struct {
				pending *pendingControl
				code    string
			}{pending: item, code: code})
		}
	}
	prune(r.pendingMod, "model_control_expired")
	prune(r.pendingGoal, "goal_control_expired")
	prune(r.pendingLock, "reminder_lock_control_expired")
	r.mu.Unlock()
	for _, item := range expired {
		if item.pending.source != nil && r.sessionMatchesIdentity(item.pending.source) {
			_ = item.pending.source.sendError(item.code, item.pending.requestID)
		}
	}
}

func pendingCapacityExceeded(pending map[string]*pendingControl, source *session, maxTotal, maxSource int) bool {
	if len(pending) >= maxTotal {
		return true
	}
	count := 0
	for _, item := range pending {
		if item.source == source {
			count++
		}
	}
	return count >= maxSource
}
func (r *Router) modelCatalog(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	var models []map[string]any
	_ = json.Unmarshal(frame["model_summaries"], &models)
	models = normalizeModels(models)
	var current map[string]any
	if raw, ok := frame["current_model"]; ok {
		_ = json.Unmarshal(raw, &current)
		current = normalizeModelSummary(current)
	}
	total := len(models)
	if value, ok := frameInt(frame, "total_count"); ok && int(value) > total {
		total = int(value)
	}
	truncated, _ := frameBool(frame, "truncated")
	truncated = truncated || total > len(models)
	r.mu.Lock()
	if !r.activeSessionLocked(s) {
		r.mu.Unlock()
		return r.leaseError(s, frame)
	}
	if current == nil {
		if previous, ok := r.models[s.agentID]; ok && previous.instanceID == s.instanceID {
			current = previous.current
		}
	}
	r.models[s.agentID] = modelProjection{instanceID: s.instanceID, leaseID: s.leaseID, current: current, available: models, total: total, truncated: truncated}
	if activity, ok := r.activities[s.agentID]; ok && activity.instanceID == s.instanceID && activity.leaseID == s.leaseID {
		activity.model = cloneMap(current)
		r.activities[s.agentID] = activity
	}
	r.mu.Unlock()
	r.broadcastModel(s.agentID)
	return nil
}
func (r *Router) projectCurrentModel(s *session, current map[string]any) {
	models := normalizeModels([]map[string]any{current})
	if len(models) == 0 {
		return
	}
	r.mu.Lock()
	projection, ok := r.models[s.agentID]
	if !ok || projection.instanceID != s.instanceID {
		projection = modelProjection{instanceID: s.instanceID}
	}
	projection.leaseID = s.leaseID
	projection.current = cloneMap(models[0])
	r.models[s.agentID] = projection
	if activity, ok := r.activities[s.agentID]; ok && activity.instanceID == s.instanceID && activity.leaseID == s.leaseID {
		activity.model = cloneMap(models[0])
		r.activities[s.agentID] = activity
	}
	r.mu.Unlock()
}

func normalizeModelSummary(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	provider, _ := value["provider"].(string)
	id, _ := value["id"].(string)
	provider = strings.TrimSpace(provider)
	id = strings.TrimSpace(id)
	if provider == "" || id == "" {
		return nil
	}
	item := map[string]any{"provider": provider, "id": id}
	if name, _ := value["name"].(string); strings.TrimSpace(name) != "" {
		item["name"] = strings.TrimSpace(name)
	}
	return item
}

func normalizeModels(values []map[string]any) []map[string]any {
	seen := map[string]bool{}
	result := []map[string]any{}
	for _, value := range values {
		item := normalizeModelSummary(value)
		if item == nil {
			continue
		}
		key := item["provider"].(string) + "\x00" + item["id"].(string)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
		if len(result) == 1000 {
			break
		}
	}
	return result
}

func (r *Router) activity(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	state := frameString(frame, "activity_state")
	if state != "active" && state != "settled" && state != "compacting" {
		return nil
	}
	count := 0
	if value, ok := frameInt(frame, "subagent_count"); ok && value >= 0 {
		count = int(value)
	}
	var subagents []map[string]any
	_ = json.Unmarshal(frame["subagents"], &subagents)
	now := r.now()
	var goal, model map[string]any
	if raw, ok := frame["goal"]; ok {
		_ = json.Unmarshal(raw, &goal)
	}
	if raw, ok := frame["current_model"]; ok {
		_ = json.Unmarshal(raw, &model)
		model = normalizeModelSummary(model)
	}
	wasSettled := false
	var previous activity
	r.mu.Lock()
	if !r.activeSessionLocked(s) {
		r.mu.Unlock()
		return r.leaseError(s, frame)
	}
	if value, ok := r.activities[s.agentID]; ok && value.instanceID == s.instanceID && value.leaseID == s.leaseID {
		previous = value
	}
	if model == nil && previous.model != nil {
		model = cloneMap(previous.model)
	}
	if model == nil {
		if value, ok := r.models[s.agentID]; ok && value.instanceID == s.instanceID {
			model = cloneMap(value.current)
		}
	}
	if model != nil {
		projection := r.models[s.agentID]
		if projection.instanceID != s.instanceID {
			projection = modelProjection{instanceID: s.instanceID}
		}
		projection.leaseID = s.leaseID
		projection.current = cloneMap(model)
		r.models[s.agentID] = projection
	}
	if goal == nil && previous.goal != nil {
		goal = cloneMap(previous.goal)
	}
	if state == "compacting" {
		count = 0
		subagents = []map[string]any{}
	}
	if state == "settled" && count == 0 {
		delete(r.activities, s.agentID)
		wasSettled = true
	} else {
		expires := now.Add(r.activityTTL)
		if state == "compacting" {
			expires = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
		}
		if count > 999 {
			count = 999
		}
		activityState := "idle"
		if state == "active" || state == "compacting" {
			activityState = state
		}
		r.activities[s.agentID] = activity{state: activityState, instanceID: s.instanceID, leaseID: s.leaseID, until: now.Add(r.activityTTL), expires: expires, count: count, subagents: subagents, goal: goal, model: model}
	}
	r.mu.Unlock()
	r.setAgentPaused(s.agentID, state == "compacting")
	if wasSettled {
		r.broadcastIdle(s.agentID, s.instanceID, now, model)
	} else {
		r.broadcastActivity(s.agentID)
	}
	if state != "compacting" {
		r.offer(s)
	}
	return nil
}
func (r *Router) broadcastIdle(agentID, instanceID string, now time.Time, model map[string]any) {
	fields := map[string]any{"type": "agent_activity", "agent_id": agentID, "instance_id": instanceID, "activity_state": "idle", "subagent_count": 0, "subagents": []map[string]any{}, "goal": map[string]any{"enabled": false, "goal": ""}, "generated_at": timestamp(now)}
	if model != nil {
		fields["current_model"] = model
	}
	r.broadcast(fields)
}

func (r *Router) modelControl(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	if s.role != "user_interaction" {
		return s.sendError("model_control_not_allowed", frameString(frame, "frame_id"))
	}
	target, targetError := r.findControlTarget(frameString(frame, "target_agent_id"), frameString(frame, "target_instance_id"))
	if targetError != "" {
		return s.sendError(targetError, frameString(frame, "frame_id"))
	}
	r.pruneExpiredPendingControls(r.now())
	key := s.agentID + "\x00" + frameString(frame, "control_id")
	r.mu.Lock()
	if _, ok := r.pendingMod[key]; ok {
		r.mu.Unlock()
		return s.sendError("model_control_already_pending", frameString(frame, "frame_id"))
	}
	pending := &pendingControl{controlID: frameString(frame, "control_id"), requestID: frameString(frame, "frame_id"), source: s, target: target, expires: r.now().Add(r.controlTTL)}
	sourceCount := 0
	for _, item := range r.pendingMod {
		if item.source == s {
			sourceCount++
		}
	}
	if len(r.pendingMod) >= r.maxControls || sourceCount >= r.maxPerSrc {
		r.mu.Unlock()
		return s.sendError("model_control_capacity_exceeded", frameString(frame, "frame_id"))
	}
	r.pendingMod[key] = pending
	r.mu.Unlock()
	// Record the correlation before writing to the target. A fast target can
	// reply as soon as the frame is written.
	if err := target.send(map[string]any{"type": "model_control", "agent_id": target.agentID, "instance_id": target.instanceID, "lease_id": target.leaseID, "source_agent_id": s.agentID, "control_id": pending.controlID, "action": frameString(frame, "action"), "model": frameAny(frame, "model"), "expires_at": timestamp(pending.expires)}); err != nil {
		r.mu.Lock()
		if r.pendingMod[key] == pending {
			delete(r.pendingMod, key)
		}
		r.mu.Unlock()
		return s.sendError("target_send_failed", frameString(frame, "frame_id"))
	}
	return nil
}
func (r *Router) modelControlResult(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	key := frameString(frame, "source_agent_id") + "\x00" + frameString(frame, "control_id")
	r.mu.Lock()
	pending := r.pendingMod[key]
	if pending != nil && pending.target == s {
		delete(r.pendingMod, key)
	}
	r.mu.Unlock()
	if pending == nil || pending.target != s {
		return s.sendError("model_control_result_not_pending", frameString(frame, "frame_id"))
	}
	result := map[string]any{"type": "model_control_result", "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "source_agent_id": frameString(frame, "source_agent_id"), "control_id": frameString(frame, "control_id")}
	for _, key := range []string{"accepted", "error_code", "current_model"} {
		if _, ok := frame[key]; ok {
			result[key] = frameAny(frame, key)
		}
	}
	if current, ok := result["current_model"].(map[string]any); ok {
		r.projectCurrentModel(s, current)
	}
	if pending.source == nil || !r.sessionMatchesIdentity(pending.source) {
		return s.sendError("model_control_result_not_pending", frameString(frame, "frame_id"))
	}
	return pending.source.send(result)
}
func (r *Router) goalControl(s *session, frame map[string]json.RawMessage) error {
	if !r.goalAvailable {
		return s.sendError("goal_control_unavailable", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	if s.role != "user_interaction" {
		return s.sendError("goal_control_not_allowed", frameString(frame, "frame_id"))
	}
	target, targetError := r.findControlTarget(frameString(frame, "target_agent_id"), frameString(frame, "target_instance_id"))
	if targetError != "" {
		return s.sendError(targetError, frameString(frame, "frame_id"))
	}
	r.pruneExpiredPendingControls(r.now())
	key := s.agentID + "\x00" + frameString(frame, "control_id")
	expires := r.now().Add(10 * time.Minute)
	config := map[string]any{"sourceAgentId": s.agentID, "controlId": frameString(frame, "control_id"), "action": frameString(frame, "action"), "goal": frameString(frame, "goal"), "settings": frameAny(frame, "settings")}
	r.mu.Lock()
	if _, ok := r.pendingGoal[key]; ok {
		r.mu.Unlock()
		return s.sendError("goal_control_already_pending", frameString(frame, "frame_id"))
	}
	if pendingCapacityExceeded(r.pendingGoal, s, r.maxControls, r.maxPerSrc) {
		r.mu.Unlock()
		return s.sendError("goal_control_capacity_exceeded", frameString(frame, "frame_id"))
	}
	desired, err := r.store.UpsertGoalControlDesired(context.Background(), target.agentID, target.instanceID, config, timestamp(expires))
	if err != nil {
		r.mu.Unlock()
		return s.sendError("goal_control_persist_failed", frameString(frame, "frame_id"))
	}
	pending := &pendingControl{controlID: frameString(frame, "control_id"), requestID: frameString(frame, "frame_id"), source: s, target: target, expires: expires, revision: desired.Revision}
	r.pendingGoal[key] = pending
	r.latestGoalRevision[target.agentID+"\x00"+target.instanceID] = desired.Revision
	r.mu.Unlock()
	if err := target.send(map[string]any{"type": "goal_control", "agent_id": target.agentID, "instance_id": target.instanceID, "lease_id": target.leaseID, "source_agent_id": s.agentID, "control_id": pending.controlID, "revision": pending.revision, "action": frameString(frame, "action"), "goal": frameString(frame, "goal"), "settings": frameAny(frame, "settings"), "expires_at": desired.ExpiresAt}); err != nil {
		r.mu.Lock()
		if r.pendingGoal[key] == pending {
			delete(r.pendingGoal, key)
		}
		r.mu.Unlock()
		return s.sendError("target_send_failed", frameString(frame, "frame_id"))
	}
	return nil
}
func (r *Router) goalControlResult(s *session, frame map[string]json.RawMessage) error {
	if !r.goalAvailable {
		return s.sendError("goal_control_unavailable", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	key := frameString(frame, "source_agent_id") + "\x00" + frameString(frame, "control_id")
	revision, revisionPresent := frameInt(frame, "revision")
	r.mu.Lock()
	desired, desiredErr := r.store.GoalControlDesiredFor(context.Background(), s.agentID, s.instanceID)
	pending := r.pendingGoal[key]
	desiredMatches := desiredErr == nil && desired != nil && desired.Revision == revision
	if desiredMatches {
		sourceAgent, sourceOK := desired.Config["sourceAgentId"].(string)
		controlID, controlOK := desired.Config["controlId"].(string)
		desiredMatches = sourceOK && controlOK && sourceAgent == frameString(frame, "source_agent_id") && controlID == frameString(frame, "control_id")
	}
	pendingMatches := pending == nil || (pending.target == s && pending.revision == revision && pending.target.instanceID == frameString(frame, "instance_id") && pending.target.leaseID == frameString(frame, "lease_id") && !r.hasNewerGoalPendingLocked(pending))
	if !revisionPresent || !desiredMatches || !pendingMatches {
		r.mu.Unlock()
		return s.sendError("goal_control_result_not_pending", frameString(frame, "frame_id"))
	}
	consumed, consumeErr := r.store.ConsumeGoalControlDesired(context.Background(), s.agentID, s.instanceID, frameString(frame, "source_agent_id"), frameString(frame, "control_id"), revision)
	if consumeErr != nil || consumed == nil {
		r.mu.Unlock()
		return s.sendError("goal_control_result_not_pending", frameString(frame, "frame_id"))
	}
	if pending != nil {
		delete(r.pendingGoal, key)
	}
	r.mu.Unlock()
	r.recordGoal(s, frame)
	if pending == nil || pending.source == nil || !r.sessionMatchesIdentity(pending.source) {
		return nil
	}
	result := map[string]any{"type": "goal_control_result", "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "source_agent_id": frameString(frame, "source_agent_id"), "control_id": frameString(frame, "control_id"), "revision": pending.revision}
	for _, key := range []string{"accepted", "error_code", "goal_state"} {
		if _, ok := frame[key]; ok {
			result[key] = frameAny(frame, key)
		}
	}
	return pending.source.send(result)
}
func (r *Router) hasNewerGoalPendingLocked(pending *pendingControl) bool {
	for _, candidate := range r.pendingGoal {
		if candidate.source == pending.source && candidate.target == pending.target && candidate.revision > pending.revision {
			return true
		}
	}
	return false
}

func (r *Router) goalStateFrame(s *session, frame map[string]json.RawMessage) error {
	if !r.goalAvailable {
		return s.sendError("goal_control_unavailable", frameString(frame, "frame_id"))
	}
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	r.recordGoal(s, frame)
	return nil
}
func (r *Router) recordGoal(s *session, frame map[string]json.RawMessage) {
	state, ok := frameObject(frame, "goal_state")
	if !ok {
		return
	}
	stateValue := rawMapToAny(state)
	r.mu.Lock()
	if !r.activeSessionLocked(s) {
		r.mu.Unlock()
		return
	}
	projection := map[string]any{"enabled": frameAny(state, "enabled"), "goal": frameAny(state, "goal")}
	previous, hadPrevious := r.goals[s.agentID]
	unchanged := hadPrevious && previous.instanceID == s.instanceID && reflect.DeepEqual(previous.state, stateValue)
	r.goals[s.agentID] = goalState{instanceID: s.instanceID, leaseID: s.leaseID, state: stateValue, projection: projection}
	if activity, ok := r.activities[s.agentID]; ok && activity.instanceID == s.instanceID && activity.leaseID == s.leaseID {
		activity.goal = cloneMap(projection)
		r.activities[s.agentID] = activity
	}
	r.mu.Unlock()
	if !unchanged {
		r.broadcastGoal(s.agentID)
	}
}
func rawMapToAny(value map[string]json.RawMessage) map[string]any {
	result := map[string]any{}
	for key, raw := range value {
		var item any
		_ = json.Unmarshal(raw, &item)
		result[key] = item
	}
	return result
}
func (r *Router) reminderLockControl(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	if s.role != "user_interaction" {
		return s.sendError("reminder_lock_control_not_allowed", frameString(frame, "frame_id"))
	}
	reminder, err := r.store.ReminderFor(context.Background(), frameString(frame, "reminder_id"))
	if err != nil || reminder == nil {
		return s.sendError("reminder_not_found", frameString(frame, "frame_id"))
	}
	if reminder.Target != frameString(frame, "target_agent_id") {
		return s.sendError("reminder_target_mismatch", frameString(frame, "frame_id"))
	}
	target, targetError := r.findControlTarget(reminder.Target, frameString(frame, "target_instance_id"))
	if targetError != "" {
		return s.sendError(targetError, frameString(frame, "frame_id"))
	}
	r.pruneExpiredPendingControls(r.now())
	key := s.agentID + "\x00" + frameString(frame, "control_id")
	r.mu.Lock()
	if _, ok := r.pendingLock[key]; ok {
		r.mu.Unlock()
		return s.sendError("reminder_lock_control_already_pending", frameString(frame, "frame_id"))
	}
	if pendingCapacityExceeded(r.pendingLock, s, r.maxControls, r.maxPerSrc) {
		r.mu.Unlock()
		return s.sendError("reminder_lock_control_capacity_exceeded", frameString(frame, "frame_id"))
	}
	pending := &pendingControl{controlID: frameString(frame, "control_id"), requestID: frameString(frame, "frame_id"), source: s, target: target, expires: r.now().Add(r.controlTTL), reminder: reminder.ID}
	r.pendingLock[key] = pending
	r.mu.Unlock()
	if err := target.send(map[string]any{"type": "reminder_lock_control", "agent_id": target.agentID, "instance_id": target.instanceID, "lease_id": target.leaseID, "source_agent_id": s.agentID, "control_id": pending.controlID, "reminder_id": reminder.ID, "action": frameString(frame, "action"), "expires_at": timestamp(pending.expires)}); err != nil {
		r.mu.Lock()
		if r.pendingLock[key] == pending {
			delete(r.pendingLock, key)
		}
		r.mu.Unlock()
		return s.sendError("target_send_failed", frameString(frame, "frame_id"))
	}
	return nil
}
func (r *Router) reminderLockResult(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	key := frameString(frame, "source_agent_id") + "\x00" + frameString(frame, "control_id")
	r.mu.Lock()
	pending := r.pendingLock[key]
	if pending != nil && pending.target == s && pending.reminder == frameString(frame, "reminder_id") {
		delete(r.pendingLock, key)
	}
	r.mu.Unlock()
	if pending == nil || pending.target != s || pending.reminder != frameString(frame, "reminder_id") {
		return s.sendError("reminder_lock_control_result_not_pending", frameString(frame, "frame_id"))
	}
	if pending.source == nil || !r.sessionMatchesIdentity(pending.source) {
		return nil
	}
	result := map[string]any{"type": "reminder_lock_control_result", "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "source_agent_id": frameString(frame, "source_agent_id"), "control_id": frameString(frame, "control_id"), "reminder_id": frameString(frame, "reminder_id")}
	for _, key := range []string{"accepted", "error_code"} {
		if _, ok := frame[key]; ok {
			result[key] = frameAny(frame, key)
		}
	}
	return pending.source.send(result)
}
func (r *Router) messageArrival(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	fields := map[string]any{"type": "message_arrival", "agent_id": s.agentID, "from": frameString(frame, "from"), "source": frameAny(frame, "source"), "preview": frameString(frame, "preview"), "body": frameString(frame, "body"), "created_at": frameString(frame, "created_at"), "message_id": frameString(frame, "message_id")}
	if subject := frameString(frame, "subject"); subject != "" {
		fields["subject"] = subject
	}
	r.broadcast(fields)
	return nil
}
func (r *Router) broadcast(fields map[string]any) {
	// Node projects live broadcasts only to conversation-sync subscribers.
	for _, candidate := range r.conversationRecipients() {
		_ = candidate.send(fields)
	}
}
func (r *Router) receipt(s *session, frame map[string]json.RawMessage) error {
	if !r.sessionMatches(s, frame) {
		return r.leaseError(s, frame)
	}
	request := store.ReceiptRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID, MessageID: frameString(frame, "message_id"), AttemptNumber: int(mustInt(frame, "attempt_number")), AllowBatchWithoutDeliveryItems: s.deliveryReceipts != "delivery_items_v1"}
	if raw, ok := frame["delivery_items"]; ok {
		_ = json.Unmarshal(raw, &request.DeliveryItems)
	}
	switch frameString(frame, "type") {
	case "injection_requested":
		_, err := r.store.RecordInjectionRequested(context.Background(), request)
		if err != nil {
			return r.receiptError(s, frame, err)
		}
		return nil
	case "ack":
		ack, err := r.store.AcknowledgeDelivery(context.Background(), request)
		if err != nil {
			return r.receiptError(s, frame, err)
		}
		if s.ackConfirmation == "correlated_v1" {
			_ = s.send(map[string]any{"type": "delivery_acknowledged", "request_id": frameString(frame, "frame_id"), "agent_id": s.agentID, "instance_id": s.instanceID, "lease_id": s.leaseID, "message_id": ack.MessageID, "message_incarnation_id": ack.MessageIncarnationID, "attempt_number": request.AttemptNumber})
		}
		r.offer(s)
		return nil
	default:
		result, err := r.store.NackDelivery(context.Background(), request, frameString(frame, "reason"), r.maxAttempts)
		if err != nil {
			return r.receiptError(s, frame, err)
		}
		_ = result
		r.offer(s)
		return nil
	}
}
func mustInt(frame map[string]json.RawMessage, key string) int64 {
	value, _ := frameInt(frame, key)
	return value
}
func (r *Router) receiptError(s *session, frame map[string]json.RawMessage, err error) error {
	var leaseErr *store.LeaseNotActiveError
	if errors.As(err, &leaseErr) {
		return r.leaseError(s, frame)
	}
	var deliveryErr *store.DeliveryNotActiveError
	if errors.As(err, &deliveryErr) {
		_ = s.sendError("delivery_not_active", frameString(frame, "frame_id"))
		r.offer(s)
		return nil
	}
	return err
}
func (r *Router) offer(s *session) {
	r.offerContext(context.Background(), s)
}
func (r *Router) offerContext(ctx context.Context, s *session) {
	offerCtx, finishStore, finishOffer, ok := r.beginDeliveryOffer(ctx)
	if !ok {
		return
	}
	storeFinished := false
	defer func() {
		if !storeFinished {
			finishStore()
		}
		finishOffer()
	}()
	fields := r.deliveryOfferFields(offerCtx, s)
	finishStore()
	storeFinished = true
	if fields != nil && s != nil {
		_ = s.send(fields)
	}
}
func (r *Router) deliveryOfferFields(ctx context.Context, s *session) map[string]any {
	// Conversation-sync sockets share a lease, but only the primary socket owns
	// durable delivery. Secondary activity must not claim the same queue.
	if s == nil || r.isClosed() || ctx.Err() != nil || !r.isPrimarySession(s) || !r.activeLeaseContext(ctx, s) || s.isPaused() {
		return nil
	}
	delivery, err := r.store.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: s.agentID, InstanceID: s.deliveryInstance, LeaseID: s.leaseID, DeliveryLeaseDuration: r.delivery})
	if err != nil || delivery == nil || ctx.Err() != nil {
		return nil
	}
	fields := map[string]any{"type": "message", "message": deliveryEnvelopeContext(ctx, r, s, delivery.Message), "attempt_number": delivery.AttemptNumber, "delivery_expires_at": delivery.DeliveryExpiresAt}
	if s.ackConfirmation == "correlated_v1" {
		fields["message_incarnation_id"] = delivery.Message.MessageIncarnationID
	}
	if len(delivery.Items) > 1 {
		fields["delivery_items"] = delivery.Items
	}
	return fields
}
func (r *Router) isClosed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}
func deliveryEnvelope(r *Router, s *session, message domain.Message) map[string]any {
	return deliveryEnvelopeContext(context.Background(), r, s, message)
}
func deliveryEnvelopeContext(ctx context.Context, r *Router, s *session, message domain.Message) map[string]any {
	value := map[string]any{"id": message.ID, "from": message.From, "to": message.To, "body": message.Body, "created_at": message.CreatedAt, "source": message.Source}
	if message.Subject != nil {
		value["subject"] = *message.Subject
	}
	if message.Sentinel != nil {
		value["sentinel"] = message.Sentinel
	}
	if domain.IsBatchedBody(message.Body) || message.Sentinel != nil || message.From == s.agentID {
		return value
	}
	r.mu.Lock()
	sender := r.sessions[message.From]
	r.mu.Unlock()
	if sender == nil || !r.activeLeaseContext(ctx, sender) {
		return value
	}
	footer := "\n\n[Reply via mailbox to " + message.From + ".]"
	if len([]byte(message.Body))+len([]byte(footer)) <= r.store.AdmissionLimits().MaxBodyBytes {
		value["body"] = message.Body + footer
	}
	return value
}

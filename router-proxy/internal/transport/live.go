package transport

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) offerAgent(agentID string) {
	r.ownershipMu.Lock()
	registering := r.ownership[agentID] != nil
	r.ownershipMu.Unlock()
	if registering {
		return
	}
	r.mu.RLock()
	s := r.sessions[agentID]
	r.mu.RUnlock()
	r.offer(s)
}

func (r *Router) sweepLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.runSweep(ctx); err != nil {
				r.emit("router_error", map[string]any{"error_code": "sweep_failed"})
			}
		}
	}
}

func (r *Router) runSweep(ctx context.Context) error {
	r.sweepMu.Lock()
	defer r.sweepMu.Unlock()
	_, err := r.sweep(ctx)
	return err
}

func (r *Router) sweepLiveState() {
	now := r.now()
	r.mu.RLock()
	expiredAgents := make([]string, 0)
	for agentID, activity := range r.activities {
		if !activity.expires.After(now) {
			expiredAgents = append(expiredAgents, agentID)
		}
	}
	r.mu.RUnlock()
	for _, agentID := range expiredAgents {
		_ = r.expireActivity(agentID, now)
	}

	type expiredControl struct {
		pending *pendingControl
		code    string
	}
	expired := make([]expiredControl, 0)
	r.mu.Lock()
	for key, pending := range r.pendingMod {
		if pending.expires.After(now) {
			continue
		}
		delete(r.pendingMod, key)
		expired = append(expired, expiredControl{pending: pending, code: "model_control_expired"})
	}
	for key, pending := range r.pendingGoal {
		if pending.expires.After(now) {
			continue
		}
		delete(r.pendingGoal, key)
		expired = append(expired, expiredControl{pending: pending, code: "goal_control_expired"})
	}
	for key, pending := range r.pendingLock {
		if pending.expires.After(now) {
			continue
		}
		delete(r.pendingLock, key)
		expired = append(expired, expiredControl{pending: pending, code: "reminder_lock_control_expired"})
	}
	r.mu.Unlock()
	for _, item := range expired {
		if item.pending.source != nil && r.sessionMatchesIdentity(item.pending.source) {
			_ = item.pending.source.sendError(item.code, item.pending.requestID)
		}
	}
}

func (r *Router) expireActivity(agentID string, now time.Time) error {
	r.mu.Lock()
	activity, ok := r.activities[agentID]
	if !ok || activity.expires.After(now) {
		r.mu.Unlock()
		return nil
	}
	delete(r.activities, agentID)
	primary := r.sessions[agentID]
	r.mu.Unlock()
	r.setAgentPaused(agentID, false)
	r.broadcast(map[string]any{"type": "agent_activity", "agent_id": agentID, "instance_id": activity.instanceID, "activity_state": "idle", "subagent_count": 0, "subagents": []map[string]any{}, "generated_at": timestamp(now)})
	r.offer(primary)
	return nil
}

func (r *Router) rateLimit(sender string) error {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	for key, values := range r.rates {
		kept := values[:0]
		for _, value := range values {
			if value.After(cutoff) {
				kept = append(kept, value)
			}
		}
		if len(kept) == 0 {
			delete(r.rates, key)
		} else {
			r.rates[key] = kept
		}
	}
	values := r.rates[sender]
	if len(values) >= 60 {
		retry := int(values[0].Add(time.Minute).Sub(now).Seconds())
		if retry < 1 {
			retry = 1
		}
		return &httpError{status: http.StatusTooManyRequests, code: "rate_limited", retryAfter: retry}
	}
	r.rates[sender] = append(values, now)
	return nil
}

func (r *Router) connectedAgents() map[string]any {
	now := r.now()
	r.mu.Lock()
	candidates := make([]*session, 0, len(r.sessions))
	for _, s := range r.sessions {
		candidates = append(candidates, s)
	}
	models := make(map[string]modelProjection, len(r.models))
	for key, value := range r.models {
		models[key] = value
	}
	activities := make(map[string]activity, len(r.activities))
	for key, value := range r.activities {
		activities[key] = value
	}
	goals := make(map[string]goalState, len(r.goals))
	for key, value := range r.goals {
		goals[key] = value
	}
	lifecycle := make(map[string]lifecycleAgent, len(r.lifecycleState))
	for key, value := range r.lifecycleState {
		lifecycle[key] = value
	}
	activitySnapshotVersion := r.nextVersion
	r.mu.Unlock()
	agents := make([]map[string]any, 0, len(candidates))
	for _, s := range candidates {
		lease, err := r.store.LeaseFor(context.Background(), s.agentID)
		if err != nil || lease == nil || lease.LeaseID != s.leaseID || lease.InstanceID != s.deliveryInstance || lease.ExpiresAt <= timestamp(now) {
			continue
		}
		item := map[string]any{"agent_id": s.agentID, "instance_id": s.instanceID, "agent_role": s.role, "connected_at": lease.AcquiredAt, "lease_expires_at": lease.ExpiresAt, "activity_state": "idle", "subagent_count": 0, "subagents": []map[string]any{}}
		if a, ok := activities[s.agentID]; ok && a.instanceID == s.instanceID && a.leaseID == lease.LeaseID && a.expires.After(now) {
			item["activity_state"] = a.state
			item["subagent_count"] = a.count
			item["subagents"] = a.subagents
		}
		if model, ok := models[s.agentID]; ok && model.instanceID == s.instanceID && model.leaseID == lease.LeaseID {
			item["available_models"] = model.available
			item["model_catalog_total_count"] = model.total
			item["model_catalog_truncated"] = model.truncated
			if model.current != nil {
				item["current_model"] = model.current
			}
		} else {
			item["available_models"] = []map[string]any{}
			item["model_catalog_total_count"] = 0
			item["model_catalog_truncated"] = false
		}
		if goal, ok := goals[s.agentID]; ok && goal.instanceID == s.instanceID && goal.leaseID == lease.LeaseID {
			item["goal"] = goal.projection
			item["goal_state"] = goal.state
		}
		if state, ok := lifecycle[s.agentID]; ok {
			item["subagent_version"] = state.version
			item["subagent_count"] = len(state.subagents)
			item["subagents"] = state.subagents
		}
		agents = append(agents, item)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i]["agent_id"].(string) < agents[j]["agent_id"].(string) })
	return map[string]any{"generated_at": timestamp(now), "activity_snapshot_version": activitySnapshotVersion, "agents": agents}
}

func (r *Router) sweep(ctx context.Context) (store.SweepResult, error) {
	// Recovery owns these deletions and transitions so the router can publish
	// the same records as Node. In particular, do not let store retention remove
	// an expired queued message before message_expired is emitted.
	expired, err := r.store.RecoverExpiredUnclaimedMessages(ctx)
	if err != nil {
		return store.SweepResult{}, err
	}
	for _, messageID := range expired {
		r.emit("message_expired", map[string]any{"message_id": messageID})
	}
	timedOut, err := r.store.RecoverTimedOutDeliveries(ctx, r.maxAttempts)
	if err != nil {
		return store.SweepResult{}, err
	}
	for _, outcome := range timedOut {
		r.emit("delivery_timed_out", map[string]any{
			"message_id": outcome.MessageID,
			"agent_id":   outcome.AgentID,
			"attempt":    outcome.Attempt,
			"lease_id":   outcome.LeaseID,
			"state":      outcome.State,
			"reason":     "delivery_timeout",
		})
		if outcome.State == "dead_letter" {
			r.emit("message_dead_lettered", map[string]any{
				"message_id": outcome.MessageID,
				"agent_id":   outcome.AgentID,
				"attempt":    outcome.Attempt,
				"lease_id":   outcome.LeaseID,
				"reason":     "delivery_timeout",
			})
			for _, reminder := range outcome.RecurringReminderRearmed {
				r.emit("recurring_reminder_rearmed", map[string]any{
					"reminder_id": reminder.ReminderID,
					"message_id":  outcome.MessageID,
					"next_due_at": reminder.NextDueAt,
					"reason":      "delivery_timeout",
				})
			}
		}
	}
	for _, outcome := range timedOut {
		r.offerAgent(outcome.AgentID)
	}

	result, err := r.store.SweepLifecycle(ctx, store.DefaultRetentionPolicy)
	if err != nil {
		return result, err
	}
	r.closeExpiredLeaseSessions(result.ExpiredLeases)
	r.publishLifecycleUpdates(ctx, result.LifecycleUpdates)
	reminderSuccess, reminderFailure := r.sweepReminders(ctx)
	r.sweepAttachmentsBestEffort()
	// Node publishes activity expiry before its heartbeat/sweep record.
	r.sweepLiveState()
	r.emitSweepRecord(result, len(expired), len(timedOut), reminderSuccess, reminderFailure)
	return result, nil
}

func (r *Router) emitSweepRecord(result store.SweepResult, expiredMessages, timedOutDeliveries, reminderSuccess, reminderFailure int) {
	counts := map[string]any{
		"stale_agent_count":        result.StaleAgents,
		"expired_message_count":    expiredMessages,
		"timed_out_delivery_count": timedOutDeliveries,
		"reminder_success_count":   reminderSuccess,
		"reminder_failure_count":   reminderFailure,
		"server_job_success_count": 0,
		"server_job_failure_count": 0,
	}
	if result.ConversationMessages > 0 {
		counts["conversation_message_pruned_count"] = result.ConversationMessages
	}
	if result.LifecycleExpired > 0 {
		counts["lifecycle_running_expired_count"] = result.LifecycleExpired
	}
	if result.LifecycleStates > 0 {
		counts["lifecycle_tombstone_pruned_count"] = result.LifecycleStates
	}
	if result.LifecycleEvents > 0 {
		counts["lifecycle_ledger_pruned_count"] = result.LifecycleEvents
	}
	if result.LifecycleProducers > 0 {
		counts["lifecycle_producer_pruned_count"] = result.LifecycleProducers
	}
	if result.GoalControlStates > 0 {
		counts["goal_control_pruned_count"] = result.GoalControlStates
	}
	work := false
	for _, value := range counts {
		if count, ok := value.(int); ok && count > 0 {
			work = true
			break
		}
	}
	now := r.now()
	if r.lastSweepRecord.IsZero() {
		r.lastSweepRecord = now
	}
	if work || now.Sub(r.lastSweepRecord) >= r.sweepHeartbeat {
		r.emit("lifecycle_sweep_completed", counts)
		r.lastSweepRecord = now
	}
}

func (r *Router) sweepAttachments() error {
	if r.attachments == nil {
		return nil
	}
	_, err := r.attachments.Sweep(func(id string) bool {
		referenced, referenceErr := r.store.ConversationAttachmentReferenced(context.Background(), id)
		// Keep the file when the reference check fails. Deleting on an
		// uncertain database read would turn a transient error into data loss.
		return referenceErr != nil || referenced
	})
	return err
}

func (r *Router) sweepAttachmentsBestEffort() {
	if err := r.sweepAttachments(); err != nil {
		r.emit("router_error", map[string]any{"error_code": "attachment_sweep_failed"})
	}
}

func (r *Router) publishLifecycleUpdates(ctx context.Context, updates []store.LifecycleUpdate) {
	for _, update := range updates {
		snapshot, err := r.store.SubagentLifecycleSnapshot(ctx, update.ParentAgentID)
		if err != nil {
			r.emit("router_error", map[string]any{"error_code": "lifecycle_projection_failed", "agent_id": update.ParentAgentID})
			continue
		}
		r.mu.Lock()
		r.lifecycleState[update.ParentAgentID] = lifecycleAgent{version: update.RouterVersion, subagents: snapshot.Subagents}
		if update.RouterVersion > r.nextVersion {
			r.nextVersion = update.RouterVersion
		}
		r.mu.Unlock()
		_ = r.broadcastLifecycle(update.ParentAgentID, update.RouterVersion)
	}
}

func (r *Router) closeExpiredLeaseSessions(expired []store.ExpiredLease) {
	if len(expired) == 0 {
		return
	}
	r.mu.RLock()
	candidates := make([]*session, 0, len(r.sessions))
	seen := make(map[*session]struct{})
	for _, candidate := range r.sessions {
		if _, ok := seen[candidate]; !ok {
			seen[candidate] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	for _, subscribers := range r.convos {
		for _, candidate := range subscribers {
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
	}
	r.mu.RUnlock()
	for _, lease := range expired {
		for _, candidate := range candidates {
			if candidate.agentID != lease.AgentID || candidate.deliveryInstance != lease.InstanceID || candidate.leaseID != lease.LeaseID || !r.activeSession(candidate) {
				continue
			}
			_ = candidate.sendError("lease_expired", "")
			_ = candidate.close(4001, "lease expired")
		}
	}
}

// handleTmux keeps the route present even when no adapter is configured. The
// production launcher can inject an adapter; tests can use the same interface.
func (r *Router) handleTmux(w http.ResponseWriter, req *http.Request) bool {
	if r.tmux == nil {
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "tmux_unavailable"})
		return true
	}
	// The adapter-backed operations are intentionally kept behind this boundary.
	// A configured adapter can be exercised through the collection read path.
	if req.Method == http.MethodGet && req.URL.Path == "/v1/tmux/windows" {
		r.mu.Lock()
		selected := r.selectedTmux
		r.mu.Unlock()
		if selected == "" {
			r.writeError(w, &httpError{status: http.StatusNotFound, code: "tmux_unavailable"})
			return true
		}
		windows, err := r.tmux.Enumerate(req.Context(), selected)
		if err != nil {
			r.writeError(w, &httpError{status: 500, code: "tmux_operation_failed"})
			return true
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"session": selected, "windows": windows})
		return true
	}
	r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
	return true
}

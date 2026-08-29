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
			if _, err := r.sweep(ctx); err != nil {
				r.emit("router_error", map[string]any{"error_code": "sweep_failed"})
			}
			r.sweepLiveState()
		}
	}
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
	result, err := r.store.Sweep(ctx, store.DefaultRetentionPolicy)
	if err != nil {
		return result, err
	}
	_, _ = r.store.RecoverExpiredUnclaimedMessages(ctx)
	_, _ = r.store.RecoverTimedOutDeliveries(ctx, r.maxAttempts)
	r.sweepReminders(ctx)
	return result, nil
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

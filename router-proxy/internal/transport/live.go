package transport

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) offerAgent(agentID string) {
	// HTTP enqueue/retry can race a WebSocket replacement. Use the same
	// ownership lock as frame dispatch before claiming a delivery.
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	r.mu.Lock()
	s := r.sessions[agentID]
	r.mu.Unlock()
	r.offer(s)
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
		if a, ok := activities[s.agentID]; ok && a.expires.After(now) {
			item["activity_state"] = a.state
			item["subagent_count"] = a.count
			item["subagents"] = a.subagents
		}
		if model, ok := models[s.agentID]; ok && model.instanceID == s.instanceID {
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
		if goal, ok := goals[s.agentID]; ok && goal.instanceID == s.instanceID {
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

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func (r *Router) beginDeliveryOffer(ctx context.Context) (context.Context, func(), func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.deliveryOfferMu.Lock()
	if r.deliveryOffersClosed {
		r.deliveryOfferMu.Unlock()
		return nil, nil, nil, false
	}
	lifecycleCtx := r.deliveryOffersContext
	r.deliveryOfferWG.Add(1)
	r.deliveryStoreWG.Add(1)
	r.deliveryOfferMu.Unlock()

	// The caller's context still controls the operation, while the lifecycle
	// context cancels it when Close begins. Both are needed because direct offers
	// originate in synchronous WebSocket handlers with a background context.
	offerCtx, cancel := context.WithCancel(ctx)
	stopLifecycle := context.AfterFunc(lifecycleCtx, cancel)
	finishStore := func() { r.deliveryStoreWG.Done() }
	finishOffer := func() {
		stopLifecycle()
		cancel()
		r.deliveryOfferWG.Done()
	}
	return offerCtx, finishStore, finishOffer, true
}

// offerAgent is the retained tmux offer path. Managed direct delivery enters
// through Router.afterEnqueue and its manager-owned lane instead.
func (r *Router) offerAgent(agentID string) {
	if r.managed != nil {
		r.managed.OfferDirect(agentID)
	}
	r.ownershipMu.Lock()
	registering := r.ownership[agentID] != nil
	r.ownershipMu.Unlock()
	if registering {
		return
	}
	r.scheduleDeliveryOffer(agentID)
}

func (r *Router) scheduleDeliveryOffer(agentID string) {
	r.deliveryOfferMu.Lock()
	defer r.deliveryOfferMu.Unlock()
	if r.deliveryOffersClosed || r.pendingDeliveryOffers[agentID] != nil {
		return
	}
	if r.deliverySession(agentID) == nil {
		return
	}
	pending := &pendingDeliveryOffer{done: make(chan struct{})}
	r.deliveryOfferWG.Add(1)
	r.deliveryStoreWG.Add(1)
	pending.timer = time.AfterFunc(DefaultDeliveryOfferCoalesce, func() {
		r.runDeliveryOffer(agentID, pending)
	})
	r.pendingDeliveryOffers[agentID] = pending
}

func (r *Router) runDeliveryOffer(agentID string, pending *pendingDeliveryOffer) {
	r.deliveryOfferMu.Lock()
	if r.pendingDeliveryOffers[agentID] == pending {
		delete(r.pendingDeliveryOffers, agentID)
	}
	r.deliveryOfferMu.Unlock()
	defer func() {
		close(pending.done)
		r.deliveryOfferWG.Done()
	}()

	// Resolve the delivery owner after the timer fires. A Pi registration always
	// wins; otherwise the current memory-only browser owner receives the batch.
	session := r.deliverySession(agentID)
	fields := func() map[string]any {
		defer r.deliveryStoreWG.Done()
		return r.deliveryOfferFields(r.deliveryOffersContext, session)
	}()
	if fields != nil && session != nil {
		_ = session.send(fields)
	}
}

func (r *Router) cancelPendingDeliveryOffer(agentID string) {
	r.deliveryOfferMu.Lock()
	pending := r.pendingDeliveryOffers[agentID]
	if pending == nil {
		r.deliveryOfferMu.Unlock()
		return
	}
	delete(r.pendingDeliveryOffers, agentID)
	stopped := pending.timer.Stop()
	r.deliveryOfferMu.Unlock()
	if stopped {
		close(pending.done)
		r.deliveryStoreWG.Done()
		r.deliveryOfferWG.Done()
		return
	}
	<-pending.done
}

func (r *Router) stopPendingDeliveryOffers(ctx context.Context) error {
	r.deliveryOfferMu.Lock()
	r.deliveryOffersClosed = true
	pending := make([]*pendingDeliveryOffer, 0, len(r.pendingDeliveryOffers))
	for agentID, offer := range r.pendingDeliveryOffers {
		delete(r.pendingDeliveryOffers, agentID)
		pending = append(pending, offer)
	}
	stopped := make([]*pendingDeliveryOffer, 0, len(pending))
	for _, offer := range pending {
		if offer.timer.Stop() {
			stopped = append(stopped, offer)
		}
	}
	r.deliveryOfferMu.Unlock()
	for _, offer := range stopped {
		close(offer.done)
		r.deliveryStoreWG.Done()
		r.deliveryOfferWG.Done()
	}
	if err := waitForDeliveryOffers(ctx, &r.deliveryStoreWG); err != nil {
		return err
	}
	return waitForDeliveryOffers(ctx, &r.deliveryOfferWG)
}

func waitForDeliveryOffers(ctx context.Context, group *sync.WaitGroup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Router) sweepLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	// Receipt retention must progress even when unrelated lifecycle work fails
	// or blocks. Join this loop on shutdown before the store can be closed.
	receiptDone := make(chan struct{})
	go func() {
		defer close(receiptDone)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				delay := time.Second
				_, err := r.store.PruneExpiredConversationReceipts(ctx)
				if err == nil {
					delay, err = r.store.ConversationReceiptCleanupDelay(ctx, time.Second)
				}
				if err != nil && ctx.Err() == nil {
					r.emitEvent(RouterErrorEvent("conversation_receipt_cleanup_failed", ""))
					delay = time.Second // retry without a tight error loop
				}
				timer.Reset(delay)
			}
		}
	}()
	defer func() { <-receiptDone }()
	ticker := time.NewTicker(r.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.runSweep(ctx); err != nil {
				r.emitEvent(RouterErrorEvent("sweep_failed", ""))
			}
		}
	}
}

func (r *Router) runSweep(ctx context.Context) error {
	r.sweepMu.Lock()
	_, err := r.sweep(ctx)
	r.sweepMu.Unlock()
	if err != nil {
		return err
	}
	// Policies are observational and must not hold the lifecycle sweep mutex.
	// A timed-out or noncooperative policy cannot delay durable recovery.
	r.runPoliciesAsync(ctx)
	if r.uiLive != nil {
		r.uiLive.publish(ctx)
	}
	return nil
}

func (r *Router) sweepLiveState() {
	now := r.now()
	r.closeExpiredUISessions(now)
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

// closeExpiredUISessions bounds connection-local browser state when a tab or
// network disappears without a WebSocket close. These sessions never have a
// SQLite lease to sweep.
func (r *Router) closeExpiredUISessions(now time.Time) {
	r.mu.RLock()
	expired := make([]*session, 0)
	for candidate := range r.uiSessions {
		if !candidate.uiLeaseActive(now) {
			expired = append(expired, candidate)
		}
	}
	r.mu.RUnlock()
	for _, candidate := range expired {
		_ = candidate.sendError("lease_expired", "")
		_ = candidate.close(4001, "lease expired")
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
	goal := cloneMap(activity.goal)
	model := cloneMap(activity.model)
	if goal == nil {
		if value, ok := r.goals[agentID]; ok && value.instanceID == activity.instanceID && value.leaseID == activity.leaseID {
			goal = cloneMap(value.projection)
		}
	}
	if model == nil {
		if value, ok := r.models[agentID]; ok && value.instanceID == activity.instanceID && value.leaseID == activity.leaseID {
			model = cloneMap(value.current)
		}
	}
	primary := r.sessions[agentID]
	r.mu.Unlock()
	r.setAgentPaused(agentID, false)
	fields := map[string]any{"type": "agent_activity", "agent_id": agentID, "instance_id": activity.instanceID, "activity_state": "idle", "subagent_count": 0, "subagents": []map[string]any{}, "generated_at": timestamp(now)}
	if goal != nil {
		fields["goal"] = goal
	}
	if model != nil {
		fields["current_model"] = model
	}
	r.broadcast(fields)
	r.offer(primary)
	return nil
}

// rateReservation is an opaque, per-admission identity. The pointer, not the
// timestamp, identifies the exact pending charge to cancel or finalize.
// This window is process-local; it does not add cross-process or crash safety.
type rateReservation struct {
	sender string
	at     time.Time
}

func (r *Router) rateLimit(sender string) error {
	now := r.now()
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if err := r.checkRateLocked(sender, now); err != nil {
		return err
	}
	r.rates[sender] = append(r.rates[sender], now)
	return nil
}

// reserveRate counts a pending ordinary send before its Store transaction.
// Callers must cancel on rejection or finalize on success, outside rateMu.
func (r *Router) reserveRate(sender string) (*rateReservation, error) {
	now := r.now()
	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if err := r.checkRateLocked(sender, now); err != nil {
		return nil, err
	}
	reservation := &rateReservation{sender: sender, at: now}
	r.rateReservations[reservation] = struct{}{}
	return reservation, nil
}

// cancelRateReservation removes only this admission's pending charge. A late
// cancel cannot remove another request's charge or a finalized charge.
func (r *Router) cancelRateReservation(reservation *rateReservation) {
	r.rateMu.Lock()
	delete(r.rateReservations, reservation)
	r.rateMu.Unlock()
}

// finalizeRateReservation keeps the successful admission's original timestamp
// in the ordinary window. Repeated or late finalization has no effect.
func (r *Router) finalizeRateReservation(reservation *rateReservation) {
	r.rateMu.Lock()
	if _, pending := r.rateReservations[reservation]; pending {
		delete(r.rateReservations, reservation)
		values := r.rates[reservation.sender]
		// Store completions may finish out of order. Keep the oldest first
		// so Retry-After remains correct for ordinary and reserved sends.
		index := sort.Search(len(values), func(i int) bool { return values[i].After(reservation.at) })
		values = append(values, time.Time{})
		copy(values[index+1:], values[index:])
		values[index] = reservation.at
		r.rates[reservation.sender] = values
	}
	r.rateMu.Unlock()
}

// checkRateLocked prunes both kinds of charge and applies the same one-minute,
// 60-admission limit used by reminder and ordinary HTTP sends. No Store, mu,
// or other external operation is called while rateMu is held.
func (r *Router) checkRateLocked(sender string, now time.Time) error {
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
	count := len(r.rates[sender])
	var oldest time.Time
	if count > 0 {
		oldest = r.rates[sender][0]
	}
	for reservation := range r.rateReservations {
		if !reservation.at.After(cutoff) {
			delete(r.rateReservations, reservation)
			continue
		}
		if reservation.sender == sender {
			count++
			if oldest.IsZero() || reservation.at.Before(oldest) {
				oldest = reservation.at
			}
		}
	}
	if count >= 60 {
		retry := int(oldest.Add(time.Minute).Sub(now).Seconds())
		if retry < 1 {
			retry = 1
		}
		return &httpError{status: http.StatusTooManyRequests, code: "rate_limited", retryAfter: retry}
	}
	return nil
}

func (r *Router) connectedAgents(ctx context.Context, includeModels bool) map[string]any {
	if ctx == nil {
		ctx = context.Background()
	}
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
	inlineToolStates := make(map[string]inlineToolState, len(r.inlineToolStates))
	for key, value := range r.inlineToolStates {
		inlineToolStates[key] = value
	}
	lifecycle := make(map[string]lifecycleAgent, len(r.lifecycleState))
	for key, value := range r.lifecycleState {
		lifecycle[key] = value
	}
	activitySnapshotVersion := r.nextVersion
	r.mu.Unlock()
	agentIDs := make([]string, 0, len(candidates))
	for _, s := range candidates {
		agentIDs = append(agentIDs, s.agentID)
	}
	durableSnapshot, _ := r.store.ConnectedAgentSnapshot(ctx, agentIDs, r.lifecycleAvailable)
	agents := make([]map[string]any, 0, len(candidates))
	for _, s := range candidates {
		lease, leaseOK := durableSnapshot.Leases[s.agentID]
		if !leaseOK || lease.LeaseID != s.leaseID || lease.InstanceID != s.deliveryInstance || lease.ExpiresAt <= timestamp(now) {
			continue
		}
		item := map[string]any{"agent_id": s.agentID, "urgent": durableSnapshot.Urgency[s.agentID], "instance_id": s.instanceID, "agent_role": s.role, "connected_at": lease.AcquiredAt, "lease_expires_at": lease.ExpiresAt, "activity_state": "idle", "subagent_count": 0, "subagents": []map[string]any{}, "inline_tool_control_available": s.inlineToolControl}
		if inlineToolState, ok := inlineToolStates[s.agentID]; ok && inlineToolState.instanceID == s.instanceID && inlineToolState.leaseID == lease.LeaseID {
			item["inline_tool_enabled"] = inlineToolState.enabled
		}
		activity, activityOK := activities[s.agentID]
		if activityOK && activity.instanceID == s.instanceID && activity.leaseID == lease.LeaseID && activity.expires.After(now) {
			item["activity_state"] = activity.state
			item["subagent_count"] = activity.count
			item["subagents"] = activity.subagents
		}
		if model, ok := models[s.agentID]; ok && model.instanceID == s.instanceID {
			if includeModels {
				item["available_models"] = model.available
				item["model_catalog_total_count"] = model.total
				item["model_catalog_truncated"] = model.truncated
			}
			if model.current != nil {
				item["current_model"] = model.current
			}
		} else if includeModels {
			item["available_models"] = []map[string]any{}
			item["model_catalog_total_count"] = 0
			item["model_catalog_truncated"] = false
		}
		if activityOK && activity.instanceID == s.instanceID && activity.leaseID == lease.LeaseID {
			if activity.goal != nil {
				item["goal"] = activity.goal
			}
			if activity.model != nil {
				item["current_model"] = activity.model
			}
		}
		if goal, ok := goals[s.agentID]; ok && goal.instanceID == s.instanceID && goal.leaseID == lease.LeaseID {
			if _, exists := item["goal"]; !exists && goal.projection != nil {
				item["goal"] = goal.projection
			}
			if goal.state != nil {
				item["goal_state"] = goal.state
			}
		}
		state, lifecycleOK := lifecycle[s.agentID]
		if !lifecycleOK && r.lifecycleAvailable {
			if durableLifecycle, ok := durableSnapshot.Lifecycle[s.agentID]; ok && durableLifecycle.Producer.Authority == "v2" {
				children := make([]map[string]any, 0, len(durableLifecycle.Agent.Subagents))
				for _, child := range durableLifecycle.Agent.Subagents {
					children = append(children, lifecycleChildFrame(child))
				}
				state = lifecycleAgent{version: durableLifecycle.Agent.SubagentVersion, subagents: children}
				lifecycleOK = true
			}
		}
		if lifecycleOK {
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
	if r.managed != nil {
		if err := r.managed.RecoverManagedRootColdStarts(ctx); err != nil {
			return store.SweepResult{}, err
		}
		r.managed.TimeoutExpiredSubagents(1 * time.Hour)
		_ = r.managed.ResumeIdleEmbeddedGoals(ctx)
	}
	_, _ = r.store.TimeoutRunningSubagents(ctx, 1*time.Hour)
	_ = r.store.PruneExpiredSubagents(ctx, 1*time.Hour)
	_, _ = r.store.TimeoutStuckLaunches(ctx, 2*time.Minute)
	// Repair active projections left by older router versions before publishing
	// the sweep snapshot. The store keeps this bounded and fence-scoped.
	if _, err := r.store.ReconcileManagedParentActivity(ctx); err != nil {
		return store.SweepResult{}, err
	}
	// Recovery owns these deletions and transitions so the router can publish
	// the same records as Node. In particular, do not let store retention remove
	// an expired queued message before message_expired is emitted.
	expired, err := r.store.RecoverExpiredUnclaimedMessages(ctx)
	if err != nil {
		return store.SweepResult{}, err
	}
	for _, messageID := range expired {
		r.emitEvent(MessageExpiredEvent(messageID))
	}
	timedOut, err := r.store.RecoverTimedOutDeliveries(ctx, r.maxAttempts)
	if err != nil {
		return store.SweepResult{}, err
	}
	for _, outcome := range timedOut {
		r.emitEvent(DeliveryTimedOutEvent(outcome.MessageID, outcome.AgentID, outcome.Attempt, outcome.State, "delivery_timeout"))
		r.recordCompleteHistoryWithPresentation(ctx, outcome.AgentID, "delivery", "error", "Message delivery failed", "delivery-timeout:"+outcome.MessageID+":"+strconv.Itoa(outcome.Attempt), &store.ManagedHistoryPresentation{Kind: "error", Summary: "The message could not be delivered.", Error: &store.ManagedHistoryError{Code: "DELIVERY_TIMEOUT", Retryable: false, DetailAvailable: false}})
		if outcome.State == "dead_letter" {
			r.emitEvent(MessageDeadLetteredEvent(outcome.MessageID, outcome.AgentID, outcome.Attempt, "delivery_timeout"))
			for _, reminder := range outcome.RecurringReminderRearmed {
				r.emitEvent(RecurringReminderRearmedEvent(reminder.ReminderID, outcome.MessageID, reminder.NextDueAt, "delivery_timeout"))
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
	if r.managed != nil {
		r.managed.SweepAgentMonitors(ctx)
	}
	r.sweepWorkflowSchedules(ctx)
	r.sweepAttachmentsBestEffort()
	// Node publishes activity expiry before its heartbeat/sweep record.
	r.sweepLiveState()
	// Managed startup, recovery, and P1 child snapshots commit outside an HTTP
	// response. The bounded sweep observes those durable changes and publishes
	// the authoritative projection without making manager code own UI sockets.
	if r.uiLive != nil && r.managed != nil {
		r.uiLive.publish(ctx)
	}
	r.emitSweepRecord(result, len(expired), len(timedOut), reminderSuccess, reminderFailure)
	return result, nil
}

func (r *Router) emitSweepRecord(result store.SweepResult, expiredMessages, timedOutDeliveries, reminderSuccess, reminderFailure int) {
	counts := SweepCounts{StaleAgents: result.StaleAgents, ExpiredMessages: expiredMessages, TimedOutDeliveries: timedOutDeliveries, ReminderSuccess: reminderSuccess, ReminderFailure: reminderFailure, ConversationMessages: result.ConversationMessages, LifecycleExpired: result.LifecycleExpired, LifecycleStates: result.LifecycleStates, LifecycleEvents: result.LifecycleEvents, LifecycleProducers: result.LifecycleProducers, GoalControlStates: result.GoalControlStates}
	work := counts.StaleAgents > 0 || counts.ExpiredMessages > 0 || counts.TimedOutDeliveries > 0 || counts.ReminderSuccess > 0 || counts.ReminderFailure > 0 || counts.ConversationMessages > 0 || counts.LifecycleExpired > 0 || counts.LifecycleStates > 0 || counts.LifecycleEvents > 0 || counts.LifecycleProducers > 0 || counts.GoalControlStates > 0
	now := r.now()
	if r.lastSweepRecord.IsZero() {
		r.lastSweepRecord = now
	}
	if work || now.Sub(r.lastSweepRecord) >= r.sweepHeartbeat {
		r.emitEvent(LifecycleSweepCompletedEvent(counts))
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
		r.emitEvent(RouterErrorEvent("attachment_sweep_failed", ""))
	}
}

func (r *Router) publishLifecycleUpdates(ctx context.Context, updates []store.LifecycleUpdate) {
	for _, update := range updates {
		snapshot, err := r.store.SubagentLifecycleSnapshot(ctx, update.ParentAgentID)
		if err != nil {
			r.emitEvent(RouterErrorEvent("lifecycle_projection_failed", update.ParentAgentID))
			continue
		}
		r.mu.Lock()
		current, ok := r.lifecycleState[update.ParentAgentID]
		if ok && current.version > update.RouterVersion {
			r.mu.Unlock()
			continue
		}
		r.lifecycleState[update.ParentAgentID] = lifecycleAgent{version: update.RouterVersion, subagents: snapshot.Subagents}
		if update.RouterVersion > r.nextVersion {
			r.nextVersion = update.RouterVersion
		}
		r.mu.Unlock()
		_ = r.broadcastLifecycle(update.ParentAgentID, update.RouterVersion)
	}
}

func (r *Router) sweepWorkflowSchedules(ctx context.Context) {
	if r.store == nil {
		return
	}
	now := r.now()
	due, err := r.store.GetDueWorkflowSchedules(ctx, now)
	if err != nil {
		r.emitEvent(RouterErrorEvent("workflow_schedule_sweep_failed", ""))
		return
	}
	for _, schedule := range due {
		var lastStatus string
		var lastErr string

		if r.managed == nil {
			lastStatus = "failed"
			lastErr = "managed runtime is unavailable"
		} else {
			var modelOverride *managed.Model
			if strings.TrimSpace(schedule.ModelProvider) != "" && strings.TrimSpace(schedule.ModelID) != "" {
				modelOverride = &managed.Model{
					Provider: strings.TrimSpace(schedule.ModelProvider),
					ID:       strings.TrimSpace(schedule.ModelID),
				}
			}
			_, launchErr := r.managed.LaunchBackgroundWorkflow(ctx, schedule.WorkflowID, schedule.Variables, schedule.TargetCwd, modelOverride)
			if launchErr != nil {
				lastStatus = "failed"
				lastErr = launchErr.Error()
			} else {
				lastStatus = "success"
				lastErr = ""
			}
		}

		nextRun, calcErr := store.NextRunAt(schedule.Schedule, now)
		var nextRunAt string
		if calcErr != nil {
			if lastErr == "" {
				lastErr = calcErr.Error()
			}
			nextRunAt = timestamp(now.Add(time.Hour))
		} else {
			nextRunAt = timestamp(nextRun)
		}

		_ = r.store.AdvanceWorkflowSchedule(ctx, schedule.ID, nextRunAt, lastStatus, lastErr)
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

// handleTmux owns the router's worker-window contract. The process adapter is
// deliberately separate, and concurrent provision requests share one tmux
// operation before the router retains the Pi session ID.
func (r *Router) handleTmux(w http.ResponseWriter, req *http.Request) bool {
	if r.tmux == nil {
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "tmux_unavailable"})
		return true
	}
	name, action, ok := tmuxRoute(req.URL.EscapedPath())
	if !ok {
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
		return true
	}
	if name != "" && !validTmuxName(name) {
		r.writeError(w, &httpError{status: http.StatusBadRequest, code: "tmux_invalid_name"})
		return true
	}
	session, err := r.tmuxSession(req.Context())
	if err != nil {
		r.writeTmuxError(w, err)
		return true
	}

	switch {
	case req.Method == http.MethodGet && name == "" && action == "":
		windows, err := r.tmux.Windows(req.Context(), session)
		if err != nil {
			r.writeTmuxError(w, err)
			return true
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"session": session, "windows": windows})
	case req.Method == http.MethodPost && name == "" && action == "":
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return true
		}
		name, err := rawString(payload, "name", true)
		if err != nil || !validTmuxName(name) {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "tmux_invalid_name"})
			return true
		}
		sessionID, err := rawString(payload, "session_id", false)
		if err != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return true
		}
		sessionID, err = r.provisionTmux(req.Context(), session, name, sessionID)
		if err != nil {
			r.writeTmuxError(w, err)
			return true
		}
		response := map[string]any{"session": map[string]any{"name": name, "selected": true}, "selected_session": session}
		if sessionID != "" {
			response["session_id"] = sessionID
		}
		r.writeJSON(w, http.StatusCreated, response)
	case req.Method == http.MethodDelete && name != "" && action == "":
		if err := r.tmux.Remove(req.Context(), session, name); err != nil {
			r.writeTmuxError(w, err)
			return true
		}
		r.mu.Lock()
		delete(r.windowIDs, tmuxWindowKey(session, name))
		r.mu.Unlock()
		r.writeNoContent(w)
	case (req.Method == http.MethodPatch || req.Method == http.MethodPost && action == "rename") && name != "":
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return true
		}
		nextName, err := rawString(payload, "name", true)
		if err != nil || !validTmuxName(nextName) {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "tmux_invalid_name"})
			return true
		}
		if err := r.tmux.Rename(req.Context(), session, name, nextName); err != nil {
			r.writeTmuxError(w, err)
			return true
		}
		r.mu.Lock()
		if sessionID := r.windowIDs[tmuxWindowKey(session, name)]; sessionID != "" {
			r.windowIDs[tmuxWindowKey(session, nextName)] = sessionID
			delete(r.windowIDs, tmuxWindowKey(session, name))
		}
		r.mu.Unlock()
		directive, _ := json.Marshal(struct {
			Type    string `json:"type"`
			OldName string `json:"old_name"`
			NewName string `json:"new_name"`
		}{Type: "tmux_window_renamed", OldName: name, NewName: nextName})
		if err := r.tmux.SendKeys(req.Context(), session, nextName, string(directive), "router.tmux.window-renamed.v1"); err != nil {
			r.writeTmuxError(w, err)
			return true
		}
		r.writeJSON(w, http.StatusOK, map[string]any{"session": map[string]any{"name": nextName}, "selected_session": session})
	case req.Method == http.MethodPost && name != "" && action != "":
		r.handleTmuxAction(w, req, session, name, action)
	default:
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
	}
	return true
}

func (r *Router) handleTmuxAction(w http.ResponseWriter, req *http.Request, session, name, action string) {
	payload, err := r.readJSON(req)
	if err != nil {
		r.writeError(w, err)
		return
	}
	var state string
	sessionID := r.tmuxWindowID(session, name)
	switch action {
	case "keys":
		keys, keyErr := rawString(payload, "keys", true)
		if keyErr != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		windowID, idErr := rawString(payload, "window_id", false)
		if idErr != nil {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		serverID, serverErr := rawString(payload, "server_id", false)
		if serverErr != nil || (windowID == "") != (serverID == "") {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		if windowID != "" {
			// Match the named window first; the final send uses the immutable ID, so
			// replacement between the check and send cannot redirect the keystrokes.
			windows, listErr := r.tmux.Windows(req.Context(), session)
			if listErr != nil {
				r.writeTmuxError(w, listErr)
				return
			}
			found := false
			for _, window := range windows {
				if window.Name == name && window.WindowID == windowID && window.ServerID == serverID {
					found = true
					break
				}
			}
			if !found {
				r.writeError(w, &httpError{status: http.StatusConflict, code: "tmux_window_replaced"})
				return
			}
			target, ok := r.tmux.(interface {
				SendKeysToWindowID(context.Context, string, string, string) error
			})
			if !ok {
				r.writeError(w, &httpError{status: http.StatusServiceUnavailable, code: "tmux_unavailable"})
				return
			}
			err = target.SendKeysToWindowID(req.Context(), windowID, serverID, keys)
		} else {
			err = r.tmux.SendKeys(req.Context(), session, name, keys, "")
		}
	case "start":
		if sessionID == "" {
			sessionID, err = rawString(payload, "session_id", false)
			if err != nil {
				r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
				return
			}
		}
		_, state, err = r.tmux.Start(req.Context(), session, name, sessionID)
	case "stop":
		_, state, err = r.tmux.Stop(req.Context(), session, name, sessionID)
	case "restart":
		_, state, err = r.tmux.Restart(req.Context(), session, name, sessionID)
	case "resume":
		if sessionID == "" {
			r.writeError(w, &httpError{status: http.StatusBadRequest, code: "invalid_request"})
			return
		}
		_, state, err = r.tmux.Resume(req.Context(), session, name, sessionID)
	default:
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
		return
	}
	if err != nil {
		r.writeTmuxError(w, err)
		return
	}
	if sessionID != "" {
		r.mu.Lock()
		r.windowIDs[tmuxWindowKey(session, name)] = sessionID
		r.mu.Unlock()
	}
	response := map[string]any{"session": map[string]any{"name": name}, "action": action, "selected_session": session}
	if state != "" {
		response["state"] = state
	}
	if sessionID != "" {
		response["session_id"] = sessionID
	}
	r.writeJSON(w, http.StatusOK, response)
}

func (r *Router) tmuxSession(ctx context.Context) (string, error) {
	r.mu.RLock()
	selected := r.selectedTmux
	r.mu.RUnlock()
	if selected != "" {
		return selected, nil
	}
	sessions, err := r.tmux.Sessions(ctx)
	if err != nil {
		return "", err
	}
	for _, candidate := range sessions {
		if strings.Contains(candidate, "mecha") {
			r.mu.Lock()
			if r.selectedTmux == "" {
				r.selectedTmux = candidate
			}
			selected = r.selectedTmux
			r.mu.Unlock()
			return selected, nil
		}
	}

	// The launcher checks the inventory before it asks to create a worker. When
	// tmux has no sessions, that check must establish the managed session first
	// or every fresh-machine launch fails before it reaches provision.
	r.mu.Lock()
	pending := r.tmuxSessionProvisioning
	if pending == nil {
		pending = &tmuxProvisioning{done: make(chan struct{})}
		r.tmuxSessionProvisioning = pending
		r.mu.Unlock()
		err := r.tmux.ProvisionSession(ctx, "mecha", "supervisor")
		r.mu.Lock()
		pending.err = err
		if err == nil {
			r.selectedTmux = "mecha"
		}
		r.tmuxSessionProvisioning = nil
		close(pending.done)
		selected = r.selectedTmux
		r.mu.Unlock()
		return selected, err
	}
	r.mu.Unlock()
	<-pending.done
	if pending.err != nil {
		return "", pending.err
	}
	r.mu.RLock()
	selected = r.selectedTmux
	r.mu.RUnlock()
	if selected == "" {
		return "", ErrTmuxUnavailable
	}
	return selected, nil
}

func (r *Router) provisionTmux(ctx context.Context, session, name, sessionID string) (string, error) {
	key := tmuxWindowKey(session, name)
	r.mu.Lock()
	pending := r.tmuxProvisionals[key]
	if pending == nil {
		pending = &tmuxProvisioning{done: make(chan struct{}), sessionID: sessionID}
		r.tmuxProvisionals[key] = pending
		r.mu.Unlock()
		err := r.tmux.Provision(ctx, session, name)
		r.mu.Lock()
		pending.err = err
		if err == nil && pending.sessionID != "" {
			r.windowIDs[key] = pending.sessionID
		}
		delete(r.tmuxProvisionals, key)
		close(pending.done)
		r.mu.Unlock()
		return pending.sessionID, err
	}
	r.mu.Unlock()
	<-pending.done
	return pending.sessionID, pending.err
}

func (r *Router) tmuxWindowID(session, name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.windowIDs[tmuxWindowKey(session, name)]
}

func (r *Router) writeTmuxError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrTmuxUnavailable) {
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "tmux_unavailable"})
		return
	}
	r.writeError(w, &httpError{status: http.StatusInternalServerError, code: "tmux_operation_failed"})
}

func tmuxRoute(path string) (string, string, bool) {
	const prefix = "/v1/tmux/windows"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", "", true
	}
	if !strings.HasPrefix(rest, "/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if len(parts) == 0 || len(parts) > 2 || parts[0] == "" {
		return "", "", false
	}
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	if len(parts) == 1 {
		return name, "", true
	}
	if parts[1] == "" {
		return "", "", false
	}
	return name, parts[1], true
}

func validTmuxName(value string) bool {
	return len(value) <= 64 && agentIDRE.MatchString(value)
}

func tmuxWindowKey(session, name string) string { return session + "\x00" + name }

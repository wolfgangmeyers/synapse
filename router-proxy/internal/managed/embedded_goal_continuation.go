package managed

import (
	"context"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type embeddedGoalTimer struct{ cancel func() }

// Recovery only restores timers for durable, unstarted snoozed reservations.
// The ordinary start path still fences ownership, policy, due, and final turn.
func (m *Manager) recoverReservedEmbeddedGoalSnoozes(binding store.EmbeddedBinding) {
	reservations, err := m.store.ReservedEmbeddedGoalSnoozes(m.runCtx, binding)
	if err != nil {
		return
	}
	for _, reservation := range reservations {
		due, err := time.Parse("2006-01-02T15:04:05.000Z", reservation.DueAt.String)
		if err != nil {
			continue
		}
		m.scheduleEmbeddedGoalContinuation(binding, reservation.EmbeddedGoalEvaluationKey, due)
	}
}

// dispatchEmbeddedGoalContinuation schedules only the saved key after its
// durable verdict. An unsnoozed reservation keeps the immediate G4 path.
func (m *Manager) dispatchEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey) {
	reservation, err := m.store.EmbeddedGoalContinuationFor(m.runCtx, binding, key)
	if err != nil || reservation.State != "reserved" {
		return
	}
	if reservation.DueAt.Valid {
		due, err := time.Parse("2006-01-02T15:04:05.000Z", reservation.DueAt.String)
		if err != nil {
			return
		}
		m.scheduleEmbeddedGoalContinuation(binding, key, due)
		return
	}
	m.startEmbeddedGoalContinuation(binding, key)
}

func (m *Manager) scheduleEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.embeddedGoalTimers == nil {
		m.embeddedGoalTimers = make(map[string]*embeddedGoalTimer)
	}
	if previous := m.embeddedGoalTimers[binding.AgentID]; previous != nil {
		previous.cancel()
	}
	timer := &embeddedGoalTimer{}
	m.embeddedGoalTimers[binding.AgentID] = timer
	fire := func() {
		m.mu.Lock()
		if m.closed || m.embeddedGoalTimers[binding.AgentID] != timer {
			m.mu.Unlock()
			return
		}
		delete(m.embeddedGoalTimers, binding.AgentID)
		m.mu.Unlock()
		m.startEmbeddedGoalContinuation(binding, key)
	}
	delay := due.Sub(m.clock.Now())
	if delay < 0 {
		delay = 0
	}
	if clock, ok := m.clock.(interface {
		AfterFunc(time.Duration, func()) func()
	}); ok {
		timer.cancel = clock.AfterFunc(delay, fire)
	} else {
		wall := time.AfterFunc(delay, fire)
		timer.cancel = func() { wall.Stop() }
	}
}

func (m *Manager) startEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey) {
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if m.runCtx.Err() != nil {
		return
	}
	lane.selection.Lock()
	saved, err := m.store.ManagedAgent(m.runCtx, binding.AgentID)
	m.mu.Lock()
	owner, ok := m.embeddedOwners[binding.AgentID]
	closed := m.closed
	m.mu.Unlock()
	if err != nil || !ok || closed || saved.Runtime != "embedded" || saved.DesiredState != "running" || saved.LifecycleState != "running" ||
		owner.binding != binding || saved.PiSessionID != binding.SessionID || saved.SessionEpoch != binding.Epoch || saved.RuntimeGeneration != binding.Generation {
		lane.selection.Unlock()
		return
	}
	if err := m.store.RenewEmbeddedOwner(m.runCtx, binding, owner.identity, embeddedOwnerTTL); err != nil {
		lane.selection.Unlock()
		return
	}
	// Store validates the original verdict, chain, and exact retained neutral
	// exception atomically with start. A Manager physical-head query is neither
	// semantic authority nor an admission fence against other Store callers.
	_, started, err := m.store.StartEmbeddedGoalContinuation(m.runCtx, binding, key)
	lane.selection.Unlock()
	if err != nil || !started {
		return
	}
	ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	// Start committed UNKNOWN before any SDK provider request. An error or
	// process loss cannot authorize replay; the saved transcript is evidence.
	_ = m.runEmbeddedSessionTurnWithContinuation(ctx, *saved, "", nil, &key)
}

func (m *Manager) hasActiveSubagents(agentID string) bool {
	m.subagentsMu.Lock()
	for _, task := range m.subagentsActive {
		if task.parent.AgentID == agentID {
			task.mu.Lock()
			st := task.status
			task.mu.Unlock()
			if st == "queued" || st == "running" {
				m.subagentsMu.Unlock()
				return true
			}
		}
	}
	m.subagentsMu.Unlock()

	ctx := m.runCtx
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	curAgent, err := m.store.ManagedAgent(ctx, agentID)
	if err != nil || curAgent == nil {
		return false
	}
	binding := store.EmbeddedBinding{
		AgentID:    curAgent.AgentID,
		SessionID:  curAgent.PiSessionID,
		Epoch:      curAgent.SessionEpoch,
		Generation: curAgent.RuntimeGeneration,
	}
	subagents, err := m.store.ManagedSubagents(ctx, binding)
	if err == nil {
		for _, s := range subagents {
			if s.Status == "queued" || s.Status == "running" {
				return true
			}
		}
	}
	return false
}

// kickoffIdleGoalContinuation checks if an agent is currently running and idle
// with an active goal and no active subagents, and triggers a continuation turn.
func (m *Manager) kickoffIdleGoalContinuation(binding store.EmbeddedBinding) {
	if m.isClosed() {
		return
	}
	agentID := binding.AgentID
	var owner embeddedOwner
	var ok bool
	for i := 0; i < 50; i++ {
		m.mu.Lock()
		owner, ok = m.embeddedOwners[agentID]
		m.mu.Unlock()
		if ok {
			break
		}
		select {
		case <-m.runCtx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !ok {
		return
	}
	if binding.SessionID != "" && owner.binding.SessionID != binding.SessionID {
		return
	}
	if binding.Epoch != 0 && owner.binding.Epoch != binding.Epoch {
		return
	}

	lane := m.laneFor(agentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()

	if m.runCtx.Err() != nil || m.isClosed() {
		return
	}

	curAgent, err := m.store.ManagedAgent(m.runCtx, agentID)
	if err != nil || curAgent == nil || curAgent.ActivityState != "idle" || curAgent.LifecycleState != "running" || curAgent.DesiredState != "running" {
		return
	}
	if curAgent.PiSessionID != owner.binding.SessionID || curAgent.SessionEpoch != owner.binding.Epoch || curAgent.RuntimeGeneration != owner.binding.Generation {
		return
	}

	if m.hasActiveSubagents(agentID) {
		return
	}

	policy, err := m.store.ManagedGoalPolicyFor(m.runCtx, agentID)
	if err != nil || policy == nil || !policy.Config.Enabled {
		return
	}
	if policy.EvaluatorState == "done" || policy.LastOutcome == store.EmbeddedGoalEvaluationDone {
		return
	}
	if policy.EvaluatorState == "evaluating" {
		return
	}
	now := formatManagedTime(m.clock.Now())
	if policy.SnoozedUntil != "" && policy.SnoozedUntil > now {
		return
	}

	if key, found, err := m.store.LatestReservedEmbeddedGoalContinuation(m.runCtx, owner.binding); err == nil && found {
		m.startManagedTask(func() {
			m.startEmbeddedGoalContinuation(owner.binding, key)
		})
		return
	}

	runCtx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	_ = m.runEmbeddedSessionTurn(runCtx, *curAgent, "Continue working on the current goal.", nil)
}


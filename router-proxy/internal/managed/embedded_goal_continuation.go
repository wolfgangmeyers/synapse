package managed

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
)

type embeddedGoalTimer struct{ cancel func() }

const defaultGoalContinuationDelay = 10 * time.Second
const defaultMaxConsecutiveGoalContinuations = 10

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
// durable verdict. It enforces the consecutive continuation cap and minimum delay pacing.
func (m *Manager) dispatchEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey) {
	reservation, err := m.store.EmbeddedGoalContinuationFor(m.runCtx, binding, key)
	if err != nil || reservation.State != "reserved" {
		return
	}
	lane := m.laneFor(binding.AgentID)
	maxContinuations := m.maxConsecutiveGoalContinuations
	if maxContinuations <= 0 {
		maxContinuations = defaultMaxConsecutiveGoalContinuations
	}
	if lane.consecutiveContinuations >= maxContinuations {
		log.Printf("WARNING: agent %s reached consecutive continuation cap (%d); pausing continuation until user prompt", binding.AgentID, lane.consecutiveContinuations)
		return
	}
	if reservation.DueAt.Valid {
		due, err := time.Parse("2006-01-02T15:04:05.000Z", reservation.DueAt.String)
		if err == nil {
			m.scheduleEmbeddedGoalContinuation(binding, key, due)
			return
		}
	}
	if m.goalContinuationDelay > 0 {
		due := m.clock.Now().Add(m.goalContinuationDelay)
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
	maxContinuations := m.maxConsecutiveGoalContinuations
	if maxContinuations <= 0 {
		maxContinuations = defaultMaxConsecutiveGoalContinuations
	}
	if lane.consecutiveContinuations >= maxContinuations {
		log.Printf("WARNING: agent %s reached consecutive continuation cap (%d); pausing continuation until user prompt", binding.AgentID, lane.consecutiveContinuations)
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
	lane.consecutiveContinuations++
	ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	prompt := m.formatGoalContinuationPrompt(ctx, owner.binding, &key)
	// Start committed UNKNOWN before any SDK provider request. An error or
	// process loss cannot authorize replay; the saved transcript is evidence.
	_ = m.runEmbeddedSessionTurnWithContinuation(ctx, *saved, prompt, nil, &key)
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
		log.Printf("kickoffIdleGoalContinuation: no owner for %s after waiting", agentID)
		return
	}
	if binding.SessionID != "" && owner.binding.SessionID != binding.SessionID {
		log.Printf("kickoffIdleGoalContinuation: session mismatch for %s", agentID)
		return
	}
	if binding.Epoch != 0 && owner.binding.Epoch != binding.Epoch {
		log.Printf("kickoffIdleGoalContinuation: epoch mismatch for %s", agentID)
		return
	}

	lane := m.laneFor(agentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()

	if m.runCtx.Err() != nil || m.isClosed() {
		return
	}

	maxContinuations := m.maxConsecutiveGoalContinuations
	if maxContinuations <= 0 {
		maxContinuations = defaultMaxConsecutiveGoalContinuations
	}
	if lane.consecutiveContinuations >= maxContinuations {
		log.Printf("WARNING: agent %s reached consecutive continuation cap (%d); pausing continuation until user prompt", agentID, lane.consecutiveContinuations)
		return
	}

	curAgent, err := m.store.ManagedAgent(m.runCtx, agentID)
	if err != nil || curAgent == nil || curAgent.ActivityState != "idle" || curAgent.LifecycleState != "running" || curAgent.DesiredState != "running" {
		log.Printf("kickoffIdleGoalContinuation: agent %s not idle/running: err=%v", agentID, err)
		return
	}
	if curAgent.PiSessionID != owner.binding.SessionID || curAgent.SessionEpoch != owner.binding.Epoch || curAgent.RuntimeGeneration != owner.binding.Generation {
		log.Printf("kickoffIdleGoalContinuation: generation mismatch for %s", agentID)
		return
	}

	if m.hasActiveSubagents(agentID) {
		log.Printf("kickoffIdleGoalContinuation: agent %s has active subagents", agentID)
		return
	}

	policy, err := m.store.ManagedGoalPolicyFor(m.runCtx, agentID)
	if err != nil || policy == nil || !policy.Config.Enabled {
		log.Printf("kickoffIdleGoalContinuation: agent %s policy disabled: %v", agentID, err)
		return
	}
	if policy.EvaluatorState == "done" || policy.LastOutcome == store.EmbeddedGoalEvaluationDone || policy.LastOutcome == store.EmbeddedGoalEvaluationCompleted {
		log.Printf("kickoffIdleGoalContinuation: agent %s goal done", agentID)
		return
	}
	if policy.EvaluatorState == "evaluating" {
		log.Printf("kickoffIdleGoalContinuation: agent %s goal evaluating", agentID)
		return
	}
	now := formatManagedTime(m.clock.Now())
	if policy.SnoozedUntil != "" && policy.SnoozedUntil > now {
		log.Printf("kickoffIdleGoalContinuation: agent %s goal snoozed until %s", agentID, policy.SnoozedUntil)
		return
	}

	if hasPending, err := m.store.HasPendingWakeOrStartedContinuation(m.runCtx, owner.binding); err == nil && hasPending {
		log.Printf("kickoffIdleGoalContinuation: agent %s has pending wake or started continuation", agentID)
		return
	}

	if key, found, err := m.store.LatestReservedEmbeddedGoalContinuation(m.runCtx, owner.binding); err == nil && found {
		m.startManagedTask(func() {
			m.startEmbeddedGoalContinuation(owner.binding, key)
		})
		return
	}

	runCtx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	prompt := m.formatGoalContinuationPrompt(runCtx, owner.binding, nil)
	lane.consecutiveContinuations++
	log.Printf("kickoffIdleGoalContinuation: starting turn for %s with prompt: %s", agentID, prompt)
	if err := m.runEmbeddedSessionTurn(runCtx, *curAgent, prompt, nil); err != nil {
		log.Printf("kickoffIdleGoalContinuation: runEmbeddedSessionTurn error for %s: %v", agentID, err)
	}
}

const goalContinuationDefaultPrompt = "Continue working on the current goal."
const goalContinuationNoSubagentsNoticePrompt = "Continue working on the current goal. Notice: There are NO active subagents running. To launch a subagent, you must invoke the subagent tool directly via the API. Otherwise, execute tools (bash, read, edit) directly to make progress on the goal."

func (m *Manager) formatGoalContinuationPrompt(ctx context.Context, binding store.EmbeddedBinding, key *store.EmbeddedGoalEvaluationKey) string {
	goal := ""
	if m.store != nil {
		if policy, err := m.store.ManagedGoalPolicyFor(ctx, binding.AgentID); err == nil && policy != nil && policy.Config.Goal != "" {
			goal = policy.Config.Goal
		}
	}

	rationale := ""
	if key != nil && m.store != nil {
		if row, err := m.store.EmbeddedEvaluationRow(ctx, binding, *key); err == nil && row.Reason != "" {
			rationale = row.Reason
		}
	}
	if rationale == "" {
		rationale = "Take the next actionable step to make progress on the goal."
	}

	var basePrompt string
	if goal != "" {
		basePrompt = fmt.Sprintf("NOT_DONE: Continue %s by completing its next actionable step now.\nVERDICT: NOT_DONE\nREASON: %s", goal, rationale)
	} else {
		basePrompt = goalContinuationDefaultPrompt
	}

	if m.store == nil {
		return basePrompt
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var snapshot store.EmbeddedSnapshot
	var err error
	if binding.OwnerToken > 0 {
		snapshot, err = m.store.ReopenEmbedded(ctx, binding)
	}
	if binding.OwnerToken <= 0 || err != nil {
		snapshot, err = m.store.ReopenLatestAgentEmbedded(ctx, binding.AgentID)
	}
	if err != nil || len(snapshot.Entries) == 0 {
		return basePrompt
	}

	var lastAssistant *store.EmbeddedEntry
	for i := len(snapshot.Entries) - 1; i >= 0; i-- {
		if snapshot.Entries[i].Kind == "assistant" {
			lastAssistant = &snapshot.Entries[i]
			break
		}
	}
	if lastAssistant == nil {
		return basePrompt
	}

	bodyLower := strings.ToLower(string(lastAssistant.Body))
	claimedSubagent := strings.Contains(bodyLower, "subagent") ||
		strings.Contains(bodyLower, "awaiting completion") ||
		strings.Contains(bodyLower, "executing")

	if !claimedSubagent {
		return basePrompt
	}

	hasActiveSubagents := m.hasActiveSubagents(binding.AgentID)
	if hasActiveSubagents {
		return basePrompt
	}

	hasToolCalls := false
	if msg, err := ai.UnmarshalMessage(lastAssistant.Body); err == nil {
		var content ai.ContentList
		switch asst := msg.(type) {
		case ai.AssistantMessage:
			content = asst.Content
		case *ai.AssistantMessage:
			if asst != nil {
				content = asst.Content
			}
		}
		for _, part := range content {
			if _, ok := part.(ai.ToolCall); ok {
				hasToolCalls = true
				break
			}
			if _, ok := part.(*ai.ToolCall); ok {
				hasToolCalls = true
				break
			}
		}
	}
	if !hasToolCalls && bytes.Contains(lastAssistant.Body, []byte(`"toolCall"`)) {
		hasToolCalls = true
	}

	if !hasToolCalls || strings.Contains(bodyLower, "subagent") || strings.Contains(bodyLower, "awaiting completion") {
		if goal == "" {
			return goalContinuationNoSubagentsNoticePrompt
		}
		return basePrompt + " Notice: There are NO active subagents running. To launch a subagent, you must invoke the subagent tool directly via the API. Otherwise, execute tools (bash, read, edit) directly to make progress on the goal."
	}

	return basePrompt
}


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

type embeddedGoalTimer struct {
	cancel  func()
	binding store.EmbeddedBinding
	key     store.EmbeddedGoalEvaluationKey
}
type embeddedAutomaticGoalKey struct{}

// validatePublishedGoalOwner never acquires ownership after a failed turn. A
// context reset may have legitimately published a newer owner in the same lane.
func (m *Manager) validatePublishedGoalOwner(ctx context.Context, origin embeddedOwner) error {
	m.mu.Lock()
	selected, ok := m.embeddedOwners[origin.binding.AgentID]
	m.mu.Unlock()
	if !ok || (selected != origin && (selected.binding.AgentID != origin.binding.AgentID ||
		selected.binding.Generation != origin.binding.Generation || selected.binding.Epoch <= origin.binding.Epoch)) {
		return fmt.Errorf("no valid published goal owner for %s", origin.binding.AgentID)
	}
	return m.store.RenewEmbeddedOwner(ctx, selected.binding, selected.identity, embeddedOwnerTTL)
}

// All automatic paths share lane-serialized attempt accounting. Checks that
// merely suppress work never consume an attempt.
// Empty-turn circuit-breaker re-nudges (strikes 1 and 2) are exempted from
// the consecutive continuation cap so the agent can either recover or reach
// strike 3 for an intentional circuit-breaker pause.
func (m *Manager) goalAttemptAllowed(lane *managedAgentLane) bool {
	if lane.goalOwnerInvalid || m.clock.Now().Before(lane.goalNextAttempt) {
		return false
	}
	emptyTurns := lane.consecutiveEmptyTurns.Load()
	if emptyTurns > 0 && emptyTurns < 3 {
		return true
	}
	if emptyTurns >= 3 {
		return false
	}
	return lane.consecutiveContinuations.Load() < m.goalContinuationLimit()
}

func (m *Manager) settleGoalAttempt(lane *managedAgentLane) {
	delay := m.goalContinuationDelay
	if delay <= 0 {
		delay = defaultGoalContinuationDelay
	}
	lane.goalNextAttempt = m.clock.Now().Add(delay)
}

const defaultGoalContinuationDelay = 10 * time.Second
const defaultMaxConsecutiveGoalContinuations = 20

var notDoneWakeups = []string{
	"NOT_DONE: Review %s and take the next concrete action now.",
	"NOT_DONE: Continue %s by completing its next actionable step now.",
	"NOT_DONE: Re-engage with %s and deliver the next concrete result now.",
	"NOT_DONE: Identify what remains for %s and act on it now.",
	"NOT_DONE: Advance %s with a specific, verifiable next action now.",
	"NOT_DONE: %s still needs action—COMPLETE the next step now!",
	"NOT_DONE: Return to %s, address the current gap, and move it forward NOW.",
	"NOT_DONE: Take ownership of %s and complete its next concrete action now!",
	"NOT_DONE: Do not leave %s idle—resolve the next blocker and proceed NOW.",
	"NOT_DONE: %s requires visible progress: take the next actionable step now!",
	"NOT_DONE: %s is NOT_DONE. Resolve the highest-priority remaining item and act now.",
	"NOT_DONE: Move %s forward immediately by completing the next required action.",
	"NOT_DONE: %s remains incomplete—address the outstanding work and produce a result NOW.",
	"NOT_DONE: Take direct action on %s now; do not stop at planning or inspection.",
	"NOT_DONE: %s needs urgent progress. Complete the next concrete deliverable NOW!",
	"NOT_DONE: %s IS STILL INCOMPLETE. Take the next required action immediately!",
	"NOT_DONE: ACT NOW on %s: resolve the top remaining issue and report the concrete result.",
	"NOT_DONE: %s cannot remain stalled. COMPLETE THE NEXT ACTION NOW and make progress visible!",
	"NOT_DONE: TAKE OWNERSHIP of %s immediately—finish the highest-priority remaining step and verify it NOW!",
	"NOT_DONE: %s IS NOT COMPLETE. ACT NOW, DELIVER THE NEXT VERIFIED RESULT, AND ACCOUNT FOR THE REMAINING WORK!",
}

// embeddedAutomaticContinuationReason is the durable reason stored for every
// settled turn with an active goal. No model judges completion.
const embeddedAutomaticContinuationReason = "Goal active after the worker settled; continuing automatically."

// goalSettledHint is included in every automatic continuation. Completion and
// blocking are decided by the agent through its own tools.
const goalSettledHint = "If the goal is complete, turn it off with the goal tool (action \"off\"). " +
	"If you are blocked from achieving it and need external help, call mecha_blocked with the reason."

// goalContinuationPrompt is the escalating wake-up plus the done/blocked hint.
func goalContinuationPrompt(goal string, attempt int) string {
	return notDoneWakeup(goal, attempt) + "\n" + goalSettledHint
}

func notDoneWakeup(goal string, attempt int) string {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(notDoneWakeups) {
		attempt = len(notDoneWakeups)
	}
	return fmt.Sprintf(notDoneWakeups[attempt-1], goal)
}

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
	if lane.consecutiveContinuations.Load() >= int64(maxContinuations) {
		log.Printf("WARNING: agent %s reached consecutive continuation cap (%d); pausing continuation until user prompt", binding.AgentID, lane.consecutiveContinuations.Load())
		return
	}
	if reservation.DueAt.Valid {
		due, err := time.Parse("2006-01-02T15:04:05.000Z", reservation.DueAt.String)
		if err == nil {
			m.scheduleEmbeddedGoalContinuation(binding, key, due)
			return
		}
	}
	delay := m.goalContinuationDelay
	if delay <= 0 {
		delay = defaultGoalContinuationDelay
	}
	m.scheduleEmbeddedGoalContinuation(binding, key, m.clock.Now().Add(delay))
}

// scheduleEmbeddedGoalContinuation schedules an automatic goal continuation wake-up at the specified due time.
func (m *Manager) scheduleEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time) {
	m.scheduleEmbeddedGoalContinuationWithPrompt(binding, key, due, "")
}

// scheduleEmbeddedGoalContinuationWithPrompt schedules a goal continuation wake-up with an optional custom prompt override at the specified due time.
func (m *Manager) scheduleEmbeddedGoalContinuationWithPrompt(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time, prompt string) {
	m.scheduleEmbeddedGoalWake(binding, key, due, func() { m.startEmbeddedGoalContinuationWithPrompt(binding, key, prompt) })
}

func (m *Manager) scheduleEmbeddedGoalWake(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, due time.Time, wake func()) {
	agentID := binding.AgentID
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.embeddedGoalTimers == nil {
		m.embeddedGoalTimers = make(map[string]*embeddedGoalTimer)
	}
	if previous := m.embeddedGoalTimers[agentID]; previous != nil {
		if previous.binding == binding && previous.key == key {
			return
		}
		previous.cancel()
	}
	timer := &embeddedGoalTimer{binding: binding, key: key}
	m.embeddedGoalTimers[agentID] = timer
	fire := func() {
		m.mu.Lock()
		if m.closed || m.embeddedGoalTimers[agentID] != timer {
			m.mu.Unlock()
			return
		}
		delete(m.embeddedGoalTimers, agentID)
		m.mu.Unlock()
		wake()
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

// Retire runtime scheduling only after the store has durably invalidated old work.
func (m *Manager) retireEmbeddedGoalTimer(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if timer := m.embeddedGoalTimers[agentID]; timer != nil {
		timer.cancel()
		delete(m.embeddedGoalTimers, agentID)
	}
}

func (m *Manager) startEmbeddedGoalContinuation(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey) {
	m.startEmbeddedGoalContinuationWithPrompt(binding, key, "")
}

func (m *Manager) startEmbeddedGoalContinuationWithPrompt(binding store.EmbeddedBinding, key store.EmbeddedGoalEvaluationKey, overridePrompt string) {
	if m.isWorkerSelfTerminationPending(binding.AgentID) {
		log.Printf("[embedded:%s] Suppressing goal continuation because self-termination is pending", binding.AgentID)
		return
	}
	if m.hasActiveSubagents(binding.AgentID) {
		log.Printf("[embedded:%s] Suppressing goal continuation because subagents are active", binding.AgentID)
		return
	}
	if curAgent, err := m.store.ManagedAgent(m.runCtx, binding.AgentID); err == nil && curAgent != nil && curAgent.Blocked {
		log.Printf("[embedded:%s] Suppressing goal continuation because agent is blocked: %s", binding.AgentID, curAgent.BlockedReason)
		return
	}
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if !m.goalAttemptAllowed(lane) {
		if !lane.goalOwnerInvalid && m.clock.Now().Before(lane.goalNextAttempt) {
			m.scheduleEmbeddedGoalContinuationWithPrompt(binding, key, lane.goalNextAttempt, overridePrompt)
		}
		return
	}
	if m.runCtx.Err() != nil {
		return
	}
	if m.isWorkerSelfTerminationPending(binding.AgentID) {
		log.Printf("[embedded:%s] Suppressing goal continuation because self-termination is pending", binding.AgentID)
		return
	}
	if m.hasActiveSubagents(binding.AgentID) {
		log.Printf("[embedded:%s] Suppressing goal continuation because subagents are active", binding.AgentID)
		return
	}
	if curAgent, err := m.store.ManagedAgent(m.runCtx, binding.AgentID); err == nil && curAgent != nil && curAgent.Blocked {
		log.Printf("[embedded:%s] Suppressing goal continuation because agent is blocked: %s", binding.AgentID, curAgent.BlockedReason)
		return
	}
	lane.selection.Lock()
	saved, err := m.store.ManagedAgent(m.runCtx, binding.AgentID)
	if err != nil || saved.Runtime != "embedded" || saved.DesiredState != "running" || saved.LifecycleState != "running" {
		lane.selection.Unlock()
		return
	}
	owner, err := m.ensureEmbeddedOwner(m.runCtx, binding.AgentID, *saved)
	if err != nil {
		lane.consecutiveContinuations.Add(1)
		m.settleGoalAttempt(lane)
		lane.goalOwnerInvalid = true
		lane.selection.Unlock()
		return
	}
	binding = owner.binding
	// Store validates the original verdict, chain, and exact retained neutral
	// exception atomically with start. A Manager physical-head query is neither
	// semantic authority nor an admission fence against other Store callers.
	_, started, err := m.store.StartEmbeddedGoalContinuation(m.runCtx, binding, key)
	lane.selection.Unlock()
	if err == nil && !started {
		return
	}
	lane.consecutiveContinuations.Add(1)
	defer m.settleGoalAttempt(lane)
	if err != nil {
		if ownerErr := m.validatePublishedGoalOwner(m.runCtx, owner); ownerErr != nil {
			lane.goalOwnerInvalid = true
		}
		return
	}
	for {
		old := lane.goalAttempt.Load()
		next := lane.consecutiveContinuations.Load()
		if next > 20 {
			next = 20
		}
		if old >= next || lane.goalAttempt.CompareAndSwap(old, next) {
			break
		}
	}
	ctx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	ctx = context.WithValue(ctx, embeddedAutomaticGoalKey{}, true)
	// The session constructs the prompt after runtime admission using current authority.
	if err := m.runEmbeddedSessionTurnWithContinuation(ctx, *saved, overridePrompt, nil, &key); err != nil {
		if ownerErr := m.validatePublishedGoalOwner(m.runCtx, owner); ownerErr != nil {
			lane.goalOwnerInvalid = true
		}
	}
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
	if agentID == "" {
		return
	}

	lane := m.laneFor(agentID)
	lane.serial.Lock()
	defer lane.serial.Unlock()
	if !m.goalAttemptAllowed(lane) {
		return
	}

	if m.runCtx.Err() != nil || m.isClosed() {
		return
	}

	// Empty turns own their keyed re-nudges. A fresh idle prompt can overtake
	// the queued verdict and must not bypass the third-empty circuit breaker.
	if lane.consecutiveEmptyTurns.Load() > 0 {
		return
	}

	maxContinuations := m.maxConsecutiveGoalContinuations
	if maxContinuations <= 0 {
		maxContinuations = defaultMaxConsecutiveGoalContinuations
	}
	if lane.consecutiveContinuations.Load() >= int64(maxContinuations) {
		return
	}

	curAgent, err := m.store.ManagedAgent(m.runCtx, agentID)
	if err != nil || curAgent == nil || curAgent.ActivityState != "idle" || curAgent.LifecycleState != "running" || curAgent.DesiredState != "running" {
		return
	}
	if curAgent.Blocked {
		return
	}

	// If there is pending mail, let mail delivery handle waking up the agent first.
	nextMail, err := m.store.NextEmbeddedMail(m.runCtx, agentID)
	if err == nil && nextMail != "" {
		m.startManagedTask(func() { m.dispatchEmbeddedMail(agentID) })
		return
	}

	if m.hasActiveSubagents(agentID) {
		return
	}

	policy, err := m.store.ManagedGoalPolicyFor(m.runCtx, agentID)
	if err != nil {
		return
	}
	now := formatManagedTime(m.clock.Now())
	if !isEmbeddedGoalActive(policy, now) {
		return
	}

	owner, err := m.ensureEmbeddedOwner(m.runCtx, agentID, *curAgent)
	if err != nil {
		lane.consecutiveContinuations.Add(1)
		m.settleGoalAttempt(lane)
		lane.goalOwnerInvalid = true
		return
	}

	hasPending, err := m.store.HasPendingWakeOrStartedContinuation(m.runCtx, owner.binding)
	if err != nil {
		return
	}
	if hasPending {
		return
	}

	key, found, err := m.store.LatestReservedEmbeddedGoalContinuation(m.runCtx, owner.binding)
	if err != nil {
		return
	}
	if found {
		m.startManagedTask(func() {
			m.dispatchEmbeddedGoalContinuation(owner.binding, key)
		})
		return
	}

	// Claude applies a revision only with a durable checked-turn receipt.
	// After that, only a reserved evaluator continuation may wake it again:
	// unavailable/failed evaluation must not become an unkeyed NOT_DONE.
	// Pending initial/replacement revisions, mail and Pi retain their paths.
	// However, if the agent has an active workflow, do NOT suppress the
	// continuation wake. An active workflow requires continuous guidance and
	// recovery even when the applied revision equals the desired revision,
	// so the worker can recover after a crashed or failed turn.
	hasActiveWorkflow := curAgent.ActiveWorkflowID != "" || curAgent.ActiveWorkflowStateID != ""
	if !hasActiveWorkflow && curAgent.SelectedModel.Provider == claudeCodeProvider &&
		policy.DesiredRevision > 0 && policy.AppliedRevision == policy.DesiredRevision && policy.ProjectionState == "applied" {
		return
	}

	runCtx := context.WithValue(m.runCtx, embeddedOwnerContextKey{}, owner)
	runCtx = context.WithValue(runCtx, embeddedAutomaticGoalKey{}, true)
	prompt := m.formatGoalContinuationPrompt(runCtx, owner.binding, nil)
	if prompt == "" {
		return
	}
	defer func() {
		m.settleGoalAttempt(lane)
		if !lane.goalOwnerInvalid {
			m.scheduleEmbeddedGoalWake(owner.binding, store.EmbeddedGoalEvaluationKey{}, lane.goalNextAttempt, func() { m.kickoffIdleGoalContinuation(owner.binding) })
		}
	}()
	lane.consecutiveContinuations.Add(1)
	incrementGoalAttempt(&lane.goalAttempt)
	log.Printf("kickoffIdleGoalContinuation: starting turn for %s with prompt: %s", agentID, prompt)
	if err := m.runEmbeddedSessionTurn(runCtx, *curAgent, prompt, nil); err != nil {
		if ownerErr := m.validatePublishedGoalOwner(m.runCtx, owner); ownerErr != nil {
			lane.goalOwnerInvalid = true
		}
		log.Printf("kickoffIdleGoalContinuation: runEmbeddedSessionTurn error for %s: %v", agentID, err)
	}
}

const goalContinuationDefaultPrompt = "Continue working on the current goal."
const goalContinuationNoSubagentsNoticePrompt = "Continue working on the current goal. Notice: There are NO active subagents running. To launch a subagent, you must invoke the subagent tool directly via the API. Otherwise, execute tools (bash, read, edit) directly to make progress on the goal."

func (m *Manager) workflowContinuationGuidance(ctx context.Context, agent *store.ManagedAgentRecord) string {
	wfName := "Workflow"
	if agent.ActiveWorkflowID != "" {
		if wf, err := m.store.GetWorkflow(ctx, agent.ActiveWorkflowID); err == nil && wf != nil && wf.Name != "" {
			wfName = wf.Name
		}
	}
	stateName := "active"
	if agent.ActiveWorkflowStateID != "" {
		if st, err := m.store.GetWorkflowState(ctx, agent.ActiveWorkflowStateID); err == nil && st != nil && st.Name != "" {
			stateName = st.Name
		}
	}
	return fmt.Sprintf("The workflow %s is currently active in state %s. You must either make progress towards completing the workflow, call workflow_snooze, exit the workflow with mecha_workflow_transition (action exit) when an exit is allowed, or call mecha_blocked with the reason.", wfName, stateName)
}

// An empty prompt means the workflow authority could not be read. Callers must
// suppress dispatch, not substitute ordinary goal guidance or replay a start.
func (m *Manager) formatGoalContinuationPrompt(ctx context.Context, binding store.EmbeddedBinding, key *store.EmbeddedGoalEvaluationKey) string {
	if m.store != nil {
		curAgent, err := m.store.ManagedAgent(ctx, binding.AgentID)
		if err != nil {
			log.Printf("[embedded:%s] read managed agent for goal continuation prompt: %v; suppressing dispatch without replay", binding.AgentID, err)
			return ""
		}
		if curAgent != nil && (curAgent.ActiveWorkflowID != "" || curAgent.ActiveWorkflowStateID != "") {
			return m.workflowContinuationGuidance(ctx, curAgent)
		}
	}

	goal := ""
	if m.store != nil {
		if policy, err := m.store.ManagedGoalPolicyFor(ctx, binding.AgentID); err == nil && policy != nil && policy.Config.Goal != "" {
			goal = policy.Config.Goal
		}
	}

	lane := m.laneFor(binding.AgentID)
	attempt := lane.goalAttempt.Load()
	if attempt <= 0 {
		attempt = lane.consecutiveContinuations.Load()
	}
	if attempt <= 0 {
		attempt = 1
	}

	var basePrompt string
	if goal != "" {
		basePrompt = goalContinuationPrompt(goal, int(attempt))
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

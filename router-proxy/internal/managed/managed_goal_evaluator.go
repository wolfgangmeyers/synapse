package managed

import (
	"context"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// ManagedGoalEvaluator decides whether the durable goal has been completed at
// one settled boundary. It receives only the committed policy, never mutable
// process state. A host can replace the conservative default with its managed
// model evaluator without gaining authority over policy state or wakeups.
type ManagedGoalEvaluator func(context.Context, store.ManagedGoalPolicy) (string, error)

func defaultManagedGoalEvaluator(context.Context, store.ManagedGoalPolicy) (string, error) {
	// A missing evaluator must not falsely mark a goal complete. The resulting
	// fenced wakeup asks the managed agent to continue and a later integration
	// can supply a model-backed evaluator through ManagerOptions.
	return store.ManagedGoalEvaluationNotDone, nil
}

// evaluateManagedGoalAtSettled owns the manager side of one native settled
// boundary. Store claim/complete transitions are the concurrency and restart
// authority; this function only invokes the evaluator and arranges the later
// source-backed continuation.
func (m *Manager) evaluateManagedGoalAtSettled(binding *managedBinding, fence store.ManagedRuntimeFence, sequence int64) {
	if binding == nil || !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	policy, claimed, err := m.store.ClaimManagedGoalEvaluation(context.Background(), fence, sequence)
	if err != nil || !claimed || policy == nil {
		return
	}
	outcome, evaluateErr := m.goalEvaluator(m.runCtx, *policy)
	if evaluateErr != nil || (outcome != store.ManagedGoalEvaluationCompleted && outcome != store.ManagedGoalEvaluationNotDone) {
		// Evaluation failures are deliberately treated as not done. This preserves
		// the active goal rather than allowing a transient provider failure to end it.
		outcome = store.ManagedGoalEvaluationNotDone
	}
	completed, changed, err := m.store.CompleteManagedGoalEvaluation(context.Background(), fence, sequence, outcome)
	if err != nil || !changed || completed == nil {
		return
	}
	m.publishProjection()
	if outcome == store.ManagedGoalEvaluationNotDone {
		m.enqueueManagedGoalWakeup(binding, fence, completed.Config.Goal)
	}
}

// enqueueManagedGoalWakeup creates an ordinary, source-marked mailbox row and
// then uses the existing managed direct-delivery receipt path. It never writes
// an unfenced RPC prompt, so a continuation cannot bypass direct settlement.
func (m *Manager) enqueueManagedGoalWakeup(binding *managedBinding, fence store.ManagedRuntimeFence, goal string) {
	if !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	id, err := m.ids.Next()
	if err != nil {
		return
	}
	body := fmt.Sprintf("NOT_DONE: Review %s and take the next concrete action now.", strings.TrimSpace(goal))
	envelope := protocol.Envelope{
		ID: id, From: "router", To: fence.AgentID, Body: body,
		CreatedAt: m.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Source:    &protocol.MessageSource{Kind: "system"},
	}
	if _, err := m.store.Enqueue(context.Background(), envelope); err != nil {
		return
	}
	// The ordinary transport resolves an immutable source snapshot before
	// delivery. The manager creates the same snapshot before offering direct
	// delivery so this system wakeup cannot be claimed as an unfenced prompt.
	if _, err := m.store.ResolveMessageTarget(context.Background(), envelope.ID); err != nil {
		return
	}
	// Recheck the immutable binding after the durable write. A replacement can
	// still retain the ordinary row for normal recovery, but cannot be prompted
	// by this old manager callback.
	if m.liveBindingForFence(m.runCtx, binding, fence) {
		m.OfferDirect(fence.AgentID)
	}
}

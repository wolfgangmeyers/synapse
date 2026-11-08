package managed

import (
	"context"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// evaluateManagedGoalAtSettled owns one native settled boundary. The router
// claims and completes durable state with a deterministic NOT_DONE verdict.
func (m *Manager) evaluateManagedGoalAtSettled(binding *managedBinding, fence store.ManagedRuntimeFence, sequence int64) {
	if binding == nil || !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	policy, claimed, err := m.store.ClaimManagedGoalEvaluation(context.Background(), fence, sequence)
	if err != nil || !claimed || policy == nil {
		return
	}

	// No model judges completion. An active goal at a settled boundary always
	// continues; the agent turns the goal off or marks itself blocked.
	outcome, reason := store.ManagedGoalEvaluationNotDone, embeddedAutomaticContinuationReason

	wakeupID := ""
	if outcome == store.ManagedGoalEvaluationNotDone {
		wakeupID, err = m.ids.Next()
		if err != nil {
			return
		}
	}
	completed, changed, err := m.store.CompleteManagedGoalEvaluationWithWakeup(context.Background(), fence, policy.EvaluatorID, sequence, outcome, reason, wakeupID)
	if err != nil || !changed || completed == nil {
		return
	}
	m.publishProjection()
	if outcome == store.ManagedGoalEvaluationNotDone && m.liveBindingForFence(m.runCtx, binding, fence) {
		// The row and exact original-fence reservation already committed. Offering
		// it is best effort only; AllocateManagedDirectAttempt will never give it
		// to a replacement runtime.
		m.OfferDirect(fence.AgentID)
	}
}

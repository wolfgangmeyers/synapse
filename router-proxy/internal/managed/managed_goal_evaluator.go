package managed

import (
	"context"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const managedGoalEvaluationTimeout = 30 * time.Second

// evaluateManagedGoalAtSettled owns one native settled boundary. The router
// claims and completes durable state; Pi only supplies a fenced model verdict.
func (m *Manager) evaluateManagedGoalAtSettled(binding *managedBinding, fence store.ManagedRuntimeFence, sequence int64) {
	if binding == nil || !m.liveBindingForFence(m.runCtx, binding, fence) {
		return
	}
	policy, claimed, err := m.store.ClaimManagedGoalEvaluation(context.Background(), fence, sequence)
	if err != nil || !claimed || policy == nil {
		return
	}

	rpc, ok := binding.rpc.(ManagedGoalEvaluationRPC)
	outcome, reason := store.ManagedGoalEvaluationNotDone, "evaluation unavailable"
	if ok && m.liveBindingForFence(m.runCtx, binding, fence) {
		ctx, cancel := context.WithTimeout(m.runCtx, managedGoalEvaluationTimeout)
		err = rpc.StartManagedGoalEvaluation(ctx, fence, *policy)
		if err == nil {
			outcome, reason, err = rpc.WaitForManagedGoalEvaluation(ctx)
		}
		if ctx.Err() != nil {
			// Pi restores the temporary no-tools profile on its settled event. Do
			// not leave a timed-out evaluator turn running behind normal work.
			abortCtx, abortCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = binding.rpc.Abort(abortCtx)
			abortCancel()
		}
		cancel()
		if err != nil || (outcome != store.ManagedGoalEvaluationCompleted && outcome != store.ManagedGoalEvaluationNotDone) {
			outcome, reason = store.ManagedGoalEvaluationNotDone, "evaluation unavailable"
		}
	}

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

package managed

import (
	"context"
	"errors"
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

	binding.serial.Lock()
	evaluatorRPC := binding.rpc
	binding.serial.Unlock()
	rpc, ok := evaluatorRPC.(ManagedGoalEvaluationRPC)
	outcome, reason := store.ManagedGoalEvaluationNotDone, "evaluation unavailable"
	if ok && m.liveBindingForFence(m.runCtx, binding, fence) {
		ctx, cancel := context.WithTimeout(m.runCtx, managedGoalEvaluationTimeout)
		err = rpc.StartManagedGoalEvaluation(ctx, fence, *policy)
		if err == nil {
			outcome, reason, err = rpc.WaitForManagedGoalEvaluation(ctx)
		}
		if err != nil {
			// Start may have written and activated Pi's evaluator before its own
			// response timed out. Reconcile from attempted ownership, not from
			// Start's return value. The exact evaluator seam keeps public Abort
			// side-effect free for lifecycle termination.
			cleanupErr := m.reconcileManagedGoalEvaluationCleanup(evaluatorRPC, fence, policy, err)
			cancel()
			if cleanupErr != nil {
				// The durable claim must not be completed or rearmed while evaluator
				// ownership is unresolved. Stop only the exact still-live binding on
				// its existing lifecycle lane; a replacement or lifecycle owner wins
				// without receiving a stale process signal.
				m.recoverManagedGoalEvaluationCleanup(binding, fence, cleanupErr)
				return
			}
		} else {
			cancel()
		}
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

func (m *Manager) reconcileManagedGoalEvaluationCleanup(evaluatorRPC RPC, fence store.ManagedRuntimeFence, policy *store.ManagedGoalPolicy, evaluationErr error) error {
	if evaluatorRPC == nil || policy == nil {
		return evaluationErr
	}
	cleanupRPC, cleanupOK := evaluatorRPC.(ManagedGoalEvaluationCleanupRPC)
	if !cleanupOK {
		abortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return evaluatorRPC.Abort(abortCtx)
	}
	abortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleanupErr := cleanupRPC.ReconcileManagedGoalEvaluation(abortCtx, fence, policy.EvaluatorID, policy.EvaluatorGoalRevision, policy.EvaluatorSettledSequence)
	if errors.Is(cleanupErr, ErrManagedGoalEvaluationCleanupStale) {
		if stateRPC, stateOK := evaluatorRPC.(managedGoalEvaluationCleanupStateRPC); stateOK && !stateRPC.ManagedGoalEvaluationCleanupPending() {
			// A validation failure before the command was written has no evaluator
			// ownership to reconcile. This is the one safe no-op case.
			return nil
		}
	}
	return cleanupErr
}

func (m *Manager) recoverManagedGoalEvaluationCleanup(binding *managedBinding, fence store.ManagedRuntimeFence, cleanupErr error) {
	if binding == nil || binding.lane == nil {
		return
	}
	m.startManagedTask(func() {
		binding.lane.serial.Lock()
		defer binding.lane.serial.Unlock()

		binding.serial.Lock()
		if binding.reaped || binding.lifecycleAction != "" || binding.process == nil || !sameBindingFence(bindingFence(binding.agentID, binding), fence) {
			binding.serial.Unlock()
			return
		}
		process := binding.process
		binding.serial.Unlock()
		if !m.liveBindingForFence(context.Background(), binding, fence) {
			return
		}

		limit := m.config.Limits.GracefulStop
		if limit <= 0 {
			limit = DefaultLimits().GracefulStop
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), limit)
		stopErr := process.Stop(stopCtx)
		cancel()
		if stopErr == nil {
			// Process.Stop owns the exact-group absence proof. The existing ready
			// watcher now records process loss and routes durable evaluator recovery;
			// do not duplicate that transition here.
			return
		}

		// A failed stop is intentionally retained for the existing exact-fence
		// process-stop/cold-recovery path. Keep the binding and evaluator claim
		// visible rather than pretending cleanup succeeded.
		binding.serial.Lock()
		binding.retainBinding = true
		binding.serial.Unlock()
		message := "goal evaluator cleanup failed: " + sanitizeError(cleanupErr) + "; process stop failed: " + sanitizeError(stopErr)
		_ = m.store.MarkManagedFailedForFence(context.Background(), fence, "PROCESS_STOP_FAILED", message)
	})
}

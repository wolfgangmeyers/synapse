package managed

import (
	"fmt"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// Only the exact still-current, never-started workflow reservation is eligible.
func (m *Manager) recoverUnstartedWorkflowReservations(binding store.EmbeddedBinding) ([]store.EmbeddedGoalEvaluationKey, error) {
	key, found, err := m.store.RecoverableWorkflowReservation(m.runCtx, binding)
	if err != nil || !found {
		return nil, err
	}
	return []store.EmbeddedGoalEvaluationKey{key}, nil
}

// The store recomputes only a deterministic check, never an entered SDK turn.
func (m *Manager) recoverInterruptedWorkflowChecks(binding store.EmbeddedBinding) ([]store.EmbeddedGoalEvaluationKey, error) {
	keys, err := m.store.InterruptedWorkflowChecks(m.runCtx, binding)
	if err != nil {
		return nil, fmt.Errorf("list interrupted workflow checks: %w", err)
	}
	var recovered []store.EmbeddedGoalEvaluationKey
	for _, k := range keys {
		changed, err := m.store.RecoverInterruptedWorkflowCheck(m.runCtx, binding, k)
		if err != nil {
			return nil, fmt.Errorf("interrupted workflow check held (final=%d): %w", k.FinalSeq, err)
		}
		if !changed {
			continue
		}
		recovered = append(recovered, k)
	}
	return recovered, nil
}

// Recover a checked head only when the store atomically proves its old SDK turn.
func (m *Manager) recoverPreclaimWorkflowCheck(binding store.EmbeddedBinding) ([]store.EmbeddedGoalEvaluationKey, error) {
	key, found, err := m.store.RecoverPreclaimWorkflowCheck(m.runCtx, binding)
	if err != nil {
		return nil, fmt.Errorf("preclaim workflow check held: %w", err)
	}
	if !found {
		return nil, nil
	}
	return []store.EmbeddedGoalEvaluationKey{key}, nil
}

package store

import (
	"context"
	"errors"
)

// embeddedGoalContinuationHead checks that the agent is running and has an enabled goal.
func (s *Store) embeddedGoalContinuationHead(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) error {
	var desiredState, lifecycleState string
	err := s.db.QueryRowContext(ctx, `SELECT desired_state, lifecycle_state FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded'`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&desiredState, &lifecycleState)
	if err != nil {
		return err
	}
	if desiredState != "running" || lifecycleState != "running" || desiredState == "terminated" || lifecycleState == "terminated" {
		return errors.New("continuation requires running agent")
	}

	var enabled int
	if err := s.db.QueryRowContext(ctx, `SELECT goal_enabled FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return errors.New("continuation requires enabled goal")
	}

	e, err := s.embeddedEvaluationRow(ctx, b, k)
	if err != nil {
		return err
	}
	if e.Generation != b.Generation || e.EmbeddedGoalEvaluationKey != k || e.State != EmbeddedGoalEvaluationNotDone || e.CompletedAt == "" {
		return errors.New("continuation requires original completed NOT_DONE evaluation")
	}

	return nil
}

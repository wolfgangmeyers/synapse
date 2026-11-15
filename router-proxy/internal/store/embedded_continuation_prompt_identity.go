package store

import (
	"context"
	"errors"
)

type EmbeddedContinuationPromptIdentity struct {
	Workflow                                                               bool
	WorkflowID, WorkflowStateID, WorkflowName, WorkflowStateName, GoalText string
}

// EmbeddedContinuationPromptIdentity returns the immutable start snapshot only
// while the exact original SDK execution remains owned by this caller.
func (s *Store) EmbeddedContinuationPromptIdentity(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedContinuationPromptIdentity, error) {
	var result EmbeddedContinuationPromptIdentity
	if ctx == nil || b.OwnerToken <= 0 || !validEmbeddedGoalEvaluationKey(k) {
		return result, errors.New("invalid continuation prompt authority")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		e, token, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		if e.Kind != "sdk" || e.Owner != b.OwnerToken || e.Token != token || e.Source == nil || e.Source.EmbeddedGoalEvaluationKey != k {
			return errors.New("stale continuation prompt execution")
		}
		err = s.db.QueryRowContext(ctx, `SELECT workflow_id,workflow_state_id,workflow_name,workflow_state_name,goal_text FROM embedded_continuation_prompt_identity WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=? AND owner_token=? AND execution_token=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest, b.OwnerToken, token).Scan(&result.WorkflowID, &result.WorkflowStateID, &result.WorkflowName, &result.WorkflowStateName, &result.GoalText)
		if err != nil {
			return err
		}
		result.Workflow = result.WorkflowID != ""
		return nil
	})
	return result, err
}

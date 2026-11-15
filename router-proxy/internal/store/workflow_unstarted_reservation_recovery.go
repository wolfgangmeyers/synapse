package store

import (
	"context"
	"database/sql"
	"errors"
)

// RecoverableWorkflowReservation returns only an exact head reservation that
// has never crossed its atomic started boundary. Dispatch rechecks authority.
func (s *Store) RecoverableWorkflowReservation(ctx context.Context, b EmbeddedBinding) (EmbeddedGoalEvaluationKey, bool, error) {
	var k EmbeddedGoalEvaluationKey
	if ctx == nil || b.OwnerToken <= 0 {
		return k, false, errors.New("invalid recovery owner")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return k, false, errors.New("store closed")
	}
	if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
		return k, false, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT c.user_seq,c.user_digest,c.goal_revision,c.final_seq,c.final_digest FROM embedded_goal_continuations c JOIN embedded_goal_evaluations e USING(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest) JOIN managed_agents a ON a.agent_id=c.agent_id JOIN workflows w ON w.id=a.active_workflow_id JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=? AND c.state='reserved' AND c.disposition='pending' AND c.started_at IS NULL AND e.state='NOT_DONE' AND c.final_seq=(SELECT MAX(seq) FROM embedded_entries WHERE agent_id=c.agent_id AND session_id=c.session_id AND epoch=c.epoch) AND c.retention_expires_at_ms>MAX((SELECT evidence_expired_through_ms+2592000000 FROM embedded_sessions s WHERE s.agent_id=c.agent_id AND s.session_id=c.session_id AND s.epoch=c.epoch AND s.generation=c.generation),?) ORDER BY c.final_seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation, s.clock.Now().UnixMilli()).Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &k.FinalSeq, &k.FinalDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return k, false, nil
	}
	if err != nil {
		return k, false, err
	}
	if err = s.embeddedGoalEvaluationGate(ctx, b, k, true); err != nil {
		return k, false, err
	}
	return k, true, nil
}

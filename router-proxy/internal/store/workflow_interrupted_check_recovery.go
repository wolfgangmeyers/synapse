package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// InterruptedWorkflowChecks lists candidate evidence only. Recovery rechecks all
// authority atomically; listing itself never grants permission to settle.
func (s *Store) InterruptedWorkflowChecks(ctx context.Context, b EmbeddedBinding) ([]EmbeddedGoalEvaluationKey, error) {
	if ctx == nil || b.OwnerToken <= 0 {
		return nil, errors.New("invalid recovery owner")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.user_seq,e.user_digest,e.goal_revision,e.final_seq,e.final_digest
 FROM embedded_goal_evaluations e
 JOIN managed_agents a ON a.agent_id=e.agent_id
 JOIN workflows w ON w.id=a.active_workflow_id
 JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id
 WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=?
 AND e.state='UNKNOWN' AND e.owner_token<?
 AND a.desired_state='running' AND a.lifecycle_state IN ('starting','running') AND a.blocked=0
 AND NOT EXISTS(SELECT 1 FROM managed_subagents c WHERE c.parent_agent_id=e.agent_id AND c.status IN ('queued','running'))
 AND e.final_seq=(SELECT MAX(seq) FROM embedded_entries WHERE agent_id=e.agent_id AND session_id=e.session_id AND epoch=e.epoch)
 ORDER BY e.final_seq DESC`, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []EmbeddedGoalEvaluationKey
	for rows.Next() {
		var k EmbeddedGoalEvaluationKey
		if err := rows.Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &k.FinalSeq, &k.FinalDigest); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// RecoverInterruptedWorkflowCheck settles only the pure workflow decision, not
// an SDK invocation. The original claim owner stays immutable as evidence.
func (s *Store) RecoverInterruptedWorkflowCheck(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (bool, error) {
	if ctx == nil || b.OwnerToken <= 0 || !validEmbeddedGoalEvaluationKey(k) {
		return false, errors.New("invalid workflow recovery identity")
	}
	changed := false
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		if err := s.embeddedGoalEvaluationGate(ctx, b, k, true); err != nil {
			return err
		}
		e, err := s.embeddedEvaluationRow(ctx, b, k)
		if err != nil {
			return err
		}
		if e.Generation != b.Generation || e.EmbeddedGoalEvaluationKey != k {
			return errors.New("conflicting workflow recovery claim")
		}
		if e.State != EmbeddedGoalEvaluationUnknown {
			return nil
		}
		if e.OwnerToken <= 0 || e.OwnerToken >= b.OwnerToken {
			return errors.New("workflow check owner not fenced")
		}
		var active int
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents a JOIN workflows w ON w.id=a.active_workflow_id JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id WHERE a.agent_id=? AND a.desired_state='running' AND a.lifecycle_state IN ('starting','running') AND a.blocked=0`, b.AgentID).Scan(&active)
		if err != nil {
			return err
		}
		if active != 1 {
			return errors.New("workflow authority unavailable")
		}
		execution, _, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		if execution.Kind != "" && (execution.Kind != "evaluator" || execution.Owner != e.OwnerToken || execution.Source == nil || execution.Source.EmbeddedGoalEvaluationKey != k) {
			return errors.New("ambiguous execution held")
		}
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_subagents WHERE parent_agent_id=? AND status IN ('queued','running')`, b.AgentID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest).Scan(&count)
		if err != nil {
			return err
		}
		if count != 0 {
			return errors.New("existing delivery evidence held")
		}
		now := formatTimestamp(s.clock.Now())
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_goal_evaluations SET state='NOT_DONE',reason=?,completed_at=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=? AND owner_token=? AND state='UNKNOWN'`, "Active workflow requires progress or workflow_snooze.", now, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest, e.OwnerToken)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale workflow recovery claim")
		}
		e.State = EmbeddedGoalEvaluationNotDone
		e.CompletedAt = now
		e.OwnerToken = b.OwnerToken
		if err = s.reserveEmbeddedGoalContinuation(ctx, e); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return changed, nil
}

// RecoverPreclaimWorkflowCheck handles the narrow crash window after a checked
// final was saved but before its deterministic workflow evaluation was claimed.
// Discovery and settlement share one write transaction, including owner, head,
// receipt, retention and original SDK execution checks.
func (s *Store) RecoverPreclaimWorkflowCheck(ctx context.Context, b EmbeddedBinding) (EmbeddedGoalEvaluationKey, bool, error) {
	var k EmbeddedGoalEvaluationKey
	if ctx == nil || b.OwnerToken <= 0 {
		return k, false, errors.New("invalid recovery owner")
	}
	found := false
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		var active int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents a JOIN workflows w ON w.id=a.active_workflow_id JOIN workflow_states st ON st.id=a.active_workflow_state_id AND st.workflow_id=w.id WHERE a.agent_id=? AND a.desired_state='running' AND a.lifecycle_state IN ('starting','running') AND a.blocked=0`, b.AgentID).Scan(&active); err != nil {
			return err
		}
		if active == 0 {
			return nil
		}
		execution, _, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		// A cleanly cleared execution is normal settled history, not evidence
		// of the pre-claim crash window.
		if execution.Kind == "" {
			return nil
		}
		var kind string
		var body []byte
		err = s.db.QueryRowContext(ctx, `SELECT seq,digest,kind,body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&k.FinalSeq, &k.FinalDigest, &kind, &body)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !embeddedCheckedStop(kind, body) {
			return nil
		}
		err = s.db.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND kind='user' AND seq<? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalSeq).Scan(&k.UserSeq, &k.UserDigest)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		// Any verdict for the head already owns this turn; never manufacture another.
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND final_seq=? AND final_digest=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalSeq, k.FinalDigest).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		err = s.db.QueryRowContext(ctx, `SELECT goal_revision FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND turn_entry_digest=? AND goal_enabled=1 ORDER BY goal_revision DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest).Scan(&k.GoalRevision)
		if err != nil {
			return fmt.Errorf("preclaim receipt held: %w", err)
		}
		if err = s.embeddedGoalEvaluationGate(ctx, b, k, true); err != nil {
			return fmt.Errorf("preclaim authority held: %w", err)
		}
		if execution.Kind != "sdk" || execution.Owner <= 0 || execution.Owner >= b.OwnerToken || execution.Current == nil || execution.Current.UserSeq != k.UserSeq || execution.Current.UserDigest != k.UserDigest || execution.Current.GoalRevision != k.GoalRevision {
			return errors.New("preclaim execution held")
		}
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_subagents WHERE parent_agent_id=? AND status IN ('queued','running')`, b.AgentID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("preclaim child work held")
		}
		now := formatTimestamp(s.clock.Now())
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at,retention_basis_ms) VALUES(?,?,?,?,?,?,?,?,?,?,'UNKNOWN',?,(SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=? AND digest=? AND kind='assistant'))`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.OwnerToken, k.FinalSeq, k.FinalDigest, now, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalSeq, k.FinalDigest)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE embedded_goal_evaluations SET state='NOT_DONE',reason=?,completed_at=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND final_seq=? AND final_digest=? AND owner_token=? AND state='UNKNOWN'`, "Active workflow requires progress or workflow_snooze.", now, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest, b.OwnerToken)
		if err != nil {
			return err
		}
		e := EmbeddedGoalEvaluation{EmbeddedGoalEvaluationKey: k, AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, OwnerToken: b.OwnerToken, State: EmbeddedGoalEvaluationNotDone, ClaimedAt: now, CompletedAt: now}
		if err = s.reserveEmbeddedGoalContinuation(ctx, e); err != nil {
			return err
		}
		found = true
		return nil
	})
	if err != nil {
		return EmbeddedGoalEvaluationKey{}, false, err
	}
	return k, found, nil
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// WorkflowInterruptionMarker is durable evidence of an unknown-effect execution,
// not authority to replay it or a claim that its effects did not occur.
type WorkflowInterruptionMarker struct {
	Key                       EmbeddedGoalEvaluationKey `json:"key"`
	SessionID                 string                    `json:"session_id"`
	Epoch                     int64                     `json:"epoch"`
	Generation                string                    `json:"generation"`
	OwnerToken                int64                     `json:"owner_token"`
	ExecutionToken            int64                     `json:"execution_token"`
	RecordedAtMS              int64                     `json:"recorded_at_ms"`
	WorkflowID                string                    `json:"workflow_id"`
	WorkflowStateID           string                    `json:"workflow_state_id"`
	WorkflowBindingGeneration int64                     `json:"workflow_binding_generation"`
}

// Called within the takeover transaction after the successor has fenced old.
func (s *Store) recordWorkflowInterruptionTx(ctx context.Context, b EmbeddedBinding, oldOwner, newOwner int64) error {
	e, token, err := s.embeddedExecutionRowTx(ctx, b)
	if err != nil {
		return err
	}
	// Fence against stale, mismatched, or out-of-order execution records during owner takeover:
	// - e.Owner != oldOwner: the execution slot does not belong to the predecessor being fenced.
	// - e.Owner >= newOwner: protects against out-of-order or concurrent owner takeover states
	//   where the recorded execution is already owned by or ahead of the new successor owner.
	if e.Kind != "sdk" || e.Owner <= 0 || e.Owner != oldOwner || e.Owner >= newOwner || e.Token != token || e.Source == nil {
		return nil
	}
	k := e.Source.EmbeddedGoalEvaluationKey
	now := s.clock.Now().UnixMilli()
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_interruption_markers(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,owner_token,execution_token,recorded_at_ms,workflow_id,workflow_state_id,workflow_binding_generation)
 SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,c.final_seq,c.final_digest,i.owner_token,i.execution_token,?,i.workflow_id,i.workflow_state_id,i.workflow_binding_generation FROM embedded_goal_continuations c
 JOIN workflow_continuation_start_identity i ON i.agent_id=c.agent_id AND i.session_id=c.session_id AND i.epoch=c.epoch AND i.generation=c.generation AND i.user_seq=c.user_seq AND i.user_digest=c.user_digest AND i.goal_revision=c.goal_revision AND i.final_seq=c.final_seq AND i.final_digest=c.final_digest
 WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=? AND c.user_seq=? AND c.user_digest=? AND c.goal_revision=? AND c.final_seq=? AND c.final_digest=? AND c.state='started' AND c.disposition='unknown' AND i.owner_token=? AND i.execution_token=?`, now, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest, e.Owner, token)
	if err != nil {
		return err
	}
	return s.pruneWorkflowInterruptionsTx(ctx, b.AgentID)
}
func (s *Store) pruneWorkflowInterruptionsTx(ctx context.Context, agent string) error {
	cutoff := s.clock.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	if agent == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM workflow_interruption_markers WHERE recorded_at_ms<=?`, cutoff)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM workflow_interruption_markers WHERE recorded_at_ms<=? OR (agent_id=? AND id NOT IN (SELECT id FROM workflow_interruption_markers WHERE agent_id=? ORDER BY recorded_at_ms DESC,id DESC LIMIT 100))`, cutoff, agent, agent)
	return err
}
func (s *Store) WorkflowInterruptionMarkers(ctx context.Context, agent string) ([]WorkflowInterruptionMarker, error) {
	if ctx == nil || agent == "" {
		return nil, errors.New("invalid interruption query")
	}
	var result []WorkflowInterruptionMarker
	err := s.runImmediate(ctx, func() error {
		if err := s.pruneWorkflowInterruptionsTx(ctx, agent); err != nil {
			return err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT session_id,epoch,generation,user_seq,user_digest,goal_revision,final_seq,final_digest,owner_token,execution_token,recorded_at_ms,workflow_id,workflow_state_id,workflow_binding_generation FROM workflow_interruption_markers WHERE agent_id=? ORDER BY recorded_at_ms DESC,id DESC`, agent)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m WorkflowInterruptionMarker
			if err = rows.Scan(&m.SessionID, &m.Epoch, &m.Generation, &m.Key.UserSeq, &m.Key.UserDigest, &m.Key.GoalRevision, &m.Key.FinalSeq, &m.Key.FinalDigest, &m.OwnerToken, &m.ExecutionToken, &m.RecordedAtMS, &m.WorkflowID, &m.WorkflowStateID, &m.WorkflowBindingGeneration); err != nil {
				return err
			}
			result = append(result, m)
		}
		return rows.Err()
	})
	return result, err
}

// LegacyContinuationAmbiguity reports unclassified pre-witness evidence. It is
// deliberately independent of current workflow bindings and cannot authorize replay.
func (s *Store) LegacyContinuationAmbiguity(ctx context.Context, agent string) (int64, error) {
	if ctx == nil || agent == "" {
		return 0, errors.New("invalid ambiguity query")
	}
	var recorded int64
	err := s.runImmediate(ctx, func() error {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM legacy_continuation_ambiguity WHERE recorded_at_ms<=?`, s.clock.Now().Add(-30*24*time.Hour).UnixMilli()); err != nil {
			return err
		}
		err := s.db.QueryRowContext(ctx, `SELECT recorded_at_ms FROM legacy_continuation_ambiguity WHERE agent_id=?`, agent).Scan(&recorded)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	return recorded, err
}

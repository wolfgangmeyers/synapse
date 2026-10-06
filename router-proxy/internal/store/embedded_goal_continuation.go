package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// EmbeddedGoalContinuation is a non-mail, single-use reservation. A started
// row has unknown external outcome until a later phase supplies observation;
// it is not a retry token.
type EmbeddedGoalContinuation struct {
	EmbeddedGoalEvaluationKey
	AgentID, SessionID, Generation, Origin    string
	Epoch                                     int64
	State, Disposition, ReservedAt, StartedAt string
	DueAt                                     sql.NullString
}

func (s *Store) embeddedGoalContinuationRow(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedGoalContinuation, error) {
	var c EmbeddedGoalContinuation
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,origin,state,disposition,reserved_at,due_at,COALESCE(started_at,'') FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision).Scan(&c.AgentID, &c.SessionID, &c.Epoch, &c.Generation, &c.UserSeq, &c.UserDigest, &c.GoalRevision, &c.Origin, &c.State, &c.Disposition, &c.ReservedAt, &c.DueAt, &c.StartedAt)
	return c, err
}

// EmbeddedGoalContinuationFor reads only the exact persisted reservation.
func (s *Store) EmbeddedGoalContinuationFor(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedGoalContinuation, error) {
	if ctx == nil || !validEmbeddedGoalEvaluationKey(k) {
		return EmbeddedGoalContinuation{}, errors.New("invalid continuation key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return EmbeddedGoalContinuation{}, errors.New("store is closed")
	}
	return s.embeddedGoalContinuationRow(ctx, b, k)
}

// LatestReservedEmbeddedGoalContinuation reads the newest not-yet-started continuation
// for the given binding.
func (s *Store) LatestReservedEmbeddedGoalContinuation(ctx context.Context, b EmbeddedBinding) (EmbeddedGoalEvaluationKey, bool, error) {
	if ctx == nil {
		return EmbeddedGoalEvaluationKey{}, false, errors.New("nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return EmbeddedGoalEvaluationKey{}, false, errors.New("store is closed")
	}
	var k EmbeddedGoalEvaluationKey
	var finalSeq sql.NullInt64
	var finalDigest sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT c.user_seq, c.user_digest, c.goal_revision, e.final_seq, e.final_digest FROM embedded_goal_continuations c JOIN embedded_goal_evaluations e ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.user_seq=c.user_seq AND e.user_digest=c.user_digest AND e.goal_revision=c.goal_revision WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.state='reserved' ORDER BY c.user_seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch).Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &finalSeq, &finalDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return k, false, nil
	}
	if err != nil {
		return k, false, err
	}
	if finalSeq.Valid {
		k.FinalSeq = finalSeq.Int64
	}
	if finalDigest.Valid {
		k.FinalDigest = finalDigest.String
	}
	return k, true, nil
}

// RestartFailedManagedAgent reconciles stale claimed wake slots to unknown and
// resets a failed managed agent to running/idle with cleared errors.
func (s *Store) RestartFailedManagedAgent(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state='unknown' WHERE state='claimed' AND (agent_id=? OR ?='')`, agentID, agentID); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='idle', lifecycle_state='running', last_error_code=NULL, last_error_message=NULL, last_error_at=NULL, updated_at=? WHERE agent_id=?`, now, agentID)
		return err
	})
}

func (s *Store) HasPendingWakeOrStartedContinuation(ctx context.Context, b EmbeddedBinding) (bool, error) {
	if ctx == nil {
		return false, errors.New("nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("store is closed")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND state IN ('planned','claimed')) + (SELECT count(*) FROM embedded_goal_continuations c WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.state='started' AND NOT EXISTS (SELECT 1 FROM embedded_entries e WHERE e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.kind='assistant' AND e.seq > c.user_seq))`, b.AgentID, b.AgentID, b.SessionID, b.Epoch).Scan(&count)
	return count > 0, err
}

// continuations for the exact recovered session. Evaluator claims and started
// UNKNOWN rows are never recovery work.
func (s *Store) ReservedEmbeddedGoalSnoozes(ctx context.Context, b EmbeddedBinding) ([]EmbeddedGoalContinuation, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.agent_id,c.session_id,c.epoch,c.generation,c.user_seq,c.user_digest,c.goal_revision,c.origin,c.state,c.disposition,c.reserved_at,c.due_at,COALESCE(c.started_at,''),e.final_seq,e.final_digest
 FROM embedded_goal_continuations c JOIN embedded_goal_evaluations e
 ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch
 AND e.user_seq=c.user_seq AND e.user_digest=c.user_digest AND e.goal_revision=c.goal_revision
 WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=?
 AND c.state='reserved' AND c.due_at IS NOT NULL AND e.state='NOT_DONE'`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []EmbeddedGoalContinuation
	for rows.Next() {
		var c EmbeddedGoalContinuation
		if err := rows.Scan(&c.AgentID, &c.SessionID, &c.Epoch, &c.Generation, &c.UserSeq, &c.UserDigest, &c.GoalRevision, &c.Origin, &c.State, &c.Disposition, &c.ReservedAt, &c.DueAt, &c.StartedAt, &c.FinalSeq, &c.FinalDigest); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// reserveEmbeddedGoalContinuation runs only within the verdict's immediate
// transaction. A committed legacy NOT_DONE row may be reconciled, but an
// UNKNOWN claim or DONE row cannot become a reservation.
func (s *Store) reserveEmbeddedGoalContinuation(ctx context.Context, e EmbeddedGoalEvaluation) error {
	if e.State != EmbeddedGoalEvaluationNotDone || e.CompletedAt == "" {
		return errors.New("continuation requires proven NOT_DONE")
	}
	if s.embeddedGoalContinuationFault != nil {
		if err := s.embeddedGoalContinuationFault("before_reserve"); err != nil {
			return err
		}
	}
	// The saved preference is only authority for this exact initiating turn and
	// revision. Compute due from the verdict commit clock, not the call clock.
	var duration sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(c.args_json,'$.duration_ms')
 FROM embedded_goal_snooze_preferences p JOIN embedded_goal_snooze_calls c ON c.call_id=p.call_id
 WHERE p.agent_id=? AND p.session_id=? AND p.epoch=? AND p.generation=?
 AND p.user_seq=? AND p.user_digest=? AND p.goal_revision=? AND p.active=1
 AND c.agent_id=p.agent_id AND c.session_id=p.session_id AND c.epoch=p.epoch
 AND c.generation=p.generation AND c.role=p.role AND c.user_seq=p.user_seq
 AND c.user_digest=p.user_digest AND c.goal_revision=p.goal_revision
 AND c.policy_version=p.policy_version`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest, e.GoalRevision).Scan(&duration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var due sql.NullString
	if err == nil {
		if !duration.Valid || duration.Int64 <= 0 || duration.Int64 > EmbeddedGoalSnoozeMaxDurationMS {
			return errors.New("invalid saved goal snooze duration")
		}
		due = sql.NullString{String: formatTimestamp(s.clock.Now().UTC().Add(time.Duration(duration.Int64) * time.Millisecond)), Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at) VALUES(?,?,?,?,?,?,?,'reserved','pending',?,?) ON CONFLICT(agent_id,session_id,epoch,user_seq,user_digest,goal_revision) DO NOTHING`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest, e.GoalRevision, formatTimestamp(s.clock.Now()), due)
	return err
}

// StartEmbeddedGoalContinuation is the sole one-shot start CAS. The caller
// must commit this before contacting the provider. A crash after this point
// leaves an explicit unknown outcome; another start cannot replay it.
func (s *Store) StartEmbeddedGoalContinuation(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedGoalContinuation, bool, error) {
	if ctx == nil || !validEmbeddedGoalEvaluationKey(k) {
		return EmbeddedGoalContinuation{}, false, errors.New("invalid continuation key")
	}
	var c EmbeddedGoalContinuation
	started := false
	err := s.runImmediate(ctx, func() error {
		var err error
		c, err = s.embeddedGoalContinuationRow(ctx, b, k)
		if err != nil {
			return err
		}
		if c.Generation != b.Generation {
			return errors.New("stale continuation generation")
		}
		if err = s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if c.State != "reserved" {
			return nil
		}
		var desired, applied int64
		var enabled int
		var projection string
		err = s.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,goal_enabled,projection_state FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&desired, &applied, &enabled, &projection)
		if err != nil {
			return err
		}
		if enabled != 1 || desired != k.GoalRevision || applied != k.GoalRevision || projection != "applied" {
			_, err = s.db.ExecContext(ctx, `UPDATE embedded_goal_continuations SET state='invalidated',disposition='invalidated' WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=? AND state='reserved'`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision)
			if err != nil {
				return err
			}
			c, err = s.embeddedGoalContinuationRow(ctx, b, k)
			return err
		}
		// The original verdict and semantic head are checked in this same
		// transaction, not in a Manager query that could race an admission.
		if c.Disposition != "pending" || c.StartedAt != "" {
			return errors.New("invalid continuation reservation")
		}
		now := formatTimestamp(s.clock.Now())
		if c.DueAt.Valid {
			due, parseErr := time.Parse("2006-01-02T15:04:05.000Z", c.DueAt.String)
			if parseErr != nil || formatTimestamp(due) != c.DueAt.String {
				return errors.New("invalid continuation due")
			}
			if c.DueAt.String > now {
				return nil
			}
		}
		if err = s.embeddedGoalContinuationHead(ctx, b, k); err != nil {
			return err
		}
		// Commit started UNKNOWN before provider entry; never replace k.
		result, err := s.db.ExecContext(ctx, `UPDATE embedded_goal_continuations SET state='started',disposition='unknown',started_at=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND state='reserved' AND (due_at IS NULL OR due_at<=?)`, now, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, now)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			c, err = s.embeddedGoalContinuationRow(ctx, b, k)
			return err // A future due remains reserved; it is not a failed start.
		}
		c, err = s.embeddedGoalContinuationRow(ctx, b, k)
		started = err == nil
		return err
	})
	if err != nil {
		return EmbeddedGoalContinuation{}, false, err
	}
	return c, started, nil
}

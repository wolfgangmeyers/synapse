package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	if err := s.embeddedEvidenceReadGate(ctx, b, k, ""); err != nil {
		return EmbeddedGoalContinuation{}, err
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
	err := s.db.QueryRowContext(ctx, `SELECT c.user_seq, c.user_digest, c.goal_revision, e.final_seq, e.final_digest FROM embedded_goal_continuations c JOIN embedded_goal_evaluations e ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.user_seq=c.user_seq AND e.user_digest=c.user_digest AND e.goal_revision=c.goal_revision WHERE c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.state='reserved' AND EXISTS(SELECT 1 FROM embedded_retention_closures x WHERE x.agent_id=c.agent_id AND x.session_id=c.session_id AND x.epoch=c.epoch AND x.generation=c.generation AND x.user_seq=c.user_seq AND x.user_digest=c.user_digest AND x.goal_revision=c.goal_revision) AND c.retention_expires_at_ms>MAX((SELECT evidence_expired_through_ms+2592000000 FROM embedded_sessions s WHERE s.agent_id=c.agent_id AND s.session_id=c.session_id AND s.epoch=c.epoch AND s.generation=c.generation),?) ORDER BY c.user_seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, s.clock.Now().UnixMilli()).Scan(&k.UserSeq, &k.UserDigest, &k.GoalRevision, &finalSeq, &finalDigest)
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
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='idle', lifecycle_state='running', accepting_prompts=1, slot_held=1, last_error_code=NULL, last_error_message=NULL, last_error_at=NULL, updated_at=? WHERE agent_id=?`, now, agentID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			_, err = s.bumpManagedProjectionVersion(ctx)
			return err
		}
		return nil
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

// GetActiveGoalContinuations returns a list of sibling agent IDs that have active goals
// or active/reserved goal continuations.
func (s *Store) GetActiveGoalContinuations(ctx context.Context, callerAgentID string) ([]string, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}

	seen := make(map[string]bool)
	var activeAgents []string

	// Query embedded_goal_continuations if table exists
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT agent_id FROM embedded_goal_continuations WHERE state IN ('reserved', 'started') AND agent_id != ?`, callerAgentID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var agentID string
			if err := rows.Scan(&agentID); err == nil && agentID != "" && !seen[agentID] {
				seen[agentID] = true
				activeAgents = append(activeAgents, agentID)
			}
		}
	}

	// Query managed_agent_policies for active goals if table exists
	pRows, err := s.db.QueryContext(ctx, `SELECT DISTINCT agent_id FROM managed_agent_policies WHERE goal_enabled = 1 AND agent_id != ?`, callerAgentID)
	if err == nil {
		defer pRows.Close()
		for pRows.Next() {
			var agentID string
			if err := pRows.Scan(&agentID); err == nil && agentID != "" && !seen[agentID] {
				seen[agentID] = true
				activeAgents = append(activeAgents, agentID)
			}
		}
	}

	return activeAgents, nil
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
 AND c.state='reserved' AND c.due_at IS NOT NULL AND e.state='NOT_DONE' AND c.retention_expires_at_ms>MAX((SELECT evidence_expired_through_ms+2592000000 FROM embedded_sessions s WHERE s.agent_id=c.agent_id AND s.session_id=c.session_id AND s.epoch=c.epoch AND s.generation=c.generation),?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, s.clock.Now().UnixMilli())
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
	b := EmbeddedBinding{AgentID: e.AgentID, SessionID: e.SessionID, Epoch: e.Epoch, Generation: e.Generation, OwnerToken: e.OwnerToken}
	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, e.AgentID, e.SessionID, e.Epoch, e.UserSeq, e.UserDigest, e.GoalRevision).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return s.embeddedEvidenceReplayTx(ctx, b, e.EmbeddedGoalEvaluationKey, "")
	}
	// Resolve relevant call-time evidence, never settlement + duration.
	var savedDue, savedResult string
	err := s.db.QueryRowContext(ctx, `SELECT p.due_at,c.result_json
 FROM embedded_goal_snooze_preferences p JOIN embedded_goal_snooze_calls c ON c.call_id=p.call_id
 WHERE p.agent_id=? AND p.session_id=? AND p.epoch=? AND p.generation=?
 AND p.user_seq=? AND p.user_digest=? AND p.goal_revision=? AND p.active=1
 AND c.agent_id=p.agent_id AND c.session_id=p.session_id AND c.epoch=p.epoch
 AND c.generation=p.generation AND c.role=p.role AND c.user_seq=p.user_seq
 AND c.user_digest=p.user_digest AND c.goal_revision=p.goal_revision AND c.policy_version=p.policy_version`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest, e.GoalRevision).Scan(&savedDue, &savedResult)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var due sql.NullString
	latest := s.clock.Now()
	consider := func(raw string) {
		if t, err := time.Parse(time.RFC3339Nano, raw); err == nil && t.After(latest) {
			latest = t
			due = sql.NullString{String: formatTimestamp(t), Valid: true}
		}
	}
	if err == nil {
		var p EmbeddedGoalSnoozePreference
		if err := json.Unmarshal([]byte(savedResult), &p); err != nil {
			return err
		}
		if p.DueAt != savedDue || p.AgentID != e.AgentID || p.SessionID != e.SessionID || p.Epoch != e.Epoch || p.Generation != e.Generation || p.UserSeq != e.UserSeq || p.UserDigest != e.UserDigest || p.GoalRevision != e.GoalRevision || p.DurationMS <= 0 || p.DurationMS > EmbeddedGoalSnoozeMaxDurationMS {
			return errors.New("invalid saved snooze evidence")
		}
		if _, err := time.Parse(time.RFC3339Nano, savedDue); err != nil {
			return errors.New("invalid saved snooze deadline")
		}
		consider(savedDue)
	}
	var policy sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT snoozed_until FROM managed_agent_policies WHERE agent_id=?`, e.AgentID).Scan(&policy)
	if err != nil {
		return err
	}
	if policy.Valid {
		consider(policy.String)
	}

	var admitted int64
	if err := s.db.QueryRowContext(ctx, `SELECT retention_admitted_at_ms FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=? AND digest=?`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest).Scan(&admitted); err != nil {
		return err
	}
	expiry := admitted + 2*embeddedEvidenceAgeMS
	if due.Valid {
		d, err := time.Parse(time.RFC3339Nano, due.String)
		if err != nil {
			return err
		}
		if d.UnixMilli() > admitted+31*24*60*60*1000 {
			return errors.New("retention-expired deadline envelope")
		}
		if d.UnixMilli()+embeddedEvidenceAgeMS > expiry {
			expiry = d.UnixMilli() + embeddedEvidenceAgeMS
		}
	}
	if err := s.embeddedEvidenceAdmissionTx(ctx, b, e.UserSeq, e.UserDigest); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at,retention_expires_at_ms) VALUES(?,?,?,?,?,?,?,'reserved','pending',?,?,?) ON CONFLICT(agent_id,session_id,epoch,user_seq,user_digest,goal_revision) DO NOTHING`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest, e.GoalRevision, formatTimestamp(s.clock.Now()), due, expiry)
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
		if err := s.embeddedEvidenceReplayTx(ctx, b, k, ""); err != nil {
			return err
		}
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
		if err := s.installEmbeddedGoalContinuationExecutionTx(ctx, b, k); err != nil {
			return err
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

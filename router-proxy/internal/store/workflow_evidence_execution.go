package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// One source and one current user, never a historical execution ledger.
type embeddedExecutionClosure struct {
	EmbeddedGoalEvaluationKey
	CallID string `json:"call_id"`
}
type embeddedExecution struct {
	Kind    string                    `json:"kind"`
	Owner   int64                     `json:"owner"`
	Token   int64                     `json:"token"`
	Source  *embeddedExecutionClosure `json:"source"`
	Current *embeddedExecutionClosure `json:"current"`
}

func (s *Store) embeddedExecutionRowTx(ctx context.Context, b EmbeddedBinding) (embeddedExecution, int64, error) {
	var raw sql.NullString
	var token int64
	err := s.db.QueryRowContext(ctx, `SELECT execution_json,execution_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&raw, &token)
	var e embeddedExecution
	if err == nil && raw.Valid {
		err = json.Unmarshal([]byte(raw.String), &e)
	}
	return e, token, err
}
func (s *Store) saveEmbeddedExecutionTx(ctx context.Context, b EmbeddedBinding, e embeddedExecution) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET execution_json=?,execution_token=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND owner_token=?`, string(raw), e.Token, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("stale execution owner")
	}
	return err
}

func (s *Store) embeddedExecutionOwnerTx(ctx context.Context, b EmbeddedBinding) error {
	if err := s.embeddedFence(ctx, b); err != nil {
		return err
	}
	var owner int64
	err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != b.OwnerToken {
		return errors.New("stale execution owner")
	}
	return nil
}

// Admission uses both wall time and the durable high-water fence. A cleared
// expired identity can never acquire a fresh execution exception after rollback.
func (s *Store) embeddedExecutionAdmissionTx(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) error {
	var admitted, cutoff int64
	var kind, digest string
	err := s.db.QueryRowContext(ctx, `SELECT e.retention_admitted_at_ms,s.evidence_expired_through_ms,e.kind,e.digest FROM embedded_entries e JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation) WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq).Scan(&admitted, &cutoff, &kind, &digest)
	if err != nil {
		return err
	}
	if kind != "user" || digest != k.UserDigest || admitted <= 0 {
		return errors.New("invalid execution user")
	}
	deadline := admitted + int64(30*24*time.Hour/time.Millisecond)
	if s.clock.Now().UnixMilli() >= deadline || cutoff >= admitted {
		return errors.New("execution retention-expired")
	}
	if err = s.refreshEmbeddedExecutionCallsTx(ctx); err != nil {
		return err
	}
	// Advancing the existing fence also makes admission monotonic on reopen.
	_, err = s.db.ExecContext(ctx, `UPDATE embedded_sessions SET evidence_expired_through_ms=MAX(evidence_expired_through_ms,?) WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, s.clock.Now().Add(-30*24*time.Hour).UnixMilli(), b.AgentID, b.SessionID, b.Epoch, b.Generation)
	return err
}
func (s *Store) executionClosureTx(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (*embeddedExecutionClosure, error) {
	c := &embeddedExecutionClosure{EmbeddedGoalEvaluationKey: k}
	err := s.db.QueryRowContext(ctx, `SELECT call_id FROM embedded_goal_snooze_preferences WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision).Scan(&c.CallID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return c, err
}
func (s *Store) newEmbeddedExecutionTx(ctx context.Context, b EmbeddedBinding, kind string, k *EmbeddedGoalEvaluationKey) (int64, error) {
	if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
		return 0, err
	}
	old, token, err := s.embeddedExecutionRowTx(ctx, b)
	if err != nil {
		return 0, err
	}
	if old.Kind != "" && old.Owner == b.OwnerToken {
		return 0, errors.New("embedded execution already active")
	}
	if token == int64(^uint64(0)>>1) {
		return 0, errors.New("execution token exhausted")
	}
	e := embeddedExecution{Kind: kind, Owner: b.OwnerToken, Token: token + 1}
	if k != nil {
		e.Source, err = s.executionClosureTx(ctx, b, *k)
		if err != nil {
			return 0, err
		}
	}
	return e.Token, s.saveEmbeddedExecutionTx(ctx, b, e)
}

func (s *Store) EnterEmbeddedGoalEvaluationExecution(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (int64, error) {
	var token int64
	err := s.runImmediate(ctx, func() error {
		if !validEmbeddedGoalEvaluationKey(k) {
			return errors.New("invalid evaluation execution key")
		}
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		e, err := s.embeddedEvaluationRow(ctx, b, k)
		if err != nil {
			return err
		}
		if e.State != EmbeddedGoalEvaluationUnknown || e.OwnerToken != b.OwnerToken || e.Generation != b.Generation || e.FinalSeq != k.FinalSeq || e.FinalDigest != k.FinalDigest {
			return errors.New("invalid evaluation execution claim")
		}
		if err = s.embeddedExecutionAdmissionTx(ctx, b, k); err != nil {
			return err
		}
		token, err = s.newEmbeddedExecutionTx(ctx, b, "evaluator", &k)
		return err
	})
	return token, err
}

// Verify the slot installed by the SQL reserved-to-started transition.
// Caller owns Start's immediate transaction; failure rolls back the started CAS.
func (s *Store) installEmbeddedGoalContinuationExecutionTx(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) error {
	if !validEmbeddedGoalEvaluationKey(k) {
		return errors.New("invalid continuation execution key")
	}
	if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
		return err
	}
	e, _, err := s.embeddedExecutionRowTx(ctx, b)
	if err != nil {
		return err
	}
	if e.Kind != "sdk" || e.Owner != b.OwnerToken || e.Source == nil || e.Source.EmbeddedGoalEvaluationKey != k || e.Current != nil {
		return errors.New("missing atomic continuation execution")
	}
	return nil
}
func (s *Store) EnterEmbeddedSDKExecution(ctx context.Context, b EmbeddedBinding, source *EmbeddedGoalEvaluationKey) (int64, error) {
	var token int64
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		if source == nil {
			var err error
			token, err = s.newEmbeddedExecutionTx(ctx, b, "sdk", nil)
			return err
		}
		e, _, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		if e.Kind != "sdk" || e.Owner != b.OwnerToken || e.Source == nil || e.Source.EmbeddedGoalEvaluationKey != *source || e.Current != nil {
			return errors.New("no exact continuation execution")
		}
		token = e.Token
		return nil
	})
	return token, err
}
func (s *Store) SetEmbeddedSDKExecutionUser(ctx context.Context, b EmbeddedBinding, token, userSeq int64, userDigest string) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		e, _, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		if e.Kind != "sdk" || e.Owner != b.OwnerToken || e.Token != token {
			return errors.New("stale execution token")
		}
		// Revalidate every provider/tool admission without rebinding the frozen
		// revision or reapplying age admission to an actually running user.
		if e.Current != nil && e.Current.UserSeq == userSeq {
			if e.Current.UserDigest != userDigest {
				return errors.New("execution current digest mismatch")
			}
			return nil
		}
		var revision int64
		if err = s.db.QueryRowContext(ctx, `SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&revision); err != nil {
			return err
		}
		k := EmbeddedGoalEvaluationKey{UserSeq: userSeq, UserDigest: userDigest, GoalRevision: revision}
		if e.Current != nil {
			if userSeq <= e.Current.UserSeq {
				return errors.New("execution current user must advance")
			}
			var head int64
			if err = s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&head); err != nil {
				return err
			}
			if head != userSeq {
				return errors.New("execution current user must be transcript head")
			}
		}
		if err = s.embeddedExecutionAdmissionTx(ctx, b, k); err != nil {
			return err
		}
		e.Current, err = s.executionClosureTx(ctx, b, k)
		if err != nil {
			return err
		}
		return s.saveEmbeddedExecutionTx(ctx, b, e)
	})
}
func (s *Store) ClearEmbeddedExecution(ctx context.Context, b EmbeddedBinding, token int64) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET execution_json=NULL WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND owner_token=? AND execution_token=? AND json_extract(execution_json,'$.owner')=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken, token, b.OwnerToken)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			err = errors.New("stale execution clear")
		}
		return err
	})
}

// Refresh only before admission closes. At/after cutoff each exact call is
// frozen, so old-turn calls cannot churn the protected historical evidence.
func (s *Store) refreshEmbeddedExecutionCallsTx(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,session_id,epoch,generation,owner_token,execution_json FROM embedded_sessions WHERE execution_json IS NOT NULL AND current=1 AND owner_token=json_extract(execution_json,'$.owner')`)
	if err != nil {
		return err
	}
	type item struct {
		b EmbeddedBinding
		e embeddedExecution
	}
	var items []item
	for rows.Next() {
		var i item
		var raw string
		if err = rows.Scan(&i.b.AgentID, &i.b.SessionID, &i.b.Epoch, &i.b.Generation, &i.b.OwnerToken, &raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &i.e); err != nil {
			break
		}
		items = append(items, i)
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, i := range items {
		for _, c := range []*embeddedExecutionClosure{i.e.Source, i.e.Current} {
			if c == nil {
				continue
			}
			var admitted, cutoff int64
			if err = s.db.QueryRowContext(ctx, `SELECT e.retention_admitted_at_ms,s.evidence_expired_through_ms FROM embedded_entries e JOIN embedded_sessions s USING(agent_id,session_id,epoch,generation) WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=?`, i.b.AgentID, i.b.SessionID, i.b.Epoch, i.b.Generation, c.UserSeq).Scan(&admitted, &cutoff); err != nil {
				return err
			}
			if cutoff >= admitted {
				continue
			}
			fresh, err := s.executionClosureTx(ctx, i.b, c.EmbeddedGoalEvaluationKey)
			if err != nil {
				return err
			}
			c.CallID = fresh.CallID
		}
		if err = s.saveEmbeddedExecutionTx(ctx, i.b, i.e); err != nil {
			return err
		}
	}
	return nil
}

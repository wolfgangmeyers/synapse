package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/sky-valley/pi/ai"
)

// EmbeddedGoalEvaluationKey is the durable turn/revision identity. Generation
// and owner token are execution fences, not a way to evaluate the same turn twice.
type EmbeddedGoalEvaluationKey struct {
	UserSeq      int64
	UserDigest   string
	GoalRevision int64
	FinalSeq     int64
	FinalDigest  string
}

type EmbeddedGoalEvaluation struct {
	EmbeddedGoalEvaluationKey
	AgentID, SessionID, Generation        string
	Epoch, OwnerToken                     int64
	State, Reason, ClaimedAt, CompletedAt string
}

const (
	EmbeddedGoalEvaluationUnknown = "UNKNOWN"
	EmbeddedGoalEvaluationDone    = "DONE"
	EmbeddedGoalEvaluationNotDone = "NOT_DONE"
)

func validEmbeddedGoalEvaluationKey(k EmbeddedGoalEvaluationKey) bool {
	return k.UserSeq > 0 && k.FinalSeq > k.UserSeq && k.GoalRevision > 0 && k.UserDigest != "" && k.FinalDigest != ""
}

func (s *Store) embeddedGoalEvaluationGate(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey, requireHead bool) error {
	if err := s.embeddedFence(ctx, b); err != nil {
		return err
	}
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
		return err
	}
	if !validEmbeddedRole(role) {
		return errors.New("invalid saved embedded role")
	}
	// The saved role is authority; a parentless HTTP worker is also allowed.
	var desired, applied int64
	var enabled int
	var projection string
	if err := s.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,goal_enabled,projection_state FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&desired, &applied, &enabled, &projection); err != nil {
		return err
	}
	if enabled != 1 || desired != k.GoalRevision || applied != desired || projection != "applied" {
		return errors.New("embedded goal not currently applied and enabled")
	}
	var receiptDigest string
	var receiptEnabled int
	if err := s.db.QueryRowContext(ctx, `SELECT turn_entry_digest,goal_enabled FROM embedded_goal_applied_receipts WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND turn_entry_seq=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.GoalRevision).Scan(&receiptDigest, &receiptEnabled); err != nil {
		return err
	}
	if receiptDigest != k.UserDigest || receiptEnabled != 1 {
		return errors.New("embedded goal receipt mismatch")
	}
	var user, final EmbeddedEntry
	user.Sequence, final.Sequence = k.UserSeq, k.FinalSeq
	if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq).Scan(&user.Kind, &user.Body, &user.ParentDigest, &user.Digest); err != nil {
		return err
	}
	if user.Kind != "user" || user.Digest != k.UserDigest || embeddedBody(user.Body) != nil || embeddedEntryDigest(b, user) != user.Digest {
		return errors.New("embedded evaluation user mismatch")
	}
	// Require the checked final assistant to be the current head. An intervening
	// user starts a new turn even if an older final happens to be present.
	query := `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`
	args := []any{b.AgentID, b.SessionID, b.Epoch, b.Generation, k.FinalSeq}
	if requireHead {
		query += ` AND seq=(SELECT MAX(seq) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=?)`
		args = append(args, b.AgentID, b.SessionID, b.Epoch)
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&final.Kind, &final.Body, &final.ParentDigest, &final.Digest); err != nil {
		return err
	}
	if final.Kind != "assistant" || final.Digest != k.FinalDigest || embeddedBody(final.Body) != nil || embeddedEntryDigest(b, final) != final.Digest {
		return errors.New("embedded evaluation final mismatch")
	}
	message, err := ai.UnmarshalMessage(final.Body)
	if err != nil {
		return err
	}
	finalStop := false
	switch assistant := message.(type) {
	case ai.AssistantMessage:
		finalStop = assistant.StopReason == ai.StopStop
	case *ai.AssistantMessage:
		finalStop = assistant.StopReason == ai.StopStop
	}
	if !finalStop {
		return errors.New("embedded evaluation requires settled final assistant")
	}
	var intervening int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq>? AND seq<? AND kind='user'`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.FinalSeq).Scan(&intervening); err != nil {
		return err
	}
	if intervening != 0 {
		return errors.New("embedded evaluation turn superseded")
	}
	return nil
}

func (s *Store) embeddedEvaluationRow(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedGoalEvaluation, error) {
	var e EmbeddedGoalEvaluation
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,reason,claimed_at,COALESCE(completed_at,'') FROM embedded_goal_evaluations WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision).Scan(&e.AgentID, &e.SessionID, &e.Epoch, &e.Generation, &e.UserSeq, &e.UserDigest, &e.GoalRevision, &e.OwnerToken, &e.FinalSeq, &e.FinalDigest, &e.State, &e.Reason, &e.ClaimedAt, &e.CompletedAt)
	return e, err
}

// ClaimEmbeddedGoalEvaluation must precede invocation. false means the claim
// already exists: UNKNOWN is interruption evidence, never retry permission.
func (s *Store) ClaimEmbeddedGoalEvaluation(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey) (EmbeddedGoalEvaluation, bool, error) {
	if ctx == nil || !validEmbeddedGoalEvaluationKey(k) {
		return EmbeddedGoalEvaluation{}, false, errors.New("invalid embedded evaluation key")
	}
	var e EmbeddedGoalEvaluation
	claimed := false
	err := s.runImmediate(ctx, func() error {
		prior, err := s.embeddedEvaluationRow(ctx, b, k)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if gateErr := s.embeddedGoalEvaluationGate(ctx, b, k, errors.Is(err, sql.ErrNoRows)); gateErr != nil {
			return gateErr
		}
		if err == nil {
			if prior.Generation != b.Generation || prior.FinalSeq != k.FinalSeq || prior.FinalDigest != k.FinalDigest {
				return errors.New("conflicting embedded evaluation")
			}
			e = prior
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		e = EmbeddedGoalEvaluation{EmbeddedGoalEvaluationKey: k, AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, OwnerToken: b.OwnerToken, State: EmbeddedGoalEvaluationUnknown, ClaimedAt: formatTimestamp(s.clock.Now())}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at) VALUES(?,?,?,?,?,?,?,?,?,?,'UNKNOWN',?)`, e.AgentID, e.SessionID, e.Epoch, e.Generation, e.UserSeq, e.UserDigest, e.GoalRevision, e.OwnerToken, e.FinalSeq, e.FinalDigest, e.ClaimedAt)
		claimed = err == nil
		return err
	})
	if err != nil {
		return EmbeddedGoalEvaluation{}, false, err
	}
	return e, claimed, nil
}

// CompleteEmbeddedGoalEvaluation CASes the original owner claim. NOT_DONE
// reserves one non-mail continuation in the same transaction. It never
// mutates the managed goal projection or the mail reply outbox.
func (s *Store) CompleteEmbeddedGoalEvaluation(ctx context.Context, b EmbeddedBinding, k EmbeddedGoalEvaluationKey, outcome, reason string) (EmbeddedGoalEvaluation, bool, error) {
	if ctx == nil || !validEmbeddedGoalEvaluationKey(k) || (outcome != EmbeddedGoalEvaluationDone && outcome != EmbeddedGoalEvaluationNotDone) || len(reason) > 512 || strings.ContainsRune(reason, 0) {
		return EmbeddedGoalEvaluation{}, false, errors.New("invalid embedded evaluation verdict")
	}
	var e EmbeddedGoalEvaluation
	changed := false
	err := s.runImmediate(ctx, func() error {
		prior, err := s.embeddedEvaluationRow(ctx, b, k)
		if err != nil {
			return err
		}
		if err := s.embeddedGoalEvaluationGate(ctx, b, k, false); err != nil {
			return err
		}
		if prior.Generation != b.Generation || prior.FinalSeq != k.FinalSeq || prior.FinalDigest != k.FinalDigest {
			return errors.New("conflicting embedded evaluation")
		}
		if prior.State != EmbeddedGoalEvaluationUnknown {
			if prior.State == EmbeddedGoalEvaluationNotDone {
				if err := s.reserveEmbeddedGoalContinuation(ctx, prior); err != nil {
					return err
				}
			}
			e = prior
			return nil
		}
		if prior.OwnerToken != b.OwnerToken {
			return errors.New("stale embedded evaluation claim owner")
		}
		result, err := s.db.ExecContext(ctx, `UPDATE embedded_goal_evaluations SET state=?,reason=?,completed_at=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND goal_revision=? AND owner_token=? AND final_seq=? AND final_digest=? AND state='UNKNOWN'`, outcome, reason, formatTimestamp(s.clock.Now()), b.AgentID, b.SessionID, b.Epoch, b.Generation, k.UserSeq, k.UserDigest, k.GoalRevision, b.OwnerToken, k.FinalSeq, k.FinalDigest)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale embedded evaluation claim")
		}
		e, err = s.embeddedEvaluationRow(ctx, b, k)
		if err != nil {
			return err
		}
		if outcome == EmbeddedGoalEvaluationNotDone {
			if err := s.reserveEmbeddedGoalContinuation(ctx, e); err != nil {
				return err
			}
		}
		changed = true
		return nil
	})
	if err != nil {
		return EmbeddedGoalEvaluation{}, false, err
	}
	return e, changed, nil
}

type EmbeddedGoalEvaluationSummary struct {
	Verdict      string `json:"verdict"`
	Reason       string `json:"reason,omitempty"`
	CompletedAt  string `json:"completed_at"`
	GoalRevision int64  `json:"goal_revision"`
}

func (s *Store) EmbeddedGoalDiagnostics(ctx context.Context, agentID string) (map[string]int64, *EmbeddedGoalEvaluationSummary, error) {
	counts := map[string]int64{"COMPLETED": 0, "NOT_DONE": 0}
	var doneCount, notDoneCount int64
	row := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN state='DONE' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN state='NOT_DONE' THEN 1 ELSE 0 END), 0)
		FROM embedded_goal_evaluations WHERE agent_id=? AND completed_at IS NOT NULL`, agentID)
	if err := row.Scan(&doneCount, &notDoneCount); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return counts, nil, err
	}
	counts["COMPLETED"] = doneCount
	counts["NOT_DONE"] = notDoneCount

	var last EmbeddedGoalEvaluationSummary
	lastRow := s.db.QueryRowContext(ctx, `SELECT state, reason, completed_at, goal_revision
		FROM embedded_goal_evaluations
		WHERE agent_id=? AND completed_at IS NOT NULL
		ORDER BY completed_at DESC, rowid DESC LIMIT 1`, agentID)
	if err := lastRow.Scan(&last.Verdict, &last.Reason, &last.CompletedAt, &last.GoalRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return counts, nil, nil
		}
		return counts, nil, err
	}
	return counts, &last, nil
}


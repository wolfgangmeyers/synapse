package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var ErrClaudeRetryNotDue = errors.New("Claude retry not eligible")

// ErrClaudeRetryInFlight means settlement cannot safely determine the execution outcome.
var ErrClaudeRetryInFlight = errors.New("Claude retry execution unresolved")

// ErrClaudeRetryAbsent means the exact fenced reservation has no retry state.
var ErrClaudeRetryAbsent = errors.New("Claude retry state absent")

// ClaudeStartAdmissionCommittedError means this call durably admitted exactly
// the returned execution, but post-commit housekeeping failed. The serialized
// caller may proceed once, while reporting the underlying error; it must not
// re-admit the execution.
type ClaudeStartAdmissionCommittedError struct{ Err error }

func (e *ClaudeStartAdmissionCommittedError) Error() string   { return e.Err.Error() }
func (e *ClaudeStartAdmissionCommittedError) Unwrap() error   { return e.Err }
func (e *ClaudeStartAdmissionCommittedError) Committed() bool { return true }

// ClaudeStartAdmissionUnknownError does not authorize execution or replay.
// The durable admission may have committed; retain the fence for recovery.
type ClaudeStartAdmissionUnknownError struct{ Err error }

func (e *ClaudeStartAdmissionUnknownError) Error() string        { return e.Err.Error() }
func (e *ClaudeStartAdmissionUnknownError) Unwrap() error        { return e.Err }
func (e *ClaudeStartAdmissionUnknownError) OutcomeUnknown() bool { return true }

func claudeAdmissionOutcome(committed, conclusive bool, err error) error {
	if err == nil {
		return nil
	}
	if committed {
		return &ClaudeStartAdmissionCommittedError{Err: err}
	}
	if !conclusive {
		return &ClaudeStartAdmissionUnknownError{Err: err}
	}
	return err
}

type ClaudeStartRetryState struct {
	Deadline, NextEligible time.Time
	Executions             int
	Category               string
	InFlight               bool
	PromptSettlement       bool
}

func claudeRetryCategory(c string) string {
	switch c {
	case "auth", "spawn", "probe-died", "pool-fenced", "grant-failed":
		return c
	default:
		return "unknown"
	}
}

func (s *Store) claudeRetryFence(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt) error {
	if expected == nil || expected.AttemptID == "" {
		return ErrStaleEmbeddedFence
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleEmbeddedFence
		}
		return err
	}
	a, err := s.embeddedMailAttemptTx(ctx, expected.MessageID)
	if err != nil {
		return err
	}
	if a == nil || a.AttemptID != expected.AttemptID || a.MessageIncarnationID != expected.MessageIncarnationID || a.SnapshotID != expected.SnapshotID || a.AcceptedDigest != expected.AcceptedDigest || a.Binding.AgentID != b.AgentID || a.Binding.SessionID != b.SessionID || a.Binding.Epoch != b.Epoch || a.Binding.Generation != b.Generation {
		return ErrStaleEmbeddedFence
	}
	if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStaleEmbeddedFence
		}
		return err
	}
	var held, consumed bool
	err = s.db.QueryRowContext(ctx, `SELECT claude_stdin_started, EXISTS(SELECT 1 FROM embedded_acks WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM embedded_append_keys WHERE append_key=?) FROM messages WHERE id=? AND message_incarnation_id=?`, a.AttemptID, "embedded-mail:"+a.AttemptID, a.MessageID, a.MessageIncarnationID).Scan(&held, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStaleEmbeddedFence
	}
	if err != nil {
		return err
	}
	if held || consumed {
		return ErrStaleEmbeddedFence
	}
	return nil
}

func (s *Store) claudeRetryRead(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt) (ClaudeStartRetryState, error) {
	var r ClaudeStartRetryState
	var deadline, next string
	err := s.db.QueryRowContext(ctx, `SELECT deadline,next_eligible,executions,category,in_flight,prompt_settlement FROM claude_start_retries WHERE message_id=? AND incarnation_id=? AND recipient=?`, a.MessageID, a.MessageIncarnationID, b.AgentID).Scan(&deadline, &next, &r.Executions, &r.Category, &r.InFlight, &r.PromptSettlement)
	if err == nil {
		r.Deadline = parseTimestamp(deadline)
		r.NextEligible = parseTimestamp(next)
		if r.Deadline.IsZero() || r.NextEligible.IsZero() || r.Executions < 1 || r.Executions > 5 {
			return r, errors.New("invalid Claude retry state")
		}
	}
	return r, err
}

// ClaudeStartRetry reads waiting state only after checking all safety fences.
func (s *Store) ClaudeStartRetry(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt) (r ClaudeStartRetryState, err error) {
	_, _, err = s.runImmediateWithCommitOutcome(ctx, func() error {
		if e := s.claudeRetryFence(ctx, b, a); e != nil {
			return e
		}
		var e error
		r, e = s.claudeRetryRead(ctx, b, a)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrClaudeRetryAbsent
		}
		return e
	})
	return
}

// AdmitClaudeStartInitial reserves the first execution before any process work.
// Its placeholder times are replaced when the first definite failure is saved.
func (s *Store) AdmitClaudeStartInitial(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt) (r ClaudeStartRetryState, err error) {
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if e := s.claudeRetryFence(ctx, b, a); e != nil {
			return e
		}
		now := s.clock.Now()
		r = ClaudeStartRetryState{Deadline: now, NextEligible: now, Executions: 1, Category: "initial", InFlight: true}
		_, e := s.db.ExecContext(ctx, `INSERT INTO claude_start_retries(message_id,incarnation_id,recipient,deadline,next_eligible,executions,category,in_flight,accepted_snapshot_id,accepted_digest,accepted_session_id,accepted_epoch,accepted_generation,accepted_attempt_id,accepted_owner_token) VALUES(?,?,?,?,?,1,'initial',1,?,?,?,?,?,?,?)`, a.MessageID, a.MessageIncarnationID, b.AgentID, formatTimestamp(now), formatTimestamp(now), a.SnapshotID, a.AcceptedDigest, b.SessionID, b.Epoch, b.Generation, a.AttemptID, b.OwnerToken)
		return e
	})
	return r, claudeAdmissionOutcome(committed, conclusive, err)
}

// RecordClaudeStartFailure requires a confirmed definite failure and completed cleanup.
func (s *Store) RecordClaudeStartFailure(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt, category string, prompt ...bool) (r ClaudeStartRetryState, err error) {
	return s.RecordClaudeStartFailureAt(ctx, b, a, category, s.clock.Now(), 0, len(prompt) > 0 && prompt[0])
}

// RecordClaudeStartFailureAt reconciles an ambiguous commit only for the same execution.
func (s *Store) RecordClaudeStartFailureAt(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt, category string, failedAt time.Time, execution int, prompt bool) (r ClaudeStartRetryState, err error) {
	_, _, err = s.runImmediateWithCommitOutcome(ctx, func() error {
		if e := s.claudeRetryFence(ctx, b, a); e != nil {
			return e
		}
		now := failedAt
		var e error
		r, e = s.claudeRetryRead(ctx, b, a)
		if e != nil {
			return e
		} else if execution != 0 && r.Executions != execution {
			return ErrStaleEmbeddedFence
		} else if !r.InFlight && execution != 0 && r.Category == claudeRetryCategory(category) && r.PromptSettlement == (prompt && (r.Category == "auth" || r.Category == "spawn")) {
			return nil
		} else if !r.InFlight {
			return fmt.Errorf("%w: failure already recorded", ErrClaudeRetryNotDue)
		}
		if r.Executions == 1 && r.Category == "initial" {
			r.Deadline = now.Add(6*time.Minute + 35*time.Second)
		}
		delays := []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 3 * time.Minute}
		r.NextEligible = r.Deadline
		if r.Executions < 5 {
			r.NextEligible = now.Add(delays[r.Executions-1])
			if r.NextEligible.After(r.Deadline) {
				r.NextEligible = r.Deadline
			}
		}
		r.Category = claudeRetryCategory(category)
		r.PromptSettlement = prompt && (r.Category == "auth" || r.Category == "spawn")
		r.InFlight = false
		_, e = s.db.ExecContext(ctx, `UPDATE claude_start_retries SET deadline=?,next_eligible=?,category=?,in_flight=0,prompt_settlement=? WHERE message_id=? AND incarnation_id=? AND recipient=?`, formatTimestamp(r.Deadline), formatTimestamp(r.NextEligible), r.Category, r.PromptSettlement, a.MessageID, a.MessageIncarnationID, b.AgentID)
		return e
	})
	return
}

// AdmitClaudeStartRetry atomically records execution before permitting a retry.
// The caller timestamp is advisory only; eligibility uses the store clock after locking.
func (s *Store) AdmitClaudeStartRetry(ctx context.Context, b EmbeddedBinding, a *EmbeddedMailAttempt, _ time.Time) (r ClaudeStartRetryState, err error) {
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if e := s.claudeRetryFence(ctx, b, a); e != nil {
			return e
		}
		var e error
		r, e = s.claudeRetryRead(ctx, b, a)
		if e != nil {
			return e
		}
		now := s.clock.Now()
		if r.InFlight || r.PromptSettlement || r.Executions >= 5 || now.Before(r.NextEligible) || !now.Before(r.Deadline) {
			return ErrClaudeRetryNotDue
		}
		_, e = s.db.ExecContext(ctx, `UPDATE claude_start_retries SET executions=executions+1,in_flight=1,rejected_release_safe=0,accepted_snapshot_id=?,accepted_digest=?,accepted_session_id=?,accepted_epoch=?,accepted_generation=?,accepted_attempt_id=?,accepted_owner_token=? WHERE message_id=? AND incarnation_id=? AND recipient=?`, a.SnapshotID, a.AcceptedDigest, b.SessionID, b.Epoch, b.Generation, a.AttemptID, b.OwnerToken, a.MessageID, a.MessageIncarnationID, b.AgentID)
		if e == nil {
			r.Executions++
			r.InFlight = true
		}
		return e
	})
	return r, claudeAdmissionOutcome(committed, conclusive, err)
}

// A crashed admitted retry is not evidence of an unsubmitted execution.
func (s *Store) claudeRetryRebindSafe(ctx context.Context, messageID, incarnation, recipient string) error {
	var inFlight bool
	err := s.db.QueryRowContext(ctx, `SELECT in_flight FROM claude_start_retries WHERE message_id=? AND incarnation_id=? AND recipient=?`, messageID, incarnation, recipient).Scan(&inFlight)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if inFlight {
		return ErrClaudeRetryNotDue
	}
	return nil
}

// ClaudeRejectedReleaseCommittedError reports a release that committed even though
// the subsequent state-file permission check failed. The cause is not suppressed.
type ClaudeRejectedReleaseCommittedError struct{ Err error }

func (e *ClaudeRejectedReleaseCommittedError) Error() string   { return e.Err.Error() }
func (e *ClaudeRejectedReleaseCommittedError) Unwrap() error   { return e.Err }
func (e *ClaudeRejectedReleaseCommittedError) Committed() bool { return true }

// ClaudeRejectedReleaseUnknownError retains the cause without implying that the
// transaction rolled back. The lane must remain fenced until recovery.
type ClaudeRejectedReleaseUnknownError struct{ Err error }

func (e *ClaudeRejectedReleaseUnknownError) Error() string        { return e.Err.Error() }
func (e *ClaudeRejectedReleaseUnknownError) Unwrap() error        { return e.Err }
func (e *ClaudeRejectedReleaseUnknownError) OutcomeUnknown() bool { return true }

// ReleaseClaudeRejectedInput clears a definite pre-submission admission only after cleanup.
func (s *Store) ReleaseClaudeRejectedInput(ctx context.Context, b EmbeddedBinding, attempt *EmbeddedMailAttempt) error {
	if attempt == nil {
		return ErrStaleEmbeddedFence
	}
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if err := s.claudeRetryFence(ctx, b, attempt); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM claude_start_retries WHERE executions=1 AND category='initial' AND in_flight=1 AND EXISTS (
              SELECT 1 FROM embedded_attempts e JOIN messages m ON m.id=e.message_id
              WHERE e.attempt_id=? AND e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=?
              AND claude_start_retries.message_id=e.message_id AND claude_start_retries.incarnation_id=m.message_incarnation_id AND claude_start_retries.recipient=e.agent_id
              AND m.claude_stdin_started=0 AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.attempt_id=e.attempt_id)
              AND NOT EXISTS (SELECT 1 FROM embedded_append_keys k WHERE k.append_key=e.append_key))`, attempt.AttemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		// A rejected retry must wait for settlement, not re-execute on another offer.
		res, err = s.db.ExecContext(ctx, `UPDATE claude_start_retries SET in_flight=0, executions=executions-1, next_eligible=deadline, rejected_release_safe=1 WHERE in_flight=1 AND executions>1 AND EXISTS (
                SELECT 1 FROM embedded_attempts e JOIN messages m ON m.id=e.message_id
                WHERE e.attempt_id=? AND e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=?
                AND claude_start_retries.message_id=e.message_id AND claude_start_retries.incarnation_id=m.message_incarnation_id AND claude_start_retries.recipient=e.agent_id
                AND m.claude_stdin_started=0 AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.attempt_id=e.attempt_id)
                AND NOT EXISTS (SELECT 1 FROM embedded_append_keys k WHERE k.append_key=e.append_key))`, attempt.AttemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if deleted+updated != 1 {
			return ErrStaleEmbeddedFence
		}
		res, err = s.db.ExecContext(ctx, `DELETE FROM embedded_attempts
 WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?
 AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.attempt_id=embedded_attempts.attempt_id)
 AND NOT EXISTS (SELECT 1 FROM embedded_append_keys k JOIN embedded_entries e
 ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq
 WHERE k.append_key=embedded_attempts.append_key AND e.kind='user'
 AND k.agent_id=embedded_attempts.agent_id AND k.session_id=embedded_attempts.session_id
 AND k.epoch=embedded_attempts.epoch AND k.generation=embedded_attempts.generation)`, attempt.AttemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStaleEmbeddedFence
		}
		return nil
	})
	if !conclusive {
		return &ClaudeRejectedReleaseUnknownError{Err: err}
	}
	if committed && err != nil {
		return &ClaudeRejectedReleaseCommittedError{Err: err}
	}
	return err
}

// ClaudeStartupGuarded checks if there is an active startup guard for the agent.
func (s *Store) ClaudeStartupGuarded(ctx context.Context, agentID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM claude_startup_guards WHERE agent_id=?`, agentID).Scan(&count)
	return count > 0, err
}

// AdmitClaudeStartupGuard sets an exact startup guard for a starting Claude root.
func (s *Store) AdmitClaudeStartupGuard(ctx context.Context, b EmbeddedBinding, operationID string) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO claude_startup_guards(agent_id, session_id, epoch, generation, owner_token, operation_id) VALUES(?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken, operationID)
		return err
	})
}

// ReleaseClaudeStartupGuard removes the exact startup guard when startup resolves.
func (s *Store) ReleaseClaudeStartupGuard(ctx context.Context, b EmbeddedBinding, operationID string) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `DELETE FROM claude_startup_guards WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND owner_token=? AND operation_id=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, b.OwnerToken, operationID)
		return err
	})
}

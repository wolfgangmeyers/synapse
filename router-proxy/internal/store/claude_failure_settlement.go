package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SettleClaudeUnsubmittedMail records only a fixed diagnostic, never provider output.
// The caller must have joined process and persistence cleanup before calling.
func (s *Store) SettleClaudeUnsubmittedMail(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt, categories ...string) (ManagedHistoryEntry, error) {
	return s.settleClaudeUnsubmittedMail(ctx, b, expected, false, categories...)
}

// SettleClaudeStartRetry permits prompt settlement only for a confirmed permanent
// preflight failure; callers must establish that fact and complete cleanup.
func (s *Store) SettleClaudeStartRetry(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt, prompt bool, categories ...string) (ManagedHistoryEntry, error) {
	return s.settleClaudeUnsubmittedMail(ctx, b, expected, prompt, categories...)
}

func (s *Store) settleClaudeUnsubmittedMail(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt, prompt bool, categories ...string) (ManagedHistoryEntry, error) {
	var entry ManagedHistoryEntry
	if expected == nil || expected.AttemptID == "" {
		return entry, errors.New("missing attempt")
	}
	category := "unknown"
	if len(categories) > 0 {
		category = categories[0]
	}
	text := claudeStartHistoryText(category)
	presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "error", Summary: "Claude could not start"}
	encoded, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return entry, err
	}
	committed, _, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, expected.MessageID)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != expected.AttemptID || a.MessageIncarnationID != expected.MessageIncarnationID || a.SnapshotID != expected.SnapshotID || a.AcceptedDigest != expected.AcceptedDigest || a.Binding.AgentID != b.AgentID || a.Binding.SessionID != b.SessionID || a.Binding.Epoch != b.Epoch || a.Binding.Generation != b.Generation {
			return fmt.Errorf("%w: stale Claude mail attempt", ErrStaleEmbeddedFence)
		}
		state, err := s.embeddedMailAcceptedTx(ctx, a, false)
		if err != nil {
			return err
		}
		if state != "queued" && state != "dead_letter" {
			return fmt.Errorf("%w: Claude mail no longer unsettled", ErrStaleEmbeddedFence)
		}
		var held, consumed bool
		err = s.db.QueryRowContext(ctx, `SELECT claude_stdin_started, EXISTS(SELECT 1 FROM embedded_acks WHERE attempt_id=?) OR EXISTS(SELECT 1 FROM embedded_append_keys WHERE append_key=?) FROM messages WHERE id=? AND message_incarnation_id=?`, a.AttemptID, "embedded-mail:"+a.AttemptID, a.MessageID, a.MessageIncarnationID).Scan(&held, &consumed)
		if err != nil {
			return err
		}
		if held || consumed {
			return fmt.Errorf("%w: Claude mail may have been submitted", ErrStaleEmbeddedFence)
		}
		if state == "queued" {
			if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
				return err
			}
		}
		// Read and gate retry state in the same write transaction as settlement.
		// The durable category wins over caller input on recovery.
		retry, retryErr := s.claudeRetryRead(ctx, b, a)
		if retryErr != nil && !errors.Is(retryErr, sql.ErrNoRows) {
			return retryErr
		}
		if retryErr == nil {
			if retry.InFlight {
				return ErrClaudeRetryInFlight
			}
			if !retry.PromptSettlement && s.clock.Now().Before(retry.Deadline) {
				return ErrClaudeRetryNotDue
			}
			text = claudeStartHistoryText(retry.Category)
		}
		key := fmt.Sprintf("claude-unsubmitted:%s", embeddedHash([]byte(a.MessageID), []byte(a.MessageIncarnationID)))
		entry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, "managed_message", "error", text, key, encoded, presentation, managedHistoryDetails{MessageContent: &text})
		if err != nil {
			return err
		}
		if state == "dead_letter" {
			return nil
		}
		res, err := s.db.ExecContext(ctx, `UPDATE messages SET state='dead_letter' WHERE id=? AND message_incarnation_id=? AND recipient=? AND state='queued' AND claude_stdin_started=0`, a.MessageID, a.MessageIncarnationID, b.AgentID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: Claude mail settlement lost race", ErrStaleEmbeddedFence)
		}
		return s.rearmFailedReminder(ctx, a.MessageID, formatTimestamp(s.clock.Now()))
	})
	if !committed {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedHistoryEntry{}, fmt.Errorf("%w: Claude accepted mail or target missing", ErrStaleEmbeddedFence)
		}
		return ManagedHistoryEntry{}, err
	}
	return entry, err
}

// HoldClaudeUncertainMail records a replay fence and diagnostic atomically.
func (s *Store) HoldClaudeUncertainMail(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt) (ManagedHistoryEntry, error) {
	var entry ManagedHistoryEntry
	if expected == nil || expected.AttemptID == "" {
		return entry, errors.New("missing attempt")
	}
	text := "Claude mail outcome is uncertain. The message is held to prevent replay until it expires; inspect the conversation before retrying."
	presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "error", Summary: "Claude mail outcome uncertain"}
	encoded, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return entry, err
	}
	committed, _, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, expected.MessageID)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != expected.AttemptID || a.MessageIncarnationID != expected.MessageIncarnationID || a.SnapshotID != expected.SnapshotID || a.AcceptedDigest != expected.AcceptedDigest || a.Binding.AgentID != b.AgentID || a.Binding.SessionID != b.SessionID || a.Binding.Epoch != b.Epoch || a.Binding.Generation != b.Generation {
			return fmt.Errorf("%w: stale Claude mail attempt", ErrStaleEmbeddedFence)
		}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=1 WHERE id=? AND message_incarnation_id=? AND state='queued'`, a.MessageID, a.MessageIncarnationID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%w: mail changed", ErrStaleEmbeddedFence)
		}
		key := fmt.Sprintf("claude-uncertain:%s", embeddedHash([]byte(a.MessageID), []byte(a.MessageIncarnationID)))
		entry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, "managed_message", "error", text, key, encoded, presentation, managedHistoryDetails{MessageContent: &text})
		return err
	})
	if !committed {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedHistoryEntry{}, fmt.Errorf("%w: Claude accepted mail or target missing", ErrStaleEmbeddedFence)
		}
		return ManagedHistoryEntry{}, err
	}
	return entry, err
}

// RecordClaudeUnclassifiedMail writes a fenced diagnostic without changing mail state.
func (s *Store) RecordClaudeUnclassifiedMail(ctx context.Context, b EmbeddedBinding, expected *EmbeddedMailAttempt, text string) (ManagedHistoryEntry, error) {
	var entry ManagedHistoryEntry
	if expected == nil {
		return entry, errors.New("missing attempt")
	}
	presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "error", Summary: "Claude mail blocked"}
	encoded, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return entry, err
	}
	committed, _, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, expected.MessageID)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != expected.AttemptID || a.MessageIncarnationID != expected.MessageIncarnationID || a.Binding.AgentID != b.AgentID || a.Binding.SessionID != b.SessionID || a.Binding.Epoch != b.Epoch || a.Binding.Generation != b.Generation {
			return ErrStaleEmbeddedFence
		}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		key := "claude-unclassified:" + embeddedHash([]byte(a.MessageID), []byte(a.MessageIncarnationID))
		entry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, "managed_message", "error", text, key, encoded, presentation, managedHistoryDetails{MessageContent: &text})
		return err
	})
	if !committed {
		return ManagedHistoryEntry{}, err
	}
	return entry, err
}

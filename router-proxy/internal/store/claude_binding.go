package store

import (
	"context"
	"errors"
)

// ClaudeBindingStarted survives process eviction; session retention owns the bit.
func (s *Store) ClaudeBindingStarted(ctx context.Context, b EmbeddedBinding) (bool, error) {
	var started bool
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		return s.db.QueryRowContext(ctx, `SELECT claude_started FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&started)
	})
	return started, err
}
func (s *Store) MarkClaudeBindingStarted(ctx context.Context, b EmbeddedBinding) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET claude_started=1 WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		return err
	})
}

// MarkClaudeMailWriteStarted fences the exact accepted reservation before stdin.
func (s *Store) MarkClaudeMailWriteStarted(ctx context.Context, b EmbeddedBinding, attemptID string) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var messageID string
		if err := s.db.QueryRowContext(ctx, `SELECT message_id FROM embedded_attempts WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&messageID); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, messageID)
		if err != nil {
			return err
		}
		if a == nil {
			return errors.New("missing mail attempt")
		}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=1 WHERE id=? AND message_incarnation_id=? AND state='queued' AND claude_stdin_started=0`, a.MessageID, a.MessageIncarnationID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("Claude stdin already started")
		}
		return nil
	})
}

// ResetClaudeMailWriteStarted resets claude_stdin_started to 0 and fences the exact accepted reservation.
func (s *Store) ResetClaudeMailWriteStarted(ctx context.Context, b EmbeddedBinding, attemptID string) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var messageID string
		if err := s.db.QueryRowContext(ctx, `SELECT message_id FROM embedded_attempts WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&messageID); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, messageID)
		if err != nil {
			return err
		}
		if a == nil {
			return errors.New("missing mail attempt")
		}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET claude_stdin_started=0 WHERE id=? AND message_incarnation_id=? AND state='queued' AND claude_stdin_started=1`, a.MessageID, a.MessageIncarnationID); err != nil {
			return err
		}
		return nil
	})
}

// ClaudeMailHeld reports uncertainty without changing mailbox state.
func (s *Store) ClaudeMailHeld(ctx context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var held bool
	err := s.db.QueryRowContext(ctx, `SELECT claude_stdin_started AND state='queued' FROM messages WHERE id=?`, id).Scan(&held)
	return held, err
}

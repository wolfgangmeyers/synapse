package store

import (
	"context"
	"errors"
)

// ReleaseEmbeddedUnconsumedAttempt is called only after its submitter and
// persistence callbacks have settled. It cannot release a consumed attempt,
// another binding, or a replacement reservation of the same message.
func (s *Store) ReleaseEmbeddedUnconsumedAttempt(ctx context.Context, b EmbeddedBinding, attemptID string) error {
	if attemptID == "" {
		return errors.New("attempt ID required")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM embedded_attempts
 WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?
 AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.attempt_id=embedded_attempts.attempt_id)
 AND NOT EXISTS (SELECT 1 FROM embedded_append_keys k JOIN embedded_entries e
 ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq
 WHERE k.append_key=embedded_attempts.append_key AND e.kind='user'
 AND k.agent_id=embedded_attempts.agent_id AND k.session_id=embedded_attempts.session_id
 AND k.epoch=embedded_attempts.epoch AND k.generation=embedded_attempts.generation)`, attemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		return err
	})
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/pi/ai"
)

// EmbeddedInterruptedNotice is internal turn work, never a mailbox attempt.
type EmbeddedInterruptedNotice struct {
	ID             string
	SourceSequence int64
	State          string
	EntrySequence  int64
	Text           string
}

// EnterEmbeddedNonMailProviderTurn records the checked user that actually reached
// the provider boundary. A saved user alone is not evidence of provider entry.
func (s *Store) EnterEmbeddedNonMailProviderTurn(ctx context.Context, b EmbeddedBinding, source int64, digest string) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO embedded_nonmail_provider_entries(agent_id,session_id,epoch,generation,user_seq,user_digest)
			SELECT e.agent_id,e.session_id,e.epoch,e.generation,e.seq,e.digest FROM embedded_entries e
			WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=? AND e.digest=? AND e.kind='user'
			AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=e.agent_id AND k.session_id=e.session_id AND k.epoch=e.epoch AND k.entry_seq=e.seq)`,
			b.AgentID, b.SessionID, b.Epoch, b.Generation, source, digest)
		if err != nil {
			return err
		}
		changed, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			var saved string
			err = s.db.QueryRowContext(ctx, `SELECT user_digest FROM embedded_nonmail_provider_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source).Scan(&saved)
			if err != nil || saved != digest {
				return errors.New("non-mail provider entry requires checked user turn")
			}
		}
		return nil
	})
}

// EmbeddedUnenteredUser checks the saved SDK user at the durable head under the
// new owner's fence. Only a non-mail turn with no provider-entry record, ACK,
// or later SDK message can resume; entry is recorded before provider dispatch.
func (s *Store) EmbeddedUnenteredUser(ctx context.Context, b EmbeddedBinding, source int64, digest string) (bool, error) {
	var eligible bool
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var count int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries e
			WHERE e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.seq=? AND e.digest=? AND e.kind='user'
			AND NOT EXISTS (SELECT 1 FROM embedded_entries later WHERE later.agent_id=e.agent_id AND later.session_id=e.session_id AND later.epoch=e.epoch AND later.seq>e.seq)
			AND NOT EXISTS (SELECT 1 FROM embedded_nonmail_provider_entries p WHERE p.agent_id=e.agent_id AND p.session_id=e.session_id AND p.epoch=e.epoch AND p.user_seq=e.seq)
			AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=e.agent_id AND k.session_id=e.session_id AND k.epoch=e.epoch AND k.entry_seq=e.seq)`,
			b.AgentID, b.SessionID, b.Epoch, b.Generation, source, digest).Scan(&count)
		eligible = count == 1
		return err
	})
	return eligible, err
}

// PrepareEmbeddedInterruptedNotice admits an entered non-mail provider turn or
// an ACKed mail turn. The caller restored the checked SDK projection first.
func (s *Store) PrepareEmbeddedInterruptedNotice(ctx context.Context, b EmbeddedBinding, source int64) (*EmbeddedInterruptedNotice, error) {
	if source <= 0 {
		return nil, errors.New("invalid interrupted turn")
	}
	var out *EmbeddedInterruptedNotice
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var attempt string
		err := s.db.QueryRowContext(ctx, `SELECT attempt FROM (
			SELECT k.attempt_id AS attempt FROM embedded_acks k JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.generation=k.generation AND e.seq=k.entry_seq WHERE k.agent_id=? AND k.session_id=? AND k.epoch=? AND k.generation=? AND k.entry_seq=? AND e.kind='user'
			UNION ALL
			SELECT '' AS attempt FROM embedded_nonmail_provider_entries p JOIN embedded_entries e ON e.agent_id=p.agent_id AND e.session_id=p.session_id AND e.epoch=p.epoch AND e.generation=p.generation AND e.seq=p.user_seq AND e.digest=p.user_digest WHERE p.agent_id=? AND p.session_id=? AND p.epoch=? AND p.generation=? AND p.user_seq=? AND e.kind='user'
		) LIMIT 1`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source, b.AgentID, b.SessionID, b.Epoch, b.Generation, source).Scan(&attempt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var finished int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_reply_outbox WHERE attempt_id=?`, attempt).Scan(&finished); err != nil {
			return err
		}
		if finished != 0 {
			return nil
		}
		// A reminder and a non-mail turn commit finals without a sender outbox.
		// Only a checked, completed assistant after this user and before another
		// user turn proves completion; fragments and StopError remain uncertain.
		rows, err := s.db.QueryContext(ctx, `SELECT seq,parent_digest,body,digest FROM embedded_entries e WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq>? AND kind='assistant' AND NOT EXISTS (SELECT 1 FROM embedded_entries u WHERE u.agent_id=e.agent_id AND u.session_id=e.session_id AND u.epoch=e.epoch AND u.kind='user' AND u.seq>? AND u.seq<e.seq) ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source, source)
		if err != nil {
			return err
		}
		completed := false
		for rows.Next() {
			var entry EmbeddedEntry
			entry.Kind = "assistant"
			if err = rows.Scan(&entry.Sequence, &entry.ParentDigest, &entry.Body, &entry.Digest); err != nil {
				break
			}
			if embeddedEntryDigest(b, entry) != entry.Digest {
				err = errors.New("corrupt embedded assistant completion")
				break
			}
			var msg ai.Message
			msg, err = ai.UnmarshalMessage(entry.Body)
			if err != nil {
				// An unfinished assistant fragment is not proof of completion.
				err = nil
				continue
			}
			if assistant, ok := msg.(ai.AssistantMessage); ok && assistant.StopReason == ai.StopStop {
				completed = true
				break
			}
		}
		if err == nil && !completed {
			err = rows.Err()
		}
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if completed {
			return nil
		}
		id := fmt.Sprintf("interrupted:%s:%s:%d:%d", b.AgentID, b.SessionID, b.Epoch, source)
		if len(id) > 256 {
			return errors.New("interrupted notice identity too long")
		}
		var existing int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND source_seq=?`, b.AgentID, b.SessionID, b.Epoch, source).Scan(&existing); err != nil {
			return err
		}
		if existing == 0 {
			var newer int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq>? AND kind='user'`, b.AgentID, b.SessionID, b.Epoch, source).Scan(&newer); err != nil {
				return err
			}
			if newer != 0 {
				return nil
			}
		}
		var n EmbeddedInterruptedNotice
		var seq sql.NullInt64
		var gen string
		if existing != 0 {
			if err := s.db.QueryRowContext(ctx, `SELECT notice_id,generation,state,entry_seq,notice_text FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND source_seq=?`, b.AgentID, b.SessionID, b.Epoch, source).Scan(&n.ID, &gen, &n.State, &seq, &n.Text); err != nil {
				return err
			}
		} else {
			n.ID = id
			n.State = "pending"
			gen = b.Generation
		}
		if gen != b.Generation || n.ID != id {
			return errors.New("stale interrupted notice")
		}
		n.SourceSequence = source
		n.EntrySequence = seq.Int64
		if existing == 0 {
			address := attempt
			if address == "" {
				address = "non-mail"
			}
			n.Text = fmt.Sprintf("The addressed turn %s was interrupted. Its provider or tool outcome may be unknown. Do not replay the interrupted turn or any uncertain effect; continue only with new work.", address)
		}
		// Retain the unknown effect IDs as evidence, not as replay instructions.
		rows, err = s.db.QueryContext(ctx, `SELECT effect_id FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown' ORDER BY effect_id LIMIT 16`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err == nil {
			err = rows.Err()
		}
		closeErr = rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if len(ids) > 0 && existing == 0 {
			n.Text += " Unknown effect IDs: " + strings.Join(ids, ", ") + "."
		}
		if existing == 0 {
			_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_interrupted_notices(agent_id,session_id,epoch,generation,source_seq,notice_id,notice_text,state) VALUES(?,?,?,?,?,?,?,'pending')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, source, id, n.Text)
			if err != nil {
				return err
			}
		}
		out = &n
		return nil
	})
	return out, err
}

// AppendEmbeddedInterruptedNotice atomically binds the SDK user entry to its
// queued disposition. A crash before this transaction is provably unstarted.
func (s *Store) AppendEmbeddedInterruptedNotice(ctx context.Context, b EmbeddedBinding, n EmbeddedInterruptedNotice, head int64, digest string, body []byte) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var state, id string
		err := s.db.QueryRowContext(ctx, `SELECT state,notice_id FROM embedded_interrupted_notices WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND source_seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, n.SourceSequence).Scan(&state, &id)
		if err != nil {
			return err
		}
		if state != "pending" || id != n.ID {
			return errors.New("interrupted notice already appended or stale")
		}
		entry, err = s.appendEmbeddedTx(ctx, b, "user", body, head, digest)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE embedded_interrupted_notices SET state='appended',entry_seq=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND source_seq=? AND state='pending'`, entry.Sequence, b.AgentID, b.SessionID, b.Epoch, b.Generation, n.SourceSequence)
		return err
	})
	return entry, err
}

// StartEmbeddedInterruptedNotice is the one-shot provider boundary. Even a
// failed provider call is uncertain and cannot be replayed after takeover.
func (s *Store) StartEmbeddedInterruptedNotice(ctx context.Context, b EmbeddedBinding, n EmbeddedInterruptedNotice) error {
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_interrupted_notices SET state='started',start_owner_token=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND source_seq=? AND notice_id=? AND state='appended' AND entry_seq IS NOT NULL`, b.OwnerToken, b.AgentID, b.SessionID, b.Epoch, b.Generation, n.SourceSequence, n.ID)
		if err != nil {
			return err
		}
		changed, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("interrupted notice already started or stale")
		}
		return nil
	})
}

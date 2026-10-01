package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/pi/ai"
)

// EmbeddedReply is an immutable, accepted-sender-bound transport envelope.
// Its ID and timestamp must be reused verbatim across an uncertain publish.
type EmbeddedReply struct {
	AttemptID, MessageID, From, To, ReplyID, Body, CreatedAt, EntryDigest string
	EntrySeq                                                              int64
}

func (s *Store) embeddedReplyTx(ctx context.Context, attemptID string) (*EmbeddedReply, error) {
	var r EmbeddedReply
	err := s.db.QueryRowContext(ctx, `SELECT o.attempt_id,a.message_id,o.sender,o.recipient,o.reply_id,o.body,o.created_at,o.entry_seq,o.entry_digest FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE o.attempt_id=?`, attemptID).Scan(&r.AttemptID, &r.MessageID, &r.From, &r.To, &r.ReplyID, &r.Body, &r.CreatedAt, &r.EntrySeq, &r.EntryDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// AppendEmbeddedFinalAndReply commits one completed SDK assistant and its
// outbox intent together. Non-final assistant/tool fragments use the normal
// ordered append path. The owner, ACK, entry head and accepted sender are all
// verified inside BEGIN IMMEDIATE.
func (s *Store) AppendEmbeddedFinalAndReply(ctx context.Context, b EmbeddedBinding, attemptID string, expectedSeq int64, expectedDigest string, body []byte) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var id string
		if err := s.db.QueryRowContext(ctx, `SELECT message_id FROM embedded_attempts WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attemptID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&id); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, id)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != attemptID {
			return errors.New("embedded reply attempt mismatch")
		}
		state, err := s.embeddedMailAcceptedTx(ctx, a, false)
		if err != nil {
			return err
		}
		if state != "acknowledged" {
			return errors.New("embedded reply requires processing ACK")
		}
		var ackSeq int64
		if err := s.db.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_acks WHERE attempt_id=?`, attemptID).Scan(&ackSeq); err != nil {
			return err
		}
		if ackSeq > expectedSeq {
			return errors.New("embedded reply precedes user append")
		}
		if existing, err := s.embeddedReplyTx(ctx, attemptID); err != nil {
			return err
		} else if existing != nil {
			return errors.New("embedded reply already finalized")
		}
		var sourceKind string
		if err := s.db.QueryRowContext(ctx, `SELECT source_kind FROM messages WHERE id=?`, id).Scan(&sourceKind); err != nil {
			return err
		}
		msg, err := ai.UnmarshalMessage(body)
		if err != nil {
			return err
		}
		assistant, ok := msg.(ai.AssistantMessage)
		if !ok || assistant.StopReason != ai.StopStop {
			return errors.New("embedded reply requires completed assistant")
		}
		var text strings.Builder
		for _, c := range assistant.Content {
			if t, ok := c.(ai.TextContent); ok {
				text.WriteString(t.Text)
			}
		}
		if text.Len() == 0 {
			return errors.New("embedded reply has no final text")
		}
		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		if seq != expectedSeq || digest != expectedDigest {
			return errors.New("embedded final head changed")
		}
		entry, err = s.appendEmbeddedTx(ctx, b, "assistant", body, seq, digest)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("root:%s:%s:%d:%s:%d", b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO embedded_append_keys(append_key,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?)`, key, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence); err != nil {
			return err
		}
		// Reminder turns complete in the SDK, but have no sender to reply to.
		if sourceKind == "reminder" {
			return nil
		}
		hash := sha256.Sum256([]byte(attemptID + "\x00" + entry.Digest))
		replyID := "embedded-reply-" + hex.EncodeToString(hash[:])
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_reply_outbox(attempt_id,entry_seq,entry_digest,reply_id,sender,recipient,body,created_at,state) VALUES(?,?,?,?,?,?,?,?,'pending')`, attemptID, entry.Sequence, entry.Digest, replyID, b.AgentID, a.Sender, text.String(), formatTimestamp(s.clock.Now()))
		return err
	})
	return entry, err
}

func (s *Store) PendingEmbeddedReplyAgents(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT a.agent_id FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE o.state='pending' ORDER BY a.agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

// PendingEmbeddedReplies is read-only and is safe to call from explicit
// recovery without asking the SDK to rerun an acknowledged turn.
func (s *Store) PendingEmbeddedReplies(ctx context.Context, agentID string) ([]EmbeddedReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.attempt_id,a.message_id,o.sender,o.recipient,o.reply_id,o.body,o.created_at,o.entry_seq,o.entry_digest FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=? AND o.state='pending' ORDER BY o.rowid`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbeddedReply
	for rows.Next() {
		var r EmbeddedReply
		if err := rows.Scan(&r.AttemptID, &r.MessageID, &r.From, &r.To, &r.ReplyID, &r.Body, &r.CreatedAt, &r.EntrySeq, &r.EntryDigest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SettleEmbeddedReply(ctx context.Context, r EmbeddedReply) error {
	return s.runImmediate(ctx, func() error {
		saved, err := s.embeddedReplyTx(ctx, r.AttemptID)
		if err != nil {
			return err
		}
		if saved == nil || *saved != r {
			return errors.New("embedded reply intent changed")
		}
		_, err = s.db.ExecContext(ctx, `UPDATE embedded_reply_outbox SET state='delivered' WHERE attempt_id=? AND state='pending'`, r.AttemptID)
		return err
	})
}

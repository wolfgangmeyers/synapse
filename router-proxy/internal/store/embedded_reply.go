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
	RowID                                                                 int64
	AttemptID, MessageID, From, To, ReplyID, Body, CreatedAt, EntryDigest string
	EntrySeq                                                              int64
}

func (s *Store) embeddedReplyTx(ctx context.Context, attemptID string) (*EmbeddedReply, error) {
	var r EmbeddedReply
	err := s.db.QueryRowContext(ctx, `SELECT o.rowid,o.attempt_id,a.message_id,o.sender,o.recipient,o.reply_id,o.body,o.created_at,o.entry_seq,o.entry_digest FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE o.attempt_id=?`, attemptID).Scan(&r.RowID, &r.AttemptID, &r.MessageID, &r.From, &r.To, &r.ReplyID, &r.Body, &r.CreatedAt, &r.EntrySeq, &r.EntryDigest)
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
	entry, _, err := s.AppendEmbeddedFinalAndReplyWithHistory(ctx, b, attemptID, expectedSeq, expectedDigest, body)
	return entry, err
}

func (s *Store) AppendEmbeddedFinalAndReplyWithHistory(ctx context.Context, b EmbeddedBinding, attemptID string, expectedSeq int64, expectedDigest string, body []byte) (EmbeddedEntry, ManagedHistoryEntry, error) {
	var entry EmbeddedEntry
	var historyEntry ManagedHistoryEntry
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if b.OwnerToken > 0 {
			var currentToken int64
			err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&currentToken)
			if err != nil {
				return err
			}
			if currentToken != b.OwnerToken {
				return errors.New("stale embedded owner")
			}
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
		var thinking strings.Builder
		for _, c := range assistant.Content {
			if t, ok := c.(ai.TextContent); ok {
				text.WriteString(t.Text)
			} else if t, ok := c.(*ai.TextContent); ok && t != nil {
				text.WriteString(t.Text)
			} else if th, ok := c.(ai.ThinkingContent); ok {
				thinking.WriteString(th.Thinking)
			} else if th, ok := c.(*ai.ThinkingContent); ok && th != nil {
				thinking.WriteString(th.Thinking)
			}
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
		// If the incoming message was from "system" or an automated system sender,
		// the worker's turn reply must NOT be sent to "system". Instead, route it
		// by default to the last human recipient / active user.
		targetRecipient := a.Sender
		if strings.EqualFold(targetRecipient, "system") || sourceKind == "system" {
			targetRecipient = s.lastHumanRecipientTx(ctx, b.AgentID)
			if strings.EqualFold(targetRecipient, "system") || targetRecipient == "" {
				targetRecipient = "user"
			}
		}

		// If the agent already sent a message to the original sender during this turn,
		// suppress automatic reply outbox insertion to avoid duplicate replies.
		entryRows, err := s.db.QueryContext(ctx, `SELECT body FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq > ? AND seq <= ? AND kind='assistant'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, ackSeq, expectedSeq)
		if err != nil {
			return err
		}
		var bodies [][]byte
		for entryRows.Next() {
			var rawBody []byte
			if err := entryRows.Scan(&rawBody); err != nil {
				entryRows.Close()
				return err
			}
			bodies = append(bodies, rawBody)
		}
		if err := entryRows.Err(); err != nil {
			entryRows.Close()
			return err
		}
		entryRows.Close()

		var alreadySent bool
		for _, rawBody := range bodies {
			msg, err := ai.UnmarshalMessage(rawBody)
			if err != nil {
				continue
			}
			if intermediateAssistant, ok := msg.(ai.AssistantMessage); ok {
				for _, c := range intermediateAssistant.Content {
					var call *ai.ToolCall
					if tc, ok := c.(ai.ToolCall); ok {
						call = &tc
					} else if tc, ok := c.(*ai.ToolCall); ok {
						call = tc
					}
					if call != nil {
						isMailSend := false
						if call.Name == "mecha_mailbox_send" {
							if recipient, ok := call.Arguments["recipient"].(string); ok && (recipient == a.Sender || recipient == targetRecipient) {
								isMailSend = true
							}
						} else if call.Name == "mecha_worker" && call.Arguments["action"] == "send" {
							if name, ok := call.Arguments["name"].(string); ok && (name == a.Sender || name == targetRecipient) {
								isMailSend = true
							}
						}
						if isMailSend {
							var effectState string
							if err := s.db.QueryRowContext(ctx, `SELECT state FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, call.ID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&effectState); err == nil && effectState == "observed" {
								alreadySent = true
								break
							} else if errors.Is(err, sql.ErrNoRows) {
								alreadySent = true
								break
							}
						}
					}
				}
			}
			if alreadySent {
				break
			}
		}
		if alreadySent {
			return nil
		}
		replyBodyText := text.String()
		if len(strings.TrimSpace(replyBodyText)) == 0 && len(strings.TrimSpace(thinking.String())) > 0 {
			replyBodyText = thinking.String()
		}
		if len(strings.TrimSpace(replyBodyText)) == 0 {
			return nil
		}
		hash := sha256.Sum256([]byte(attemptID + "\x00" + entry.Digest))
		replyID := "embedded-reply-" + hex.EncodeToString(hash[:])
		if sourceKind != "reminder" {
			_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_reply_outbox(attempt_id,entry_seq,entry_digest,reply_id,sender,recipient,body,created_at,state) VALUES(?,?,?,?,?,?,?,?,'pending')`, attemptID, entry.Sequence, entry.Digest, replyID, b.AgentID, targetRecipient, replyBodyText, formatTimestamp(s.clock.Now()))
			if err != nil {
				return err
			}
		}
		replyBody := replyBodyText
		category := "managed_message"
		messageText := "Message sent"
		presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success", From: b.AgentID, To: targetRecipient, Thinking: thinking.String()}
		presentationJSON, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		eventKey := fmt.Sprintf("embedded-reply:%s:%s", b.AgentID, replyID)
		details := managedHistoryDetails{MessageContent: &replyBody, SenderIdentity: &b.AgentID}
		historyEntry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, category, "info", messageText, eventKey, presentationJSON, presentation, details)
		return err
	})
	return entry, historyEntry, err
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
	rows, err := s.db.QueryContext(ctx, `SELECT o.rowid,o.attempt_id,a.message_id,o.sender,o.recipient,o.reply_id,o.body,o.created_at,o.entry_seq,o.entry_digest FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=? AND o.state='pending' ORDER BY o.rowid`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EmbeddedReply
	for rows.Next() {
		var r EmbeddedReply
		if err := rows.Scan(&r.RowID, &r.AttemptID, &r.MessageID, &r.From, &r.To, &r.ReplyID, &r.Body, &r.CreatedAt, &r.EntrySeq, &r.EntryDigest); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DismissEmbeddedReply(ctx context.Context, attemptID string) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE embedded_reply_outbox SET state='delivered' WHERE attempt_id=? AND state='pending'`, attemptID)
		return err
	})
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

// LastHumanRecipient returns the last known human recipient or active user for an agent.
func (s *Store) LastHumanRecipient(ctx context.Context, agentID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("store is closed")
	}
	return s.lastHumanRecipientTx(ctx, agentID), nil
}

func (s *Store) lastHumanRecipientTx(ctx context.Context, agentID string) string {
	var recipient string

	// 1. Most recent inbound message to this agent from a human UI client (web_ui, android, discord)
	err := s.db.QueryRowContext(ctx, `SELECT sender FROM conversation_messages WHERE recipient=? AND source_kind IN ('web_ui','android','discord') AND sender NOT IN ('system','reminders','router') ORDER BY conversation_sequence DESC LIMIT 1`, agentID).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	// 2. Most recent outbound message from this agent to a recipient who sent a human UI message
	err = s.db.QueryRowContext(ctx, `SELECT recipient FROM conversation_messages WHERE sender=? AND recipient IN (SELECT sender FROM conversation_messages WHERE source_kind IN ('web_ui','android','discord')) AND recipient NOT IN ('system','reminders','router') ORDER BY conversation_sequence DESC LIMIT 1`, agentID).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	// 3. Most recent human message across the entire router in conversation_messages
	err = s.db.QueryRowContext(ctx, `SELECT sender FROM conversation_messages WHERE source_kind IN ('web_ui','android','discord') AND sender NOT IN ('system','reminders','router') ORDER BY conversation_sequence DESC LIMIT 1`).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	// 4. Any inbound message to this agent from any non-system sender
	err = s.db.QueryRowContext(ctx, `SELECT sender FROM conversation_messages WHERE recipient=? AND sender NOT IN ('system','reminders','router') AND source_kind NOT IN ('system','reminder') ORDER BY conversation_sequence DESC LIMIT 1`, agentID).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	// 5. Any outbound message from this agent to any non-system recipient
	err = s.db.QueryRowContext(ctx, `SELECT recipient FROM conversation_messages WHERE sender=? AND recipient NOT IN ('system','reminders','router') ORDER BY conversation_sequence DESC LIMIT 1`, agentID).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	// 6. Active human/tmux recipient in recipient_sources (e.g. wolfgang)
	err = s.db.QueryRowContext(ctx, `SELECT recipient_name FROM recipient_sources WHERE source_kind IN ('tmux','web_ui') AND recipient_name NOT IN ('system','reminders','router','managed-runtime-readiness','reflex-router-proxy-launcher') AND state='active' ORDER BY updated_at DESC LIMIT 1`).Scan(&recipient)
	if err == nil && recipient != "" && !strings.EqualFold(recipient, "system") {
		return recipient
	}

	return "user"
}

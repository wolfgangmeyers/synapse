package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
)

// EmbeddedToolInvocation is fresh, one-use authority for the two addressed
// managed tools. No saved receipt, intent or conversation can reconstruct it.
// The existing goal source fence checks the same current user/provider boundary.
// This witness additionally checks the original assistant SDK tool producer.
type EmbeddedToolInvocation struct {
	mu                                                        sync.Mutex
	origin                                                    EmbeddedGoalInvocation
	call                                                      ai.ToolCall
	producerSeq                                               int64
	producerDigest                                            string
	executing, admitted, completed, consumed                  bool
	domainContent                                             []byte
	domainSettled, domainCommitted, domainConclusive, handoff bool
	domainErr, handoffErr                                     error
}

// Only the original live witness can claim the handoff of its actual COMMIT.
// This disposition is not a receipt-derived admission or an SDK success.
func ClaimEmbeddedToolHandoff(w *EmbeddedToolInvocation) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.executing || !w.admitted || !w.domainCommitted || !w.domainConclusive || w.handoff || w.consumed {
		return false
	}
	w.handoff = true
	return true
}

// Retain the original handoff's safe error, not new authority or a retry.
func RecordEmbeddedToolHandoffError(w *EmbeddedToolInvocation, err error) {
	if w == nil || err == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handoff && !w.consumed && w.handoffErr == nil {
		w.handoffErr = err
	}
}

// Called with w.mu held after the original domain transaction, never replayed.
func (w *EmbeddedToolInvocation) recordDomainDisposition(committed, conclusive bool, err error) {
	if w.domainSettled {
		return
	}
	w.domainSettled, w.domainCommitted, w.domainConclusive, w.domainErr = true, committed, conclusive, err
}

func (s *Store) embeddedToolFenceTx(ctx context.Context, w *EmbeddedToolInvocation, checkProducer bool) error {
	if w == nil || w.origin.store != s {
		return errors.New("missing current managed tool invocation")
	}
	if err := s.embeddedGoalInvocationFenceTx(ctx, &w.origin); err != nil {
		return err
	}
	b := w.origin.binding
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
		return err
	}
	if role != "root" && role != "worker" {
		return errors.New("managed tool requires saved root or worker role")
	}
	var control int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND session_id=? AND epoch=? AND runtime_generation=? AND seq=?) + (SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND state='claimed')`, b.AgentID, b.SessionID, b.Epoch, b.Generation, w.origin.userSeq, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&control); err != nil {
		return err
	}
	if control != 0 {
		return errors.New("recovery wake is not ordinary managed tool authority")
	}
	if !checkProducer {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq>? ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch, b.Generation, w.origin.userSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	matches := 0
	for rows.Next() {
		var entry EmbeddedEntry
		if err := rows.Scan(&entry.Sequence, &entry.Kind, &entry.Body, &entry.ParentDigest, &entry.Digest); err != nil {
			return err
		}
		if embeddedEntryDigest(b, entry) != entry.Digest {
			return errors.New("managed tool producer entry corrupt")
		}
		message, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			return err
		}
		switch msg := message.(type) {
		case ai.AssistantMessage:
			if msg.StopReason == ai.StopStop {
				return errors.New("managed tool user turn already settled")
			}
			for _, content := range msg.Content {
				call, ok := content.(ai.ToolCall)
				if !ok || call.ID != w.call.ID {
					continue
				}
				encoded, err := json.Marshal(call)
				if err != nil || !bytes.Equal(encoded, w.origin.intent) {
					return errors.New("managed tool original SDK producer conflicts")
				}
				matches++
				if w.producerSeq != 0 && (w.producerSeq != entry.Sequence || w.producerDigest != entry.Digest) {
					return errors.New("managed tool producer changed")
				}
				w.producerSeq, w.producerDigest = entry.Sequence, entry.Digest
			}
		case ai.ToolResultMessage:
			if msg.ToolCallID == w.call.ID {
				return errors.New("managed tool SDK result already exists: no replay")
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if matches != 1 {
		return errors.New("managed tool has no unique checked SDK producer")
	}
	return nil
}

// Called only from the SDK BeforeToolExecute callback, after the original
// assistant append. The current process and source are supplied by the runner.
func (s *Store) RecordEmbeddedToolInvocation(ctx context.Context, b EmbeddedBinding, process, userSeq int64, userDigest, attempt string, call ai.ToolCall) (*EmbeddedToolInvocation, error) {
	if call.ID == "" || len(call.ID) > 256 || (call.Name != "mecha_mailbox_send" && call.Name != "mecha_send_image" && call.Name != "mecha_agent_urgency") {
		return nil, errors.New("invalid managed tool call")
	}
	intent, err := json.Marshal(call)
	if err != nil || embeddedBody(intent) != nil {
		return nil, errors.New("invalid managed tool intent")
	}
	w := &EmbeddedToolInvocation{origin: EmbeddedGoalInvocation{store: s, binding: b, process: process, userSeq: userSeq, userDigest: userDigest, attempt: attempt, id: call.ID, name: call.Name, intent: bytes.Clone(intent)}}
	// Detach the canonical arguments from caller-owned mutable maps.
	if err := json.Unmarshal(intent, &w.call); err != nil {
		return nil, err
	}
	err = s.runImmediate(ctx, func() error {
		if err := s.embeddedToolFenceTx(ctx, w, true); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO embedded_effects(effect_id,agent_id,session_id,epoch,generation,intent,state) VALUES(?,?,?,?,?,?,'unknown')`, call.ID, b.AgentID, b.SessionID, b.Epoch, b.Generation, intent)
		return err
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Store) checkEmbeddedToolIntentTx(ctx context.Context, w *EmbeddedToolInvocation) error {
	if err := s.embeddedToolFenceTx(ctx, w, true); err != nil {
		return err
	}
	var state string
	var intent []byte
	b := w.origin.binding
	if err := s.db.QueryRowContext(ctx, `SELECT state,intent FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, w.call.ID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&state, &intent); err != nil {
		return err
	}
	if state != "unknown" || !bytes.Equal(intent, w.origin.intent) {
		return errors.New("managed tool original intent is not fresh UNKNOWN")
	}
	return nil
}

func (s *Store) StartEmbeddedToolInvocation(ctx context.Context, w *EmbeddedToolInvocation, call ai.ToolCall) error {
	if w == nil {
		return errors.New("missing managed tool invocation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.executing || w.consumed {
		return errors.New("managed tool Execute already consumed")
	}
	w.executing = true
	body, err := json.Marshal(call.Arguments)
	original, originalErr := json.Marshal(w.call.Arguments)
	if err != nil || originalErr != nil || call.ID != w.call.ID || call.Name != w.call.Name || !bytes.Equal(body, original) {
		w.consumed = true
		return errors.New("managed tool Execute differs from original SDK call")
	}
	err = s.runImmediate(ctx, func() error { return s.checkEmbeddedToolIntentTx(ctx, w) })
	if err != nil {
		w.consumed = true
	}
	return err
}

// The caller holds w.mu; this runs inside the existing domain transaction.
func (s *Store) admitEmbeddedToolTx(ctx context.Context, w *EmbeddedToolInvocation) error {
	if w == nil || !w.executing || w.admitted || w.completed || w.consumed || w.domainSettled {
		return errors.New("managed tool has no unused Execute admission")
	}
	w.admitted = true // A failed or ambiguous transaction never restores authority.
	return s.checkEmbeddedToolIntentTx(ctx, w)
}

// Content exists only after this witness's actual successful domain admission.
func EmbeddedToolResultContent(w *EmbeddedToolInvocation) ([]byte, error) {
	if w == nil {
		return nil, errors.New("missing managed tool result producer")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.admitted || w.consumed || len(w.domainContent) == 0 {
		return nil, errors.New("managed tool outcome UNKNOWN: no domain result")
	}
	return bytes.Clone(w.domainContent), nil
}

func (s *Store) CompleteEmbeddedToolInvocation(ctx context.Context, w *EmbeddedToolInvocation, result ai.ToolResultMessage) error {
	if w == nil {
		return errors.New("missing managed tool completion")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.admitted || w.completed || w.consumed || w.domainErr != nil || w.handoffErr != nil || len(w.domainContent) == 0 || result.ToolCallID != w.call.ID || result.ToolName != w.call.Name || result.IsError || len(result.Content) != 1 {
		return errors.New("managed tool has no current domain completion")
	}
	text, ok := result.Content[0].(ai.TextContent)
	if !ok || !bytes.Equal([]byte(text.Text), w.domainContent) {
		return errors.New("managed tool result differs from current domain result")
	}
	if err := s.runImmediate(ctx, func() error { return s.checkEmbeddedToolIntentTx(ctx, w) }); err != nil {
		return err
	}
	w.completed = true
	return nil
}

// Checked SDK result append and effect observation share one transaction.
// A dropped producer witness cannot be replaced by saved domain/SDK output.
func (s *Store) AppendEmbeddedToolInvocationResult(ctx context.Context, w *EmbeddedToolInvocation, seq int64, digest, key string, result ai.ToolResultMessage, body []byte) (EmbeddedEntry, error) {
	if w == nil {
		return EmbeddedEntry{}, errors.New("missing managed tool checked result")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	// An absent/retired urgency target is a conclusive domain rejection, not
	// uncertain execution. Only the original live witness can check this error;
	// storage, stale-fence and post-COMMIT failures remain UNKNOWN.
	rejectedUrgency := w.call.Name == "mecha_agent_urgency" && w.executing && w.admitted &&
		w.domainSettled && w.domainConclusive && !w.domainCommitted && w.handoffErr == nil &&
		errors.Is(w.domainErr, ErrAgentNotFound)
	if (!w.completed && !rejectedUrgency) || w.consumed {
		disposition := errors.New("managed tool outcome UNKNOWN: no producer completion")
		if w.domainCommitted && w.domainConclusive {
			disposition = errors.New("managed tool admission committed: no checked SDK producer completion")
		}
		if w.handoffErr != nil {
			return EmbeddedEntry{}, errors.Join(disposition, w.handoffErr)
		}
		return EmbeddedEntry{}, errors.Join(disposition, w.domainErr)
	}
	w.consumed = true
	encoded, err := json.Marshal(result)
	if err != nil || !bytes.Equal(encoded, body) || result.Timestamp <= 0 || result.ToolCallID != w.call.ID || result.ToolName != w.call.Name || result.IsError != rejectedUrgency || len(result.Content) != 1 || seq < w.producerSeq {
		return EmbeddedEntry{}, errors.New("managed tool checked SDK result mismatch")
	}
	text, ok := result.Content[0].(ai.TextContent)
	expected := w.domainContent
	if rejectedUrgency {
		expected = []byte(w.domainErr.Error())
	}
	if !ok || !bytes.Equal([]byte(text.Text), expected) {
		return EmbeddedEntry{}, errors.New("managed tool checked content mismatch")
	}
	var entry EmbeddedEntry
	err = s.runImmediate(ctx, func() error {
		if err := s.checkEmbeddedToolIntentTx(ctx, w); err != nil {
			return err
		}
		var prior int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_append_keys WHERE append_key=?`, key).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 {
			return errors.New("managed tool checked result cannot replay")
		}
		var err error
		entry, err = s.appendEmbeddedKeyedTx(ctx, w.origin.binding, seq, digest, key, string(ai.RoleToolResult), body, nil)
		if err != nil {
			return err
		}
		b := w.origin.binding
		updated, err := s.db.ExecContext(ctx, `UPDATE embedded_effects SET state='observed',result=? WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown'`, bytes.Clone(body), w.call.ID, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		n, err := updated.RowsAffected()
		if err != nil || n != 1 {
			return errors.New("managed tool observation lost")
		}
		return nil
	})
	return entry, err
}

// Uses ordinary bounded mail receipts/queue admission, with an additional
// producer guard inside that very transaction. No second mail ledger.
func (s *Store) EnqueueEmbeddedToolMail(ctx context.Context, w *EmbeddedToolInvocation) (EnqueueResult, error) {
	if w == nil {
		return EnqueueResult{}, errors.New("missing mail producer")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.domainSettled {
		return EnqueueResult{}, errors.New("mail admission already consumed")
	}
	recipient, _ := w.call.Arguments["recipient"].(string)
	if strings.EqualFold(recipient, "system") {
		return EnqueueResult{}, errors.New("failed to send message: 'system' is an automated system notifier and does not accept incoming messages. Do not reply to system notifications.")
	}
	body, _ := w.call.Arguments["body"].(string)
	envelope := protocol.Envelope{ID: "embedded-mail-" + embeddedHash([]byte(w.origin.binding.AgentID), []byte(w.origin.binding.SessionID), []byte(strconv.FormatInt(w.origin.binding.Epoch, 10)), []byte(w.origin.binding.Generation), []byte(w.origin.userDigest), []byte(w.producerDigest), w.origin.intent), From: w.origin.binding.AgentID, To: recipient, Body: body, CreatedAt: formatTimestamp(s.clock.Now())}
	if subject, ok := w.call.Arguments["subject"].(string); ok {
		envelope.Subject = &subject
	}
	result, err := s.enqueue(ctx, envelope, &EnqueueOptions{Conversation: &ConversationOptions{Body: &envelope.Body}}, w)
	if w.domainCommitted && w.domainConclusive {
		var encodeErr error
		w.domainContent, encodeErr = json.Marshal(map[string]any{"status": "accepted", "request_id": envelope.ID, "message_id": result.Message.ID, "state": string(result.Message.State)})
		err = errors.Join(err, encodeErr)
	}
	return result, err
}

func (s *Store) EnqueueEmbeddedToolImage(ctx context.Context, w *EmbeddedToolInvocation, envelope protocol.Envelope, rawAttachments []json.RawMessage, pending []attachments.Pending) (EnqueueResult, error) {
	if w == nil {
		return EnqueueResult{}, errors.New("missing mail producer")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.domainSettled {
		return EnqueueResult{}, errors.New("mail admission already consumed")
	}
	result, err := s.enqueue(ctx, envelope, &EnqueueOptions{
		Conversation: &ConversationOptions{
			Body:        &envelope.Body,
			Attachments: rawAttachments,
			Source:      envelope.Source,
		},
	}, w)
	if w.domainCommitted && w.domainConclusive && len(pending) > 0 {
		var encodeErr error
		w.domainContent, encodeErr = json.Marshal(map[string]any{
			"status":        "accepted",
			"message_id":    result.Message.ID,
			"attachment_id": pending[0].Attachment.ID,
			"url":           pending[0].Attachment.URL,
			"recipient":     envelope.To,
		})
		err = errors.Join(err, encodeErr)
	}
	return result, err
}

func (s *Store) embeddedToolMailGuardTx(ctx context.Context, w *EmbeddedToolInvocation, envelope protocol.Envelope) error {
	if err := s.admitEmbeddedToolTx(ctx, w); err != nil {
		return err
	}
	if w.call.Name == "mecha_send_image" {
		if envelope.From != w.origin.binding.AgentID {
			return errors.New("image envelope sender differs from origin agent")
		}
		recipient, _ := w.call.Arguments["recipient"].(string)
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			recipient = "user"
		}
		if recipient != envelope.To {
			return errors.New("image envelope recipient differs from tool call")
		}
		if envelope.ID == "" {
			return errors.New("image envelope ID cannot be empty")
		}
		return nil
	}
	args := w.call.Arguments
	recipient := args["recipient"]
	if envelope.From != w.origin.binding.AgentID || recipient != envelope.To || args["body"] != envelope.Body || envelope.ID == "" {
		return errors.New("mail envelope differs from current addressed SDK call")
	}
	if subject, exists := args["subject"]; exists {
		if envelope.Subject == nil || subject != *envelope.Subject {
			return errors.New("mail subject differs from SDK call")
		}
	} else if envelope.Subject != nil {
		return errors.New("unexpected mail subject")
	}
	return nil
}

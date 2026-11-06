package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

const conversationReceiptHorizon = 30 * time.Minute
const receiptPruneBatch = 128

type conversationCandidate struct {
	protocol.Envelope
	Attachments   []json.RawMessage
	QuietEligible bool
}

func prepareConversation(candidate protocol.Envelope, options *ConversationOptions) (*conversationCandidate, error) {
	if options == nil {
		return nil, nil
	}
	body := candidate.Body
	if options.Body != nil {
		body = *options.Body
	}
	source := protocol.NormalizeSource(candidate.Source)
	if options.Source != nil {
		source = protocol.NormalizeSource(options.Source)
	}
	attachments := append([]json.RawMessage{}, options.Attachments...)
	return &conversationCandidate{Envelope: protocol.Envelope{ID: candidate.ID, From: candidate.From, To: candidate.To, Subject: candidate.Subject, Body: body, CreatedAt: candidate.CreatedAt, Sentinel: candidate.Sentinel, Source: &source}, Attachments: attachments, QuietEligible: options.QuietEligible}, nil
}
func (s *Store) latestConversation(ctx context.Context, id string) (*domain.ConversationMessage, error) {
	var c domain.ConversationMessage
	var subject, source, attachments sql.NullString
	query := `SELECT conversation_sequence,message_id,message_incarnation_id,delivery_message_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind FROM conversation_messages WHERE message_id = ? ORDER BY conversation_sequence DESC LIMIT 1`
	if s.receiptLifecycle {
		query = `SELECT conversation_sequence,message_id,message_incarnation_id,delivery_message_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind FROM conversation_messages WHERE message_id = ? AND receipt_backed=0 ORDER BY conversation_sequence DESC LIMIT 1`
	}
	err := s.db.QueryRowContext(ctx, query, id).Scan(&c.Sequence, &c.ID, &c.MessageIncarnationID, &c.DeliveryMessageID, &c.From, &c.To, &subject, &c.Body, &c.CreatedAt, &c.RecordedAt, &attachments, &source)
	if subject.Valid {
		c.Subject = &subject.String
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if attachments.Valid {
		_ = json.Unmarshal([]byte(attachments.String), &c.Attachments)
	}
	c.Source.Kind = "unknown"
	if source.Valid {
		c.Source.Kind = source.String
	}
	return &c, nil
}
func sameConversation(c *domain.ConversationMessage, candidate *conversationCandidate) bool {
	if c.ID != candidate.ID || c.From != candidate.From || c.To != candidate.To || !subjectEqual(c.Subject, candidate.Subject) || c.Body != candidate.Body || c.CreatedAt != candidate.CreatedAt {
		return false
	}
	wanted, _ := json.Marshal(candidate.Attachments)
	actual, _ := json.Marshal(c.Attachments)
	return string(wanted) == string(actual) && (c.Source.Kind == "unknown" || c.Source.Kind == candidate.Source.Kind)
}

// HasAcceptedSend lets HTTP retries bypass new-send rate limits. Enqueue still
// performs the authoritative receipt comparison inside its write transaction.
func (s *Store) HasAcceptedSend(ctx context.Context, id string) (bool, error) {
	var accepted bool
	query := `SELECT EXISTS(SELECT 1 FROM messages WHERE id=?) OR EXISTS(SELECT 1 FROM conversation_send_receipts WHERE message_id=?) OR EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=?)`
	if s.receiptLifecycle {
		query = `SELECT EXISTS(SELECT 1 FROM messages WHERE id=?) OR EXISTS(SELECT 1 FROM conversation_send_receipts WHERE message_id=? AND deadline_at>?) OR EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND receipt_backed=0)`
		err := s.db.QueryRowContext(ctx, query, id, id, formatTimestamp(s.clock.Now()), id).Scan(&accepted)
		return accepted, err
	}
	err := s.db.QueryRowContext(ctx, query, id, id, id).Scan(&accepted)
	return accepted, err
}
func (s *Store) conversationReceipt(ctx context.Context, id string) (*domain.ConversationMessage, *domain.Message, string, string, error) {
	var conversationJSON, deliveryJSON, acceptedAt, deadlineAt string
	var err error
	if s.receiptLifecycle {
		err = s.db.QueryRowContext(ctx, `SELECT conversation_json,delivery_json,accepted_at,deadline_at FROM conversation_send_receipts WHERE message_id=?`, id).Scan(&conversationJSON, &deliveryJSON, &acceptedAt, &deadlineAt)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT conversation_json,delivery_json FROM conversation_send_receipts WHERE message_id=?`, id).Scan(&conversationJSON, &deliveryJSON)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", "", nil
	}
	if err != nil {
		return nil, nil, "", "", err
	}
	var conversation domain.ConversationMessage
	var delivery domain.Message
	if err := json.Unmarshal([]byte(conversationJSON), &conversation); err != nil {
		return nil, nil, "", "", err
	}
	if err := json.Unmarshal([]byte(deliveryJSON), &delivery); err != nil {
		return nil, nil, "", "", err
	}
	conversation.DeliveryMessageID = delivery.ID
	return &conversation, &delivery, acceptedAt, deadlineAt, nil
}

// Prune only the receipt. The delivery can still be queued, dead-lettered,
// or shared by alias receipts and is owned by the delivery lifecycle.
func (s *Store) pruneConversationReceipts(ctx context.Context, now string) (int, error) {
	return s.pruneConversationReceiptsFor(ctx, now, "")
}

func (s *Store) pruneConversationReceiptsFor(ctx context.Context, now, id string) (int, error) {
	if !s.receiptLifecycle {
		return 0, nil
	}
	// Both queries select the same indexed batch under BEGIN IMMEDIATE. Sum
	// persisted bytes, not character counts or reconstructed payload lengths.
	selection := `SELECT message_id FROM conversation_send_receipts INDEXED BY conversation_send_receipts_deadline_idx
	 WHERE deadline_at<=? AND (?='' OR message_id=?) ORDER BY deadline_at,message_id LIMIT ?`
	args := []any{now, id, id, receiptPruneBatch}
	var count int
	var bytes int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB))),0)
	 FROM conversation_send_receipts WHERE message_id IN (`+selection+`)`, args...).Scan(&count, &bytes)
	if err != nil || count == 0 {
		return 0, err
	}
	removed, err := s.db.ExecContext(ctx, `DELETE FROM conversation_send_receipts WHERE message_id IN (`+selection+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := removed.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != int64(count) {
		return 0, fmt.Errorf("receipt prune count changed: selected %d, deleted %d", count, n)
	}
	charged, err := s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET used_count=used_count-?,used_bytes=used_bytes-? WHERE singleton=1 AND used_count>=? AND used_bytes>=?`, count, bytes, count, bytes)
	if err != nil {
		return 0, err
	}
	n, err = charged.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, fmt.Errorf("receipt capacity inconsistent for pruned batch")
	}
	return count, nil
}

// PruneExpiredConversationReceipts commits each bounded batch independently.
// It does not depend on send traffic or on the larger lifecycle sweep succeeding.
func (s *Store) PruneExpiredConversationReceipts(ctx context.Context) (int, error) {
	if !s.receiptLifecycle {
		return 0, nil
	}
	total := 0
	for {
		var pruned int
		err := s.runImmediate(ctx, func() error {
			var err error
			pruned, err = s.pruneConversationReceipts(ctx, formatTimestamp(s.clock.Now()))
			return err
		})
		if err != nil {
			return total, err
		}
		total += pruned
		if pruned < receiptPruneBatch {
			return total, nil
		}
	}
}

// ConversationReceiptCleanupDelay wakes at the earliest deadline, with a
// short poll as a fallback for other store handles and test-clock jumps.
func (s *Store) ConversationReceiptCleanupDelay(ctx context.Context, maximum time.Duration) (time.Duration, error) {
	if !s.receiptLifecycle {
		return maximum, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("store is closed")
	}
	var deadline string
	err := s.db.QueryRowContext(ctx, `SELECT deadline_at FROM conversation_send_receipts ORDER BY deadline_at,message_id LIMIT 1`).Scan(&deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return maximum, nil
	}
	if err != nil {
		return 0, err
	}
	at := parseTimestamp(deadline)
	if at.IsZero() {
		return 0, fmt.Errorf("invalid conversation receipt deadline")
	}
	delay := at.Sub(s.clock.Now())
	if delay < 0 {
		return 0, nil
	}
	if delay < maximum {
		return delay, nil
	}
	return maximum, nil
}

func (s *Store) insertConversation(ctx context.Context, candidate *conversationCandidate, incarnation, recorded string, delivery *domain.Message) (*domain.ConversationMessage, error) {
	a, b := candidate.From, candidate.To
	if b < a {
		a, b = b, a
	}
	attachments, _ := json.Marshal(candidate.Attachments)
	quietEligible := 0
	if candidate.QuietEligible {
		quietEligible = 1
	}
	query := `INSERT INTO conversation_messages (message_id,message_incarnation_id,delivery_message_id,sender,recipient,participant_a,participant_b,subject,body,created_at,recorded_at,attachments_json,source_kind,quiet_eligible) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if s.receiptLifecycle {
		query = `INSERT INTO conversation_messages (message_id,message_incarnation_id,delivery_message_id,sender,recipient,participant_a,participant_b,subject,body,created_at,recorded_at,attachments_json,source_kind,quiet_eligible,receipt_backed) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,1)`
	}
	_, err := s.db.ExecContext(ctx, query, candidate.ID, incarnation, delivery.ID, candidate.From, candidate.To, a, b, nullableSubject(candidate.Subject), candidate.Body, candidate.CreatedAt, recorded, string(attachments), candidate.Source.Kind, quietEligible)
	if err != nil {
		return nil, err
	}
	row, err := s.conversationByIncarnation(ctx, candidate.ID, incarnation)
	if err != nil {
		return nil, err
	}
	conversationJSON, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	deliveryJSON, err := json.Marshal(delivery)
	if err != nil {
		return nil, err
	}
	// Charge serialized UTF-8 bytes in the same immediate transaction.
	bytes := int64(len(candidate.ID) + len(conversationJSON) + len(deliveryJSON))
	res, err := s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity
		SET used_count=used_count+1, used_bytes=used_bytes+?
		WHERE singleton=1 AND used_count < max_count AND ? <= max_bytes AND used_bytes <= max_bytes-?`, bytes, bytes, bytes)
	if err != nil {
		return nil, err
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if updated != 1 {
		return nil, &ConversationReceiptCapacityError{}
	}
	if s.receiptLifecycle {
		accepted := parseTimestamp(recorded)
		_, err = s.db.ExecContext(ctx, `INSERT INTO conversation_send_receipts(message_id,conversation_json,delivery_json,accepted_at,deadline_at) VALUES (?,?,?,?,?)`, candidate.ID, string(conversationJSON), string(deliveryJSON), recorded, formatTimestamp(accepted.Add(conversationReceiptHorizon)))
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO conversation_send_receipts(message_id,conversation_json,delivery_json) VALUES (?,?,?)`, candidate.ID, string(conversationJSON), string(deliveryJSON))
	}
	return row, err
}

func (s *Store) conversationByIncarnation(ctx context.Context, id, incarnation string) (*domain.ConversationMessage, error) {
	var c domain.ConversationMessage
	var subject, source, attachments sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,delivery_message_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind FROM conversation_messages WHERE message_id=? AND message_incarnation_id=?`, id, incarnation).Scan(&c.Sequence, &c.ID, &c.MessageIncarnationID, &c.DeliveryMessageID, &c.From, &c.To, &subject, &c.Body, &c.CreatedAt, &c.RecordedAt, &attachments, &source)
	if err != nil {
		return nil, err
	}
	if subject.Valid {
		c.Subject = &subject.String
	}
	if attachments.Valid {
		_ = json.Unmarshal([]byte(attachments.String), &c.Attachments)
	}
	c.Source.Kind = "unknown"
	if source.Valid {
		c.Source.Kind = source.String
	}
	return &c, nil
}

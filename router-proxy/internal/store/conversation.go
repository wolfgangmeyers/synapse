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

const conversationReceiptHorizon = 30 * 24 * time.Hour
const receiptPruneBatch = 128

type ConversationReceiptExpiredUnresolvedError struct {
	MessageID, AcceptedAt, DeadlineAt, DeliveryState, Reason, IncarnationID string
}

func (e *ConversationReceiptExpiredUnresolvedError) Error() string {
	return fmt.Sprintf("conversation receipt %s expired with unresolved delivery (%s)", e.MessageID, e.DeliveryState)
}
func (e *ConversationReceiptExpiredUnresolvedError) Code() string {
	return "conversation_receipt_expired_unresolved"
}

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
		query = `SELECT EXISTS(SELECT 1 FROM messages WHERE id=?) OR EXISTS(SELECT 1 FROM conversation_send_receipts WHERE message_id=?) OR EXISTS(SELECT 1 FROM conversation_messages WHERE message_id=? AND receipt_backed=0)`
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

// A live original delivery incarnation (including dead letter) is unresolved.
// The receipt key can be an alias: dedupe stores A's delivery under B's receipt.
func (s *Store) receiptResolution(ctx context.Context, id, incarnation string) (bool, string, string, error) {
	var deliveryJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT delivery_json FROM conversation_send_receipts WHERE message_id=?`, id).Scan(&deliveryJSON); err != nil {
		return false, "", "", err
	}
	var identity struct {
		ID          string `json:"id"`
		Incarnation string `json:"message_incarnation_id"`
	}
	if err := json.Unmarshal([]byte(deliveryJSON), &identity); err != nil {
		return false, "", "", fmt.Errorf("receipt %s has invalid delivery identity: %w", id, err)
	}
	if identity.ID == "" || identity.Incarnation == "" || identity.Incarnation != incarnation {
		return false, "", "", fmt.Errorf("receipt %s has missing or inconsistent delivery identity", id)
	}
	var state, reason sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state, (SELECT failure_reason FROM delivery_attempts WHERE message_id=messages.id ORDER BY attempt_number DESC LIMIT 1) FROM messages WHERE id=? AND message_incarnation_id=?`, identity.ID, identity.Incarnation).Scan(&state, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return true, "", "", nil
	}
	if err != nil {
		return false, "", "", err
	}
	return false, state.String, reason.String, nil
}

// The indexed eligibility predicate skips unresolved prefixes; one call
// touches at most receiptPruneBatch receipts regardless of ledger size.
func (s *Store) pruneConversationReceipts(ctx context.Context, now string) (int, error) {
	return s.pruneConversationReceiptsFor(ctx, now, "")
}

func (s *Store) pruneConversationReceiptsFor(ctx context.Context, now, id string) (int, error) {
	if !s.receiptLifecycle {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.message_id,
	 length(CAST(r.message_id AS BLOB))+length(CAST(r.conversation_json AS BLOB))+length(CAST(r.delivery_json AS BLOB))
	 FROM conversation_send_receipts r INDEXED BY conversation_send_receipts_deadline_idx
	 WHERE r.deadline_at<=? AND (?='' OR r.message_id=?) AND CASE WHEN json_valid(r.delivery_json) THEN
	   json_type(r.delivery_json,'$.id')='text' AND json_extract(r.delivery_json,'$.id')!=''
	   AND json_type(r.delivery_json,'$.message_incarnation_id')='text'
	   AND json_extract(r.delivery_json,'$.message_incarnation_id')!=''
	   AND NOT EXISTS (
	     SELECT 1 FROM messages m WHERE m.id=json_extract(r.delivery_json,'$.id')
	     AND m.message_incarnation_id=json_extract(r.delivery_json,'$.message_incarnation_id'))
	 ELSE 0 END
	 ORDER BY r.deadline_at,r.message_id LIMIT ?`, now, id, id, receiptPruneBatch)
	if err != nil {
		return 0, err
	}
	type entry struct {
		id    string
		bytes int64
	}
	var selected []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.bytes); err != nil {
			_ = rows.Close()
			return 0, err
		}
		selected = append(selected, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, e := range selected {
		removed, err := s.db.ExecContext(ctx, `DELETE FROM conversation_send_receipts WHERE message_id=?`, e.id)
		if err != nil {
			return 0, err
		}
		n, err := removed.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != 1 {
			return 0, fmt.Errorf("receipt %s disappeared during prune", e.id)
		}
		charged, err := s.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET used_count=used_count-1,used_bytes=used_bytes-? WHERE singleton=1 AND used_count>=1 AND used_bytes>=?`, e.bytes, e.bytes)
		if err != nil {
			return 0, err
		}
		n, err = charged.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != 1 {
			return 0, fmt.Errorf("receipt capacity inconsistent for %s", e.id)
		}
	}
	return len(selected), nil
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

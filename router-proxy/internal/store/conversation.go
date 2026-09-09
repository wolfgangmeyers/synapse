package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

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
	err := s.db.QueryRowContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,delivery_message_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind FROM conversation_messages WHERE message_id = ? ORDER BY conversation_sequence DESC LIMIT 1`, id).Scan(&c.Sequence, &c.ID, &c.MessageIncarnationID, &c.DeliveryMessageID, &c.From, &c.To, &subject, &c.Body, &c.CreatedAt, &c.RecordedAt, &attachments, &source)
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
func (s *Store) insertConversation(ctx context.Context, candidate *conversationCandidate, incarnation, deliveryID, recorded string) (*domain.ConversationMessage, error) {
	a, b := candidate.From, candidate.To
	if b < a {
		a, b = b, a
	}
	attachments, _ := json.Marshal(candidate.Attachments)
	quietEligible := 0
	if candidate.QuietEligible {
		quietEligible = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_messages (message_id,message_incarnation_id,delivery_message_id,sender,recipient,participant_a,participant_b,subject,body,created_at,recorded_at,attachments_json,source_kind,quiet_eligible) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, candidate.ID, incarnation, deliveryID, candidate.From, candidate.To, a, b, nullableSubject(candidate.Subject), candidate.Body, candidate.CreatedAt, recorded, string(attachments), candidate.Source.Kind, quietEligible)
	if err != nil {
		return nil, err
	}
	return s.latestConversation(ctx, candidate.ID)
}

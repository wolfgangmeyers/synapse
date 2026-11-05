package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

// PromptTemplate is the persisted shared prompt-template projection exposed by
// the HTTP API.
type PromptTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type PromptTemplateRequest struct {
	Name      string
	Body      string
	ID        *string
	UpdatedAt *string
}

type PromptTemplateValidationError struct{}

func (e *PromptTemplateValidationError) Error() string { return "invalid prompt template request" }
func (e *PromptTemplateValidationError) Code() string  { return "invalid_prompt_template" }

type PromptTemplateNameConflictError struct{ Name string }

func (e *PromptTemplateNameConflictError) Error() string {
	return "prompt template name already exists"
}
func (e *PromptTemplateNameConflictError) Code() string { return "prompt_template_name_conflict" }

type PromptTemplateCapacityError struct{}

func (e *PromptTemplateCapacityError) Error() string { return "prompt template collection is full" }
func (e *PromptTemplateCapacityError) Code() string  { return "prompt_template_capacity_exceeded" }

const (
	maxPromptTemplates = 100
	maxPromptNameBytes = 128
	maxPromptBodyBytes = 65536
)

var (
	promptTemplateTimestampPattern   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	generatedPromptTemplateIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// MigrationIDs returns the migrations configured for this store in order.
func (s *Store) MigrationIDs() []string {
	ids := make([]string, 0, len(s.migrations))
	for _, migration := range s.migrations {
		ids = append(ids, migration.ID)
	}
	return ids
}

// AdmissionLimits reports the limits used by enqueue validation.
type AdmissionLimits struct {
	MaxBodyBytes      int `json:"maxBodyBytes"`
	MaxQueuedMessages int `json:"maxQueuedMessages"`
	MaxQueuedBytes    int `json:"maxQueuedBytes"`
	MaxBatchBodyChars int `json:"maxBatchBodyChars"`
}

func (s *Store) AdmissionLimits() AdmissionLimits {
	return AdmissionLimits{MaxBodyBytes: s.maxBodyBytes, MaxQueuedMessages: s.maxQueuedMessages, MaxQueuedBytes: s.maxQueuedBytes, MaxBatchBodyChars: s.maxBatchBodyChars}
}

func (s *Store) conversationHistoryForPairPaged(ctx context.Context, owner, peer string, before int64, limit int) ([]domain.ConversationMessage, *int64, error) {
	a, b := owner, peer
	if b < a {
		a, b = b, a
	}
	var rows *sql.Rows
	var err error
	if limit <= 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind
			FROM conversation_messages WHERE participant_a = ? AND participant_b = ? AND (? = 0 OR conversation_sequence < ?) ORDER BY conversation_sequence ASC`, a, b, before, before)
	} else {
		// Fetch limit + 1 in DESC order so we always load the most recent window first, then reverse to ASC.
		rows, err = s.db.QueryContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind
			FROM conversation_messages WHERE participant_a = ? AND participant_b = ? AND (? = 0 OR conversation_sequence < ?) ORDER BY conversation_sequence DESC LIMIT ?`, a, b, before, before, limit+1)
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []domain.ConversationMessage
	for rows.Next() {
		var message domain.ConversationMessage
		var subject, attachments, source sql.NullString
		if err := rows.Scan(&message.Sequence, &message.ID, &message.MessageIncarnationID, &message.From, &message.To, &subject, &message.Body, &message.CreatedAt, &message.RecordedAt, &attachments, &source); err != nil {
			return nil, nil, err
		}
		if subject.Valid {
			message.Subject = &subject.String
		}
		message.Attachments = []json.RawMessage{}
		if attachments.Valid && attachments.String != "" {
			if err := json.Unmarshal([]byte(attachments.String), &message.Attachments); err != nil {
				return nil, nil, err
			}
		}
		message.Source = protocol.MessageSource{Kind: "unknown"}
		if source.Valid {
			message.Source.Kind = source.String
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var nextBefore *int64
	if limit > 0 && len(result) > limit {
		next := result[limit-1].Sequence
		nextBefore = &next
		result = result[:limit]
	}
	if limit > 0 {
		// Reverse from DESC (query order) to chronological ASC for display
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}
	return result, nextBefore, nil
}

func (s *Store) ConversationHistory(ctx context.Context, owner, peer string) ([]domain.ConversationMessage, error) {
	messages, _, err := s.ConversationHistoryPaged(ctx, owner, peer, 0, 0)
	return messages, err
}

func (s *Store) ConversationHistoryPaged(ctx context.Context, owner, peer string, before int64, limit int) ([]domain.ConversationMessage, *int64, error) {
	if owner == "" || peer == "" {
		return nil, nil, errors.New("owner and peer are required")
	}
	result, nextBefore, err := s.conversationHistoryForPairPaged(ctx, owner, peer, before, limit)
	if err != nil {
		return nil, nil, err
	}
	// Postel's Law: If the requester connected as generic "user" or has an empty history with peer,
	// check if the primary human operator has conversation history with this peer.
	// The router is the authoritative source of record for conversation history.
	if owner == "user" {
		human, humanErr := s.LastHumanRecipient(ctx, peer)
		if humanErr == nil && human != "" && human != "user" && human != owner {
			humanResult, humanNextBefore, hErr := s.conversationHistoryForPairPaged(ctx, human, peer, before, limit)
			if hErr == nil && len(humanResult) > 0 {
				if len(result) == 0 {
					return humanResult, humanNextBefore, nil
				}
				seen := make(map[int64]bool, len(result)+len(humanResult))
				var merged []domain.ConversationMessage
				for _, m := range result {
					seen[m.Sequence] = true
					merged = append(merged, m)
				}
				for _, m := range humanResult {
					if !seen[m.Sequence] {
						seen[m.Sequence] = true
						merged = append(merged, m)
					}
				}
				sort.Slice(merged, func(i, j int) bool {
					return merged[i].Sequence < merged[j].Sequence
				})
				if limit > 0 && len(merged) > limit {
					merged = merged[len(merged)-limit:]
				}
				return merged, nextBefore, nil
			}
		}
	}
	return result, nextBefore, nil
}

func (s *Store) RecentAgentMessages(ctx context.Context, agentID string, limit int) ([]domain.ConversationMessage, error) {
	return s.RecentAgentMessagesSince(ctx, agentID, 0, limit)
}

func (s *Store) RecentAgentMessagesSince(ctx context.Context, agentID string, sinceSeq int64, limit int) ([]domain.ConversationMessage, error) {
	if agentID == "" {
		return nil, errors.New("agentID is required")
	}
	var rows *sql.Rows
	var err error
	if limit <= 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind
			FROM conversation_messages WHERE (sender = ? OR recipient = ?) AND conversation_sequence > ? ORDER BY conversation_sequence ASC`, agentID, agentID, sinceSeq)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT conversation_sequence,message_id,message_incarnation_id,sender,recipient,subject,body,created_at,recorded_at,attachments_json,source_kind
			FROM conversation_messages WHERE (sender = ? OR recipient = ?) AND conversation_sequence > ? ORDER BY conversation_sequence DESC LIMIT ?`, agentID, agentID, sinceSeq, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ConversationMessage
	for rows.Next() {
		var message domain.ConversationMessage
		var subject, attachments, source sql.NullString
		if err := rows.Scan(&message.Sequence, &message.ID, &message.MessageIncarnationID, &message.From, &message.To, &subject, &message.Body, &message.CreatedAt, &message.RecordedAt, &attachments, &source); err != nil {
			return nil, err
		}
		if subject.Valid {
			message.Subject = &subject.String
		}
		message.Attachments = []json.RawMessage{}
		if attachments.Valid && attachments.String != "" {
			if err := json.Unmarshal([]byte(attachments.String), &message.Attachments); err != nil {
				return nil, err
			}
		}
		message.Source = protocol.MessageSource{Kind: "unknown"}
		if source.Valid {
			message.Source.Kind = source.String
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if limit > 0 {
		// Reverse from DESC (query order) to chronological ASC for display
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}
	return result, nil
}

func (s *Store) ConversationHighWaterSequence(ctx context.Context) (int64, error) {
	var sequence sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'conversation_messages'`).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) || !sequence.Valid {
		return 0, nil
	}
	return sequence.Int64, err
}

func (s *Store) ConversationAttachmentReferenced(ctx context.Context, attachmentID string) (bool, error) {
	if attachmentID == "" {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT attachments_json FROM conversation_messages WHERE attachments_json != '[]'`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var attachments []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(raw), &attachments) == nil {
			for _, attachment := range attachments {
				if attachment.ID == attachmentID {
					return true, nil
				}
			}
		}
	}
	return false, rows.Err()
}

func (s *Store) PromptTemplates(ctx context.Context) ([]PromptTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,body,created_at,updated_at FROM prompt_templates ORDER BY created_at ASC,id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PromptTemplate, 0)
	for rows.Next() {
		var item PromptTemplate
		if err := rows.Scan(&item.ID, &item.Name, &item.Body, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) PromptTemplateFor(ctx context.Context, id string) (*PromptTemplate, error) {
	var item PromptTemplate
	err := s.db.QueryRowContext(ctx, `SELECT id,name,body,created_at,updated_at FROM prompt_templates WHERE id = ?`, id).Scan(&item.ID, &item.Name, &item.Body, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) CreatePromptTemplate(ctx context.Context, request PromptTemplateRequest) (*PromptTemplate, error) {
	if err := validatePromptTemplateRequest(request); err != nil {
		return nil, err
	}
	var result *PromptTemplate
	err := s.runImmediate(ctx, func() error {
		var existing string
		err := s.db.QueryRowContext(ctx, "SELECT id FROM prompt_templates WHERE name = ?", request.Name).Scan(&existing)
		if err == nil {
			return &PromptTemplateNameConflictError{Name: request.Name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM prompt_templates").Scan(&count); err != nil {
			return err
		}
		if count >= maxPromptTemplates {
			return &PromptTemplateCapacityError{}
		}

		id := ""
		if request.ID != nil && *request.ID != "" {
			id = *request.ID
			var existingID string
			err := s.db.QueryRowContext(ctx, "SELECT id FROM prompt_templates WHERE id = ?", id).Scan(&existingID)
			if err == nil {
				return &PromptTemplateValidationError{}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		} else {
			baseSlug := SlugifyPromptTemplate(request.Name)
			if baseSlug == "" {
				return &PromptTemplateValidationError{}
			}
			id = baseSlug
			suffix := 2
			for {
				var existingID string
				err := s.db.QueryRowContext(ctx, "SELECT id FROM prompt_templates WHERE id = ?", id).Scan(&existingID)
				if errors.Is(err, sql.ErrNoRows) {
					break
				}
				if err != nil {
					return err
				}
				id = fmt.Sprintf("%s-%d", baseSlug, suffix)
				suffix++
			}
		}

		createdAt := formatTimestamp(s.clock.Now())
		updatedAt := createdAt
		if request.UpdatedAt != nil {
			updatedAt = *request.UpdatedAt
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO prompt_templates (id,name,body,created_at,updated_at) VALUES (?,?,?,?,?)`, id, request.Name, request.Body, createdAt, updatedAt); err != nil {
			return err
		}
		result, err = s.PromptTemplateFor(ctx, id)
		return err
	})
	return result, err
}

func (s *Store) UpdatePromptTemplate(ctx context.Context, id string, patch map[string]json.RawMessage) (*PromptTemplate, error) {
	if !IsPortablePromptTemplateID(id) || len(patch) == 0 {
		return nil, &PromptTemplateValidationError{}
	}
	for key := range patch {
		if key != "name" && key != "body" && key != "updated_at" {
			return nil, &PromptTemplateValidationError{}
		}
	}
	// Node validates patch metadata before it looks up the item. Keep that
	// ordering so malformed metadata on an unknown ID remains a 400.
	var patchName, patchBody, updatedAt *string
	if raw, ok := patch["name"]; ok {
		var value string
		if json.Unmarshal(raw, &value) != nil || !validPromptTemplateText(value, maxPromptNameBytes) {
			return nil, &PromptTemplateValidationError{}
		}
		patchName = &value
	}
	if raw, ok := patch["body"]; ok {
		var value string
		if json.Unmarshal(raw, &value) != nil || !validPromptTemplateText(value, maxPromptBodyBytes) {
			return nil, &PromptTemplateValidationError{}
		}
		patchBody = &value
	}
	if raw, ok := patch["updated_at"]; ok {
		var value string
		if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || !isCanonicalPromptTemplateTimestamp(value) {
			return nil, &PromptTemplateValidationError{}
		}
		updatedAt = &value
	}
	existing, err := s.PromptTemplateFor(ctx, id)
	if err != nil || existing == nil {
		return existing, err
	}
	name, body := existing.Name, existing.Body
	if patchName != nil {
		name = *patchName
	}
	if patchBody != nil {
		body = *patchBody
	}
	if err := validatePromptTemplateRequest(PromptTemplateRequest{Name: name, Body: body, UpdatedAt: updatedAt}); err != nil {
		return nil, err
	}
	var result *PromptTemplate
	err = s.runImmediate(ctx, func() error {
		var conflict string
		err := s.db.QueryRowContext(ctx, "SELECT id FROM prompt_templates WHERE name = ? AND id <> ?", name, id).Scan(&conflict)
		if err == nil {
			return &PromptTemplateNameConflictError{Name: name}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if updatedAt == nil {
			value := formatTimestamp(s.clock.Now())
			updatedAt = &value
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE prompt_templates SET name = ?, body = ?, updated_at = ? WHERE id = ?", name, body, *updatedAt, id); err != nil {
			return err
		}
		result, err = s.PromptTemplateFor(ctx, id)
		return err
	})
	return result, err
}

func (s *Store) DeletePromptTemplate(ctx context.Context, id string) (bool, error) {
	if !IsPortablePromptTemplateID(id) {
		return false, nil
	}
	var deleted bool
	err := s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, "DELETE FROM prompt_templates WHERE id = ?", id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		deleted = count == 1
		return err
	})
	return deleted, err
}

func validatePromptTemplateRequest(request PromptTemplateRequest) error {
	if !validPromptTemplateText(request.Name, maxPromptNameBytes) || !validPromptTemplateText(request.Body, maxPromptBodyBytes) {
		return &PromptTemplateValidationError{}
	}
	if request.ID != nil && !IsPortablePromptTemplateID(*request.ID) {
		return &PromptTemplateValidationError{}
	}
	if request.ID != nil && *request.ID != "" && !IsValidPromptTemplateSlug(*request.ID) && !IsPortablePromptTemplateID(*request.ID) {
		return &PromptTemplateValidationError{}
	}
	if (request.ID == nil || *request.ID == "") && SlugifyPromptTemplate(request.Name) == "" {
		return &PromptTemplateValidationError{}
	}
	if request.UpdatedAt != nil && !isCanonicalPromptTemplateTimestamp(*request.UpdatedAt) {
		return &PromptTemplateValidationError{}
	}
	return nil
}

// IsPortablePromptTemplateID applies the cross-router import and item-route limit.
func IsPortablePromptTemplateID(value string) bool {
	return value != "" && utf8.ValidString(value) && !containsNUL(value) && len([]byte(value)) <= 1024
}

func isCanonicalPromptTemplateTimestamp(value string) bool {
	if !promptTemplateTimestampPattern.MatchString(value) {
		return false
	}
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	return err == nil && parsed.UTC().Format("2006-01-02T15:04:05.000Z") == value
}

func validPromptTemplateText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && trimNonEmpty(value) != "" && !containsNUL(value)
}

// trimNonEmpty matches ECMAScript String.prototype.trim's whitespace set.
// unicode.IsSpace is close, but it includes U+0085 and omits U+FEFF.
func trimNonEmpty(value string) string {
	return strings.TrimFunc(value, isECMAScriptTrimSpace)
}

func isECMAScriptTrimSpace(r rune) bool {
	switch {
	case r == '\t', r == '\n', r == '\v', r == '\f', r == '\r':
		return true
	case r == ' ', r == '\u00a0', r == '\ufeff':
		return true
	case r == '\u1680', r == '\u2028', r == '\u2029', r == '\u202f', r == '\u205f', r == '\u3000':
		return true
	case r >= '\u2000' && r <= '\u200a':
		return true
	default:
		return false
	}
}
func containsNUL(value string) bool {
	for _, r := range value {
		if r == 0 {
			return true
		}
	}
	return false
}

// UpdateReminder applies the same content/cadence-only PATCH surface as Node.
type ReminderUpdateResult struct {
	Reminder          *domain.Reminder
	SchedulingChanged bool
}

type ReminderCancellationConflictError struct{ ID, State, LinkedMessageState string }

func (e *ReminderCancellationConflictError) Error() string { return "reminder is not cancelable" }
func (e *ReminderCancellationConflictError) Code() string  { return "reminder_not_cancelable" }

type CancelAllResult struct {
	Canceled int `json:"canceled"`
}

func (s *Store) UpdateReminder(ctx context.Context, id string, patch map[string]json.RawMessage) (*ReminderUpdateResult, error) {
	if !reminderIDPattern.MatchString(id) || len(patch) == 0 {
		return nil, &ReminderValidationError{}
	}
	for key := range patch {
		switch key {
		case "interval_ms", "next_due_at", "body", "subject", "target":
		default:
			return nil, &ReminderValidationError{}
		}
	}
	existing, err := s.ReminderFor(ctx, id)
	if err != nil || existing == nil {
		return nil, err
	}
	candidate := ReminderRequest{ID: existing.ID, Target: existing.Target, Creator: existing.Creator, Subject: existing.Subject, Body: existing.Body, CreatedAt: existing.CreatedAt, NextDueAt: existing.NextDueAt, IntervalMS: existing.IntervalMS, Sentinel: existing.Sentinel}
	nextDueSupplied := false
	for key, raw := range patch {
		switch key {
		case "interval_ms":
			if string(raw) == "null" {
				candidate.IntervalMS = nil
			} else {
				var value int64
				if json.Unmarshal(raw, &value) != nil {
					return nil, &ReminderValidationError{}
				}
				candidate.IntervalMS = &value
			}
		case "next_due_at":
			if json.Unmarshal(raw, &candidate.NextDueAt) != nil {
				return nil, &ReminderValidationError{}
			}
			nextDueSupplied = true
		case "body":
			if json.Unmarshal(raw, &candidate.Body) != nil {
				return nil, &ReminderValidationError{}
			}
		case "subject":
			if string(raw) == "null" {
				candidate.Subject = nil
			} else {
				var value string
				if json.Unmarshal(raw, &value) != nil {
					return nil, &ReminderValidationError{}
				}
				candidate.Subject = &value
			}
		case "target":
			if json.Unmarshal(raw, &candidate.Target) != nil {
				return nil, &ReminderValidationError{}
			}
		}
	}
	if _, err := validateReminderRequest(candidate, s.maxBodyBytes); err != nil {
		return nil, err
	}
	now := formatTimestamp(s.clock.Now())
	changed := !sameInterval(existing.IntervalMS, candidate.IntervalMS) || existing.NextDueAt != candidate.NextDueAt
	if !sameInterval(existing.IntervalMS, candidate.IntervalMS) && !nextDueSupplied && candidate.IntervalMS != nil {
		candidate.NextDueAt, err = protocol.TimestampAfter(now, time.Duration(*candidate.IntervalMS)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		changed = true
	}
	var result *ReminderUpdateResult
	err = s.runImmediate(ctx, func() error {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO agents (agent_id,first_seen_at) VALUES (?,?) ON CONFLICT(agent_id) DO NOTHING`, candidate.Target, now); err != nil {
			return err
		}
		sentinel, err := protocol.SentinelJSON(candidate.Sentinel)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE reminders SET target=?,subject=?,body=?,next_due_at=?,interval_ms=?,sentinel_json=? WHERE id=?`, candidate.Target, nullableSubject(candidate.Subject), candidate.Body, candidate.NextDueAt, nullableInterval(candidate.IntervalMS), nullableString(sentinel), id)
		if err != nil {
			return err
		}
		updated, err := s.reminderForTx(ctx, id)
		if err != nil {
			return err
		}
		result = &ReminderUpdateResult{Reminder: updated, SchedulingChanged: changed}
		return nil
	})
	return result, err
}

func (s *Store) CancelAllReminders(ctx context.Context) (CancelAllResult, error) {
	var result CancelAllResult
	err := s.runImmediate(ctx, func() error {
		var id, state, linked sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT reminders.id,reminders.state,messages.state FROM reminders LEFT JOIN messages ON messages.id=reminders.linked_message_id WHERE reminders.state NOT IN ('scheduled','failed') OR (reminders.state='scheduled' AND reminders.linked_message_id IS NOT NULL) ORDER BY reminders.id ASC LIMIT 1`).Scan(&id, &state, &linked)
		if err == nil {
			return &ReminderCancellationConflictError{ID: id.String, State: state.String, LinkedMessageState: linked.String}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM reminders WHERE state='failed' OR (state='scheduled' AND linked_message_id IS NULL)`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		res, err := s.db.ExecContext(ctx, `DELETE FROM reminders WHERE state='failed' OR (state='scheduled' AND linked_message_id IS NULL)`)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		result.Canceled = int(n)
		for _, id := range ids {
			delete(s.reminderIncarnations, id)
		}
		return err
	})
	return result, err
}

func (s *Store) ReminderIncarnation(ctx context.Context, id string) (string, error) {
	if _, err := s.ReminderFor(ctx, id); err != nil {
		return "", err
	}
	if _, ok := s.reminderIncarnations[id]; !ok {
		value, err := s.ids.Next()
		if err != nil {
			return "", err
		}
		s.reminderIncarnations[id] = value
	}
	return s.reminderIncarnations[id], nil
}

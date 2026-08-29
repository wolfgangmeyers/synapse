package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

const reminderFailureRetry = time.Minute

var reminderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$`)

type ReminderValidationError struct{ Reason string }

func (e *ReminderValidationError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return "invalid reminder request"
}
func (e *ReminderValidationError) Code() string { return "invalid_reminder" }

type ReminderIdempotencyConflictError struct{ ID string }

func (e *ReminderIdempotencyConflictError) Error() string {
	return fmt.Sprintf("reminder ID %s already belongs to a different request", e.ID)
}
func (e *ReminderIdempotencyConflictError) Code() string { return "reminder_idempotency_conflict" }

type ReminderNotCancelableError struct{ ID, State string }

func (e *ReminderNotCancelableError) Error() string {
	return fmt.Sprintf("reminder %s is not cancelable in state %s", e.ID, e.State)
}
func (e *ReminderNotCancelableError) Code() string { return "reminder_not_cancelable" }

type ReminderFireAttempt struct{ ReminderID, ScheduledFor string }
type ReminderFireOutcome struct {
	Reminder            *domain.Reminder
	Message             *domain.Message
	ConversationMessage *domain.ConversationMessage
	ErrorCode           string
}

// ReminderRequest contains the durable fields accepted by the Node store.
type ReminderRequest struct {
	ID, Target, Creator        string
	Subject                    *string
	Body, CreatedAt, NextDueAt string
	IntervalMS                 *int64
	Sentinel                   *protocol.SentinelDescriptor
}

func (s *Store) CreateReminder(ctx context.Context, request ReminderRequest) (bool, *domain.Reminder, error) {
	candidate, err := validateReminderRequest(request, s.maxBodyBytes)
	if err != nil {
		return false, nil, err
	}
	now := formatTimestamp(s.clock.Now())
	var created bool
	var result *domain.Reminder
	err = s.runImmediate(ctx, func() error {
		existing, err := s.reminderForTx(ctx, candidate.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if !sameReminderRequest(existing, candidate) {
				return &ReminderIdempotencyConflictError{ID: candidate.ID}
			}
			result, created = existing, false
			return nil
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?) ON CONFLICT (agent_id) DO NOTHING`, candidate.Target, now); err != nil {
			return err
		}
		sentinel, err := protocol.SentinelJSON(candidate.Sentinel)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO reminders (id,target,creator,subject,body,created_at,next_due_at,interval_ms,sentinel_json,state,linked_message_id,occurrence_number,attempt_count,last_attempt_at) VALUES (?,?,?,?,?,?,?,?,?,'scheduled',NULL,0,0,NULL)`, candidate.ID, candidate.Target, candidate.Creator, nullableSubject(candidate.Subject), candidate.Body, candidate.CreatedAt, candidate.NextDueAt, nullableInterval(candidate.IntervalMS), nullableString(sentinel))
		if err != nil {
			return err
		}
		result, created = nil, true
		result, err = s.reminderForTx(ctx, candidate.ID)
		return err
	})
	return created, result, err
}

func (s *Store) ReminderFor(ctx context.Context, id string) (*domain.Reminder, error) {
	return s.reminderForTx(ctx, id)
}
func (s *Store) Reminders(ctx context.Context) ([]domain.Reminder, error) {
	rows, err := s.db.QueryContext(ctx, reminderSelect+" ORDER BY reminders.next_due_at ASC, reminders.id ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Reminder
	for rows.Next() {
		reminder, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *reminder)
	}
	return result, rows.Err()
}

func (s *Store) CancelReminder(ctx context.Context, id string) (bool, error) {
	var canceled bool
	err := s.runImmediate(ctx, func() error {
		current, err := s.reminderForTx(ctx, id)
		if err != nil || current == nil {
			return err
		}
		if current.State != "failed" && !(current.State == "scheduled" && current.LinkedMessageID == nil) {
			return &ReminderNotCancelableError{ID: id, State: current.State}
		}
		result, err := s.db.ExecContext(ctx, "DELETE FROM reminders WHERE id = ? AND (state = 'failed' OR (state = 'scheduled' AND linked_message_id IS NULL))", id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		canceled = count == 1
		return err
	})
	return canceled, err
}

// FireDueReminders claims and advances due rows one at a time inside immediate
// transactions. Store's single write path makes concurrent calls harmless.
func (s *Store) FireDueReminders(ctx context.Context, onAttempt func(ReminderFireAttempt)) ([]ReminderFireOutcome, error) {
	now := formatTimestamp(s.clock.Now())
	rows, err := s.db.QueryContext(ctx, `SELECT id,next_due_at,last_attempt_at FROM reminders WHERE state = 'scheduled' AND next_due_at <= ? ORDER BY next_due_at ASC,id ASC`, now)
	if err != nil {
		return nil, err
	}
	type dueRow struct {
		id, due string
		last    sql.NullString
	}
	var due []dueRow
	for rows.Next() {
		var row dueRow
		if err := rows.Scan(&row.id, &row.due, &row.last); err != nil {
			_ = rows.Close()
			return nil, err
		}
		due = append(due, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var outcomes []ReminderFireOutcome
	for _, dueRow := range due {
		if dueRow.last.Valid {
			last, parseErr := protocol.ParseTimestamp(dueRow.last.String)
			if parseErr != nil || last.Add(reminderFailureRetry).After(parseTimestamp(now)) {
				continue
			}
		}
		var outcome *ReminderFireOutcome
		err := s.runImmediate(ctx, func() error {
			reminder, err := s.reminderForTx(ctx, dueRow.id)
			if err != nil || reminder == nil || reminder.State != "scheduled" || reminder.NextDueAt > now {
				return err
			}
			if reminder.IntervalMS == nil {
				return s.fireOneShot(ctx, reminder, now, onAttempt, &outcome)
			}
			return s.fireRecurring(ctx, reminder, now, onAttempt, &outcome)
		})
		if err != nil {
			_ = s.recordReminderFailure(ctx, dueRow.id, now)
			outcomes = append(outcomes, ReminderFireOutcome{ErrorCode: reminderErrorCode(err)})
			continue
		}
		if outcome != nil {
			outcomes = append(outcomes, *outcome)
		}
	}
	return outcomes, nil
}

func (s *Store) fireOneShot(ctx context.Context, reminder *domain.Reminder, now string, onAttempt func(ReminderFireAttempt), output **ReminderFireOutcome) error {
	notifyAttempt(onAttempt, ReminderFireAttempt{ReminderID: reminder.ID, ScheduledFor: reminder.NextDueAt})
	message, conversation, err := s.enqueueReminderTx(ctx, reminder, now)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE reminders SET state='message_queued',linked_message_id=? WHERE id=? AND state='scheduled' AND interval_ms IS NULL AND next_due_at <= ?`, message.ID, reminder.ID, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("reminder changed while its due message was firing")
	}
	updated, err := s.reminderForTx(ctx, reminder.ID)
	if err != nil {
		return err
	}
	*output = &ReminderFireOutcome{Reminder: updated, Message: message, ConversationMessage: conversation}
	return nil
}

func (s *Store) fireRecurring(ctx context.Context, reminder *domain.Reminder, now string, onAttempt func(ReminderFireAttempt), output **ReminderFireOutcome) error {
	var lastConnected sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT last_connected_at FROM agents WHERE agent_id = ?", reminder.Target).Scan(&lastConnected)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !lastConnected.Valid {
		_, err = s.db.ExecContext(ctx, "DELETE FROM agents WHERE agent_id = ?", reminder.Target)
		return err
	}
	interval := time.Duration(*reminder.IntervalMS) * time.Millisecond
	next, err := recurringDueAtAfterNow(reminder.NextDueAt, interval, now)
	if err != nil {
		return err
	}
	var leaseExpires sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT expires_at FROM agent_leases WHERE agent_id = ?", reminder.Target).Scan(&leaseExpires)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && leaseExpires.String <= now) {
		connectedAt, parseErr := protocol.ParseTimestamp(lastConnected.String)
		if parseErr != nil {
			return parseErr
		}
		current, _ := protocol.ParseTimestamp(now)
		if current.Sub(connectedAt) >= time.Hour {
			_, err = s.db.ExecContext(ctx, "DELETE FROM agents WHERE agent_id = ?", reminder.Target)
			return err
		}
		_, err = s.db.ExecContext(ctx, "UPDATE reminders SET next_due_at=? WHERE id=? AND state='scheduled'", next, reminder.ID)
		return err
	}
	if err != nil {
		return err
	}
	if reminder.LinkedMessageID != nil {
		_, err = s.db.ExecContext(ctx, "UPDATE reminders SET next_due_at=? WHERE id=? AND state='scheduled'", next, reminder.ID)
		return err
	}
	prior, _ := protocol.ParseTimestamp(reminder.NextDueAt)
	current, _ := protocol.ParseTimestamp(now)
	if !prior.Add(interval).After(current) {
		_, err = s.db.ExecContext(ctx, "UPDATE reminders SET next_due_at=? WHERE id=? AND state='scheduled' AND linked_message_id IS NULL", next, reminder.ID)
		return err
	}
	notifyAttempt(onAttempt, ReminderFireAttempt{ReminderID: reminder.ID, ScheduledFor: reminder.NextDueAt})
	message, conversation, err := s.enqueueReminderTx(ctx, reminder, now)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE reminders SET linked_message_id=?,occurrence_number=occurrence_number+1,next_due_at=? WHERE id=? AND state='scheduled' AND interval_ms IS NOT NULL AND linked_message_id IS NULL AND next_due_at <= ?`, message.ID, next, reminder.ID, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("reminder changed while its due message was firing")
	}
	updated, err := s.reminderForTx(ctx, reminder.ID)
	if err != nil {
		return err
	}
	*output = &ReminderFireOutcome{Reminder: updated, Message: message, ConversationMessage: conversation}
	return nil
}

func (s *Store) enqueueReminderTx(ctx context.Context, reminder *domain.Reminder, now string) (*domain.Message, *domain.ConversationMessage, error) {
	id := fmt.Sprintf("reminder-%s-%d", reminder.ID, reminder.OccurrenceNumber)
	source := protocol.MessageSource{Kind: "reminder"}
	candidate := protocol.Envelope{ID: id, From: "reminders", To: reminder.Target, Subject: reminder.Subject, Body: reminder.Body, CreatedAt: now, Sentinel: reminder.Sentinel, Source: &source}
	if err := protocol.ValidateEnvelope(candidate, s.maxBodyBytes); err != nil {
		return nil, nil, err
	}
	conversation := &conversationCandidate{Envelope: candidate}
	if existing, err := s.messageForTx(ctx, id); err != nil {
		return nil, nil, err
	} else if existing != nil {
		incarnation, err := s.ids.Next()
		if err != nil {
			return nil, nil, err
		}
		row, err := s.insertConversation(ctx, conversation, incarnation, existing.ID, now)
		return existing, row, err
	}
	sentinel, err := protocol.SentinelJSON(candidate.Sentinel)
	if err != nil {
		return nil, nil, err
	}
	duplicate, err := s.selectLiveDuplicate(ctx, candidate, sentinel)
	if err != nil {
		return nil, nil, err
	}
	if duplicate != nil {
		incarnation, err := s.ids.Next()
		if err != nil {
			return nil, nil, err
		}
		row, err := s.insertConversation(ctx, conversation, incarnation, duplicate.ID, now)
		return duplicate, row, err
	}
	count, bytes, err := s.queueUsage(ctx, candidate.To)
	if err != nil {
		return nil, nil, err
	}
	if count >= s.maxQueuedMessages || bytes+envelopeBytes(candidate) > s.maxQueuedBytes {
		return nil, nil, &QueueLimitError{Recipient: candidate.To}
	}
	expires, err := protocol.TimestampAfter(now, time.Hour)
	if err != nil {
		return nil, nil, err
	}
	incarnation, err := s.ids.Next()
	if err != nil {
		return nil, nil, err
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO agents (agent_id,first_seen_at) VALUES (?,?) ON CONFLICT(agent_id) DO NOTHING", candidate.To, now); err != nil {
		return nil, nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO messages (id,sender,recipient,subject,body,created_at,expires_at,message_incarnation_id,sentinel_json,source_kind,state) VALUES (?,?,?,?,?,?,?,?,?,'reminder','queued')`, candidate.ID, candidate.From, candidate.To, nullableSubject(candidate.Subject), candidate.Body, candidate.CreatedAt, expires, incarnation, nullableString(mustSentinelJSON(candidate.Sentinel)))
	if err != nil {
		return nil, nil, err
	}
	message, err := s.messageForTx(ctx, id)
	if err != nil || message == nil {
		return message, nil, err
	}
	row, err := s.insertConversation(ctx, conversation, message.MessageIncarnationID, message.ID, now)
	if err != nil {
		return nil, nil, err
	}
	return message, row, nil
}

func mustSentinelJSON(value *protocol.SentinelDescriptor) string {
	result, _ := protocol.SentinelJSON(value)
	return result
}
func (s *Store) recordReminderFailure(ctx context.Context, id, now string) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, "UPDATE reminders SET attempt_count=attempt_count+1,last_attempt_at=? WHERE id=? AND state='scheduled'", now, id)
		return err
	})
}
func notifyAttempt(callback func(ReminderFireAttempt), attempt ReminderFireAttempt) {
	if callback == nil {
		return
	}
	defer func() { _ = recover() }()
	callback(attempt)
}
func reminderErrorCode(err error) string {
	if coded, ok := err.(interface{ Code() string }); ok {
		return coded.Code()
	}
	return "internal_error"
}
func nullableInterval(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func validateReminderRequest(request ReminderRequest, maxBodyBytes int) (ReminderRequest, error) {
	if !reminderIDPattern.MatchString(request.ID) {
		return ReminderRequest{}, &ReminderValidationError{Reason: "reminder id is invalid"}
	}
	if request.IntervalMS != nil && *request.IntervalMS <= 0 {
		return ReminderRequest{}, &ReminderValidationError{Reason: "interval_ms must be null or a positive safe integer"}
	}
	candidate := protocol.Envelope{ID: "reminder-" + request.ID + "-0", From: request.Creator, To: request.Target, Subject: request.Subject, Body: request.Body, CreatedAt: request.CreatedAt, Sentinel: request.Sentinel}
	if err := protocol.ValidateEnvelope(candidate, maxBodyBytes); err != nil {
		return ReminderRequest{}, &ReminderValidationError{}
	}
	if _, err := protocol.ParseTimestamp(request.NextDueAt); err != nil {
		return ReminderRequest{}, &ReminderValidationError{}
	}
	if request.IntervalMS != nil {
		due, _ := protocol.ParseTimestamp(request.NextDueAt)
		if due.Add(time.Duration(*request.IntervalMS) * time.Millisecond).Before(due) {
			return ReminderRequest{}, &ReminderValidationError{}
		}
	}
	return request, nil
}
func sameReminderRequest(current *domain.Reminder, candidate ReminderRequest) bool {
	return current.ID == candidate.ID && current.Target == candidate.Target && current.Creator == candidate.Creator && subjectEqual(current.Subject, candidate.Subject) && current.Body == candidate.Body && current.CreatedAt == candidate.CreatedAt && current.NextDueAt == candidate.NextDueAt && sameInterval(current.IntervalMS, candidate.IntervalMS) && protocol.SameSentinel(current.Sentinel, candidate.Sentinel)
}
func sameInterval(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

const reminderSelect = `SELECT reminders.id,reminders.target,reminders.creator,reminders.subject,reminders.body,reminders.created_at,reminders.next_due_at,reminders.interval_ms,reminders.sentinel_json,reminders.state,reminders.linked_message_id,messages.state,reminders.occurrence_number,reminders.attempt_count,reminders.last_attempt_at FROM reminders LEFT JOIN messages ON messages.id=reminders.linked_message_id`

func (s *Store) reminderForTx(ctx context.Context, id string) (*domain.Reminder, error) {
	return scanReminder(s.db.QueryRowContext(ctx, reminderSelect+" WHERE reminders.id = ?", id))
}
func scanReminder(row interface{ Scan(...any) error }) (*domain.Reminder, error) {
	var result domain.Reminder
	var subject, sentinel, linked, linkedState, last sql.NullString
	var interval sql.NullInt64
	if err := row.Scan(&result.ID, &result.Target, &result.Creator, &subject, &result.Body, &result.CreatedAt, &result.NextDueAt, &interval, &sentinel, &result.State, &linked, &linkedState, &result.OccurrenceNumber, &result.AttemptCount, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if subject.Valid {
		result.Subject = &subject.String
	}
	if interval.Valid {
		result.IntervalMS = &interval.Int64
	}
	if linked.Valid {
		result.LinkedMessageID = &linked.String
	}
	if linkedState.Valid {
		result.LinkedMessageState = &linkedState.String
	}
	if last.Valid {
		result.LastAttemptAt = &last.String
	}
	if sentinel.Valid {
		if err := json.Unmarshal([]byte(sentinel.String), &result.Sentinel); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

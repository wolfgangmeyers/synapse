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

const (
	DefaultDeliveryLeaseDuration = 30 * time.Second
	DefaultMaxDeliveryAttempts   = 3
)

type IdempotencyConflictError struct{ ID string }

func (e *IdempotencyConflictError) Error() string {
	return fmt.Sprintf("message ID %s already belongs to a different envelope", e.ID)
}
func (e *IdempotencyConflictError) Code() string { return "idempotency_conflict" }

type QueueLimitError struct{ Recipient string }

func (e *QueueLimitError) Error() string {
	return fmt.Sprintf("recipient %s has reached its queue limit", e.Recipient)
}
func (e *QueueLimitError) Code() string { return "queue_full" }

type DuplicateAgentLeaseError struct{ AgentID, ActiveInstanceID string }

func (e *DuplicateAgentLeaseError) Error() string {
	return fmt.Sprintf("agent %s already has an active lease", e.AgentID)
}
func (e *DuplicateAgentLeaseError) Code() string { return "duplicate_identity" }

type LeaseNotActiveError struct{ AgentID string }

func (e *LeaseNotActiveError) Error() string {
	return fmt.Sprintf("agent %s does not own an active lease", e.AgentID)
}
func (e *LeaseNotActiveError) Code() string { return "lease_not_active" }

type DeliveryNotActiveError struct {
	MessageID     string
	AttemptNumber int
}

func (e *DeliveryNotActiveError) Error() string {
	return fmt.Sprintf("delivery attempt %s/%d is not active", e.MessageID, e.AttemptNumber)
}
func (e *DeliveryNotActiveError) Code() string { return "delivery_not_active" }

type ConversationOptions struct {
	Body        *string
	Attachments []json.RawMessage
	Source      *protocol.MessageSource
}
type EnqueueOptions struct {
	DisableBatching bool
	Conversation    *ConversationOptions
}
type EnqueueResult struct {
	Created             bool                        `json:"created"`
	Deduped             bool                        `json:"deduped,omitempty"`
	Message             domain.Message              `json:"message"`
	ConversationMessage *domain.ConversationMessage `json:"conversation_message,omitempty"`
	ConversationCreated bool                        `json:"conversation_created,omitempty"`
}
type LeaseRequest struct {
	AgentID, InstanceID, LeaseID string
	LeaseDuration                time.Duration
}
type ClaimRequest struct {
	AgentID, InstanceID, LeaseID string
	DeliveryLeaseDuration        time.Duration
}
type ReceiptRequest struct {
	AgentID, InstanceID, LeaseID, MessageID string
	AttemptNumber                           int
	DeliveryItems                           []protocol.DeliveryItem
	AllowBatchWithoutDeliveryItems          bool
}
type FailureResult struct {
	MessageID                string            `json:"message_id"`
	State                    string            `json:"state"`
	AgentID                  string            `json:"agent_id,omitempty"`
	Attempt                  int               `json:"attempt,omitempty"`
	LeaseID                  string            `json:"lease_id,omitempty"`
	RecurringReminderRearmed []RearmedReminder `json:"recurring_reminder_rearmed,omitempty"`
}
type RearmedReminder struct {
	ReminderID string `json:"reminder_id"`
	NextDueAt  string `json:"next_due_at"`
}
type BatchFailureResult struct {
	MessageID                string            `json:"message_id"`
	State                    string            `json:"state"`
	RecurringReminderRearmed []RearmedReminder `json:"recurring_reminder_rearmed,omitempty"`
}

func (s *Store) Enqueue(ctx context.Context, envelope protocol.Envelope, options ...EnqueueOptions) (EnqueueResult, error) {
	if err := protocol.ValidateEnvelope(envelope, s.maxBodyBytes); err != nil {
		return EnqueueResult{}, err
	}
	candidate := protocol.CopyEnvelope(envelope)
	source := protocol.NormalizeSource(candidate.Source)
	candidate.Source = &source
	sentinelJSON, err := protocol.SentinelJSON(candidate.Sentinel)
	if err != nil {
		return EnqueueResult{}, err
	}
	var enqueueOptions EnqueueOptions
	if len(options) > 0 {
		enqueueOptions = options[0]
	}
	_ = enqueueOptions.DisableBatching // batching is a claim-time property.
	conversation, err := prepareConversation(candidate, enqueueOptions.Conversation)
	if err != nil {
		return EnqueueResult{}, err
	}
	var result EnqueueResult
	err = s.runImmediate(ctx, func() error {
		if conversation != nil {
			latest, err := s.latestConversation(ctx, candidate.ID)
			if err != nil {
				return err
			}
			if latest != nil {
				if !sameConversation(latest, conversation) {
					return &IdempotencyConflictError{ID: candidate.ID}
				}
				message, err := s.messageForTx(ctx, latest.DeliveryMessageID)
				if err != nil {
					return err
				}
				if message == nil {
					return errors.New("conversation delivery message disappeared")
				}
				result = EnqueueResult{Message: *message, ConversationMessage: latest}
				return nil
			}
		}
		existing, err := s.messageForTx(ctx, candidate.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if !sameEnvelope(existing, candidate) {
				return &IdempotencyConflictError{ID: candidate.ID}
			}
			result = EnqueueResult{Message: *existing}
			return nil
		}
		duplicate, err := s.selectLiveDuplicate(ctx, candidate, sentinelJSON)
		if err != nil {
			return err
		}
		if duplicate != nil {
			result = EnqueueResult{Deduped: true, Message: *duplicate}
			if conversation != nil {
				incarnation, err := s.ids.Next()
				if err != nil {
					return err
				}
				recorded := formatTimestamp(s.clock.Now())
				row, err := s.insertConversation(ctx, conversation, incarnation, duplicate.ID, recorded)
				if err != nil {
					return err
				}
				result.ConversationMessage, result.ConversationCreated = row, true
			}
			return nil
		}
		count, bytes, err := s.queueUsage(ctx, candidate.To)
		if err != nil {
			return err
		}
		if count >= s.maxQueuedMessages || bytes+envelopeBytes(candidate) > s.maxQueuedBytes {
			return &QueueLimitError{Recipient: candidate.To}
		}
		now := formatTimestamp(s.clock.Now())
		expires, err := protocol.TimestampAfter(now, time.Hour)
		if err != nil {
			return err
		}
		incarnation, err := s.ids.Next()
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO agents (agent_id, first_seen_at) VALUES (?, ?) ON CONFLICT (agent_id) DO NOTHING`, candidate.To, now); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO messages (id, sender, recipient, subject, body, created_at, expires_at, message_incarnation_id, sentinel_json, source_kind, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued')`, candidate.ID, candidate.From, candidate.To, nullableSubject(candidate.Subject), candidate.Body, candidate.CreatedAt, expires, incarnation, nullableString(sentinelJSON), source.Kind)
		if err != nil {
			return err
		}
		message, err := s.messageForTx(ctx, candidate.ID)
		if err != nil {
			return err
		}
		if message == nil {
			return errors.New("message disappeared after enqueue")
		}
		result = EnqueueResult{Created: true, Message: *message}
		if conversation != nil {
			row, err := s.insertConversation(ctx, conversation, message.MessageIncarnationID, message.ID, formatTimestamp(s.clock.Now()))
			if err != nil {
				return err
			}
			result.ConversationMessage, result.ConversationCreated = row, true
		}
		return nil
	})
	return result, err
}

func (s *Store) AcquireAgentLease(ctx context.Context, request LeaseRequest) (domain.Lease, error) {
	return s.acquireOrRenewLease(ctx, request, false)
}
func (s *Store) RenewAgentLease(ctx context.Context, request LeaseRequest) (domain.Lease, error) {
	return s.acquireOrRenewLease(ctx, request, true)
}
func (s *Store) acquireOrRenewLease(ctx context.Context, request LeaseRequest, renew bool) (domain.Lease, error) {
	if err := protocol.ValidateLeaseIdentity(request.AgentID, request.InstanceID, request.LeaseID); err != nil {
		return domain.Lease{}, err
	}
	if request.LeaseDuration == 0 {
		request.LeaseDuration = DefaultDeliveryLeaseDuration
	}
	if request.LeaseDuration <= 0 {
		return domain.Lease{}, errors.New("leaseDurationMs must be a positive safe integer")
	}
	now := formatTimestamp(s.clock.Now())
	expires, err := protocol.TimestampAfter(now, request.LeaseDuration)
	if err != nil {
		return domain.Lease{}, err
	}
	var result domain.Lease
	err = s.runImmediate(ctx, func() error {
		existing, err := s.leaseForTx(ctx, request.AgentID)
		if err != nil {
			return err
		}
		if renew {
			if existing == nil || existing.LeaseID != request.LeaseID || existing.InstanceID != request.InstanceID {
				return &LeaseNotActiveError{AgentID: request.AgentID}
			}
			if existing.ExpiresAt <= now {
				_ = s.deleteLease(ctx, request.AgentID)
				_ = s.markDisconnected(ctx, request.AgentID, now)
				return &LeaseNotActiveError{AgentID: request.AgentID}
			}
			_, err := s.db.ExecContext(ctx, "UPDATE agent_leases SET expires_at = ? WHERE agent_id = ?", expires, request.AgentID)
			if err != nil {
				return err
			}
			result = domain.Lease{LeaseID: request.LeaseID, AgentID: request.AgentID, InstanceID: request.InstanceID, AcquiredAt: existing.AcquiredAt, ExpiresAt: expires}
			return nil
		}
		recovered := existing != nil && existing.ExpiresAt <= now
		if existing != nil && !recovered && existing.InstanceID != request.InstanceID {
			return &DuplicateAgentLeaseError{AgentID: request.AgentID, ActiveInstanceID: existing.InstanceID}
		}
		if existing != nil {
			if err := s.deleteLease(ctx, request.AgentID); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO agents (agent_id, first_seen_at, last_connected_at, disconnected_at) VALUES (?, ?, ?, NULL) ON CONFLICT (agent_id) DO UPDATE SET last_connected_at = excluded.last_connected_at, disconnected_at = NULL`, request.AgentID, now, now); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_leases (lease_id, agent_id, instance_id, acquired_at, expires_at) VALUES (?, ?, ?, ?, ?)`, request.LeaseID, request.AgentID, request.InstanceID, now, expires); err != nil {
			return err
		}
		result = domain.Lease{LeaseID: request.LeaseID, AgentID: request.AgentID, InstanceID: request.InstanceID, AcquiredAt: now, ExpiresAt: expires, Recovered: recovered}
		return nil
	})
	return result, err
}
func (s *Store) ReleaseAgentLease(ctx context.Context, request LeaseRequest) (bool, error) {
	if err := protocol.ValidateLeaseIdentity(request.AgentID, request.InstanceID, request.LeaseID); err != nil {
		return false, err
	}
	now := formatTimestamp(s.clock.Now())
	released := false
	err := s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, "DELETE FROM agent_leases WHERE agent_id = ? AND instance_id = ? AND lease_id = ?", request.AgentID, request.InstanceID, request.LeaseID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err == nil && count == 1 {
			released = true
			err = s.markDisconnected(ctx, request.AgentID, now)
		}
		return err
	})
	return released, err
}

func (s *Store) ClaimNextMessage(ctx context.Context, request ClaimRequest) (*domain.Delivery, error) {
	if err := protocol.ValidateLeaseIdentity(request.AgentID, request.InstanceID, request.LeaseID); err != nil {
		return nil, err
	}
	if request.DeliveryLeaseDuration == 0 {
		request.DeliveryLeaseDuration = DefaultDeliveryLeaseDuration
	}
	if request.DeliveryLeaseDuration <= 0 {
		return nil, errors.New("deliveryLeaseDurationMs must be a positive safe integer")
	}
	now := formatTimestamp(s.clock.Now())
	expires, err := protocol.TimestampAfter(now, request.DeliveryLeaseDuration)
	if err != nil {
		return nil, err
	}
	var result *domain.Delivery
	err = s.runImmediate(ctx, func() error {
		if err := s.requireActiveLease(ctx, request.AgentID, request.InstanceID, request.LeaseID, now); err != nil {
			return err
		}
		active, err := s.activeDelivery(ctx, request.AgentID, request.LeaseID)
		if err != nil {
			return err
		}
		if active != nil {
			result = active
			return nil
		}
		rows, err := s.queuedRows(ctx, request.AgentID, now)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		batchID, err := s.ids.Next()
		if err != nil {
			return err
		}
		items := make([]protocol.DeliveryItem, 0, len(rows))
		for i := range rows {
			if rows[i].MessageIncarnationID == "" || !protocol.ValidIncarnation(rows[i].MessageIncarnationID) {
				id, err := s.ids.Next()
				if err != nil {
					return err
				}
				rows[i].MessageIncarnationID = id
				if _, err := s.db.ExecContext(ctx, "UPDATE messages SET message_incarnation_id = ? WHERE id = ? AND state = 'queued'", id, rows[i].ID); err != nil {
					return err
				}
			}
			attempt, err := s.nextAttempt(ctx, rows[i].ID)
			if err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = 'leased', claimed_at = COALESCE(claimed_at, ?), retry_batch_id = NULL WHERE id = ? AND state = 'queued'", now, rows[i].ID); err != nil {
				return err
			}
			rows[i].State = domain.MessageLeased
			if _, err := s.db.ExecContext(ctx, `INSERT INTO delivery_attempts (message_id, attempt_number, lease_id, owner_lease_id, state, created_at, updated_at, delivery_expires_at, delivery_batch_id) VALUES (?, ?, ?, ?, 'leased', ?, ?, ?, ?)`, rows[i].ID, attempt, request.LeaseID, request.LeaseID, now, now, expires, batchID); err != nil {
				return err
			}
			items = append(items, protocol.DeliveryItem{MessageID: rows[i].ID, MessageIncarnationID: rows[i].MessageIncarnationID, AttemptNumber: attempt, DeliveryExpiresAt: expires})
		}
		result = deliveryFromRows(rows, items, expires)
		return nil
	})
	return result, err
}

func (s *Store) RecordInjectionRequested(ctx context.Context, request ReceiptRequest) (domain.Delivery, error) {
	if err := protocol.ValidateDeliveryIdentity(protocol.DeliveryReceipt{AgentID: request.AgentID, InstanceID: request.InstanceID, LeaseID: request.LeaseID, MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}); err != nil {
		return domain.Delivery{}, err
	}
	now := formatTimestamp(s.clock.Now())
	var result domain.Delivery
	err := s.runImmediate(ctx, func() error {
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		if expiredDelivery(deliveries, now) {
			return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
		}
		allInjectionRequested := true
		for _, d := range deliveries {
			if d.AttemptState != domain.DeliveryInjectionRequested {
				allInjectionRequested = false
				break
			}
		}
		if !allInjectionRequested {
			for _, d := range deliveries {
				if d.AttemptState != domain.DeliveryLeased || d.MessageState != domain.MessageLeased {
					return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
				}
			}
			for _, d := range deliveries {
				if _, err := s.db.ExecContext(ctx, "UPDATE delivery_attempts SET state = 'injection_requested', updated_at = ? WHERE message_id = ? AND attempt_number = ? AND state = 'leased'", now, d.MessageID, d.AttemptNumber); err != nil {
					return err
				}
				if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = 'injection_requested' WHERE id = ? AND state = 'leased'", d.MessageID); err != nil {
					return err
				}
			}
		}
		stored := make([]storedDeliveryRow, 0, len(deliveries))
		items := make([]protocol.DeliveryItem, 0, len(deliveries))
		for _, d := range deliveries {
			message, err := s.messageForTx(ctx, d.MessageID)
			if err != nil {
				return err
			}
			if message == nil {
				return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
			}
			stored = append(stored, storedDeliveryRow{Message: *message})
			items = append(items, protocol.DeliveryItem{MessageID: d.MessageID, MessageIncarnationID: d.MessageIncarnationID, AttemptNumber: d.AttemptNumber, DeliveryExpiresAt: d.DeliveryExpiresAt})
		}
		delivery := deliveryFromRows(stored, items, deliveries[0].DeliveryExpiresAt)
		result = *delivery
		return nil
	})
	return result, err
}

func (s *Store) AcknowledgeDelivery(ctx context.Context, request ReceiptRequest) (protocol.DeliveryItem, error) {
	if err := protocol.ValidateDeliveryIdentity(protocol.DeliveryReceipt{AgentID: request.AgentID, InstanceID: request.InstanceID, LeaseID: request.LeaseID, MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}); err != nil {
		return protocol.DeliveryItem{}, err
	}
	now := formatTimestamp(s.clock.Now())
	var item protocol.DeliveryItem
	err := s.runImmediate(ctx, func() error {
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		if expiredDelivery(deliveries, now) {
			return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
		}
		for _, d := range deliveries {
			if d.AttemptState != domain.DeliveryInjectionRequested || d.MessageState != domain.MessageInjectionRequested {
				return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
			}
		}
		for _, d := range deliveries {
			if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = 'acknowledged' WHERE id = ?", d.MessageID); err != nil {
				return err
			}
			if err := s.completeLinkedReminder(ctx, d.MessageID); err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ?", d.MessageID); err != nil {
				return err
			}
		}
		item = protocol.DeliveryItem{MessageID: request.MessageID, MessageIncarnationID: deliveries[0].MessageIncarnationID, AttemptNumber: request.AttemptNumber, DeliveryExpiresAt: deliveries[0].DeliveryExpiresAt}
		return nil
	})
	return item, err
}

func (s *Store) NackDelivery(ctx context.Context, request ReceiptRequest, reason string, maxAttempts int) (BatchFailureResult, error) {
	if err := protocol.ValidateDeliveryIdentity(protocol.DeliveryReceipt{AgentID: request.AgentID, InstanceID: request.InstanceID, LeaseID: request.LeaseID, MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}); err != nil {
		return BatchFailureResult{}, err
	}
	if !protocol.ValidFailureReason(reason) {
		return BatchFailureResult{}, errors.New("reason must be a safe failure code")
	}
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxDeliveryAttempts
	}
	if maxAttempts < 1 {
		return BatchFailureResult{}, errors.New("maxDeliveryAttempts must be a positive safe integer")
	}
	now := formatTimestamp(s.clock.Now())
	var result BatchFailureResult
	err := s.runImmediate(ctx, func() error {
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		if expiredDelivery(deliveries, now) {
			return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
		}
		for _, d := range deliveries {
			if (d.AttemptState != domain.DeliveryLeased && d.AttemptState != domain.DeliveryInjectionRequested) || (d.MessageState != domain.MessageLeased && d.MessageState != domain.MessageInjectionRequested) {
				return &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
			}
		}
		result = BatchFailureResult{MessageID: request.MessageID, State: domain.MessageQueued}
		for _, d := range deliveries {
			outcome, err := s.failDelivery(ctx, d, "nacked", reason, now, maxAttempts)
			if err != nil {
				return err
			}
			if outcome.State == domain.MessageDeadLetter {
				result.State = domain.MessageDeadLetter
			}
			result.RecurringReminderRearmed = append(result.RecurringReminderRearmed, outcome.RecurringReminderRearmed...)
		}
		return nil
	})
	return result, err
}

func (s *Store) RecoverTimedOutDeliveries(ctx context.Context, maxAttempts int) ([]FailureResult, error) {
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxDeliveryAttempts
	}
	if maxAttempts < 1 {
		return nil, errors.New("maxDeliveryAttempts must be a positive safe integer")
	}
	now := formatTimestamp(s.clock.Now())
	var result []FailureResult
	err := s.runImmediate(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT messages.id, messages.recipient, messages.state, delivery_attempts.attempt_number, delivery_attempts.owner_lease_id, delivery_attempts.delivery_batch_id, delivery_attempts.state FROM delivery_attempts JOIN messages ON messages.id = delivery_attempts.message_id WHERE delivery_attempts.state IN ('leased', 'injection_requested') AND messages.state IN ('leased', 'injection_requested') AND delivery_attempts.delivery_expires_at <= ? ORDER BY messages.enqueue_sequence ASC`, now)
		if err != nil {
			return err
		}
		var timedOut []storedDelivery
		for rows.Next() {
			var d storedDelivery
			var batch sql.NullString
			if err := rows.Scan(&d.MessageID, &d.AgentID, &d.MessageState, &d.AttemptNumber, &d.OwnerLeaseID, &batch, &d.AttemptState); err != nil {
				_ = rows.Close()
				return err
			}
			if batch.Valid {
				d.BatchID = batch.String
			}
			timedOut = append(timedOut, d)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, d := range timedOut {
			outcome, err := s.failDelivery(ctx, d, "timed_out", "delivery_timeout", now, maxAttempts)
			if err != nil {
				return err
			}
			result = append(result, FailureResult{MessageID: outcome.MessageID, State: outcome.State, AgentID: d.AgentID, Attempt: d.AttemptNumber, LeaseID: d.OwnerLeaseID})
		}
		return nil
	})
	return result, err
}

func (s *Store) RecoverExpiredUnclaimedMessages(ctx context.Context) ([]string, error) {
	now := formatTimestamp(s.clock.Now())
	var expired []string
	err := s.runImmediate(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, "SELECT id FROM messages WHERE state = 'queued' AND claimed_at IS NULL AND expires_at <= ? ORDER BY enqueue_sequence ASC", now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			expired = append(expired, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range expired {
			if err := s.completeLinkedReminder(ctx, id); err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ? AND state = 'queued' AND claimed_at IS NULL AND expires_at <= ?", id, now); err != nil {
				return err
			}
		}
		return nil
	})
	return expired, err
}

func (s *Store) RetryDeadLetter(ctx context.Context, messageID string) (*domain.Message, error) {
	if messageID == "" {
		return nil, errors.New("messageId is required")
	}
	var message *domain.Message
	now := formatTimestamp(s.clock.Now())
	err := s.runImmediate(ctx, func() error {
		current, err := s.messageForTx(ctx, messageID)
		if err != nil || current == nil || current.State != domain.MessageDeadLetter {
			return err
		}
		count, bytes, err := s.queueUsage(ctx, current.To)
		if err != nil {
			return err
		}
		if count >= s.maxQueuedMessages || bytes+envelopeBytes(current.Envelope()) > s.maxQueuedBytes {
			return &QueueLimitError{Recipient: current.To}
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM delivery_attempts WHERE message_id = ?", messageID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = 'queued' WHERE id = ? AND state = 'dead_letter'", messageID); err != nil {
			return err
		}
		if err := s.rearmFailedReminder(ctx, messageID, now); err != nil {
			return err
		}
		message, err = s.messageForTx(ctx, messageID)
		return err
	})
	return message, err
}
func (s *Store) DismissDeadLetter(ctx context.Context, messageID string) (bool, error) {
	if messageID == "" {
		return false, errors.New("messageId is required")
	}
	dismissed := false
	err := s.runImmediate(ctx, func() error {
		current, err := s.messageForTx(ctx, messageID)
		if err != nil || current == nil || current.State != domain.MessageDeadLetter {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "DELETE FROM reminders WHERE linked_message_id = ? AND state = 'failed'", messageID); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE id = ? AND state = 'dead_letter'", messageID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		dismissed = count == 1
		return err
	})
	return dismissed, err
}

func (s *Store) MessageFor(ctx context.Context, messageID string) (*domain.Message, error) {
	return s.messageForTx(ctx, messageID)
}
func (s *Store) LeaseFor(ctx context.Context, agentID string) (*domain.Lease, error) {
	return s.leaseForTx(ctx, agentID)
}

// Stored delivery rows and SQL helpers follow the Node query ordering exactly.
type storedDeliveryRow struct {
	domain.Message
	RetryBatchID *string
}
type storedDelivery struct {
	MessageID, AgentID, MessageState, MessageIncarnationID, OwnerLeaseID, BatchID, AttemptState string
	AttemptNumber                                                                               int
	DeliveryExpiresAt                                                                           string
}

func (s *Store) messageForTx(ctx context.Context, id string) (*domain.Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT enqueue_sequence, id, sender, recipient, subject, body, created_at, expires_at, message_incarnation_id, state, sentinel_json, source_kind FROM messages WHERE id = ?`, id)
	return scanMessage(row)
}
func scanMessage(row interface{ Scan(...any) error }) (*domain.Message, error) {
	var m domain.Message
	var subject, sentinel, source sql.NullString
	if err := row.Scan(&m.EnqueueSequence, &m.ID, &m.From, &m.To, &subject, &m.Body, &m.CreatedAt, &m.ExpiresAt, &m.MessageIncarnationID, &m.State, &sentinel, &source); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if subject.Valid {
		m.Subject = &subject.String
	}
	m.Source = protocol.MessageSource{Kind: "unknown"}
	if source.Valid {
		m.Source.Kind = source.String
	}
	if sentinel.Valid {
		var d protocol.SentinelDescriptor
		if err := json.Unmarshal([]byte(sentinel.String), &d); err != nil {
			return nil, err
		}
		m.Sentinel = &d
	}
	return &m, nil
}
func sameEnvelope(m *domain.Message, e protocol.Envelope) bool {
	return m.ID == e.ID && m.From == e.From && m.To == e.To && subjectEqual(m.Subject, e.Subject) && m.Body == e.Body && m.CreatedAt == e.CreatedAt && protocol.SameSentinel(m.Sentinel, e.Sentinel)
}
func subjectEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func nullableSubject(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func envelopeBytes(e protocol.Envelope) int {
	n := len([]byte(e.ID)) + len([]byte(e.From)) + len([]byte(e.To)) + len([]byte(e.Body)) + len([]byte(e.CreatedAt))
	if e.Subject != nil {
		n += len([]byte(*e.Subject))
	}
	return n
}
func (s *Store) selectLiveDuplicate(ctx context.Context, e protocol.Envelope, sentinel string) (*domain.Message, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id FROM messages WHERE id <> ? AND sender = ? AND recipient = ? AND subject IS ? AND body = ? AND sentinel_json IS ? AND state IN ('queued','leased','injection_requested') ORDER BY enqueue_sequence ASC LIMIT 1`, e.ID, e.From, e.To, nullableSubject(e.Subject), e.Body, nullableString(sentinel))
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s.messageForTx(ctx, id)
}
func (s *Store) queueUsage(ctx context.Context, recipient string) (int, int, error) {
	var count, bytes int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(id AS BLOB))+length(CAST(sender AS BLOB))+length(CAST(recipient AS BLOB))+length(CAST(COALESCE(subject,'') AS BLOB))+length(CAST(body AS BLOB))+length(CAST(created_at AS BLOB))),0) FROM messages WHERE recipient = ? AND state IN ('queued','leased','injection_requested')`, recipient).Scan(&count, &bytes)
	return count, bytes, err
}
func (s *Store) leaseForTx(ctx context.Context, agent string) (*domain.Lease, error) {
	var l domain.Lease
	err := s.db.QueryRowContext(ctx, "SELECT lease_id, agent_id, instance_id, acquired_at, expires_at FROM agent_leases WHERE agent_id = ?", agent).Scan(&l.LeaseID, &l.AgentID, &l.InstanceID, &l.AcquiredAt, &l.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}
func (s *Store) deleteLease(ctx context.Context, agent string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM agent_leases WHERE agent_id = ?", agent)
	return err
}
func (s *Store) markDisconnected(ctx context.Context, agent, at string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE agents SET disconnected_at = ? WHERE agent_id = ?", at, agent)
	return err
}
func (s *Store) requireActiveLease(ctx context.Context, agent, instance, lease, now string) error {
	l, err := s.leaseForTx(ctx, agent)
	if err != nil {
		return err
	}
	if l == nil || l.InstanceID != instance || l.LeaseID != lease {
		return &LeaseNotActiveError{AgentID: agent}
	}
	if l.ExpiresAt <= now {
		_ = s.deleteLease(ctx, agent)
		_ = s.markDisconnected(ctx, agent, now)
		return &LeaseNotActiveError{AgentID: agent}
	}
	return nil
}
func (s *Store) nextAttempt(ctx context.Context, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(attempt_number),0)+1 FROM delivery_attempts WHERE message_id = ?", id).Scan(&n)
	return n, err
}
func (s *Store) queuedRows(ctx context.Context, recipient, now string) ([]storedDeliveryRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT enqueue_sequence,id,sender,recipient,subject,body,created_at,expires_at,message_incarnation_id,state,sentinel_json,source_kind,retry_batch_id FROM messages WHERE recipient = ? AND state = 'queued' AND (claimed_at IS NOT NULL OR expires_at > ?) ORDER BY enqueue_sequence ASC`, recipient, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []storedDeliveryRow
	for rows.Next() {
		var m domain.Message
		var subject, sentinel, source, retry sql.NullString
		if err := rows.Scan(&m.EnqueueSequence, &m.ID, &m.From, &m.To, &subject, &m.Body, &m.CreatedAt, &m.ExpiresAt, &m.MessageIncarnationID, &m.State, &sentinel, &source, &retry); err != nil {
			return nil, err
		}
		if subject.Valid {
			m.Subject = &subject.String
		}
		if source.Valid {
			m.Source.Kind = source.String
		} else {
			m.Source.Kind = "unknown"
		}
		if sentinel.Valid {
			var d protocol.SentinelDescriptor
			if err := json.Unmarshal([]byte(sentinel.String), &d); err != nil {
				return nil, err
			}
			m.Sentinel = &d
		}
		all = append(all, storedDeliveryRow{Message: m})
		if retry.Valid {
			all[len(all)-1].RetryBatchID = &retry.String
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return all, nil
	}
	if all[0].Sentinel != nil {
		return all[:1], nil
	}
	selected := []storedDeliveryRow{all[0]}
	retry := all[0].RetryBatchID
	body := all[0].Body
	for _, row := range all[1:] {
		if row.Sentinel != nil {
			break
		}
		if retry != nil && (row.RetryBatchID == nil || *row.RetryBatchID != *retry) {
			continue
		}
		if retry == nil && row.RetryBatchID != nil {
			break
		}
		candidate := row.Message
		target := selected[0].Message
		target.Body = body
		next := domain.BatchedBody(target, candidate)
		if utf16Length(next) > s.maxBatchBodyChars || len([]byte(next)) > s.maxBodyBytes {
			break
		}
		body = next
		selected = append(selected, row)
	}
	return selected, nil
}
func utf16Length(v string) int {
	n := 0
	for _, r := range v {
		if r > 0xffff {
			n += 2
		} else {
			n++
		}
	}
	return n
}
func deliveryFromRows(rows []storedDeliveryRow, items []protocol.DeliveryItem, expires string) *domain.Delivery {
	messages := make([]domain.Message, len(rows))
	for i := range rows {
		messages[i] = rows[i].Message
	}
	return &domain.Delivery{Message: domain.BuildDeliveryMessage(messages), AttemptNumber: items[0].AttemptNumber, DeliveryExpiresAt: expires, Items: items}
}
func (s *Store) activeDelivery(ctx context.Context, agent, lease string) (*domain.Delivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT messages.enqueue_sequence,messages.id,messages.sender,messages.recipient,messages.subject,messages.body,messages.created_at,messages.expires_at,messages.message_incarnation_id,messages.state,messages.sentinel_json,messages.source_kind,delivery_attempts.attempt_number,delivery_attempts.delivery_expires_at,delivery_attempts.delivery_batch_id,delivery_attempts.owner_lease_id FROM messages JOIN delivery_attempts ON delivery_attempts.message_id = messages.id WHERE messages.recipient = ? AND messages.state IN ('leased','injection_requested') AND delivery_attempts.state IN ('leased','injection_requested') ORDER BY messages.enqueue_sequence ASC`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type activeRow struct {
		storedDeliveryRow
		attempt               int
		expires, batch, owner string
	}
	var all []activeRow
	for rows.Next() {
		var r activeRow
		var subject, sentinel, source, batch, owner sql.NullString
		if err := rows.Scan(&r.EnqueueSequence, &r.ID, &r.From, &r.To, &subject, &r.Body, &r.CreatedAt, &r.ExpiresAt, &r.MessageIncarnationID, &r.State, &sentinel, &source, &r.attempt, &r.expires, &batch, &owner); err != nil {
			return nil, err
		}
		if subject.Valid {
			r.Subject = &subject.String
		}
		if sentinel.Valid {
			var d protocol.SentinelDescriptor
			if err := json.Unmarshal([]byte(sentinel.String), &d); err != nil {
				return nil, err
			}
			r.Sentinel = &d
		}
		r.Source.Kind = "unknown"
		if source.Valid {
			r.Source.Kind = source.String
		}
		r.batch = batch.String
		r.owner = owner.String
		all = append(all, r)
	}
	if len(all) == 0 {
		return nil, nil
	}
	batch := all[0].batch
	selected := all[:1]
	if batch != "" {
		selected = nil
		for _, r := range all {
			if r.batch == batch {
				selected = append(selected, r)
			}
		}
	}
	for _, r := range selected {
		if r.owner != lease {
			if batch == "" {
				_, err = s.db.ExecContext(ctx, "UPDATE delivery_attempts SET lease_id = ?, owner_lease_id = ? WHERE message_id = ? AND state IN ('leased','injection_requested')", lease, lease, r.ID)
			} else {
				_, err = s.db.ExecContext(ctx, "UPDATE delivery_attempts SET lease_id = ?, owner_lease_id = ? WHERE delivery_batch_id = ? AND state IN ('leased','injection_requested')", lease, lease, batch)
			}
			if err != nil {
				return nil, err
			}
			break
		}
	}
	stored := make([]storedDeliveryRow, len(selected))
	items := make([]protocol.DeliveryItem, len(selected))
	for i, r := range selected {
		stored[i] = r.storedDeliveryRow
		items[i] = protocol.DeliveryItem{MessageID: r.ID, MessageIncarnationID: r.MessageIncarnationID, AttemptNumber: r.attempt, DeliveryExpiresAt: r.expires}
	}
	return deliveryFromRows(stored, items, items[0].DeliveryExpiresAt), nil
}

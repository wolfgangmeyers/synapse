package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// ConversationReceiptCapacityError means the bounded accepted-ID ledger is
// full. Protected retries/conflicts remain available at capacity.
type ConversationReceiptCapacityError struct{}

func (e *ConversationReceiptCapacityError) Error() string {
	return "conversation send receipt capacity exhausted"
}
func (e *ConversationReceiptCapacityError) Code() string { return "conversation_receipt_capacity" }

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
	Body          *string
	Attachments   []json.RawMessage
	Source        *protocol.MessageSource
	QuietEligible bool
}
type EnqueueOptions struct {
	DisableBatching       bool
	Conversation          *ConversationOptions
	ManagedQuietRequestID string
	// OnFreshAdmission runs only after the authoritative replay/conflict checks,
	// inside the write transaction and before either fresh acceptance path.
	// It must only touch the rate clock/map: no Store, router.mu, or external I/O.
	// The caller may already hold router.mu. The returned function settles
	// this admission with the actual SQLite COMMIT outcome, even on a later error.
	OnFreshAdmission func() (settle func(committed bool), err error)
}
type EnqueueResult struct {
	Created             bool                        `json:"created"`
	Deduped             bool                        `json:"deduped,omitempty"`
	Message             domain.Message              `json:"message"`
	ConversationMessage *domain.ConversationMessage `json:"conversation_message,omitempty"`
	ConversationCreated bool                        `json:"conversation_created,omitempty"`
	QuietEligible       bool                        `json:"-"`
}
type LeaseRequest struct {
	AgentID, InstanceID, LeaseID string
	LeaseDuration                time.Duration
}
type ClaimRequest struct {
	AgentID, InstanceID, LeaseID string
	// SourceOwnerID is the durable tmux source, never the socket instance.
	// Empty preserves direct Store test fixtures that predate source snapshots.
	SourceOwnerID         string
	DeliveryLeaseDuration time.Duration
	// AllowMemoryLease is reserved for the router's connection-local Web UI
	// delivery owner. The router validates that lease before entering the store,
	// so a browser never creates or renews an agent_leases row just to read mail.
	AllowMemoryLease bool
}
type ReceiptRequest struct {
	AgentID, InstanceID, LeaseID, MessageID string
	AttemptNumber                           int
	DeliveryItems                           []protocol.DeliveryItem
	AllowBatchWithoutDeliveryItems          bool
	// AllowMemoryLease has the same router-validated Web UI boundary as the
	// matching ClaimRequest. Delivery ownership still has to match the stored
	// owner_lease_id for every receipt.
	AllowMemoryLease bool
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
	var option *EnqueueOptions
	if len(options) > 0 {
		option = &options[0]
	}
	return s.enqueue(ctx, envelope, option, nil)
}

func (s *Store) enqueue(ctx context.Context, envelope protocol.Envelope, option *EnqueueOptions, producer *EmbeddedToolInvocation) (EnqueueResult, error) {
	if strings.EqualFold(envelope.To, "system") {
		return EnqueueResult{}, errors.New("cannot send message to 'system': system notifications do not accept incoming messages or replies")
	}
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
	if option != nil {
		enqueueOptions = *option
	}
	_ = enqueueOptions.DisableBatching // batching is a claim-time property.
	conversation, err := prepareConversation(candidate, enqueueOptions.Conversation)
	if err != nil {
		return EnqueueResult{}, err
	}
	// Commit expiry cleanup separately: a capacity, rate, or later admission
	// failure must not roll it back and leave the ledger permanently full.
	if conversation != nil {
		if _, err := s.PruneExpiredConversationReceipts(ctx); err != nil {
			if producer != nil {
				// No mail transaction started, so this witness conclusively
				// failed and must not be reused for another admission.
				producer.recordDomainDisposition(false, true, err)
			}
			return EnqueueResult{}, err
		}
	}
	var result EnqueueResult
	var settleAdmission func(bool)
	committed, conclusive, transactionErr := s.runImmediateWithCommitOutcome(ctx, func() error {
		if producer != nil {
			if err := s.embeddedToolMailGuardTx(ctx, producer, candidate); err != nil {
				return err
			}
		}
		admittedAt := s.clock.Now().UTC().Truncate(time.Millisecond)
		now := formatTimestamp(admittedAt)
		receipt, accepted, _, deadlineAt, err := s.conversationReceipt(ctx, candidate.ID)
		if err != nil {
			return err
		}
		if receipt != nil && s.receiptLifecycle && now >= deadlineAt {
			// A deadline may pass between the independent cleanup and this
			// transaction. The expired record must not replay or pin the ID.
			if _, err := s.pruneConversationReceiptsFor(ctx, now, candidate.ID); err != nil {
				return err
			}
			receipt = nil
		}
		if receipt != nil {
			if conversation == nil || !sameConversation(receipt, conversation) || (accepted.ID == candidate.ID && !sameEnvelope(accepted, candidate)) {
				return &IdempotencyConflictError{ID: candidate.ID}
			}
			result = EnqueueResult{Message: *accepted, ConversationMessage: receipt}
			return nil
		}
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
					// History from before migration 078 still owns the accepted ID.
					message = &domain.Message{ID: latest.ID, From: latest.From, To: latest.To, Subject: latest.Subject, Body: latest.Body, CreatedAt: latest.CreatedAt, MessageIncarnationID: latest.MessageIncarnationID, State: domain.MessageAcknowledged, Source: latest.Source}
				}
				if message.ID == candidate.ID && !sameEnvelope(message, candidate) {
					return &IdempotencyConflictError{ID: candidate.ID}
				}
				result = EnqueueResult{Message: *message, ConversationMessage: latest}
				return nil
			}
		}
		if conversation != nil && s.receiptLifecycle {
			if _, err := s.pruneConversationReceipts(ctx, now); err != nil {
				return err
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
		if enqueueOptions.OnFreshAdmission != nil {
			settleAdmission, err = enqueueOptions.OnFreshAdmission()
			if err != nil {
				return err
			}
		}
		if duplicate != nil {
			result = EnqueueResult{Deduped: true, Message: *duplicate}
			if conversation != nil {
				incarnation, err := s.ids.Next()
				if err != nil {
					return err
				}
				if enqueueOptions.ManagedQuietRequestID != "" {
					conversation.QuietEligible, err = s.consumeManagedQuietSendAuthorizationTx(ctx, enqueueOptions.ManagedQuietRequestID, candidate.From, candidate.To, now)
					if err != nil {
						return err
					}
				}
				recorded := now
				row, err := s.insertConversation(ctx, conversation, incarnation, recorded, duplicate)
				if err != nil {
					return err
				}
				result.ConversationMessage, result.ConversationCreated, result.QuietEligible = row, true, conversation.QuietEligible
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
		expires, err := protocol.TimestampAfter(now, time.Hour)
		if err != nil {
			return err
		}
		incarnation, err := s.ids.Next()
		if err != nil {
			return err
		}
		if conversation != nil && enqueueOptions.ManagedQuietRequestID != "" {
			conversation.QuietEligible, err = s.consumeManagedQuietSendAuthorizationTx(ctx, enqueueOptions.ManagedQuietRequestID, candidate.From, candidate.To, now)
			if err != nil {
				return err
			}
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
		msgWithAtts := *message
		if conversation != nil && len(conversation.Attachments) > 0 {
			msgWithAtts.Attachments = conversation.Attachments
		}
		if err := s.recordTimelineMessageTx(ctx, msgWithAtts, source); err != nil {
			return err
		}
		if conversation != nil {
			row, err := s.insertConversation(ctx, conversation, message.MessageIncarnationID, now, message)
			if err != nil {
				return err
			}
			result.ConversationMessage, result.ConversationCreated, result.QuietEligible = row, true, conversation.QuietEligible
		}
		return nil
	})
	if producer != nil {
		producer.recordDomainDisposition(committed, conclusive, transactionErr)
	}
	if settleAdmission != nil && conclusive {
		settleAdmission(committed)
	}
	return result, transactionErr
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
		if err := reactivateAgentIdentityTx(ctx, s.db, request.AgentID); err != nil {
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
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, request.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		if !request.AllowMemoryLease {
			if err := s.requireActiveLease(ctx, request.AgentID, request.InstanceID, request.LeaseID, now); err != nil {
				return err
			}
		}
		active, err := s.activeDelivery(ctx, request.AgentID, request.LeaseID, request.AllowMemoryLease)
		if err != nil {
			return err
		}
		if active != nil {
			result = active
			return nil
		}
		rows, err := s.queuedRows(ctx, request.AgentID, request.SourceOwnerID, now)
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
		// delivery_attempts.lease_id is a foreign key to durable agent leases.
		// The router fences a memory-only Web UI with owner_lease_id instead.
		leaseReference := any(request.LeaseID)
		if request.AllowMemoryLease {
			leaseReference = nil
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
			if _, err := s.db.ExecContext(ctx, `INSERT INTO delivery_attempts (message_id, attempt_number, lease_id, owner_lease_id, state, created_at, updated_at, delivery_expires_at, delivery_batch_id) VALUES (?, ?, ?, ?, 'leased', ?, ?, ?, ?)`, rows[i].ID, attempt, leaseReference, request.LeaseID, now, now, expires, batchID); err != nil {
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
	timedOut := false
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, request.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		expired, err := expireDeliveryBatch(s, ctx, deliveries, now, DefaultMaxDeliveryAttempts)
		if err != nil {
			return err
		}
		if expired {
			timedOut = true
			return nil
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
	if timedOut {
		return domain.Delivery{}, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	return result, err
}

func (s *Store) AcknowledgeDelivery(ctx context.Context, request ReceiptRequest) (protocol.DeliveryItem, error) {
	if err := protocol.ValidateDeliveryIdentity(protocol.DeliveryReceipt{AgentID: request.AgentID, InstanceID: request.InstanceID, LeaseID: request.LeaseID, MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}); err != nil {
		return protocol.DeliveryItem{}, err
	}
	now := formatTimestamp(s.clock.Now())
	var item protocol.DeliveryItem
	timedOut := false
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, request.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		expired, err := expireDeliveryBatch(s, ctx, deliveries, now, DefaultMaxDeliveryAttempts)
		if err != nil {
			return err
		}
		if expired {
			timedOut = true
			return nil
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
	if timedOut {
		return protocol.DeliveryItem{}, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
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
	timedOut := false
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, request.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		deliveries, err := s.requireDeliveryBatch(ctx, request, now)
		if err != nil {
			return err
		}
		expired, err := expireDeliveryBatch(s, ctx, deliveries, now, maxAttempts)
		if err != nil {
			return err
		}
		if expired {
			timedOut = true
			return nil
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
	if timedOut {
		return BatchFailureResult{}, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
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
			frozen, err := managedTerminationSettlementFrozenForMessageTx(ctx, s.db, id)
			if err != nil {
				return err
			}
			if frozen {
				continue
			}
			if err := s.expireManagedDirectForMessageTx(ctx, id, now); err != nil {
				return err
			}
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
		var held bool
		if err := s.db.QueryRowContext(ctx, `SELECT claude_stdin_started FROM messages WHERE id=?`, messageID).Scan(&held); err != nil {
			return err
		}
		if held {
			return errors.New("Claude stdin outcome uncertain; retry denied")
		}
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, current.To)
		if err != nil {
			return err
		}
		if frozen {
			message = current
			return nil
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
		if _, err := s.db.ExecContext(ctx, "DELETE FROM managed_direct_delivery_attempts WHERE message_id = ?", messageID); err != nil {
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
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, current.To)
		if err != nil {
			return err
		}
		if frozen {
			return nil
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

// DismissMailboxMessage transitions a mailbox message to dead_letter state.
// Used for poison pill prevention when a message fails repeated execution attempts.
func (s *Store) DismissMailboxMessage(ctx context.Context, messageID string) error {
	if messageID == "" {
		return errors.New("messageId is required")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = 'dead_letter' WHERE id = ?", messageID); err != nil {
			return err
		}
		return s.rearmFailedReminder(ctx, messageID, now)
	})
}

func (s *Store) MessageFor(ctx context.Context, messageID string) (*domain.Message, error) {
	return s.messageForTx(ctx, messageID)
}

// HasLiveMessage reports whether one sender already has an unsettled message
// for a recipient. Native audit reports use one durable slot so changing audit
// findings cannot accumulate a queue while the supervisor is away.
func (s *Store) HasLiveMessage(ctx context.Context, sender, recipient string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE sender = ? AND recipient = ? AND state IN ('queued','leased','injection_requested'))`, sender, recipient).Scan(&exists)
	return exists, err
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
func (s *Store) queuedRows(ctx context.Context, recipient, owner, now string) ([]storedDeliveryRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT enqueue_sequence,id,sender,recipient,subject,body,created_at,expires_at,message_incarnation_id,state,sentinel_json,source_kind,retry_batch_id
		FROM messages WHERE recipient = ? AND state = 'queued' AND claude_stdin_started=0 AND (claimed_at IS NOT NULL OR expires_at > ?)
		AND (NOT EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id = messages.id)
			OR EXISTS (SELECT 1 FROM message_target_snapshots target JOIN recipient_sources source ON source.recipient_name = target.recipient_name
				WHERE target.message_id = messages.id AND target.source_kind = 'tmux' AND source.source_kind = 'tmux'
				AND source.state = 'active' AND source.owner_identity = target.owner_identity AND target.owner_identity = ? AND ? <> ''))
		ORDER BY created_at ASC, enqueue_sequence ASC, id ASC`, recipient, now, owner, owner)
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

// MessageTargetSnapshot permanently selects one delivery transport for an
// ordinary message.  It intentionally has no live process or socket fields.
type MessageTargetSnapshot struct {
	SnapshotID, MessageID, MessageIncarnationID, RecipientName, SourceKind, OwnerIdentity, CreatedAt string
}

type ManagedDirectAttempt struct {
	MessageID, MessageIncarnationID string
	AttemptNumber                   int
	SnapshotID                      string
	Fence                           ManagedRuntimeFence
	State                           string
	// SessionEntryWatermark stores the immutable reconciliation key. The
	// historical name is retained because migration 026 owns this column.
	SessionEntryWatermark                                                       string
	ReceiptEntryIdentity                                                        string
	CreatedAt, PromptedAt, AcknowledgedAt, ReleasedAt, ExpiresAt, NextAttemptAt string
	FailureCode, FailureDetail                                                  string
	Message                                                                     domain.Message
}

type ManagedDirectNotReadyError struct{ AgentID string }

func (e *ManagedDirectNotReadyError) Error() string { return "managed direct delivery is not ready" }
func (e *ManagedDirectNotReadyError) Code() string  { return "managed_direct_not_ready" }

type ManagedDirectNotActiveError struct {
	MessageID     string
	AttemptNumber int
}

func (e *ManagedDirectNotActiveError) Error() string {
	return "managed direct delivery attempt is not active"
}
func (e *ManagedDirectNotActiveError) Code() string { return "managed_direct_not_active" }

// DirectMailSourceMismatchError is deliberately data-free: callers can retry a
// termination cleanup without learning the source row that rejected it.
type DirectMailSourceMismatchError struct{}

func (e *DirectMailSourceMismatchError) Error() string {
	return "managed direct recipient source mismatch"
}
func (e *DirectMailSourceMismatchError) Code() string { return "DIRECT_MAIL_SOURCE_MISMATCH" }

type managedDirectRouteStatus uint8

const (
	managedDirectRouteActive managedDirectRouteStatus = iota
	managedDirectRouteUnavailable
	managedDirectRouteInvalidTarget
)

// managedDirectTerminationTestHook stays private to the direct-mail store. The
// registry is keyed by one Store so concurrent test stores cannot share a hook.
type managedDirectTerminationTestHook struct {
	afterSourceDisposition  func() error
	afterAttemptsDeleted    func() error
	afterDiagnosticsDeleted func() error
}

var managedDirectTerminationTestHooks sync.Map // map[*Store]*managedDirectTerminationTestHook

func (s *Store) setManagedDirectTerminationTestHook(hook *managedDirectTerminationTestHook) func() {
	previous, found := managedDirectTerminationTestHooks.Load(s)
	if hook == nil {
		managedDirectTerminationTestHooks.Delete(s)
	} else {
		managedDirectTerminationTestHooks.Store(s, hook)
	}
	return func() {
		if found {
			managedDirectTerminationTestHooks.Store(s, previous)
			return
		}
		managedDirectTerminationTestHooks.Delete(s)
	}
}

func (s *Store) runManagedDirectTerminationTestHook(point func(*managedDirectTerminationTestHook) func() error) error {
	value, found := managedDirectTerminationTestHooks.Load(s)
	if !found {
		return nil
	}
	if callback := point(value.(*managedDirectTerminationTestHook)); callback != nil {
		return callback()
	}
	return nil
}

func retiredManagedRecipientOwner(agentID string) string {
	digest := sha256.Sum256([]byte(agentID))
	return fmt.Sprintf("retired:%x", digest)
}

// RetireManagedRecipientSourceForTermination is the direct-mail cleanup
// boundary for a later lifecycle owner. It never changes ordinary mail or
// immutable target snapshots.
func (s *Store) RetireManagedRecipientSourceForTermination(ctx context.Context, agentID, recipientName string) error {
	if !managedIdentifierRE.MatchString(agentID) || !managedRecipientRE.MatchString(recipientName) {
		return errors.New("recipient source identity is invalid")
	}
	sentinel := retiredManagedRecipientOwner(agentID)
	return s.runImmediate(ctx, func() error {
		archiveActive, err := managedTerminationArchiveActiveTx(ctx, s.db, agentID)
		if err != nil {
			return err
		}
		if archiveActive {
			return retireManagedRecipientSourceTx(ctx, s.db, agentID, recipientName, formatTimestamp(s.clock.Now()))
		}
		source, err := recipientSourceForTx(ctx, s.db, recipientName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			now := formatTimestamp(s.clock.Now())
			switch {
			case source.SourceKind == "managed" && source.OwnerIdentity == agentID && (source.State == "active" || source.State == "reserved"):
				_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
					SET owner_identity=?,state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
					WHERE recipient_name=?`, sentinel, now, recipientName)
			case source.SourceKind == "managed" && source.OwnerIdentity == agentID && source.State == "conflicted" && source.ConflictKind == "tmux":
				_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
					SET source_kind=?,owner_identity=?,state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
					WHERE recipient_name=?`, source.ConflictKind, source.ConflictOwner, now, recipientName)
			case source.SourceKind == "tmux" && source.State == "conflicted" && source.ConflictKind == "managed" && source.ConflictOwner == agentID:
				_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
					SET state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
					WHERE recipient_name=?`, now, recipientName)
			case source.SourceKind == "managed" && source.OwnerIdentity == sentinel && source.State == "retired":
				// Exact retirement identity is the only repeatable retired state.
			default:
				return &DirectMailSourceMismatchError{}
			}
			if err != nil {
				return err
			}
		}
		if err := s.runManagedDirectTerminationTestHook(func(hook *managedDirectTerminationTestHook) func() error { return hook.afterSourceDisposition }); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if err := interruptManagedParentWorkMatchingTx(ctx, s.db, "agent_id=?", []any{agentID}, now); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_attempts WHERE agent_id=?`, agentID); err != nil {
			return err
		}
		if err := s.runManagedDirectTerminationTestHook(func(hook *managedDirectTerminationTestHook) func() error { return hook.afterAttemptsDeleted }); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_diagnostics WHERE agent_id=?`, agentID); err != nil {
			return err
		}
		if err := s.runManagedDirectTerminationTestHook(func(hook *managedDirectTerminationTestHook) func() error { return hook.afterDiagnosticsDeleted }); err != nil {
			return err
		}
		return s.recomputeManagedParentActivityForAgentTx(ctx, agentID)
	})
}

// ResolveMessageTarget is idempotent.  It never infers tmux: absent, reserved,
// retired, and conflicted directory rows leave the ordinary message queued.
func (s *Store) ResolveMessageTarget(ctx context.Context, messageID string) (*MessageTargetSnapshot, error) {
	var result *MessageTargetSnapshot
	err := s.runImmediate(ctx, func() error {
		var err error
		result, err = s.resolveMessageTargetTx(ctx, messageID)
		return err
	})
	return result, err
}

func (s *Store) resolveMessageTargetTx(ctx context.Context, messageID string) (*MessageTargetSnapshot, error) {
	existing, err := snapshotForMessageTx(ctx, s.db, messageID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	message, err := s.messageForTx(ctx, messageID)
	if err != nil || message == nil || message.State != domain.MessageQueued {
		return nil, err
	}
	source, err := recipientSourceForTx(ctx, s.db, message.To)
	if errors.Is(err, sql.ErrNoRows) || source == nil || source.State != "active" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if message.MessageIncarnationID == "" {
		incarnation, err := s.ids.Next()
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET message_incarnation_id = ? WHERE id = ? AND message_incarnation_id IS NULL`, incarnation, messageID); err != nil {
			return nil, err
		}
		message.MessageIncarnationID = incarnation
	}
	// The ordinary message incarnation is already router-generated and unique.
	// Deriving the snapshot ID avoids consuming unrelated delivery IDs during
	// idempotent reconciliation.
	result := &MessageTargetSnapshot{SnapshotID: "target-v1:" + message.MessageIncarnationID, MessageID: message.ID, MessageIncarnationID: message.MessageIncarnationID, RecipientName: message.To, SourceKind: source.SourceKind, OwnerIdentity: source.OwnerIdentity, CreatedAt: formatTimestamp(s.clock.Now())}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO message_target_snapshots (snapshot_id,message_id,message_incarnation_id,recipient_name,source_kind,owner_identity,created_at) VALUES (?,?,?,?,?,?,?)`, result.SnapshotID, result.MessageID, result.MessageIncarnationID, result.RecipientName, result.SourceKind, result.OwnerIdentity, result.CreatedAt); err != nil {
		if isConstraintError(err) {
			return snapshotForMessageTx(ctx, s.db, messageID)
		}
		return nil, err
	}
	return result, nil
}

func snapshotForMessageTx(ctx context.Context, db *sql.DB, messageID string) (*MessageTargetSnapshot, error) {
	result := &MessageTargetSnapshot{}
	err := db.QueryRowContext(ctx, `SELECT snapshot_id,message_id,message_incarnation_id,recipient_name,source_kind,owner_identity,created_at FROM message_target_snapshots WHERE message_id = ?`, messageID).Scan(&result.SnapshotID, &result.MessageID, &result.MessageIncarnationID, &result.RecipientName, &result.SourceKind, &result.OwnerIdentity, &result.CreatedAt)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) MessageTargetSnapshot(ctx context.Context, messageID string) (*MessageTargetSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return snapshotForMessageTx(ctx, s.db, messageID)
}

// ReconcileMessageTargets repairs only rows that have no snapshot. Existing
// snapshots are immutable even if source ownership later changes.
func (s *Store) ReconcileMessageTargets(ctx context.Context) error {
	return s.runImmediate(ctx, func() error { return s.reconcileMessageTargetsTx(ctx) })
}

// reconcileMessageTargetsTx runs under the same immediate transaction as a
// source activation when readiness makes a reserved managed recipient live.
// That prevents startup-queued rows from being left without an immutable
// transport selection after the recipient becomes ready.
func (s *Store) reconcileMessageTargetsTx(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE state = 'queued' AND NOT EXISTS (SELECT 1 FROM message_target_snapshots target WHERE target.message_id = messages.id) ORDER BY enqueue_sequence ASC`)
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
	for _, id := range ids {
		if _, err := s.resolveMessageTargetTx(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func validDirectFenceTx(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence) bool {
	if !validManagedRuntimeFence(fence) {
		return false
	}
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id = ? AND desired_state = 'running' AND lifecycle_state = 'running' AND accepting_prompts = 1 AND slot_held = 1
		AND pi_session_id = ? AND session_epoch = ? AND process_pid = ? AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?`, managedFenceValues(fence)...).Scan(&found)
	return err == nil && managedReadinessSourcesConfirmedForFenceTxStatic(ctx, db, fence) == nil
}

func managedReadinessSourcesConfirmedForFenceTxStatic(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence) error {
	var desired, desiredHash, applied, appliedHash []byte
	err := db.QueryRowContext(ctx, `SELECT desired_policy_canonical,desired_policy_sha256,applied_policy_canonical,applied_policy_sha256 FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere()+` AND applied_policy_canonical IS NOT NULL AND applied_policy_sha256 IS NOT NULL AND policy_confirmed_at IS NOT NULL AND snapshot_id IS NOT NULL AND snapshot_owner_session_id IS NOT NULL AND snapshot_sequence IS NOT NULL AND snapshot_canonical IS NOT NULL AND snapshot_sha256 IS NOT NULL AND snapshot_confirmed_at IS NOT NULL`, readinessSourceFenceArgs(fence)...).Scan(&desired, &desiredHash, &applied, &appliedHash)
	if err != nil || string(desired) != string(applied) || string(desiredHash) != string(appliedHash) {
		return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
	}
	return nil
}

// AllocateManagedDirectAttempt creates one durable pending record before any
// RPC write. It only considers managed snapshots and never touches tmux state.
func (s *Store) AllocateManagedDirectAttempt(ctx context.Context, fence ManagedRuntimeFence) (*ManagedDirectAttempt, error) {
	var result *ManagedDirectAttempt
	err := s.runImmediate(ctx, func() error {
		if !validDirectFenceTx(ctx, s.db, fence) {
			return &ManagedDirectNotReadyError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		row := s.db.QueryRowContext(ctx, `SELECT m.id,m.message_incarnation_id,m.enqueue_sequence FROM messages m
			JOIN message_target_snapshots target ON target.message_id = m.id
			JOIN recipient_sources source ON source.recipient_name = target.recipient_name
			WHERE m.state = 'queued' AND m.claude_stdin_started=0 AND m.expires_at > ? AND target.source_kind = 'managed' AND target.owner_identity = ?
			AND source.source_kind = 'managed' AND source.state = 'active' AND source.owner_identity = target.owner_identity
			AND (NOT EXISTS (SELECT 1 FROM managed_goal_wakeups wakeup WHERE wakeup.message_id=m.id)
				OR EXISTS (SELECT 1 FROM managed_goal_wakeups wakeup WHERE wakeup.message_id=m.id AND wakeup.agent_id=? AND wakeup.pi_session_id=? AND wakeup.session_epoch=? AND wakeup.process_pid=? AND wakeup.process_start_marker=? AND wakeup.process_incarnation=? AND wakeup.runtime_generation=?))
			AND NOT EXISTS (SELECT 1 FROM managed_direct_delivery_attempts active WHERE active.message_id = m.id AND active.message_incarnation_id = m.message_incarnation_id AND active.state IN ('direct_pending','direct_prompted'))
			AND (NOT EXISTS (SELECT 1 FROM managed_direct_delivery_attempts previous WHERE previous.message_id = m.id AND previous.message_incarnation_id = m.message_incarnation_id)
				OR EXISTS (SELECT 1 FROM managed_direct_delivery_attempts previous WHERE previous.message_id = m.id AND previous.message_incarnation_id = m.message_incarnation_id AND previous.attempt_number = (SELECT MAX(last.attempt_number) FROM managed_direct_delivery_attempts last WHERE last.message_id = m.id AND last.message_incarnation_id = m.message_incarnation_id) AND previous.state = 'released' AND previous.next_attempt_at <= ?))
			ORDER BY m.created_at ASC, m.id ASC LIMIT 1`, now, fence.AgentID, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, now)
		var id, incarnation string
		var ignored int64
		if err := row.Scan(&id, &incarnation, &ignored); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		message, err := s.messageForTx(ctx, id)
		if err != nil || message == nil {
			return err
		}
		snapshot, err := snapshotForMessageTx(ctx, s.db, id)
		if err != nil {
			return err
		}
		var attempt int
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(attempt_number),0)+1 FROM managed_direct_delivery_attempts WHERE message_id = ? AND message_incarnation_id = ?`, id, incarnation).Scan(&attempt); err != nil {
			return err
		}
		if attempt > 5 {
			return nil
		}
		result = &ManagedDirectAttempt{MessageID: id, MessageIncarnationID: incarnation, AttemptNumber: attempt, SnapshotID: snapshot.SnapshotID, Fence: fence, State: "direct_pending", CreatedAt: now, ExpiresAt: message.ExpiresAt, Message: *message}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_direct_delivery_attempts (message_id,message_incarnation_id,attempt_number,snapshot_id,agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,state,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, result.MessageID, result.MessageIncarnationID, result.AttemptNumber, result.SnapshotID, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash, result.State, now, result.ExpiresAt)
		return err
	})
	return result, err
}

func directAttemptMatches(result sql.Result) (bool, error) {
	n, e := result.RowsAffected()
	return n == 1, e
}

func (s *Store) MarkManagedDirectPrompted(ctx context.Context, attempt ManagedDirectAttempt, cursor string) error {
	if cursor == "" {
		return errors.New("managed direct recovery cursor is required")
	}
	return s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		if !validDirectFenceTx(ctx, s.db, attempt.Fence) {
			return &ManagedDirectNotReadyError{AgentID: attempt.Fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state='direct_prompted',prompted_at=?,session_entry_watermark=? WHERE message_id=? AND message_incarnation_id=? AND attempt_number=? AND state='direct_pending' AND snapshot_id=? AND `+managedFenceWhere(""), append([]any{now, cursor, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber, attempt.SnapshotID}, managedFenceValues(attempt.Fence)...)...)
		if err != nil {
			return err
		}
		ok, err := directAttemptMatches(result)
		if err != nil {
			return err
		}
		if !ok {
			return &ManagedDirectNotActiveError{MessageID: attempt.MessageID, AttemptNumber: attempt.AttemptNumber}
		}
		if err := s.openManagedParentWorkTx(ctx, attempt, now); err != nil {
			return err
		}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
}

func (s *Store) AcknowledgeManagedDirect(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string, requireCurrentFence bool) error {
	_, err := s.AcknowledgeManagedDirectResult(ctx, attempt, receiptEntry, requireCurrentFence)
	return err
}

// AcknowledgeManagedDirectResult reports whether this call performed the
// durable direct-message acknowledgement. A false result is an idempotent
// duplicate or an already-disposed source and must not trigger a second event.
func (s *Store) AcknowledgeManagedDirectResult(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string, requireCurrentFence bool) (bool, error) {
	changed, _, err := s.AcknowledgeManagedDirectResultWithHistory(ctx, attempt, receiptEntry, requireCurrentFence)
	return changed, err
}

// AcknowledgeManagedDirectResultWithHistory is the manager-owned acknowledgement
// seam. It returns the committed Complete fact so publication can happen only
// after the settlement transaction succeeds.
func (s *Store) AcknowledgeManagedDirectResultWithHistory(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string, requireCurrentFence bool) (bool, ManagedHistoryEntry, error) {
	changed := false
	var historyEntry ManagedHistoryEntry
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		status, err := managedDirectRouteStatusTx(ctx, s.db, attempt.SnapshotID, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if status == managedDirectRouteInvalidTarget {
			return nil
		}
		if requireCurrentFence && !validDirectFenceTx(ctx, s.db, attempt.Fence) {
			return &ManagedDirectNotReadyError{AgentID: attempt.Fence.AgentID}
		}
		held, err := s.claudeDirectAttemptHeldTx(ctx, attempt)
		if err != nil {
			return err
		}
		if held {
			return ErrManagedDirectClaudeInputHeld
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state='acknowledged',acknowledged_at=?,receipt_entry_identity=?,next_attempt_at=NULL WHERE message_id=? AND message_incarnation_id=? AND attempt_number=? AND state='direct_prompted' AND snapshot_id=? AND `+managedFenceWhere(""), append([]any{now, nullIfEmpty(receiptEntry), attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber, attempt.SnapshotID}, managedFenceValues(attempt.Fence)...)...)
		if err != nil {
			return err
		}
		updated, err := directAttemptMatches(result)
		if err != nil {
			return err
		}
		if !updated {
			// A second receipt after settlement is harmless; it cannot recreate an ordinary row.
			var count int
			err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, attempt.MessageID).Scan(&count)
			if err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
			return &ManagedDirectNotActiveError{MessageID: attempt.MessageID, AttemptNumber: attempt.AttemptNumber}
		}
		changed = true
		metadata, err := s.managedDirectAcknowledgementMetadataTx(ctx, attempt.MessageID)
		if err != nil || metadata == nil {
			if err == nil {
				err = errors.New("acknowledged message disappeared before history recording")
			}
			return err
		}
		historyEntry, err = s.recordManagedDirectAcknowledgementHistoryTx(ctx, *metadata, attempt)
		if err != nil {
			return err
		}
		if err := s.completeLinkedReminder(ctx, attempt.MessageID); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, attempt.MessageID)
		if err != nil {
			return err
		}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
	if err != nil {
		return false, ManagedHistoryEntry{}, err
	}
	return changed, historyEntry, nil
}

// claudeDirectAttemptHeldTx runs inside the settlement transaction. An older
// Pi settlement cannot change a later uncertain Claude submission of the same
// message incarnation, even if its direct attempt is still prompted.
func (s *Store) claudeDirectAttemptHeldTx(ctx context.Context, attempt ManagedDirectAttempt) (bool, error) {
	var held bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=? AND message_incarnation_id=? AND state='queued' AND claude_stdin_started=1)`, attempt.MessageID, attempt.MessageIncarnationID).Scan(&held)
	return held, err
}

// AcknowledgeManagedDirectRecovered settles an old prompted attempt after the
// replacement Pi reads its exact persisted append marker. The old runtime
// fence is intentionally not compared to the replacement binding: the marker
// itself is fenced by the recorded session, generation, epoch, and content.
func (s *Store) AcknowledgeManagedDirectRecovered(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string) error {
	_, err := s.AcknowledgeManagedDirectRecoveredResult(ctx, attempt, receiptEntry)
	return err
}

func (s *Store) AcknowledgeManagedDirectRecoveredResult(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string) (bool, error) {
	changed, _, err := s.AcknowledgeManagedDirectRecoveredResultWithHistory(ctx, attempt, receiptEntry)
	return changed, err
}

// AcknowledgeManagedDirectRecoveredResultWithHistory is the recovered-attempt
// counterpart used by the manager to publish the committed Complete fact.
func (s *Store) AcknowledgeManagedDirectRecoveredResultWithHistory(ctx context.Context, attempt ManagedDirectAttempt, receiptEntry string) (bool, ManagedHistoryEntry, error) {
	if receiptEntry == "" {
		return false, ManagedHistoryEntry{}, errors.New("managed direct recovered receipt identity is required")
	}
	changed := false
	var historyEntry ManagedHistoryEntry
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		held, err := s.claudeDirectAttemptHeldTx(ctx, attempt)
		if err != nil {
			return err
		}
		if held {
			return ErrManagedDirectClaudeInputHeld
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state='acknowledged',acknowledged_at=?,receipt_entry_identity=?,next_attempt_at=NULL WHERE message_id=? AND message_incarnation_id=? AND attempt_number=? AND state='direct_prompted' AND snapshot_id=?`, now, receiptEntry, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber, attempt.SnapshotID)
		if err != nil {
			return err
		}
		updated, err := directAttemptMatches(result)
		if err != nil {
			return err
		}
		if !updated {
			status, err := managedDirectRouteStatusTx(ctx, s.db, attempt.SnapshotID, attempt.Fence.AgentID)
			if err != nil {
				return err
			}
			if status == managedDirectRouteInvalidTarget {
				return nil
			}
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id=?`, attempt.MessageID).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
			return &ManagedDirectNotActiveError{MessageID: attempt.MessageID, AttemptNumber: attempt.AttemptNumber}
		}
		changed = true
		metadata, err := s.managedDirectAcknowledgementMetadataTx(ctx, attempt.MessageID)
		if err != nil || metadata == nil {
			if err == nil {
				err = errors.New("recovered message disappeared before history recording")
			}
			return err
		}
		historyEntry, err = s.recordManagedDirectAcknowledgementHistoryTx(ctx, *metadata, attempt)
		if err != nil {
			return err
		}
		ownsLinkedReminder, err := s.recoveredAttemptOwnsLinkedReminderTx(ctx, attempt)
		if err != nil {
			return err
		}
		if !ownsLinkedReminder {
			return nil
		}
		if err := s.completeLinkedReminder(ctx, attempt.MessageID); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, attempt.MessageID)
		return err
	})
	if err != nil {
		return false, ManagedHistoryEntry{}, err
	}
	return changed, historyEntry, nil
}

const managedDirectAcknowledgementEventVersion = "managed-complete-activity/v1"

// managedDirectAcknowledgementEventKey hashes length-delimited fields so
// retry receipt spelling cannot change the identity of a successful ack.
func managedDirectAcknowledgementEventKey(recipientAgent, messageID, incarnationID string, attemptNumber int) string {
	fields := []string{managedDirectAcknowledgementEventVersion, recipientAgent, messageID, incarnationID, fmt.Sprintf("%d", attemptNumber)}
	canonical := make([]byte, 0, len(fields)*16)
	for _, field := range fields {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len([]byte(field))))
		canonical = append(canonical, length[:]...)
		canonical = append(canonical, []byte(field)...)
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", digest[:])
}

// managedDirectAcknowledgementMetadata is copied from the durable queue row
// before acknowledgement deletes it, so Complete history retains the exact
// incoming body and sender for click-through details.
type managedDirectAcknowledgementMetadata struct {
	ID, Recipient, IncarnationID, SourceKind, Sender, Body string
}

func (s *Store) managedDirectAcknowledgementMetadataTx(ctx context.Context, messageID string) (*managedDirectAcknowledgementMetadata, error) {
	var metadata managedDirectAcknowledgementMetadata
	err := s.db.QueryRowContext(ctx, `SELECT id,recipient,message_incarnation_id,COALESCE(source_kind,'unknown'),sender,body FROM messages WHERE id=?`, messageID).Scan(&metadata.ID, &metadata.Recipient, &metadata.IncarnationID, &metadata.SourceKind, &metadata.Sender, &metadata.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

// recordManagedDirectAcknowledgementHistoryTx records the concise Complete fact
// and its exact incoming click-through details in the acknowledgement transaction.
func (s *Store) recordManagedDirectAcknowledgementHistoryTx(ctx context.Context, metadata managedDirectAcknowledgementMetadata, attempt ManagedDirectAttempt) (ManagedHistoryEntry, error) {
	category := "managed_message"
	messageText := "Message received"
	presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success"}
	if metadata.SourceKind == "reminder" {
		category = "reminder"
		messageText = "Reminder received"
		presentation = &ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"}
	}
	presentationJSON, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	eventKey := managedDirectAcknowledgementEventKey(metadata.Recipient, metadata.ID, metadata.IncarnationID, attempt.AttemptNumber)
	details := managedHistoryDetails{MessageContent: &metadata.Body, SenderIdentity: &metadata.Sender}
	return s.recordManagedCompleteHistoryStrictTx(ctx, metadata.Recipient, category, "info", messageText, eventKey, presentationJSON, presentation, details)
}

// recoveredAttemptOwnsLinkedReminderTx keeps an old recovered attempt from
// completing a recurring occurrence that has already moved to a replacement
// recipient. The attempt itself was settled above; only the linked reminder and
// message need this ownership check. A same-recipient runtime restart remains
// allowed to settle its old occurrence, including while that recipient is
// unavailable.
func (s *Store) recoveredAttemptOwnsLinkedReminderTx(ctx context.Context, attempt ManagedDirectAttempt) (bool, error) {
	var targetKind, targetOwner, sourceKind, sourceOwner string
	err := s.db.QueryRowContext(ctx, `SELECT target.source_kind,target.owner_identity,COALESCE(source.source_kind,''),COALESCE(source.owner_identity,'')
		FROM reminders reminder
		JOIN message_target_snapshots target ON target.message_id=reminder.linked_message_id
		LEFT JOIN recipient_sources source ON source.recipient_name=reminder.target
		WHERE reminder.linked_message_id=? AND reminder.interval_ms IS NOT NULL
			AND reminder.state IN ('scheduled','message_queued')
		LIMIT 1`, attempt.MessageID).Scan(&targetKind, &targetOwner, &sourceKind, &sourceOwner)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return targetKind == "managed" && targetOwner == attempt.Fence.AgentID && sourceKind == "managed" && sourceOwner == attempt.Fence.AgentID, nil
}

func directRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Second
	case 2:
		return 5 * time.Second
	case 3:
		return 30 * time.Second
	case 4:
		return 120 * time.Second
	default:
		return 0
	}
}

// ErrManagedDirectClaudeInputHeld means the exact message may have reached Claude.
// No direct settlement or automatic retry is authorized.
var ErrManagedDirectClaudeInputHeld = errors.New("managed direct message held after uncertain Claude stdin")

func (s *Store) ReleaseManagedDirect(ctx context.Context, attempt ManagedDirectAttempt, code, detail string) (bool, error) {
	terminal := false
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		held, err := s.claudeDirectAttemptHeldTx(ctx, attempt)
		if err != nil {
			return err
		}
		if held {
			return ErrManagedDirectClaudeInputHeld
		}
		if code == "" {
			code = "direct_failure"
		}
		if len(code) > 128 {
			code = code[:128]
		}
		if len([]byte(detail)) > 1024 {
			detail = string([]byte(detail)[:1024])
		}
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		delay := directRetryDelay(attempt.AttemptNumber)
		terminal = delay == 0
		state := "released"
		var due any
		if terminal {
			state = "failed"
			due = nil
		} else {
			due = formatTimestamp(now.Add(delay))
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state=?,released_at=?,next_attempt_at=?,failure_code=?,failure_detail=? WHERE message_id=? AND message_incarnation_id=? AND attempt_number=? AND state IN ('direct_pending','direct_prompted') AND snapshot_id=? AND `+managedFenceWhere(""), append([]any{state, nowText, due, code, nullIfEmpty(detail), attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber, attempt.SnapshotID}, managedFenceValues(attempt.Fence)...)...)
		if err != nil {
			return err
		}
		changed, err := directAttemptMatches(result)
		if err != nil {
			return err
		}
		if !changed {
			return &ManagedDirectNotActiveError{MessageID: attempt.MessageID, AttemptNumber: attempt.AttemptNumber}
		}
		if err := interruptManagedParentWorkTx(ctx, s.db, attempt.Fence, nowText); err != nil {
			return err
		}
		if terminal {
			if err := s.deadLetterDirectAttemptTx(ctx, attempt, nowText); err != nil {
				return err
			}
			return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
		}
		return s.recomputeManagedParentActivityTx(ctx, attempt.Fence)
	})
	return terminal, err
}

// ReleaseManagedDirectRecovery handles interrupted pending or prompted work.
// It checks the immutable target directory without requiring the old process
// fence to still be live, because controlled replacement changes that fence.
func (s *Store) ReleaseManagedDirectRecovery(ctx context.Context, attempt ManagedDirectAttempt, code string) (bool, error) {
	terminal := false
	err := s.runImmediate(ctx, func() error {
		frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, attempt.Fence.AgentID)
		if err != nil {
			return err
		}
		if frozen {
			return nil
		}
		held, err := s.claudeDirectAttemptHeldTx(ctx, attempt)
		if err != nil {
			return err
		}
		if held {
			return ErrManagedDirectClaudeInputHeld
		}
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		validTarget, err := managedDirectTargetActiveTx(ctx, s.db, attempt, nowText)
		if err != nil {
			return err
		}
		delay := directRetryDelay(attempt.AttemptNumber)
		terminal = !validTarget || delay == 0
		state := "released"
		var due any
		if terminal {
			state = "failed"
			due = nil
		} else {
			due = formatTimestamp(now.Add(delay))
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state=?,released_at=?,next_attempt_at=?,failure_code=?,failure_detail=NULL WHERE message_id=? AND message_incarnation_id=? AND attempt_number=? AND state IN ('direct_pending','direct_prompted') AND snapshot_id=?`, state, nowText, due, code, attempt.MessageID, attempt.MessageIncarnationID, attempt.AttemptNumber, attempt.SnapshotID)
		if err != nil {
			return err
		}
		changed, err := directAttemptMatches(result)
		if err != nil {
			return err
		}
		if !changed {
			return &ManagedDirectNotActiveError{MessageID: attempt.MessageID, AttemptNumber: attempt.AttemptNumber}
		}
		if err := interruptManagedParentWorkTx(ctx, s.db, attempt.Fence, nowText); err != nil {
			return err
		}
		if !terminal {
			// The original fence is no longer live, so its parent projection cannot
			// be updated. The exact work row is terminal and a replacement starts
			// a new independent source if the ordinary message is retried.
			return nil
		}
		if err := s.deadLetterDirectAttemptTx(ctx, attempt, nowText); err != nil {
			return err
		}
		return s.recomputeManagedParentActivityForAgentTx(ctx, attempt.Fence.AgentID)
	})
	return terminal, err
}

// Only the exact message incarnation may be dead-lettered or have its reminder rearmed.
func (s *Store) deadLetterDirectAttemptTx(ctx context.Context, attempt ManagedDirectAttempt, now string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE messages SET state='dead_letter' WHERE id=? AND message_incarnation_id=? AND state='queued'`, attempt.MessageID, attempt.MessageIncarnationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	return s.rearmFailedReminder(ctx, attempt.MessageID, now)
}

func managedDirectTargetActiveTx(ctx context.Context, db *sql.DB, attempt ManagedDirectAttempt, now string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM messages m
		JOIN message_target_snapshots target ON target.message_id = m.id
		JOIN recipient_sources source ON source.recipient_name = target.recipient_name
		WHERE m.id=? AND m.message_incarnation_id=? AND m.state='queued' AND m.expires_at > ?
		AND target.snapshot_id=? AND target.source_kind='managed' AND target.owner_identity=?
		AND source.source_kind='managed' AND source.state='active' AND source.owner_identity=target.owner_identity`, attempt.MessageID, attempt.MessageIncarnationID, now, attempt.SnapshotID, attempt.Fence.AgentID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// managedDirectRouteStatusTx has only the three meaningful outcomes for an
// immutable managed route. A missing snapshot or source cannot be a valid
// route and is safely fenced as invalid_target.
func managedDirectRouteStatusTx(ctx context.Context, db *sql.DB, snapshotID, expectedAgentID string) (managedDirectRouteStatus, error) {
	var targetKind, targetOwner, sourceKind, sourceOwner, sourceState string
	err := db.QueryRowContext(ctx, `SELECT target.source_kind,target.owner_identity,source.source_kind,source.owner_identity,source.state
		FROM message_target_snapshots target
		JOIN recipient_sources source ON source.recipient_name=target.recipient_name
		WHERE target.snapshot_id=?`, snapshotID).Scan(&targetKind, &targetOwner, &sourceKind, &sourceOwner, &sourceState)
	if errors.Is(err, sql.ErrNoRows) {
		return managedDirectRouteInvalidTarget, nil
	}
	if err != nil {
		return managedDirectRouteInvalidTarget, err
	}
	if targetKind != "managed" || targetOwner != expectedAgentID || sourceKind != "managed" || sourceOwner != expectedAgentID {
		return managedDirectRouteInvalidTarget, nil
	}
	if sourceState == "active" {
		return managedDirectRouteActive, nil
	}
	return managedDirectRouteUnavailable, nil
}

// managedDirectSourceOwnsDiagnosticsTx preserves pre-ready diagnostics for an
// owned reservation, but blocks late producers once termination retires,
// promotes, or reuses the recipient source.
func managedDirectSourceOwnsDiagnosticsTx(ctx context.Context, db *sql.DB, agentID string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM recipient_sources
		WHERE source_kind='managed' AND owner_identity=? AND state IN ('reserved','active') LIMIT 1`, agentID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) ManagedDirectAttempt(ctx context.Context, messageID string, attemptNumber int) (*ManagedDirectAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedDirectAttemptForTx(ctx, s.db, messageID, attemptNumber)
}
func managedDirectAttemptForTx(ctx context.Context, db *sql.DB, messageID string, attemptNumber int) (*ManagedDirectAttempt, error) {
	result := &ManagedDirectAttempt{}
	var watermark sql.NullString
	var prompted, acknowledged, released, next, receipt, code, detail sql.NullString
	err := db.QueryRowContext(ctx, `SELECT a.message_id,a.message_incarnation_id,a.attempt_number,a.snapshot_id,a.agent_id,a.pi_session_id,a.session_epoch,a.process_pid,a.process_start_marker,a.process_incarnation,a.runtime_generation,a.manifest_revision,a.manifest_hash,a.state,a.session_entry_watermark,a.receipt_entry_identity,a.created_at,a.prompted_at,a.acknowledged_at,a.released_at,a.expires_at,a.next_attempt_at,a.failure_code,a.failure_detail FROM managed_direct_delivery_attempts a WHERE a.message_id=? AND a.attempt_number=?`, messageID, attemptNumber).Scan(&result.MessageID, &result.MessageIncarnationID, &result.AttemptNumber, &result.SnapshotID, &result.Fence.AgentID, &result.Fence.PiSessionID, &result.Fence.SessionEpoch, &result.Fence.ProcessPID, &result.Fence.ProcessStartMarker, &result.Fence.ProcessIncarnation, &result.Fence.RuntimeGeneration, &result.Fence.ManifestRevision, &result.Fence.ManifestHash, &result.State, &watermark, &receipt, &result.CreatedAt, &prompted, &acknowledged, &released, &result.ExpiresAt, &next, &code, &detail)
	if err != nil {
		return nil, err
	}
	result.SessionEntryWatermark = watermark.String
	result.ReceiptEntryIdentity = receipt.String
	result.PromptedAt = prompted.String
	result.AcknowledgedAt = acknowledged.String
	result.ReleasedAt = released.String
	result.NextAttemptAt = next.String
	result.FailureCode = code.String
	result.FailureDetail = detail.String
	message, err := safelyMessageForAttempt(ctx, db, messageID)
	if err == nil && message != nil {
		result.Message = *message
	}
	return result, err
}
func safelyMessageForAttempt(ctx context.Context, db *sql.DB, id string) (*domain.Message, error) {
	row := db.QueryRowContext(ctx, `SELECT enqueue_sequence,id,sender,recipient,subject,body,created_at,expires_at,message_incarnation_id,state,sentinel_json,source_kind FROM messages WHERE id=?`, id)
	return scanMessage(row)
}

func (s *Store) RecordManagedDirectDiagnostic(ctx context.Context, agentID, messageID string, attemptNumber int, code, detail string) error {
	return s.runImmediate(ctx, func() error {
		var incarnation, snapshot any
		if messageID != "" {
			currentSnapshot, err := snapshotForMessageTx(ctx, s.db, messageID)
			if err == nil {
				status, err := managedDirectRouteStatusTx(ctx, s.db, currentSnapshot.SnapshotID, agentID)
				if err != nil {
					return err
				}
				if status == managedDirectRouteInvalidTarget {
					return nil
				}
			} else if errors.Is(err, sql.ErrNoRows) {
				active, err := managedDirectSourceOwnsDiagnosticsTx(ctx, s.db, agentID)
				if err != nil {
					return err
				}
				if !active {
					return nil
				}
			} else {
				return err
			}
			var i, sp string
			_ = s.db.QueryRowContext(ctx, `SELECT message_incarnation_id,snapshot_id FROM managed_direct_delivery_attempts WHERE message_id=? AND attempt_number=?`, messageID, attemptNumber).Scan(&i, &sp)
			incarnation = nullIfEmpty(i)
			snapshot = nullIfEmpty(sp)
		} else {
			active, err := managedDirectSourceOwnsDiagnosticsTx(ctx, s.db, agentID)
			if err != nil {
				return err
			}
			if !active {
				return nil
			}
		}
		if code == "" {
			code = "direct_diagnostic"
		}
		if len(code) > 128 {
			code = code[:128]
		}
		if len([]byte(detail)) > 1024 {
			detail = string([]byte(detail)[:1024])
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_direct_delivery_diagnostics(agent_id,message_id,message_incarnation_id,attempt_number,snapshot_id,code,detail,observed_at) VALUES(?,?,?,?,?,?,?,?)`, agentID, nullIfEmpty(messageID), incarnation, attemptNumber, snapshot, code, nullIfEmpty(detail), now); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_diagnostics WHERE agent_id=? AND observed_at < ?`, agentID, formatTimestamp(s.clock.Now().Add(-30*24*time.Hour))); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `DELETE FROM managed_direct_delivery_diagnostics WHERE agent_id=? AND diagnostic_id NOT IN (SELECT diagnostic_id FROM managed_direct_delivery_diagnostics WHERE agent_id=? ORDER BY observed_at DESC,diagnostic_id DESC LIMIT 200)`, agentID, agentID)
		return err
	})
}

func (s *Store) NextManagedDirectRetry(ctx context.Context, agentID string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return time.Time{}, false, errors.New("store is closed")
	}
	var due string
	err := s.db.QueryRowContext(ctx, `SELECT a.next_attempt_at FROM managed_direct_delivery_attempts a JOIN messages m ON m.id=a.message_id AND m.message_incarnation_id=a.message_incarnation_id WHERE a.agent_id=? AND a.state='released' AND a.next_attempt_at IS NOT NULL AND m.claude_stdin_started=0 ORDER BY a.next_attempt_at ASC LIMIT 1`, agentID).Scan(&due)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return parseTimestamp(due), true, nil
}

// ExpireManagedDirectForMessage writes the terminal attempt state before the
// ordinary row is deleted. The foreign key then removes volatile attempt rows.
func (s *Store) expireManagedDirectForMessageTx(ctx context.Context, messageID, now string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT agent_id FROM managed_parent_work WHERE message_id=? AND state IN ('pending_turn','final_delivery_pending')`, messageID)
	if err != nil {
		return err
	}
	var agentIDs []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			_ = rows.Close()
			return err
		}
		agentIDs = append(agentIDs, agentID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	frozen, err := managedTerminationSettlementFrozenForMessageTx(ctx, s.db, messageID)
	if err != nil {
		return err
	}
	if frozen {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts SET state='failed',released_at=?,next_attempt_at=NULL,failure_code='expired',failure_detail=NULL WHERE message_id=? AND state IN ('direct_pending','direct_prompted','released')`, now, messageID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_final_deliveries SET state='interrupted',terminal_at=? WHERE work_id IN (SELECT work_id FROM managed_parent_work WHERE message_id=? AND state IN ('pending_turn','final_delivery_pending')) AND state IN ('pending','delivering')`, now, messageID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE managed_parent_work SET state='interrupted',terminal_at=? WHERE message_id=? AND state IN ('pending_turn','final_delivery_pending')`, now, messageID); err != nil {
		return err
	}
	for _, agentID := range agentIDs {
		if err := s.recomputeManagedParentActivityForAgentTx(ctx, agentID); err != nil {
			return err
		}
	}
	return nil
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
	return &domain.Delivery{
		Message:           domain.BuildDeliveryMessage(messages),
		Messages:          messages,
		AttemptNumber:     items[0].AttemptNumber,
		DeliveryExpiresAt: expires,
		Items:             items,
	}
}

func (s *Store) ManagedActiveDirectAttempts(ctx context.Context, agentID string) ([]ManagedDirectAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT message_id,attempt_number FROM managed_direct_delivery_attempts WHERE agent_id = ? AND state IN ('direct_pending','direct_prompted') ORDER BY message_id,attempt_number`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []struct {
		id string
		n  int
	}
	for rows.Next() {
		var key struct {
			id string
			n  int
		}
		if err := rows.Scan(&key.id, &key.n); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]ManagedDirectAttempt, 0, len(keys))
	for _, key := range keys {
		attempt, err := managedDirectAttemptForTx(ctx, s.db, key.id, key.n)
		if err != nil {
			return nil, err
		}
		result = append(result, *attempt)
	}
	return result, nil
}

func (s *Store) activeDelivery(ctx context.Context, agent, lease string, allowMemoryLease bool) (*domain.Delivery, error) {
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
	leaseReference := any(lease)
	if allowMemoryLease {
		leaseReference = nil
	}
	for _, r := range selected {
		if r.owner != lease {
			if batch == "" {
				_, err = s.db.ExecContext(ctx, "UPDATE delivery_attempts SET lease_id = ?, owner_lease_id = ? WHERE message_id = ? AND state IN ('leased','injection_requested')", leaseReference, lease, r.ID)
			} else {
				_, err = s.db.ExecContext(ctx, "UPDATE delivery_attempts SET lease_id = ?, owner_lease_id = ? WHERE delivery_batch_id = ? AND state IN ('leased','injection_requested')", leaseReference, lease, batch)
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

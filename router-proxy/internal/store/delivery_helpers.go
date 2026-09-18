package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

func (s *Store) requireDeliveryBatch(ctx context.Context, request ReceiptRequest, now string) ([]storedDelivery, error) {
	if !request.AllowMemoryLease {
		if err := s.requireActiveLease(ctx, request.AgentID, request.InstanceID, request.LeaseID, now); err != nil {
			return nil, err
		}
	}
	first, err := s.selectDelivery(ctx, request.MessageID, request.AttemptNumber)
	if err != nil || first == nil {
		if err != nil {
			return nil, err
		}
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	if first.AgentID != request.AgentID || first.OwnerLeaseID != request.LeaseID || !activeMessageState(first.MessageState) || !activeDeliveryState(first.AttemptState) {
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT messages.id,messages.recipient,messages.state,messages.message_incarnation_id,delivery_attempts.attempt_number,delivery_attempts.owner_lease_id,delivery_attempts.delivery_expires_at,delivery_attempts.delivery_batch_id,delivery_attempts.state FROM delivery_attempts JOIN messages ON messages.id = delivery_attempts.message_id WHERE messages.recipient = ? AND messages.state IN ('leased','injection_requested') AND delivery_attempts.state IN ('leased','injection_requested') AND ((delivery_attempts.delivery_batch_id IS NOT NULL AND delivery_attempts.delivery_batch_id = ?) OR (delivery_attempts.delivery_batch_id IS NULL AND delivery_attempts.message_id = ?)) ORDER BY messages.enqueue_sequence ASC`, first.AgentID, nullString(first.BatchID), first.MessageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []storedDelivery
	for rows.Next() {
		var d storedDelivery
		var batch sql.NullString
		if err := rows.Scan(&d.MessageID, &d.AgentID, &d.MessageState, &d.MessageIncarnationID, &d.AttemptNumber, &d.OwnerLeaseID, &d.DeliveryExpiresAt, &batch, &d.AttemptState); err != nil {
			return nil, err
		}
		if batch.Valid {
			d.BatchID = batch.String
		}
		deliveries = append(deliveries, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(deliveries) == 0 {
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	if deliveries[0].MessageID != request.MessageID || deliveries[0].AttemptNumber != request.AttemptNumber {
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	for _, d := range deliveries {
		if d.OwnerLeaseID != request.LeaseID {
			return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
		}
	}
	if len(deliveries) > 1 && !request.AllowBatchWithoutDeliveryItems && request.DeliveryItems == nil {
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	if request.DeliveryItems != nil && !sameDeliveryItems(request.DeliveryItems, deliveries) {
		return nil, &DeliveryNotActiveError{MessageID: request.MessageID, AttemptNumber: request.AttemptNumber}
	}
	return deliveries, nil
}
func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (s *Store) selectDelivery(ctx context.Context, messageID string, attempt int) (*storedDelivery, error) {
	var d storedDelivery
	var batch sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT messages.id,messages.recipient,messages.state,messages.message_incarnation_id,delivery_attempts.attempt_number,delivery_attempts.owner_lease_id,delivery_attempts.delivery_expires_at,delivery_attempts.delivery_batch_id,delivery_attempts.state FROM delivery_attempts JOIN messages ON messages.id = delivery_attempts.message_id WHERE delivery_attempts.message_id = ? AND delivery_attempts.attempt_number = ?`, messageID, attempt).Scan(&d.MessageID, &d.AgentID, &d.MessageState, &d.MessageIncarnationID, &d.AttemptNumber, &d.OwnerLeaseID, &d.DeliveryExpiresAt, &batch, &d.AttemptState)
	if batch.Valid {
		d.BatchID = batch.String
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &d, err
}
func activeMessageState(v string) bool {
	return v == domain.MessageLeased || v == domain.MessageInjectionRequested
}
func activeDeliveryState(v string) bool {
	return v == domain.DeliveryLeased || v == domain.DeliveryInjectionRequested
}
func expireDeliveryBatch(s *Store, ctx context.Context, rows []storedDelivery, now string, maxAttempts int) (bool, error) {
	expired := false
	for _, d := range rows {
		if d.DeliveryExpiresAt <= now {
			if _, err := s.failDelivery(ctx, d, domain.DeliveryTimedOut, "delivery_timeout", now, maxAttempts); err != nil {
				return false, err
			}
			expired = true
		}
	}
	return expired, nil
}
func sameDeliveryItems(actual []protocol.DeliveryItem, expected []storedDelivery) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i, d := range expected {
		if actual[i].MessageID != d.MessageID || actual[i].MessageIncarnationID != d.MessageIncarnationID || actual[i].AttemptNumber != d.AttemptNumber || actual[i].DeliveryExpiresAt != d.DeliveryExpiresAt {
			return false
		}
	}
	return true
}

func (s *Store) failDelivery(ctx context.Context, d storedDelivery, attemptState, reason, now string, maxAttempts int) (FailureResult, error) {
	frozen, err := managedTerminationSettlementFrozenTx(ctx, s.db, d.AgentID)
	if err != nil {
		return FailureResult{}, err
	}
	if frozen {
		return FailureResult{MessageID: d.MessageID, State: d.MessageState}, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE delivery_attempts SET state = ?, updated_at = ?, failure_reason = ? WHERE message_id = ? AND attempt_number = ? AND state IN ('leased','injection_requested')`, attemptState, now, reason, d.MessageID, d.AttemptNumber); err != nil {
		return FailureResult{}, err
	}
	state := domain.MessageQueued
	if d.AttemptNumber >= maxAttempts {
		state = domain.MessageDeadLetter
	}
	retry := any(nil)
	if state == domain.MessageQueued && d.BatchID != "" {
		retry = d.BatchID
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE messages SET state = ?, retry_batch_id = ? WHERE id = ? AND state = ?", state, retry, d.MessageID, d.MessageState); err != nil {
		return FailureResult{}, err
	}
	out := FailureResult{MessageID: d.MessageID, State: state}
	if state == domain.MessageDeadLetter {
		rearmed, err := s.rearmRecurring(ctx, d.MessageID, now)
		if err != nil {
			return FailureResult{}, err
		}
		out.RecurringReminderRearmed = rearmed
		if _, err := s.db.ExecContext(ctx, `UPDATE reminders SET state = 'failed', attempt_count = attempt_count + 1, last_attempt_at = ? WHERE linked_message_id = ? AND interval_ms IS NULL AND state = 'message_queued'`, now, d.MessageID); err != nil {
			return FailureResult{}, err
		}
	}
	return out, nil
}
func (s *Store) rearmRecurring(ctx context.Context, messageID, now string) ([]RearmedReminder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,next_due_at,interval_ms FROM reminders WHERE linked_message_id = ? AND interval_ms IS NOT NULL AND state = 'scheduled' ORDER BY id ASC`, messageID)
	if err != nil {
		return nil, err
	}
	type r struct {
		id, due  string
		interval int64
	}
	var list []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.due, &x.interval); err != nil {
			_ = rows.Close()
			return nil, err
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var out []RearmedReminder
	for _, x := range list {
		next, err := recurringDueAtAfterNow(x.due, x.interval, now)
		if err != nil {
			return nil, err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE reminders SET linked_message_id = NULL,next_due_at = ?,attempt_count = attempt_count + 1,last_attempt_at = ? WHERE id = ? AND linked_message_id = ? AND interval_ms IS NOT NULL AND state = 'scheduled'`, next, now, x.id, messageID)
		if err != nil {
			return nil, err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			out = append(out, RearmedReminder{ReminderID: x.id, NextDueAt: next})
		}
	}
	return out, nil
}
func recurringDueAtAfterNow(prior string, intervalMS int64, now string) (string, error) {
	if intervalMS <= 0 || intervalMS > maxJSSafeInteger {
		return "", &ReminderValidationError{Reason: "interval_ms must be a positive safe integer"}
	}
	p, err := protocol.ParseTimestamp(prior)
	if err != nil {
		return "", err
	}
	n, err := protocol.ParseTimestamp(now)
	if err != nil {
		return "", err
	}
	if p.After(n) {
		return prior, nil
	}
	priorMS, nowMS := p.UnixMilli(), n.UnixMilli()
	steps := (nowMS-priorMS)/intervalMS + 1
	if steps <= 0 || steps > (maxUTCMillis-priorMS)/intervalMS {
		return "", &ReminderValidationError{Reason: "recurrence cannot advance beyond supported UTC timestamps"}
	}
	nextMS := priorMS + steps*intervalMS
	if nextMS > maxUTCMillis {
		return "", &ReminderValidationError{Reason: "recurrence cannot advance beyond supported UTC timestamps"}
	}
	return timeFromUnixMilli(nextMS), nil
}

func timeFromUnixMilli(value int64) string {
	return time.UnixMilli(value).UTC().Format("2006-01-02T15:04:05.000Z")
}
func (s *Store) rearmFailedReminder(ctx context.Context, messageID, now string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id,next_due_at,interval_ms FROM reminders WHERE linked_message_id = ? AND state = 'failed'", messageID)
	if err != nil {
		return err
	}
	type r struct {
		id, due  string
		interval sql.NullInt64
	}
	var list []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.due, &x.interval); err != nil {
			_ = rows.Close()
			return err
		}
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, x := range list {
		if !x.interval.Valid {
			if _, err := s.db.ExecContext(ctx, "UPDATE reminders SET state = 'message_queued' WHERE id = ? AND linked_message_id = ? AND state = 'failed'", x.id, messageID); err != nil {
				return err
			}
			continue
		}
		next, err := recurringDueAtAfterNow(x.due, x.interval.Int64, now)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE reminders SET state = 'scheduled',linked_message_id = NULL,next_due_at = ? WHERE id = ? AND linked_message_id = ? AND state = 'failed'", next, x.id, messageID); err != nil {
			return err
		}
	}
	return nil
}

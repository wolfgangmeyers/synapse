package store

import (
	"context"
	"errors"
	"time"
)

const managedQuietSendAuthorizationTTL = 5 * time.Minute

// ReserveManagedQuietSendAuthorization is called only after a fenced managed
// mail.send control is durably admitted. The later public message request is
// still unauthenticated, but it cannot classify itself as quiet without this
// single-use ledger record.
func (s *Store) ReserveManagedQuietSendAuthorization(ctx context.Context, requestID, senderAgentID, recipientAgentID string) error {
	if ctx == nil || !managedIdentifierRE.MatchString(requestID) || !managedIdentifierRE.MatchString(senderAgentID) || !managedRecipientRE.MatchString(recipientAgentID) {
		return errors.New("managed quiet send authorization is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_quiet_send_authorizations WHERE expires_at <= ?`, now); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO managed_quiet_send_authorizations(request_id,sender_agent_id,recipient_agent_id,expires_at) VALUES (?,?,?,?)
			ON CONFLICT(request_id) DO UPDATE SET sender_agent_id=excluded.sender_agent_id,recipient_agent_id=excluded.recipient_agent_id,expires_at=excluded.expires_at`, requestID, senderAgentID, recipientAgentID, formatTimestamp(s.clock.Now().Add(managedQuietSendAuthorizationTTL)))
		return err
	})
}

// ConsumeManagedQuietSendAuthorization freezes the trusted tool provenance at
// the exact message write. It returns false for an expired, reused, or ordinary
// HTTP request; no caller-provided source field can alter that decision.
func (s *Store) ConsumeManagedQuietSendAuthorization(ctx context.Context, requestID, senderAgentID, recipientAgentID string) (bool, error) {
	if ctx == nil || !managedIdentifierRE.MatchString(requestID) || !managedIdentifierRE.MatchString(senderAgentID) || !managedRecipientRE.MatchString(recipientAgentID) {
		return false, errors.New("managed quiet send authorization is invalid")
	}
	consumed := false
	err := s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_quiet_send_authorizations WHERE expires_at <= ?`, now); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `DELETE FROM managed_quiet_send_authorizations WHERE request_id = ? AND sender_agent_id = ? AND recipient_agent_id = ? AND expires_at > ?`, requestID, senderAgentID, recipientAgentID, now)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		consumed = changed == 1
		return nil
	})
	return consumed, err
}

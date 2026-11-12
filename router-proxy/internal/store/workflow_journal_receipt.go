package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrWorkflowJournalOperationConflict = errors.New("workflow journal operation ID conflicts with earlier request")
var ErrWorkflowJournalReceiptCapacity = errors.New("workflow journal receipt capacity reached")

// WorkflowJournalOperation checks receipts before applying any mutable admission
// checks. apply must use transaction-aware internal methods, not perform effects.
func (s *Store) WorkflowJournalOperation(ctx context.Context, agent, kind, operationID, fingerprint string, apply func() (string, error)) (result string, replayed bool, err error) {
	if strings.TrimSpace(agent) == "" || strings.TrimSpace(kind) == "" || strings.TrimSpace(operationID) == "" || len(agent) > 1024 || len(kind) > 128 || len(operationID) > 1024 || len(fingerprint) > 4096 || apply == nil {
		return "", false, errors.New("invalid workflow journal operation")
	}
	// Commit clock advancement independently: failed mutations cannot roll it back.
	now := s.clock.Now()
	err = s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_retention(id,cutoff) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET cutoff=MAX(cutoff,excluded.cutoff)`, formatTimestamp(now))
		return err
	})
	if err != nil {
		return
	}
	err = s.runImmediate(ctx, func() error {
		var cutoff string
		if err := s.db.QueryRowContext(ctx, `SELECT cutoff FROM workflow_journal_retention WHERE id=1`).Scan(&cutoff); err != nil {
			return err
		}
		// Eligibility is based on monotonic persisted time. Wall-clock jumps must
		// not expire an operation receipt ahead of the committed retention cutoff.
		// Replay an unexpired receipt before current-agent admission or target
		// lookups in apply. Expired receipts are ineligible at the inclusive deadline.
		var existing string
		var expiresAt string
		err := s.db.QueryRowContext(ctx, `SELECT fingerprint,result,expires_at FROM workflow_journal_receipts WHERE agent_id=? AND kind=? AND operation_id=?`, agent, kind, operationID).Scan(&existing, &result, &expiresAt)
		if err == nil && expiresAt > cutoff {
			if existing != fingerprint {
				return ErrWorkflowJournalOperationConflict
			}
			replayed = true
			return nil
		}
		if err == nil {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM workflow_journal_receipts WHERE agent_id=? AND kind=? AND operation_id=? AND expires_at<=?`, agent, kind, operationID, cutoff); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// A deleted agent may use only an existing unexpired receipt. Once lookup
		// misses (including expiry), require a live owner before admitting mutation.
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id=?`, agent).Scan(&present); errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotFoundError{AgentID: agent}
		} else if err != nil {
			return err
		}
		var count, bytes int64
		if err = s.db.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(bytes),0) FROM workflow_journal_receipts`).Scan(&count, &bytes); err != nil {
			return err
		}
		if count >= 100000 || bytes >= 200*1024*1024 {
			return ErrWorkflowJournalReceiptCapacity
		}
		result, err = apply()
		if err != nil {
			return err
		}
		size := len(agent) + len(kind) + len(operationID) + len(fingerprint) + len(result)
		if bytes+int64(size) > 200*1024*1024 {
			return ErrWorkflowJournalReceiptCapacity
		}
		base, err := time.Parse(time.RFC3339Nano, cutoff)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_receipts(agent_id,kind,operation_id,fingerprint,result,expires_at,bytes) VALUES(?,?,?,?,?,?,?)`, agent, kind, operationID, fingerprint, result, formatTimestamp(base.Add(30*time.Minute)), size)
		return err
	})
	return
}

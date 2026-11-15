package store

import (
	"context"
	"errors"
	"fmt"
)

// WithCurrentEmbeddedWorkflowResponse linearizes a bounded in-memory response
// update against owner, execution and workflow changes in every SQLite writer.
// The callback must not call Store methods or block on the turn's serial lock.
func (s *Store) WithCurrentEmbeddedWorkflowResponse(ctx context.Context, b EmbeddedBinding, token, userSeq int64, userDigest, workflowID string, accept func()) (retErr error) {
	validate := func() error {
		if err := s.embeddedExecutionOwnerTx(ctx, b); err != nil {
			return err
		}
		e, _, err := s.embeddedExecutionRowTx(ctx, b)
		if err != nil {
			return err
		}
		if token <= 0 || e.Kind != "sdk" || e.Owner != b.OwnerToken || e.Token != token || e.Current == nil || e.Current.UserSeq != userSeq || e.Current.UserDigest != userDigest {
			return errors.New("stale response execution")
		}
		var workflow, state string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(active_workflow_id,''),COALESCE(active_workflow_state_id,'') FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&workflow, &state); err != nil {
			return err
		}
		if workflow != workflowID || (workflow == "" && state == "") {
			return errors.New("stale response workflow")
		}
		return nil
	}
	// First require a successful committed authority check. Recheck under a
	// second write lock before the callback: no other SQLite writer can revoke
	// authority between that check and the in-memory transition.
	if err := s.runImmediate(ctx, func() error {
		if err := validate(); err != nil {
			return err
		}
		if s.workflowResponseCommitFault != nil {
			return s.workflowResponseCommitFault()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.lockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	// This second transaction is read-only. Its rollback merely releases the
	// cross-connection writer lock; no second COMMIT gates the callback.
	defer func() {
		var rollbackErr error
		if s.workflowResponseRollbackFault != nil {
			rollbackErr = s.workflowResponseRollbackFault()
		}
		if rollbackErr == nil {
			_, rollbackErr = s.db.ExecContext(context.Background(), "ROLLBACK")
		}
		if rollbackErr != nil {
			// The single pooled connection may still hold a transaction and a
			// writer lock. Never return it to service in an unknown state.
			s.closed = true
			closeErr := s.db.Close()
			retErr = errors.Join(retErr, fmt.Errorf("workflow response authority rollback: %w", rollbackErr), closeErr)
		}
	}()
	if err := validate(); err != nil {
		return err
	}
	accept()
	return nil
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ReserveEmbeddedReset commits the embedded reset operation and its replay response.
func (s *Store) ReserveEmbeddedReset(ctx context.Context, agentID string, operation ManagedOperation) (*ManagedOperation, bool, error) {
	var replay *ManagedOperation
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, operation.Method, operation.RequestID)
		if err == nil {
			if existing.CanonicalPath != operation.CanonicalPath || existing.CanonicalBodyHash != operation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			replay = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, agentID), nil)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotFoundError{AgentID: agentID}
		}
		if err != nil {
			return err
		}
		if err := loadManagedRuntimeTx(ctx, s.db, agent); err != nil {
			return err
		}
		if agent.Runtime != "embedded" || agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedAgentNotReadyError{AgentID: agentID}
		}
		var active int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE agent_id=? AND state NOT IN ('succeeded','failed','cancelled','superseded')`, agentID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return &ManagedLifecycleConflictError{AgentID: agentID}
		}
		now := s.clock.Now()
		operation.State = "terminal"
		operation.CreatedAt = formatTimestamp(now)
		operation.TerminalAt = formatTimestamp(now)
		operation.ExpiresAt = formatTimestamp(now.Add(24 * time.Hour))
		if err := insertManagedOperation(ctx, s.db, operation); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if replay != nil {
		return replay, true, nil
	}
	return &operation, false, nil
}

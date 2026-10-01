package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Embedded browser controls use the current owner, never an RPC process fence.
// Mutation and HTTP replay receipt commit together. The existing operation
// sweeper retains terminal receipts for 24 hours.
func (s *Store) embeddedBrowserControl(ctx context.Context, b EmbeddedBinding, operation *ManagedOperation, mutate func() error) error {
	return s.runImmediate(ctx, func() error {
		if operation != nil {
			_, err := managedOperationForWorkerScopeTx(ctx, s.db, operation.Method, operation.RequestID)
			if err == nil {
				return &ManagedIdempotencyConflictError{}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var ready int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id=? AND runtime='embedded' AND desired_state='running' AND lifecycle_state='running' AND accepting_prompts=1 AND slot_held=1`, b.AgentID).Scan(&ready); err != nil {
			return err
		}
		if ready != 1 {
			return &ManagedAgentNotReadyError{AgentID: b.AgentID}
		}
		if err := mutate(); err != nil {
			return err
		}
		if operation != nil {
			now := s.clock.Now()
			operation.CreatedAt, operation.TerminalAt = formatTimestamp(now), formatTimestamp(now)
			operation.ExpiresAt = formatTimestamp(now.Add(24 * time.Hour))
			operation.State = "terminal"
			return insertManagedOperation(ctx, s.db, *operation)
		}
		return nil
	})
}

// UpdateEmbeddedBrowserGoal shares the goal domain writer, but creates no SDK
// call row: a browser mutation does not require a checked SDK result receipt.
func (s *Store) UpdateEmbeddedBrowserGoal(ctx context.Context, b EmbeddedBinding, config ManagedGoalConfig, operation *ManagedOperation, response func(*ManagedGoalPolicy) ([]byte, error), expectedRevision ...int64) (*ManagedGoalPolicy, error) {
	if !validManagedGoalConfig(config) || response == nil {
		return nil, errors.New("invalid embedded browser goal")
	}
	settings, err := managedGoalSettingsJSON(config.Settings)
	if err != nil {
		return nil, err
	}
	var policy *ManagedGoalPolicy
	err = s.embeddedBrowserControl(ctx, b, operation, func() error {
		if len(expectedRevision) > 0 {
			var current int64
			err := s.db.QueryRowContext(ctx, `SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&current)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if current != expectedRevision[0] {
				return errors.New("embedded browser goal changed")
			}
		}
		var err error
		policy, err = s.updateManagedGoalPolicyTx(ctx, b.AgentID, config, settings)
		if err != nil {
			return err
		}
		// Existing goal-domain triggers invalidate pending continuations and
		// snooze preferences on revision/off. Started UNKNOWN evidence remains.
		if _, err = s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		if operation != nil {
			operation.ResponseBody, err = response(policy)
		}
		return err
	})
	return policy, err
}

// UpdateEmbeddedBrowserPolicy uses a CAS on the desired policy read by Manager.
// This saved browser preference does not gate the approved embedded built-ins.
func (s *Store) UpdateEmbeddedBrowserPolicy(ctx context.Context, b EmbeddedBinding, previous, next ManagedDesiredPolicy, operation *ManagedOperation) error {
	if len(next.Canonical) < 2 || len(next.Canonical) > 32768 || !managedSHA256RE.MatchString(next.SHA256) {
		return errors.New("invalid embedded browser policy")
	}
	return s.embeddedBrowserControl(ctx, b, operation, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET desired_policy_canonical=?,desired_policy_sha256=? WHERE agent_id=? AND desired_policy_canonical=? AND desired_policy_sha256=?`, next.Canonical, next.SHA256, b.AgentID, previous.Canonical, previous.SHA256)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("embedded browser policy changed")
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func loadManagedBlockedTx(ctx context.Context, db *sql.DB, agent *ManagedAgentRecord) error {
	hasCol, err := managedAgentColumnTx(ctx, db, "blocked")
	if err != nil || !hasCol || agent == nil {
		return err
	}
	var blocked sql.NullInt64
	var reason, blockedAt sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT blocked, blocked_reason, blocked_at FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&blocked, &reason, &blockedAt); err != nil {
		return err
	}
	agent.Blocked = blocked.Int64 != 0
	agent.BlockedReason = reason.String
	agent.BlockedAt = blockedAt.String
	return nil
}

// SetAgentBlocked updates the blocked execution state of an agent.
// When blocked is true, reason must not be empty.
// When blocked is false, blocked_reason and blocked_at are cleared to NULL.
func (s *Store) SetAgentBlocked(ctx context.Context, agentID string, blocked bool, reason string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent_id must not be empty")
	}
	if blocked && strings.TrimSpace(reason) == "" {
		return errors.New("reason is required when marking an agent as blocked")
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		var blockedVal int
		var reasonVal, blockedAtVal any
		if blocked {
			blockedVal = 1
			reasonVal = strings.TrimSpace(reason)
			blockedAtVal = now
		} else {
			blockedVal = 0
			reasonVal = nil
			blockedAtVal = nil
		}

		res, err := s.db.ExecContext(ctx, `
			UPDATE managed_agents
			SET blocked = ?,
			    blocked_reason = ?,
			    blocked_at = ?,
			    updated_at = ?
			WHERE agent_id = ?`,
			blockedVal, reasonVal, blockedAtVal, now, agentID,
		)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return &ManagedAgentNotFoundError{AgentID: agentID}
		}

		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// GetAgentBlocked returns the blocked execution state, reason, and blocked timestamp for an agent.
// Returns (blocked, reason, blockedAt, error).
func (s *Store) GetAgentBlocked(ctx context.Context, agentID string) (bool, string, string, error) {
	if strings.TrimSpace(agentID) == "" {
		return false, "", "", errors.New("agent_id must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, "", "", errors.New("store is closed")
	}

	var blocked sql.NullInt64
	var reason, blockedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT blocked, blocked_reason, blocked_at
		FROM managed_agents
		WHERE agent_id = ?`,
		agentID,
	).Scan(&blocked, &reason, &blockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", "", &ManagedAgentNotFoundError{AgentID: agentID}
	}
	if err != nil {
		return false, "", "", err
	}

	return blocked.Int64 != 0, reason.String, blockedAt.String, nil
}

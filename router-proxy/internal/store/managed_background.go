package store

import (
	"context"
	"database/sql"
	"time"
)

func loadManagedBackgroundTx(ctx context.Context, db *sql.DB, agent *ManagedAgentRecord) error {
	hasCol, err := managedAgentColumnTx(ctx, db, "is_background")
	if err != nil || !hasCol || agent == nil {
		return err
	}
	var isBg sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT is_background FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&isBg); err != nil {
		return err
	}
	agent.IsBackground = isBg.Int64 != 0
	return nil
}

// PruneExpiredBackgroundAgents deletes background agents (is_background = 1) that are
// terminated (lifecycle_state = 'terminated' or desired_state = 'terminated') and whose updated_at
// is older than time.Now().Add(-maxAge). If any are pruned, calls s.bumpManagedProjectionVersion(ctx).
func (s *Store) PruneExpiredBackgroundAgents(ctx context.Context, maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		maxAge = 1 * time.Hour
	}
	var count int
	err := s.runImmediate(ctx, func() error {
		hasCol, err := managedAgentColumnTx(ctx, s.db, "is_background")
		if err != nil || !hasCol {
			return err
		}
		cutoff := formatTimestamp(s.clock.Now().Add(-maxAge))
		res, err := s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE is_background = 1 AND (lifecycle_state = 'terminated' OR desired_state = 'terminated') AND updated_at < ?`, cutoff)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		count = int(n)
		if count > 0 {
			if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
				return err
			}
		}
		return nil
	})
	return count, err
}

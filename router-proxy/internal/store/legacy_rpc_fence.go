package store

import (
	"context"
	"database/sql"
	"errors"
)

// LegacyRPCAgent returns any unsupported record, including child agents.
func (s *Store) LegacyRPCAgent(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("store is closed")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_agents WHERE runtime <> 'embedded' ORDER BY agent_id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

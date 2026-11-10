package store

import (
	"context"
	"database/sql"
)

// WorkflowBindingSnapshot fences changes to the active binding, including ABA
// and assignment of the same state. It does not fence workflow definition edits.
type WorkflowBindingSnapshot struct {
	StateID       string
	Generation    int64
	PredecessorID string
}

func (s *Store) WorkflowBindingVersion(ctx context.Context, agentID string) (WorkflowBindingSnapshot, error) {
	var result WorkflowBindingSnapshot
	var previous, state sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT active_workflow_state_id, workflow_binding_generation, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?`, agentID).Scan(&state, &result.Generation, &previous)
	result.StateID = state.String
	result.PredecessorID = previous.String
	return result, err
}

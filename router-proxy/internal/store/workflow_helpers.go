package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// FindWorkflowByIDOrName finds a workflow by its exact ID or unique name.
func (s *Store) FindWorkflowByIDOrName(ctx context.Context, idOrName string) (*Workflow, error) {
	trimmed := strings.TrimSpace(idOrName)
	if trimmed == "" {
		return nil, errors.New("workflow identifier or name cannot be empty")
	}

	// Try by ID first
	wf, err := s.GetWorkflow(ctx, trimmed)
	if err != nil {
		return nil, err
	}
	if wf != nil {
		return wf, nil
	}

	// Try by name
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM workflows WHERE name = ?`, trimmed).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return s.GetWorkflow(ctx, id)
}

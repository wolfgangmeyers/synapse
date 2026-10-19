package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// AgentProgressEntry represents one point-in-time progress update in the audit history.
type AgentProgressEntry struct {
	Percent   int    `json:"percent"`
	Note      string `json:"note"`
	Timestamp string `json:"timestamp"`
}

// SetAgentProgress records a new progress update for a managed agent.
// Validates:
// - agentID is not empty
// - 0 <= percent <= 100
// - len(note) <= 120 (measured by UTF-8 characters / runes or byte length, utf8.RuneCountInString)
// Updates managed_agents table, appends to agent_progress_history, and prunes older history entries beyond 1,000 using FIFO.
func (s *Store) SetAgentProgress(ctx context.Context, agentID string, percent int, note string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent_id must not be empty")
	}
	if percent < 0 || percent > 100 {
		return fmt.Errorf("percent must be between 0 and 100 inclusive, got %d", percent)
	}
	if utf8.RuneCountInString(note) > 120 {
		return fmt.Errorf("note exceeds maximum length of 120 characters, got %d", utf8.RuneCountInString(note))
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		// Update managed_agents
		res, err := s.db.ExecContext(ctx, `
			UPDATE managed_agents
			SET progress_percent = ?,
			    progress_note = ?,
			    progress_updated_at = ?,
			    updated_at = ?
			WHERE agent_id = ?`,
			percent, note, now, now, agentID,
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

		// Insert into agent_progress_history
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO agent_progress_history (agent_id, percent, note, created_at)
			VALUES (?, ?, ?, ?)`,
			agentID, percent, note, now,
		); err != nil {
			return err
		}

		// FIFO prune entries beyond 1,000 for this agent
		if _, err := s.db.ExecContext(ctx, `
			DELETE FROM agent_progress_history
			WHERE agent_id = ?
			  AND id NOT IN (
			    SELECT id FROM agent_progress_history
			    WHERE agent_id = ?
			    ORDER BY id DESC
			    LIMIT 1000
			  )`,
			agentID, agentID,
		); err != nil {
			return err
		}

		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ClearAgentProgress resets the current progress snapshot on managed_agents to NULL.
// Does NOT delete agent_progress_history.
func (s *Store) ClearAgentProgress(ctx context.Context, agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent_id must not be empty")
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		res, err := s.db.ExecContext(ctx, `
			UPDATE managed_agents
			SET progress_percent = NULL,
			    progress_note = NULL,
			    progress_updated_at = NULL,
			    updated_at = ?
			WHERE agent_id = ?`,
			now, agentID,
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

// GetAgentProgress returns the current progress snapshot and total history update count for an agent.
// Returns (percent, note, updatedAt, totalUpdates, error).
func (s *Store) GetAgentProgress(ctx context.Context, agentID string) (*int, string, string, int, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, "", "", 0, errors.New("agent_id must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, "", "", 0, errors.New("store is closed")
	}

	var percent sql.NullInt64
	var note, updatedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT progress_percent, progress_note, progress_updated_at
		FROM managed_agents
		WHERE agent_id = ?`,
		agentID,
	).Scan(&percent, &note, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", 0, &ManagedAgentNotFoundError{AgentID: agentID}
	}
	if err != nil {
		return nil, "", "", 0, err
	}

	var totalUpdates int
	err = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM agent_progress_history
		WHERE agent_id = ?`,
		agentID,
	).Scan(&totalUpdates)
	if err != nil {
		return nil, "", "", 0, err
	}

	var p *int
	if percent.Valid {
		val := int(percent.Int64)
		p = &val
	}

	return p, note.String, updatedAt.String, totalUpdates, nil
}

// GetAgentProgressHistory returns all retained progress history entries for an agent ordered chronologically.
func (s *Store) GetAgentProgressHistory(ctx context.Context, agentID string) ([]AgentProgressEntry, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}

	// First verify agent exists
	var dummy int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id = ?`, agentID).Scan(&dummy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &ManagedAgentNotFoundError{AgentID: agentID}
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT percent, note, created_at
		FROM agent_progress_history
		WHERE agent_id = ?
		ORDER BY created_at ASC, id ASC`,
		agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []AgentProgressEntry
	for rows.Next() {
		var entry AgentProgressEntry
		var note sql.NullString
		if err := rows.Scan(&entry.Percent, &note, &entry.Timestamp); err != nil {
			return nil, err
		}
		entry.Note = note.String
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return history, nil
}

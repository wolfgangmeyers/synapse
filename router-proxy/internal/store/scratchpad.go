package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type ScratchpadEntry struct {
	ID        string `json:"id"`
	AgentID   string `json:"agent_id"`
	Content   string `json:"content"`
	Pinned    bool   `json:"pinned"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (s *Store) AppendScratchpadEntry(ctx context.Context, agentID, content string, pinned bool) (*ScratchpadEntry, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	if runeLen := len([]rune(content)); runeLen > 4000 {
		return nil, fmt.Errorf("scratchpad entry exceeds maximum allowed length of 4000 characters (got %d); save large content to a file and record the path", runeLen)
	}
	var id string
	if s.ids != nil {
		id, _ = s.ids.Next()
	}
	if id == "" {
		id = uuid.New().String()
	}

	now := formatTimestamp(s.clock.Now())
	pinnedVal := 0
	if pinned {
		pinnedVal = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO scratchpad_entries (id, agent_id, content, pinned, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, agentID, content, pinnedVal, now, now,
	)
	if err != nil {
		return nil, err
	}

	return &ScratchpadEntry{
		ID:        id,
		AgentID:   agentID,
		Content:   content,
		Pinned:    pinned,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (s *Store) FetchScratchpadRecent(ctx context.Context, agentID string, limit int) ([]ScratchpadEntry, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	if limit < 0 {
		limit = 0
	}

	pinnedRows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, content, pinned, created_at, updated_at
		 FROM scratchpad_entries
		 WHERE agent_id = ? AND pinned = 1
		 ORDER BY created_at DESC`,
		agentID,
	)
	if err != nil {
		return nil, err
	}
	defer pinnedRows.Close()

	var entries []ScratchpadEntry
	for pinnedRows.Next() {
		var entry ScratchpadEntry
		var pinnedInt int
		if err := pinnedRows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entry.Pinned = pinnedInt == 1
		entries = append(entries, entry)
	}
	if err := pinnedRows.Err(); err != nil {
		return nil, err
	}

	if limit > 0 {
		unpinnedRows, err := s.db.QueryContext(ctx,
			`SELECT id, agent_id, content, pinned, created_at, updated_at
			 FROM scratchpad_entries
			 WHERE agent_id = ? AND pinned = 0
			 ORDER BY created_at DESC
			 LIMIT ?`,
			agentID, limit,
		)
		if err != nil {
			return nil, err
		}
		defer unpinnedRows.Close()

		for unpinnedRows.Next() {
			var entry ScratchpadEntry
			var pinnedInt int
			if err := unpinnedRows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
				return nil, err
			}
			entry.Pinned = pinnedInt == 1
			entries = append(entries, entry)
		}
		if err := unpinnedRows.Err(); err != nil {
			return nil, err
		}
	}

	if entries == nil {
		entries = []ScratchpadEntry{}
	}
	return entries, nil
}

func (s *Store) PaginateScratchpad(ctx context.Context, agentID string, limit, offset int) ([]ScratchpadEntry, int, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, 0, errors.New("agent_id must not be empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scratchpad_entries WHERE agent_id = ?`, agentID).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, content, pinned, created_at, updated_at
		 FROM scratchpad_entries
		 WHERE agent_id = ?
		 ORDER BY created_at DESC
		 LIMIT ? OFFSET ?`,
		agentID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []ScratchpadEntry
	for rows.Next() {
		var entry ScratchpadEntry
		var pinnedInt int
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, 0, err
		}
		entry.Pinned = pinnedInt == 1
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if entries == nil {
		entries = []ScratchpadEntry{}
	}
	return entries, total, nil
}

func (s *Store) SearchScratchpad(ctx context.Context, agentID, query string, limit int) ([]ScratchpadEntry, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	if limit <= 0 {
		limit = 20
	}

	likePattern := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, content, pinned, created_at, updated_at
		 FROM scratchpad_entries
		 WHERE agent_id = ? AND content LIKE ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		agentID, likePattern, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ScratchpadEntry
	for rows.Next() {
		var entry ScratchpadEntry
		var pinnedInt int
		if err := rows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		entry.Pinned = pinnedInt == 1
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if entries == nil {
		entries = []ScratchpadEntry{}
	}
	return entries, nil
}

func (s *Store) SetScratchpadPinned(ctx context.Context, agentID, entryID string, pinned bool) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent_id must not be empty")
	}
	if strings.TrimSpace(entryID) == "" {
		return errors.New("entry_id must not be empty")
	}

	now := formatTimestamp(s.clock.Now())
	pinnedVal := 0
	if pinned {
		pinnedVal = 1
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE scratchpad_entries SET pinned = ?, updated_at = ? WHERE id = ? AND agent_id = ?`,
		pinnedVal, now, entryID, agentID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("scratchpad entry %q not found for agent %q: %w", entryID, agentID, sql.ErrNoRows)
	}
	return nil
}

func (s *Store) DeleteScratchpadEntry(ctx context.Context, agentID, entryID string) error {
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent_id must not be empty")
	}
	if strings.TrimSpace(entryID) == "" {
		return errors.New("entry_id must not be empty")
	}

	res, err := s.db.ExecContext(ctx,
		`DELETE FROM scratchpad_entries WHERE agent_id = ? AND id = ?`,
		agentID, entryID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("scratchpad entry not found")
	}
	return nil
}

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

var ErrPinnedLimitReached = errors.New("pinned entry limit reached (maximum 10 pinned entries); please unpin an obsolete entry before pinning a new one")

func (s *Store) CountPinnedScratchpadEntries(ctx context.Context, agentID string) (int, error) {
	if strings.TrimSpace(agentID) == "" {
		return 0, errors.New("agent_id must not be empty")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM scratchpad_entries WHERE agent_id = ? AND pinned = 1`, agentID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) AppendScratchpadEntry(ctx context.Context, agentID, content string, pinned bool) (*ScratchpadEntry, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	if runeLen := len([]rune(content)); runeLen > 4000 {
		return nil, fmt.Errorf("scratchpad entry exceeds maximum allowed length of 4000 characters (got %d); save large content to a file and record the path", runeLen)
	}
	if pinned {
		pinnedCount, err := s.CountPinnedScratchpadEntries(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if pinnedCount >= 10 {
			return nil, ErrPinnedLimitReached
		}
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

func (s *Store) CountScratchpadEntries(ctx context.Context, agentID string) (int, error) {
	if strings.TrimSpace(agentID) == "" {
		return 0, errors.New("agent_id must not be empty")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM scratchpad_entries WHERE agent_id = ?`, agentID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) FetchScratchpadRecent(ctx context.Context, agentID string, limit int) ([]ScratchpadEntry, int, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, 0, errors.New("agent_id must not be empty")
	}
	if limit < 0 {
		limit = 0
	}

	totalCount, err := s.CountScratchpadEntries(ctx, agentID)
	if err != nil {
		return nil, 0, err
	}

	pinnedRows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, content, pinned, created_at, updated_at
		 FROM scratchpad_entries
		 WHERE agent_id = ? AND pinned = 1
		 ORDER BY created_at DESC`,
		agentID,
	)
	if err != nil {
		return nil, 0, err
	}
	defer pinnedRows.Close()

	var entries []ScratchpadEntry
	for pinnedRows.Next() {
		var entry ScratchpadEntry
		var pinnedInt int
		if err := pinnedRows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
			return nil, 0, err
		}
		entry.Pinned = pinnedInt == 1
		entries = append(entries, entry)
	}
	if err := pinnedRows.Err(); err != nil {
		return nil, 0, err
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
			return nil, 0, err
		}
		defer unpinnedRows.Close()

		for unpinnedRows.Next() {
			var entry ScratchpadEntry
			var pinnedInt int
			if err := unpinnedRows.Scan(&entry.ID, &entry.AgentID, &entry.Content, &pinnedInt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
				return nil, 0, err
			}
			entry.Pinned = pinnedInt == 1
			entries = append(entries, entry)
		}
		if err := unpinnedRows.Err(); err != nil {
			return nil, 0, err
		}
	}

	if entries == nil {
		entries = []ScratchpadEntry{}
	}
	return entries, totalCount, nil
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

	total, err := s.CountScratchpadEntries(ctx, agentID)
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

	if pinned {
		var currentPinned int
		err := s.db.QueryRowContext(ctx, `SELECT pinned FROM scratchpad_entries WHERE id = ? AND agent_id = ?`, entryID, agentID).Scan(&currentPinned)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("scratchpad entry %q not found for agent %q: %w", entryID, agentID, sql.ErrNoRows)
			}
			return err
		}
		if currentPinned != 1 {
			pinnedCount, err := s.CountPinnedScratchpadEntries(ctx, agentID)
			if err != nil {
				return err
			}
			if pinnedCount >= 10 {
				return ErrPinnedLimitReached
			}
		}
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

// SetScratchpadEntriesPinned updates the pinned status of multiple scratchpad entries.
// It returns the list of entry IDs that were actually updated, or an error if none were found
// or if pinning would exceed the 10 pinned entry limit.
func (s *Store) SetScratchpadEntriesPinned(ctx context.Context, agentID string, entryIDs []string, pinned bool) ([]string, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	cleanIDs := dedupeScratchpadIDs(entryIDs)
	if len(cleanIDs) == 0 {
		return nil, errors.New("at least one entry ID is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	placeholders := strings.TrimRight(strings.Repeat("?,", len(cleanIDs)), ",")
	queryArgs := make([]any, 0, len(cleanIDs)+1)
	queryArgs = append(queryArgs, agentID)
	for _, id := range cleanIDs {
		queryArgs = append(queryArgs, id)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id, pinned FROM scratchpad_entries WHERE agent_id = ? AND id IN (`+placeholders+`)`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	foundOrder := make(map[string]bool)
	var foundIDs []string
	newPins := 0
	for rows.Next() {
		var id string
		var isPinned int
		if err := rows.Scan(&id, &isPinned); err != nil {
			return nil, err
		}
		foundOrder[id] = true
		foundIDs = append(foundIDs, id)
		if pinned && isPinned != 1 {
			newPins++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(foundIDs) == 0 {
		return nil, errors.New("scratchpad entry not found")
	}

	if pinned && newPins > 0 {
		var currentPinned int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM scratchpad_entries WHERE agent_id = ? AND pinned = 1`, agentID).Scan(&currentPinned)
		if err != nil {
			return nil, err
		}
		if currentPinned+newPins > 10 {
			return nil, ErrPinnedLimitReached
		}
	}

	now := formatTimestamp(s.clock.Now())
	pinnedVal := 0
	if pinned {
		pinnedVal = 1
	}

	updateArgs := make([]any, 0, len(foundIDs)+3)
	updateArgs = append(updateArgs, pinnedVal, now, agentID)
	for _, id := range foundIDs {
		updateArgs = append(updateArgs, id)
	}
	updatePlaceholders := strings.TrimRight(strings.Repeat("?,", len(foundIDs)), ",")

	_, err = tx.ExecContext(ctx,
		`UPDATE scratchpad_entries SET pinned = ?, updated_at = ? WHERE agent_id = ? AND id IN (`+updatePlaceholders+`)`,
		updateArgs...,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	var orderedFound []string
	for _, id := range cleanIDs {
		if foundOrder[id] {
			orderedFound = append(orderedFound, id)
		}
	}

	return orderedFound, nil
}

// DeleteScratchpadEntries deletes multiple scratchpad entries for an agent.
// It returns the list of entry IDs that were actually deleted, or an error if none were found.
func (s *Store) DeleteScratchpadEntries(ctx context.Context, agentID string, entryIDs []string) ([]string, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent_id must not be empty")
	}
	cleanIDs := dedupeScratchpadIDs(entryIDs)
	if len(cleanIDs) == 0 {
		return nil, errors.New("at least one entry ID is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	placeholders := strings.TrimRight(strings.Repeat("?,", len(cleanIDs)), ",")
	queryArgs := make([]any, 0, len(cleanIDs)+1)
	queryArgs = append(queryArgs, agentID)
	for _, id := range cleanIDs {
		queryArgs = append(queryArgs, id)
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM scratchpad_entries WHERE agent_id = ? AND id IN (`+placeholders+`)`,
		queryArgs...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	foundOrder := make(map[string]bool)
	var foundIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		foundOrder[id] = true
		foundIDs = append(foundIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(foundIDs) == 0 {
		return nil, errors.New("scratchpad entry not found")
	}

	deleteArgs := make([]any, 0, len(foundIDs)+1)
	deleteArgs = append(deleteArgs, agentID)
	for _, id := range foundIDs {
		deleteArgs = append(deleteArgs, id)
	}
	deletePlaceholders := strings.TrimRight(strings.Repeat("?,", len(foundIDs)), ",")

	_, err = tx.ExecContext(ctx,
		`DELETE FROM scratchpad_entries WHERE agent_id = ? AND id IN (`+deletePlaceholders+`)`,
		deleteArgs...,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	var orderedFound []string
	for _, id := range cleanIDs {
		if foundOrder[id] {
			orderedFound = append(orderedFound, id)
		}
	}

	return orderedFound, nil
}

func dedupeScratchpadIDs(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	var result []string
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; !exists {
			seen[trimmed] = struct{}{}
			result = append(result, trimmed)
		}
	}
	return result
}

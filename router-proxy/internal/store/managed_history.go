package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

const maxManagedHistoryEntries = 200

type ManagedHistoryEntry struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	At      string `json:"at"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type ManagedHistoryPage struct {
	View       string                `json:"view"`
	Entries    []ManagedHistoryEntry `json:"entries"`
	NextBefore *int64                `json:"next_before"`
}

// AppendManagedCompleteHistory records a bounded, already-redacted operational
// fact. It deliberately has no details field: raw process output and request
// bodies do not belong in the browser projection.
func (s *Store) AppendManagedCompleteHistory(ctx context.Context, agentID, category, level, message string) (ManagedHistoryEntry, error) {
	return s.RecordManagedCompleteHistory(ctx, agentID, category, level, message, "")
}

// RecordManagedCompleteHistory is the idempotent producer boundary for durable
// activity facts. eventKey is optional for one-shot facts and required by
// retrying producers such as managed controls and delivery recovery.
func (s *Store) RecordManagedCompleteHistory(ctx context.Context, agentID, category, level, message, eventKey string) (ManagedHistoryEntry, error) {
	if ctx == nil || !isLifecycleText(agentID) || category == "" || level == "" || message == "" || len(category) > 64 || len(level) > 32 || len(message) > 512 || len(eventKey) > 200 || !utf8.ValidString(category) || !utf8.ValidString(level) || !utf8.ValidString(message) || !utf8.ValidString(eventKey) {
		return ManagedHistoryEntry{}, errors.New("managed history entry is invalid")
	}
	var entry ManagedHistoryEntry
	err := s.runImmediate(ctx, func() error {
		if eventKey != "" {
			var sequence int64
			err := s.db.QueryRowContext(ctx, `SELECT sequence,occurred_at,level,message FROM managed_complete_history WHERE event_key = ?`, eventKey).Scan(&sequence, &entry.At, &entry.Level, &entry.Message)
			if err == nil {
				entry.ID = fmt.Sprintf("complete:%d", sequence)
				entry.AgentID = agentID
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		occurredAt := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key) VALUES (?,?,?,?,?,NULLIF(?,''))`, nullableString(agentID), category, level, message, occurredAt, eventKey)
		if err != nil {
			return err
		}
		sequence, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
			return err
		}
		entry = ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: agentID, At: occurredAt, Level: level, Message: message}
		return nil
	})
	return entry, err
}

// ManagedHistoryPage is an independent cursor read for the two browser views.
// Quiet reads only rows whose eligibility was frozen alongside their ordinary
// message; complete reads the separate capped operational ledger.
func (s *Store) ManagedHistoryPage(ctx context.Context, agentID, view string, before int64, limit int) (ManagedHistoryPage, error) {
	if ctx == nil || !isLifecycleText(agentID) || (view != "quiet" && view != "complete") || before < 0 || limit < 1 || limit > maxManagedHistoryEntries {
		return ManagedHistoryPage{}, errors.New("managed history query is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedHistoryPage{}, errors.New("store is closed")
	}
	page := ManagedHistoryPage{View: view, Entries: []ManagedHistoryEntry{}}
	var rows *sql.Rows
	var err error
	if view == "quiet" {
		rows, err = s.db.QueryContext(ctx, `SELECT ?,conversation_sequence,message_id,recorded_at,body FROM conversation_messages WHERE quiet_eligible = 1 AND (sender=? OR recipient=?) AND (? = 0 OR conversation_sequence < ?) ORDER BY conversation_sequence DESC LIMIT ?`, agentID, agentID, agentID, before, before, limit+1)
	} else {
		// Message rows are already rendered from the selected conversation. Hide
		// legacy transport bookkeeping so Complete stays an agent's meaningful
		// lifecycle and delivery feed rather than a request log.
		rows, err = s.db.QueryContext(ctx, `SELECT agent_id,sequence,occurred_at,level,message FROM managed_complete_history WHERE agent_id=? AND category NOT IN ('managed_http','message') AND (? = 0 OR sequence < ?) ORDER BY sequence DESC LIMIT ?`, agentID, before, before, limit+1)
	}
	if err != nil {
		return ManagedHistoryPage{}, err
	}
	defer rows.Close()
	var lastReturnedSequence int64
	for rows.Next() {
		var sequence int64
		var entryAgentID, id, at, one string
		if err := rows.Scan(&entryAgentID, &sequence, &id, &at, &one); err != nil {
			return ManagedHistoryPage{}, err
		}
		if len(page.Entries) == limit {
			next := lastReturnedSequence
			page.NextBefore = &next
			break
		}
		if view == "quiet" {
			entry := ManagedHistoryEntry{ID: id, AgentID: entryAgentID, At: at, Level: "message", Message: one}
			page.Entries = append(page.Entries, entry)
		} else {
			entry := ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: entryAgentID, At: id, Level: at, Message: one}
			page.Entries = append(page.Entries, entry)
		}
		lastReturnedSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return ManagedHistoryPage{}, err
	}
	return page, nil
}

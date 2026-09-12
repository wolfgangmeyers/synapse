package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxManagedHistoryEntries = 200

type ManagedHistoryError struct {
	Code            string `json:"code"`
	Retryable       bool   `json:"retryable"`
	DetailAvailable bool   `json:"detail_available"`
}

// ManagedHistoryPresentation is a bounded, display-only contract. It is not a
// diagnostic channel: tool arguments, output, process data, and raw errors
// stay out of complete history even when an operator expands a timeline row.
type ManagedHistoryPresentation struct {
	Kind       string               `json:"kind"`
	ToolName   string               `json:"tool_name,omitempty"`
	Outcome    string               `json:"outcome,omitempty"`
	From       string               `json:"from,omitempty"`
	To         string               `json:"to,omitempty"`
	ChildLabel string               `json:"child_label,omitempty"`
	ChildState string               `json:"child_state,omitempty"`
	Summary    string               `json:"summary,omitempty"`
	Error      *ManagedHistoryError `json:"error,omitempty"`
}

type ManagedHistoryEntry struct {
	ID           string                      `json:"id"`
	AgentID      string                      `json:"agent_id"`
	At           string                      `json:"at"`
	Level        string                      `json:"level"`
	Message      string                      `json:"message"`
	Presentation *ManagedHistoryPresentation `json:"presentation,omitempty"`
}

type ManagedHistoryPage struct {
	View       string                `json:"view"`
	Entries    []ManagedHistoryEntry `json:"entries"`
	NextBefore *int64                `json:"next_before"`
}

func managedHistoryText(value string, maximum int, allowEmpty bool) bool {
	if (value == "" && !allowEmpty) || !utf8.ValidString(value) || len([]byte(value)) > maximum {
		return false
	}
	lower := strings.ToLower(value)
	for _, sensitive := range []string{"token", "secret", "password", "authorization", "cookie", "credential", "argument", "request body", "stdout", "stderr", "environment", "session_", "session id", "process_", "process id", "runtime_generation", "/users/", "/home/", "/tmp/"} {
		if strings.Contains(lower, sensitive) {
			return false
		}
	}
	return true
}

func validManagedHistoryPresentation(value *ManagedHistoryPresentation) bool {
	if value == nil {
		return true
	}
	if !map[string]bool{"message": true, "lifecycle": true, "state": true, "tool": true, "child": true, "compaction": true, "recovery": true, "error": true, "reminder": true, "model": true, "auth": true}[value.Kind] {
		return false
	}
	if !managedHistoryText(value.ToolName, 128, true) || !managedHistoryText(value.From, 48, true) || !managedHistoryText(value.To, 48, true) || !managedHistoryText(value.ChildLabel, 160, true) || !managedHistoryText(value.Summary, 512, true) {
		return false
	}
	if value.ToolName != "" && strings.ContainsAny(value.ToolName, " \t\r\n\x00") {
		return false
	}
	if value.Outcome != "" && !map[string]bool{"success": true, "warning": true, "error": true, "unknown": true, "compacting": true, "completed": true, "failed": true, "aborted": true, "reconciled_unknown": true}[value.Outcome] {
		return false
	}
	if value.ChildState != "" && !map[string]bool{"queued": true, "running": true, "completed": true, "steered": true, "aborted": true, "stopped": true, "error": true, "confirmed": true, "interrupted_unknown": true}[value.ChildState] {
		return false
	}
	return value.Error == nil || (utf8.ValidString(value.Error.Code) && len([]byte(value.Error.Code)) > 0 && len([]byte(value.Error.Code)) <= 128 && value.Error.DetailAvailable == false)
}

func managedHistoryPresentationJSON(value *ManagedHistoryPresentation) (string, error) {
	if value == nil {
		return "", nil
	}
	if !validManagedHistoryPresentation(value) {
		return "", errors.New("managed history presentation is invalid")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) < 2 || len(encoded) > 2048 {
		return "", errors.New("managed history presentation is invalid")
	}
	return string(encoded), nil
}

func managedHistoryPresentation(value string) *ManagedHistoryPresentation {
	if value == "" || len(value) > 2048 {
		return nil
	}
	var presentation ManagedHistoryPresentation
	if json.Unmarshal([]byte(value), &presentation) != nil || !validManagedHistoryPresentation(&presentation) {
		return nil
	}
	return &presentation
}

// AppendManagedCompleteHistory records a bounded, already-redacted operational
// fact. Producers that have a canonical event type should use the presentation
// overload so the UI never guesses semantics from the message string.
func (s *Store) AppendManagedCompleteHistory(ctx context.Context, agentID, category, level, message string) (ManagedHistoryEntry, error) {
	return s.RecordManagedCompleteHistory(ctx, agentID, category, level, message, "")
}

// RecordManagedCompleteHistory is the idempotent producer boundary for durable
// activity facts. eventKey is optional for one-shot facts and required by
// retrying producers such as managed controls and delivery recovery.
func (s *Store) RecordManagedCompleteHistory(ctx context.Context, agentID, category, level, message, eventKey string) (ManagedHistoryEntry, error) {
	return s.RecordManagedCompleteHistoryWithPresentation(ctx, agentID, category, level, message, eventKey, nil)
}

func (s *Store) RecordManagedCompleteHistoryWithPresentation(ctx context.Context, agentID, category, level, message, eventKey string, presentation *ManagedHistoryPresentation) (ManagedHistoryEntry, error) {
	if ctx == nil || !isLifecycleText(agentID) || category == "" || level == "" || !managedHistoryText(message, 512, false) || len(category) > 64 || len(level) > 32 || len(eventKey) > 200 || !utf8.ValidString(category) || !utf8.ValidString(level) || !utf8.ValidString(eventKey) {
		return ManagedHistoryEntry{}, errors.New("managed history entry is invalid")
	}
	presentationJSON, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	var entry ManagedHistoryEntry
	err = s.runImmediate(ctx, func() error {
		if eventKey != "" {
			var sequence int64
			var storedPresentation sql.NullString
			err := s.db.QueryRowContext(ctx, `SELECT sequence,occurred_at,level,message,COALESCE(presentation_json,'') FROM managed_complete_history WHERE event_key = ?`, eventKey).Scan(&sequence, &entry.At, &entry.Level, &entry.Message, &storedPresentation)
			if err == nil {
				entry.ID = fmt.Sprintf("complete:%d", sequence)
				entry.AgentID = agentID
				entry.Presentation = managedHistoryPresentation(storedPresentation.String)
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		occurredAt := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''))`, nullableString(agentID), category, level, message, occurredAt, eventKey, presentationJSON)
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
		entry = ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: agentID, At: occurredAt, Level: level, Message: message, Presentation: presentation}
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
		rows, err = s.db.QueryContext(ctx, `SELECT ?,conversation_sequence,message_id,recorded_at,body,NULL FROM conversation_messages WHERE quiet_eligible = 1 AND (sender=? OR recipient=?) AND (? = 0 OR conversation_sequence < ?) ORDER BY conversation_sequence DESC LIMIT ?`, agentID, agentID, agentID, before, before, limit+1)
	} else {
		// Message rows are rendered from the selected conversation. Hide legacy
		// transport bookkeeping so Complete stays a meaningful operational feed.
		rows, err = s.db.QueryContext(ctx, `SELECT agent_id,sequence,occurred_at,level,message,COALESCE(presentation_json,'') FROM managed_complete_history WHERE agent_id=? AND category NOT IN ('managed_http','message') AND (? = 0 OR sequence < ?) ORDER BY sequence DESC LIMIT ?`, agentID, before, before, limit+1)
	}
	if err != nil {
		return ManagedHistoryPage{}, err
	}
	defer rows.Close()
	var lastReturnedSequence int64
	for rows.Next() {
		var sequence int64
		var entryAgentID, first, second, message string
		var presentation sql.NullString
		if err := rows.Scan(&entryAgentID, &sequence, &first, &second, &message, &presentation); err != nil {
			return ManagedHistoryPage{}, err
		}
		if len(page.Entries) == limit {
			next := lastReturnedSequence
			page.NextBefore = &next
			break
		}
		if view == "quiet" {
			page.Entries = append(page.Entries, ManagedHistoryEntry{ID: first, AgentID: entryAgentID, At: second, Level: "message", Message: message})
		} else {
			page.Entries = append(page.Entries, ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: entryAgentID, At: first, Level: second, Message: message, Presentation: managedHistoryPresentation(presentation.String)})
		}
		lastReturnedSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return ManagedHistoryPage{}, err
	}
	return page, nil
}

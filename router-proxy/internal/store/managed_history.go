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
// diagnostic channel: tool output, process data, and raw errors stay out of
// Complete history. Redacted invocation input is stored separately for details.
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
	ID             string                      `json:"id"`
	AgentID        string                      `json:"agent_id"`
	At             string                      `json:"at"`
	Level          string                      `json:"level"`
	Message        string                      `json:"message"`
	Presentation   *ManagedHistoryPresentation `json:"presentation,omitempty"`
	MessageContent *string                     `json:"message_content,omitempty"`
	SenderIdentity *string                     `json:"sender_identity,omitempty"`
	ToolInput      json.RawMessage             `json:"tool_input,omitempty"`
}

type managedHistoryDetails struct {
	MessageContent *string
	SenderIdentity *string
	ToolInputJSON  string
}

type ManagedHistoryPage struct {
	View       string                `json:"view"`
	Entries    []ManagedHistoryEntry `json:"entries"`
	NextBefore *int64                `json:"next_before"`
}

func managedHistoryDetail(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func managedHistoryDetailEqual(stored sql.NullString, expected *string) bool {
	if stored.Valid != (expected != nil) {
		return false
	}
	return !stored.Valid || stored.String == *expected
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
	if value.Outcome != "" && !map[string]bool{"active": true, "success": true, "warning": true, "error": true, "unknown": true, "compacting": true, "completed": true, "failed": true, "interrupted": true, "aborted": true, "reconciled_unknown": true}[value.Outcome] {
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

// defaultManagedHistoryPresentation gives historical and generic producers a
// closed display fact. It intentionally derives only from already-redacted
// category/level metadata, never from operational payloads or raw failures.
func defaultManagedHistoryPresentation(category, level, message string) *ManagedHistoryPresentation {
	presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success"}
	if level == "error" {
		return &ManagedHistoryPresentation{Kind: "error", Outcome: "error", Summary: "The operation could not be completed.", Error: &ManagedHistoryError{Code: "OPERATION_FAILED", Retryable: false, DetailAvailable: false}}
	}
	switch category {
	case "managed_parent_compaction":
		outcome := "compacting"
		switch message {
		case "Context compacted":
			outcome = "completed"
		case "Context compaction failed":
			outcome = "failed"
		case "Context compaction interrupted — outcome unknown":
			outcome = "reconciled_unknown"
		}
		return &ManagedHistoryPresentation{Kind: "compaction", Outcome: outcome}
	case "managed_child":
		return &ManagedHistoryPresentation{Kind: "child", Outcome: "success"}
	case "managed_lifecycle":
		return &ManagedHistoryPresentation{Kind: "lifecycle", Outcome: "success"}
	case "managed_control":
		return &ManagedHistoryPresentation{Kind: "tool", Outcome: "success"}
	case "reminder":
		return &ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"}
	case "delivery":
		return &ManagedHistoryPresentation{Kind: "error", Outcome: "error", Summary: "The message could not be delivered.", Error: &ManagedHistoryError{Code: "DELIVERY_FAILED", Retryable: false, DetailAvailable: false}}
	default:
		return presentation
	}
}

func effectiveManagedHistoryPresentation(category, level, message, stored string) *ManagedHistoryPresentation {
	if presentation := managedHistoryPresentation(stored); presentation != nil {
		return presentation
	}
	return defaultManagedHistoryPresentation(category, level, message)
}

// reserveManagedHistoryBoundaryTx starts one new default-history window for a
// launch while retaining earlier capped rows as internal audit evidence.
func (s *Store) reserveManagedHistoryBoundaryTx(ctx context.Context, agentID, incarnationID, createdAt string) error {
	if !isLifecycleText(agentID) || incarnationID == "" || createdAt == "" {
		return errors.New("managed history boundary is invalid")
	}
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM managed_complete_history`).Scan(&sequence); err != nil {
		// Focused migration fixtures can include the managed-agent shape without
		// the later Complete table. Their boundary remains valid at sequence 0.
		if !strings.Contains(err.Error(), "no such table: managed_complete_history") {
			return err
		}
		sequence = 0
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_history_incarnations(incarnation_id,agent_id,start_sequence,created_at) VALUES (?,?,?,?)`, incarnationID, agentID, sequence, createdAt); err != nil {
		if strings.Contains(err.Error(), "no such table: managed_history_incarnations") {
			return nil
		}
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO managed_history_boundaries(agent_id,incarnation_id,start_sequence,created_at) VALUES (?,?,?,?)`, agentID, incarnationID, sequence, createdAt)
	if strings.Contains(fmt.Sprint(err), "no such table: managed_history_boundaries") {
		return nil
	}
	return err
}

func (s *Store) closeManagedHistoryBoundaryTx(ctx context.Context, agentID, closedAt string) error {
	if !isLifecycleText(agentID) || closedAt == "" {
		return errors.New("managed history boundary is invalid")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE managed_history_incarnations SET closed_at=? WHERE incarnation_id=(SELECT incarnation_id FROM managed_history_boundaries WHERE agent_id=?) AND closed_at IS NULL`, closedAt, agentID)
	return err
}

// managedHistoryBoundaryTx returns the active launch window. A historic
// incarnation without an active boundary is intentionally invisible through
// the default agent endpoint. Rows from before this migration remain readable
// only when no managed incarnation has ever been recorded for that name.
func (s *Store) managedHistoryBoundaryTx(ctx context.Context, agentID string) (int64, bool, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `SELECT start_sequence FROM managed_history_boundaries WHERE agent_id=?`, agentID).Scan(&sequence)
	if err == nil {
		return sequence, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	var historic int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_history_incarnations WHERE agent_id=? LIMIT 1`, agentID).Scan(&historic)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	return 0, false, nil
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
	return s.RecordManagedCompleteHistoryWithPresentationAndInput(ctx, agentID, category, level, message, eventKey, presentation, nil)
}

func (s *Store) RecordManagedCompleteHistoryWithPresentationAndInput(ctx context.Context, agentID, category, level, message, eventKey string, presentation *ManagedHistoryPresentation, toolInput any) (ManagedHistoryEntry, error) {
	if ctx == nil || !isLifecycleText(agentID) || category == "" || level == "" || !managedHistoryText(message, 512, false) || len(category) > 64 || len(level) > 32 || len(eventKey) > 200 || !utf8.ValidString(category) || !utf8.ValidString(level) || !utf8.ValidString(eventKey) {
		return ManagedHistoryEntry{}, errors.New("managed history entry is invalid")
	}
	if presentation == nil {
		presentation = defaultManagedHistoryPresentation(category, level, message)
	}
	presentationJSON, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	toolInputJSON, err := managedHistoryToolInputJSON(toolInput)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	var entry ManagedHistoryEntry
	err = s.runImmediate(ctx, func() error {
		entry, err = s.recordManagedCompleteHistoryTx(ctx, agentID, category, level, message, eventKey, presentationJSON, presentation, managedHistoryDetails{ToolInputJSON: toolInputJSON})
		return err
	})
	return entry, err
}

// recordManagedCompleteHistoryStrictTx is the fail-closed variant used by
// acknowledgement transactions. A reused event key is valid only when every
// canonical display field matches the fact being acknowledged.
func (s *Store) recordManagedCompleteHistoryStrictTx(ctx context.Context, agentID, category, level, message, eventKey, presentationJSON string, presentation *ManagedHistoryPresentation, details ...managedHistoryDetails) (ManagedHistoryEntry, error) {
	detail := managedHistoryDetails{}
	if len(details) > 0 {
		detail = details[0]
	}
	if eventKey != "" {
		var sequence int64
		var storedAgent, storedCategory, storedAt, storedLevel, storedMessage, storedPresentation string
		var storedContent, storedSender, storedToolInput sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT sequence,COALESCE(agent_id,''),category,occurred_at,level,message,COALESCE(presentation_json,''),message_content,sender_identity,tool_input_json FROM managed_complete_history WHERE event_key=?`, eventKey).Scan(&sequence, &storedAgent, &storedCategory, &storedAt, &storedLevel, &storedMessage, &storedPresentation, &storedContent, &storedSender, &storedToolInput)
		if err == nil {
			if storedAgent != agentID || storedCategory != category || storedLevel != level || storedMessage != message || storedPresentation != presentationJSON || !managedHistoryDetailEqual(storedContent, detail.MessageContent) || !managedHistoryDetailEqual(storedSender, detail.SenderIdentity) || storedToolInput.String != detail.ToolInputJSON {
				return ManagedHistoryEntry{}, errors.New("managed complete history event key conflicts with canonical metadata")
			}
			storedDetails := managedHistoryDetails{MessageContent: managedHistoryDetail(storedContent), SenderIdentity: managedHistoryDetail(storedSender), ToolInputJSON: storedToolInput.String}
			return managedHistoryEntry(sequence, storedAgent, storedAt, storedLevel, storedMessage, effectiveManagedHistoryPresentation(storedCategory, storedLevel, storedMessage, storedPresentation), storedDetails), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ManagedHistoryEntry{}, err
		}
	}
	return s.recordManagedCompleteHistoryTx(ctx, agentID, category, level, message, eventKey, presentationJSON, presentation, detail)
}

// recordManagedCompleteHistoryTx inserts and prunes one Complete row while the
// caller's immediate transaction is open. Generic callers retain the existing
// idempotent event-key behavior; acknowledgement callers use the strict helper
// above so an unrelated row can never be silently reused.
func (s *Store) recordManagedCompleteHistoryTx(ctx context.Context, agentID, category, level, message, eventKey, presentationJSON string, presentation *ManagedHistoryPresentation, details ...managedHistoryDetails) (ManagedHistoryEntry, error) {
	detail := managedHistoryDetails{}
	if len(details) > 0 {
		detail = details[0]
	}
	var entry ManagedHistoryEntry
	if eventKey != "" {
		var sequence int64
		var storedCategory string
		var storedPresentation, storedContent, storedSender, storedToolInput sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT sequence,category,occurred_at,level,message,COALESCE(presentation_json,''),message_content,sender_identity,tool_input_json FROM managed_complete_history WHERE event_key = ?`, eventKey).Scan(&sequence, &storedCategory, &entry.At, &entry.Level, &entry.Message, &storedPresentation, &storedContent, &storedSender, &storedToolInput)
		if err == nil {
			effectiveToolInput := storedToolInput.String
			if effectiveToolInput == "" && detail.ToolInputJSON != "" {
				effectiveToolInput = detail.ToolInputJSON
				if _, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level=?,message=?,presentation_json=?,tool_input_json=NULLIF(?,'') WHERE sequence=?`, level, message, presentationJSON, effectiveToolInput, sequence); err != nil {
					return ManagedHistoryEntry{}, err
				}
				entry.Level = level
				entry.Message = message
				entry.Presentation = presentation
			} else if storedPresentation.String != presentationJSON || entry.Level != level || entry.Message != message {
				if _, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level=?,message=?,presentation_json=? WHERE sequence=?`, level, message, presentationJSON, sequence); err != nil {
					return ManagedHistoryEntry{}, err
				}
				entry.Level = level
				entry.Message = message
				entry.Presentation = presentation
			} else {
				entry.Presentation = effectiveManagedHistoryPresentation(storedCategory, entry.Level, entry.Message, storedPresentation.String)
			}
			entry.ID = fmt.Sprintf("complete:%d", sequence)
			entry.AgentID = agentID
			entry.MessageContent = managedHistoryDetail(storedContent)
			entry.SenderIdentity = managedHistoryDetail(storedSender)
			entry.ToolInput = rawManagedHistoryJSON(effectiveToolInput)
			return entry, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ManagedHistoryEntry{}, err
		}
	}
	occurredAt := formatTimestamp(s.clock.Now())
	result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json,message_content,sender_identity,tool_input_json) VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,NULLIF(?,''))`, nullableString(agentID), category, level, message, occurredAt, eventKey, presentationJSON, detail.MessageContent, detail.SenderIdentity, detail.ToolInputJSON)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
		return ManagedHistoryEntry{}, err
	}
	return managedHistoryEntry(sequence, agentID, occurredAt, level, message, presentation, detail), nil
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
	startSequence := int64(0)
	if view == "complete" {
		var active bool
		startSequence, active, err = s.managedHistoryBoundaryTx(ctx, agentID)
		if err != nil {
			return ManagedHistoryPage{}, err
		}
		if !active {
			return page, nil
		}
	}
	if view == "quiet" {
		rows, err = s.db.QueryContext(ctx, `SELECT ?,conversation_sequence,message_id,recorded_at,body,NULL FROM conversation_messages WHERE quiet_eligible = 1 AND (sender=? OR recipient=?) AND (? = 0 OR conversation_sequence < ?) ORDER BY conversation_sequence DESC LIMIT ?`, agentID, agentID, agentID, before, before, limit+1)
	} else {
		// Message rows are rendered from the selected conversation. Hide legacy
		// transport bookkeeping so Complete stays a meaningful operational feed.
		rows, err = s.db.QueryContext(ctx, `SELECT agent_id,sequence,category,occurred_at,level,message,COALESCE(presentation_json,''),message_content,sender_identity,tool_input_json FROM managed_complete_history WHERE agent_id=? AND sequence>? AND category NOT IN ('managed_http','message') AND (? = 0 OR sequence < ?) ORDER BY sequence DESC LIMIT ?`, agentID, startSequence, before, before, limit+1)
	}
	if err != nil {
		return ManagedHistoryPage{}, err
	}
	defer rows.Close()
	var lastReturnedSequence int64
	for rows.Next() {
		var sequence int64
		var entryAgentID, first, second, message, category string
		var presentation, content, sender, toolInput sql.NullString
		var scanErr error
		if view == "quiet" {
			scanErr = rows.Scan(&entryAgentID, &sequence, &first, &second, &message, &presentation)
		} else {
			scanErr = rows.Scan(&entryAgentID, &sequence, &category, &first, &second, &message, &presentation, &content, &sender, &toolInput)
		}
		if scanErr != nil {
			return ManagedHistoryPage{}, scanErr
		}
		if len(page.Entries) == limit {
			next := lastReturnedSequence
			page.NextBefore = &next
			break
		}
		if view == "quiet" {
			page.Entries = append(page.Entries, ManagedHistoryEntry{ID: first, AgentID: entryAgentID, At: second, Level: "message", Message: message})
		} else {
			page.Entries = append(page.Entries, managedHistoryEntry(sequence, entryAgentID, first, second, message, effectiveManagedHistoryPresentation(category, second, message, presentation.String), managedHistoryDetails{MessageContent: managedHistoryDetail(content), SenderIdentity: managedHistoryDetail(sender), ToolInputJSON: toolInput.String}))
		}
		lastReturnedSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return ManagedHistoryPage{}, err
	}
	return page, nil
}

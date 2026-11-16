package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxManagedHistoryEntries = 5000

type ManagedHistoryError struct {
	Code            string `json:"code"`
	Retryable       bool   `json:"retryable"`
	DetailAvailable bool   `json:"detail_available"`
}

// ManagedHistoryPresentation is a bounded, display-only contract. It is not a
// diagnostic channel: tool output, process data, and raw errors stay out of
// Complete history. Redacted invocation input is stored separately for details.
type ManagedHistoryPresentation struct {
	Kind          string               `json:"kind"`
	ToolName      string               `json:"tool_name,omitempty"`
	Outcome       string               `json:"outcome,omitempty"`
	From          string               `json:"from,omitempty"`
	To            string               `json:"to,omitempty"`
	ChildLabel    string               `json:"child_label,omitempty"`
	ChildState    string               `json:"child_state,omitempty"`
	Summary       string               `json:"summary,omitempty"`
	Error         *ManagedHistoryError `json:"error,omitempty"`
	Thinking      string               `json:"thinking,omitempty"`
	CompanionText string               `json:"companion_text,omitempty"`
	OccurredAt    string               `json:"occurred_at,omitempty"`
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
	Thinking       string                      `json:"thinking,omitempty"`
}

type managedHistoryDetails struct {
	MessageContent *string
	SenderIdentity *string
	ToolInputJSON  string
	OccurredAt     string
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
	for _, sensitive := range []string{"token=", "token:", "secret=", "secret:", "password=", "password:", "authorization:", "cookie=", "cookie:", "set-cookie:", "credential=", "credential:", "--argument", "arguments:", "request body", "stdout:", "stderr:", "environment:", "session_", "session id", "process_", "process id", "runtime_generation", "/users/", "/home/", "/tmp/"} {
		if strings.Contains(lower, sensitive) {
			return false
		}
	}
	return true
}

func IsSafeManagedHistoryText(value string, maximum int, allowEmpty bool) bool {
	return managedHistoryText(value, maximum, allowEmpty)
}

func validManagedHistoryPresentation(value *ManagedHistoryPresentation) bool {
	if value == nil {
		return true
	}
	if !map[string]bool{"message": true, "lifecycle": true, "state": true, "tool": true, "child": true, "compaction": true, "recovery": true, "error": true, "reminder": true, "model": true, "auth": true, "thinking": true, "goal_evaluation": true, "subagent_complete": true, "subagent": true, "context_reset": true}[value.Kind] {
		return false
	}
	if !managedHistoryText(value.ToolName, 128, true) || !managedHistoryText(value.From, 128, true) || !managedHistoryText(value.To, 128, true) || !managedHistoryText(value.ChildLabel, 160, true) || (value.OccurredAt != "" && !managedHistoryText(value.OccurredAt, 64, true)) || !managedHistoryText(value.CompanionText, 65536, true) {
		return false
	}
	if value.Kind == "goal_evaluation" {
		if !utf8.ValidString(value.Summary) || len([]byte(value.Summary)) > 512 {
			return false
		}
	} else {
		if !managedHistoryText(value.Summary, 512, true) {
			return false
		}
	}
	if value.ToolName != "" && strings.ContainsAny(value.ToolName, " \t\r\n\x00") {
		return false
	}
	if value.Outcome != "" && !map[string]bool{"active": true, "success": true, "warning": true, "error": true, "unknown": true, "compacting": true, "completed": true, "failed": true, "interrupted": true, "aborted": true, "reconciled_unknown": true, "done": true, "not_done": true}[value.Outcome] {
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
	case "managed_subagent":
		outcome := "completed"
		if level == "error" || strings.Contains(strings.ToLower(message), "failed") {
			outcome = "failed"
		}
		return &ManagedHistoryPresentation{Kind: "subagent_complete", ToolName: "subagent", Outcome: outcome}
	case "context_reset":
		return &ManagedHistoryPresentation{Kind: "context_reset", Outcome: "success"}
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

func (s *Store) pruneManagedCompleteHistoryTx(ctx context.Context, agentID string) error {
	if agentID != "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE agent_id=? AND sequence NOT IN (SELECT sequence FROM managed_complete_history WHERE agent_id=? ORDER BY sequence DESC LIMIT ?)`, agentID, agentID, maxManagedHistoryEntries)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE agent_id IS NULL AND sequence NOT IN (SELECT sequence FROM managed_complete_history WHERE agent_id IS NULL ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries)
	return err
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
	return s.RecordManagedCompleteHistoryWithDetails(ctx, agentID, category, level, message, eventKey, presentation, nil, nil)
}

func (s *Store) RecordManagedCompleteHistoryWithPresentationAndInput(ctx context.Context, agentID, category, level, message, eventKey string, presentation *ManagedHistoryPresentation, toolInput any) (ManagedHistoryEntry, error) {
	return s.RecordManagedCompleteHistoryWithDetails(ctx, agentID, category, level, message, eventKey, presentation, toolInput, nil)
}

func (s *Store) RecordManagedCompleteHistoryWithDetails(ctx context.Context, agentID, category, level, message, eventKey string, presentation *ManagedHistoryPresentation, toolInput any, messageContent *string) (ManagedHistoryEntry, error) {
	return s.RecordManagedCompleteHistoryWithFullDetails(ctx, agentID, category, level, message, eventKey, presentation, toolInput, messageContent, nil)
}

func (s *Store) RecordManagedCompleteHistoryWithFullDetails(ctx context.Context, agentID, category, level, message, eventKey string, presentation *ManagedHistoryPresentation, toolInput any, messageContent *string, senderIdentity *string) (ManagedHistoryEntry, error) {
	message = boundManagedHistoryMessage(message)
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
	var cleanContent *string
	if messageContent != nil {
		str := strings.ToValidUTF8(*messageContent, "")
		str = strings.ReplaceAll(str, "\x00", "")
		if len(str) > 65536 {
			rIdx := 65536
			for rIdx > 0 && !utf8.RuneStart(str[rIdx]) {
				rIdx--
			}
			str = str[:rIdx]
		}
		cleanContent = &str
	}
	var cleanSender *string
	if senderIdentity != nil {
		str := strings.ToValidUTF8(*senderIdentity, "")
		str = strings.ReplaceAll(str, "\x00", "")
		if len(str) > 128 {
			str = str[:128]
		}
		cleanSender = &str
	}
	var entry ManagedHistoryEntry
	err = s.runImmediate(ctx, func() error {
		entry, err = s.recordManagedCompleteHistoryTx(ctx, agentID, category, level, message, eventKey, presentationJSON, presentation, managedHistoryDetails{ToolInputJSON: toolInputJSON, MessageContent: cleanContent, SenderIdentity: cleanSender})
		return err
	})
	return entry, err
}

// RecordManagedLifecycleHistoryForIncarnation discards stale UI projection facts
// atomically with termination cleanup and same-ID relaunch.
func (s *Store) RecordManagedLifecycleHistoryForIncarnation(ctx context.Context, agentID, incarnation, message, eventKey string, presentation *ManagedHistoryPresentation) (ManagedHistoryEntry, error) {
	if ctx == nil || !isLifecycleText(agentID) || !managedHistoryText(message, 512, false) || len(eventKey) > 200 || !utf8.ValidString(eventKey) {
		return ManagedHistoryEntry{}, errors.New("managed lifecycle history entry is invalid")
	}
	if incarnation == "" {
		return ManagedHistoryEntry{}, nil
	}
	encoded, err := managedHistoryPresentationJSON(presentation)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	var entry ManagedHistoryEntry
	err = s.runImmediate(ctx, func() error {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_history_boundaries b JOIN managed_agents a ON a.agent_id=b.agent_id WHERE b.agent_id=? AND b.incarnation_id=?`, agentID, incarnation).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		entry, err = s.recordManagedCompleteHistoryTx(ctx, agentID, "managed_lifecycle", "info", message, eventKey, encoded, presentation)
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

func boundManagedHistoryMessage(message string) string {
	msg := strings.TrimSpace(message)
	if len(msg) == 0 {
		return "operational event"
	}
	if len(msg) > 512 {
		return msg[:509] + "..."
	}
	return msg
}

// recordManagedCompleteHistoryTx inserts and prunes one Complete row while the
// caller's immediate transaction is open. Generic callers retain the existing
// idempotent event-key behavior; acknowledgement callers use the strict helper
// above so an unrelated row can never be silently reused.
func (s *Store) recordManagedCompleteHistoryTx(ctx context.Context, agentID, category, level, message, eventKey, presentationJSON string, presentation *ManagedHistoryPresentation, details ...managedHistoryDetails) (ManagedHistoryEntry, error) {
	message = boundManagedHistoryMessage(message)
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
			}
			effectiveContent := storedContent
			if detail.MessageContent != nil {
				effectiveContent = sql.NullString{String: *detail.MessageContent, Valid: true}
			}
			if storedPresentation.String != presentationJSON || entry.Level != level || entry.Message != message || storedToolInput.String != effectiveToolInput || storedContent.String != effectiveContent.String || storedContent.Valid != effectiveContent.Valid {
				if _, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level=?,message=?,presentation_json=?,tool_input_json=NULLIF(?,''),message_content=NULLIF(?,'') WHERE sequence=?`, level, message, presentationJSON, effectiveToolInput, effectiveContent.String, sequence); err != nil {
					return ManagedHistoryEntry{}, err
				}
				entry.Level = level
				entry.Message = message
				entry.Presentation = presentation
				if presentation != nil && presentation.Thinking != "" {
					entry.Thinking = presentation.Thinking
				}
			} else {
				entry.Presentation = effectiveManagedHistoryPresentation(storedCategory, entry.Level, entry.Message, storedPresentation.String)
				if entry.Presentation != nil && entry.Presentation.Thinking != "" {
					entry.Thinking = entry.Presentation.Thinking
				}
			}
			entry.ID = fmt.Sprintf("complete:%d", sequence)
			entry.AgentID = agentID
			entry.MessageContent = managedHistoryDetail(effectiveContent)
			entry.SenderIdentity = managedHistoryDetail(storedSender)
			entry.ToolInput = rawManagedHistoryJSON(effectiveToolInput)
			if err := s.recordTimelineHistoryTx(ctx, agentID, storedCategory, entry.Level, entry.Message, entry); err != nil {
				return ManagedHistoryEntry{}, err
			}
			return entry, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ManagedHistoryEntry{}, err
		}
	}
	occurredAt := formatTimestamp(s.clock.Now())
	if detail.OccurredAt != "" {
		occurredAt = detail.OccurredAt
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json,message_content,sender_identity,tool_input_json) VALUES (?,?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,NULLIF(?,''))`, nullableString(agentID), category, level, message, occurredAt, eventKey, presentationJSON, detail.MessageContent, detail.SenderIdentity, detail.ToolInputJSON)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	if err := s.pruneManagedCompleteHistoryTx(ctx, agentID); err != nil {
		return ManagedHistoryEntry{}, err
	}
	entry = managedHistoryEntry(sequence, agentID, occurredAt, level, message, presentation, detail)
	if err := s.recordTimelineHistoryTx(ctx, agentID, category, level, message, entry); err != nil {
		return ManagedHistoryEntry{}, err
	}
	return entry, nil
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

// GetManagedCompleteHistory returns complete history entries for an agent.
func (s *Store) GetManagedCompleteHistory(ctx context.Context, agentID string) ([]ManagedHistoryEntry, error) {
	page, err := s.ManagedHistoryPage(ctx, agentID, "complete", 0, maxManagedHistoryEntries)
	if err == nil && len(page.Entries) > 0 {
		return page.Entries, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,sequence,category,occurred_at,level,message,COALESCE(presentation_json,''),message_content,sender_identity,tool_input_json FROM managed_complete_history WHERE agent_id=? ORDER BY sequence DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ManagedHistoryEntry
	for rows.Next() {
		var sequence int64
		var entryAgentID, occurredAt, level, message, category string
		var presentation, content, sender, toolInput sql.NullString
		if err := rows.Scan(&entryAgentID, &sequence, &category, &occurredAt, &level, &message, &presentation, &content, &sender, &toolInput); err != nil {
			return nil, err
		}
		entries = append(entries, managedHistoryEntry(sequence, entryAgentID, occurredAt, level, message, effectiveManagedHistoryPresentation(category, level, message, presentation.String), managedHistoryDetails{MessageContent: managedHistoryDetail(content), SenderIdentity: managedHistoryDetail(sender), ToolInputJSON: toolInput.String}))
	}
	return entries, rows.Err()
}

// ShouldRecordGoalEvaluation checks whether a goal evaluation should be recorded
// or if it is a redundant consecutive NOT_DONE evaluation that would flood history.
func (s *Store) ShouldRecordGoalEvaluation(ctx context.Context, agentID, outcome, summary string) (bool, error) {
	if strings.ToUpper(outcome) != "NOT_DONE" {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("store is closed")
	}
	var lastMessage, lastSummary string
	err := s.db.QueryRowContext(ctx, `
		SELECT message, COALESCE(message_content, '')
		FROM managed_complete_history
		WHERE agent_id = ? AND category = 'managed_goal_evaluation'
		ORDER BY sequence DESC LIMIT 1
	`, agentID).Scan(&lastMessage, &lastSummary)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	if lastMessage == "Goal Evaluation • NOT_DONE" && lastSummary == summary {
		return false, nil
	}
	return true, nil
}

// lastGoalActivityMatchesReasonTx compares the parsed reason, never raw output.
func (s *Store) lastGoalActivityMatchesReasonTx(ctx context.Context, agentID, reason string) (bool, error) {
	var message, content string
	var storedReason sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT h.message,COALESCE(h.message_content,''),p.reason FROM managed_complete_history h LEFT JOIN embedded_goal_activity_publications p ON p.event_key=h.event_key AND (h.event_key NOT LIKE 'goal-evaluation:%' OR h.event_key LIKE 'goal-evaluation:v2:%') AND (SELECT count(*) FROM embedded_goal_activity_publications p2 WHERE p2.event_key=h.event_key)=1 WHERE h.agent_id=? AND h.category='managed_goal_evaluation' ORDER BY h.sequence DESC LIMIT 1`, agentID).Scan(&message, &content, &storedReason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedReason.Valid {
		content = storedReason.String
	}
	return message == "Goal Evaluation • NOT_DONE" && content == reason, nil
}

// GoalEvaluationActivityKey identifies the durable claim independently of owner changes.
func GoalEvaluationActivityKey(b EmbeddedBinding, k EmbeddedGoalEvaluationKey) string {
	identity := fmt.Sprintf("%q:%q:%d:%d:%q:%d:%d:%q", b.AgentID, b.SessionID, b.Epoch, k.UserSeq, k.UserDigest, k.GoalRevision, k.FinalSeq, k.FinalDigest)
	return fmt.Sprintf("goal-evaluation:v2:%x", sha256.Sum256([]byte(identity)))
}

// BackfillEmbeddedGoalEvaluations ensures that completed evaluations stored in
// embedded_goal_evaluations have corresponding rows in managed_complete_history.
// Consecutive identical NOT_DONE evaluations are deduplicated without removing distinct activity.
func (s *Store) BackfillEmbeddedGoalEvaluations(ctx context.Context) error {
	return s.runImmediate(ctx, func() error { return s.backfillEmbeddedGoalEvaluationsTx(ctx) })
}

func (s *Store) backfillEmbeddedGoalEvaluationsTx(ctx context.Context) error {

	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='embedded_goal_evaluations'`).Scan(&tableExists); err != nil || tableExists == 0 {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.agent_id, e.session_id, e.epoch, e.user_seq, e.user_digest, e.goal_revision, e.final_seq, e.final_digest, e.state, e.reason, COALESCE(e.completed_at, '')
		FROM embedded_goal_evaluations e JOIN managed_agent_policies p ON p.agent_id=e.agent_id
        JOIN embedded_sessions ses ON ses.agent_id=e.agent_id AND ses.session_id=e.session_id AND ses.epoch=e.epoch AND ses.generation=e.generation AND ses.current=1
        JOIN managed_agents m ON m.agent_id=e.agent_id AND m.pi_session_id=e.session_id AND m.session_epoch=e.epoch AND m.runtime_generation=e.generation AND m.runtime='embedded' AND m.desired_state='running' AND (m.lifecycle_state='running' OR (m.lifecycle_state='starting' AND EXISTS (SELECT 1 FROM managed_http_operations o WHERE o.operation_id=m.launch_operation_id AND o.state='pending')))
		WHERE e.state IN ('DONE', 'NOT_DONE', 'COMPLETED') AND e.completed_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM embedded_goal_activity_publications a WHERE a.agent_id=e.agent_id AND a.session_id=e.session_id AND a.epoch=e.epoch AND a.user_seq=e.user_seq AND a.user_digest=e.user_digest AND a.goal_revision=e.goal_revision)
		AND p.desired_revision=e.goal_revision AND p.applied_revision=e.goal_revision AND p.projection_state='applied'
		AND (p.goal_enabled=1 OR (e.state IN ('DONE','COMPLETED') AND p.goal_enabled=0 AND p.evaluator_state='done' AND p.last_outcome='COMPLETED'))
		ORDER BY e.completed_at ASC, e.user_seq ASC, e.final_seq ASC, e.session_id ASC, e.epoch ASC, e.user_digest ASC, e.goal_revision ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type evalRecord struct {
		agentID      string
		sessionID    string
		epoch        int64
		userDigest   string
		goalRevision int64
		userSeq      int64
		finalSeq     int64
		finalDigest  string
		state        string
		reason       string
		completedAt  string
	}
	agentRecords := make(map[string][]evalRecord)
	for rows.Next() {
		var r evalRecord
		if err := rows.Scan(&r.agentID, &r.sessionID, &r.epoch, &r.userSeq, &r.userDigest, &r.goalRevision, &r.finalSeq, &r.finalDigest, &r.state, &r.reason, &r.completedAt); err != nil {
			return err
		}
		agentRecords[r.agentID] = append(agentRecords[r.agentID], r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, records := range agentRecords {
		for _, r := range records {
			if r.state == "DONE" {
				r.state = "COMPLETED"
			}
			// Legacy keys omit session and revision, so an existing row cannot
			// establish that this particular evaluation was published. Migration
			// 128 already marks all pre-upgrade completed evidence as handled.
			eventKey := GoalEvaluationActivityKey(EmbeddedBinding{AgentID: r.agentID, SessionID: r.sessionID, Epoch: r.epoch}, EmbeddedGoalEvaluationKey{UserSeq: r.userSeq, UserDigest: r.userDigest, GoalRevision: r.goalRevision, FinalSeq: r.finalSeq, FinalDigest: r.finalDigest})
			if r.state == "NOT_DONE" {
				duplicate, err := s.lastGoalActivityMatchesReasonTx(ctx, r.agentID, r.reason)
				if err != nil {
					return err
				}
				if duplicate {
					if _, err := s.db.ExecContext(ctx, `INSERT INTO embedded_goal_activity_publications(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,event_key,reason) VALUES (?,?,?,?,?,?,?,?)`, r.agentID, r.sessionID, r.epoch, r.userSeq, r.userDigest, r.goalRevision, eventKey, r.reason); err != nil {
						return err
					}
					continue
				}
			}
			summary := r.reason
			if summary == "" {
				if r.state == "NOT_DONE" {
					summary = "Actionable steps remain"
				} else {
					summary = "COMPLETED"
				}
			}
			if len(summary) > 500 {
				summary = summary[:497] + "..."
			}
			outcome := strings.ToLower(r.state)
			pres := &ManagedHistoryPresentation{
				Kind:    "goal_evaluation",
				Outcome: outcome,
				Summary: summary,
			}
			presentationJSON, err := managedHistoryPresentationJSON(pres)
			if err != nil {
				continue
			}
			msg := "Goal Evaluation • " + r.state
			reasonCopy := r.reason
			detail := managedHistoryDetails{
				MessageContent: &reasonCopy,
				OccurredAt:     r.completedAt,
			}
			if _, err := s.recordManagedCompleteHistoryTx(ctx, r.agentID, "managed_goal_evaluation", "info", msg, eventKey, presentationJSON, pres, detail); err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, `INSERT INTO embedded_goal_activity_publications(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,event_key,reason) VALUES (?,?,?,?,?,?,?,?)`, r.agentID, r.sessionID, r.epoch, r.userSeq, r.userDigest, r.goalRevision, eventKey, r.reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// BackfillEmbeddedRecentToolCalls restores recent tool calls from embedded_entries into managed_complete_history
// so agents whose complete history was truncated or missing tool calls display their full operational timeline.
func (s *Store) BackfillEmbeddedRecentToolCalls(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}

	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='embedded_entries'`).Scan(&tableExists); err != nil || tableExists == 0 {
		return nil
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT agent_id, body
		FROM embedded_entries
		WHERE kind = 'assistant' AND body LIKE '%toolCall%'
		ORDER BY rowid DESC LIMIT 150
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rawToolItem struct {
		agentID   string
		callID    string
		name      string
		arguments json.RawMessage
	}
	var items []rawToolItem

	for rows.Next() {
		var agentID, bodyStr string
		if err := rows.Scan(&agentID, &bodyStr); err != nil {
			return err
		}
		var parsed struct {
			Content []struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
		}
		if err := json.Unmarshal([]byte(bodyStr), &parsed); err != nil {
			continue
		}
		for _, c := range parsed.Content {
			if c.Type == "toolCall" && c.ID != "" && c.Name != "" {
				items = append(items, rawToolItem{
					agentID:   agentID,
					callID:    c.ID,
					name:      c.Name,
					arguments: c.Arguments,
				})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Reverse to chronological order (older first)
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}

	for _, item := range items {
		eventKey := fmt.Sprintf("tool:%s:%s", item.agentID, item.callID)
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_complete_history WHERE event_key=?`, eventKey).Scan(&exists)
		if err == nil && exists == 1 {
			continue
		}
		pres := &ManagedHistoryPresentation{
			Kind:     "tool",
			ToolName: item.name,
			Outcome:  "completed",
			Summary:  "Ran " + item.name,
		}
		presentationJSON, err := managedHistoryPresentationJSON(pres)
		if err != nil {
			continue
		}
		var toolInput any
		if len(item.arguments) > 0 {
			_ = json.Unmarshal(item.arguments, &toolInput)
		}
		detail := managedHistoryDetails{
			ToolInputJSON: string(item.arguments),
		}
		msg := "Ran " + item.name
		_, _ = s.recordManagedCompleteHistoryTx(ctx, item.agentID, "managed_tool", "info", msg, eventKey, presentationJSON, pres, detail)
	}
	return nil
}

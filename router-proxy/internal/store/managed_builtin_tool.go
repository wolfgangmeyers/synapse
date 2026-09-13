package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrManagedBuiltinInvocationNotFound = errors.New("managed builtin invocation was not found")
var ErrManagedBuiltinInvocationConflict = errors.New("managed builtin invocation has a conflicting terminal outcome")

type ManagedBuiltinToolOutcome string

const (
	ManagedBuiltinToolActive      ManagedBuiltinToolOutcome = "active"
	ManagedBuiltinToolSucceeded   ManagedBuiltinToolOutcome = "succeeded"
	ManagedBuiltinToolFailed      ManagedBuiltinToolOutcome = "failed"
	ManagedBuiltinToolInterrupted ManagedBuiltinToolOutcome = "interrupted"
)

func validManagedBuiltinToolName(name string) bool {
	return name == "read" || name == "bash" || name == "edit" || name == "write"
}

func validManagedBuiltinToolCallID(value string) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func managedBuiltinInvocationKey(fence ManagedRuntimeFence, callID string) string {
	// Each component is length-delimited so IDs cannot collide by containing a
	// separator. The persisted fence columns remain the auditable identity.
	parts := []string{fence.AgentID, fence.PiSessionID, fmt.Sprintf("%d", fence.SessionEpoch), fmt.Sprintf("%d", fence.ProcessPID), fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fmt.Sprintf("%d", fence.ManifestRevision), fence.ManifestHash, callID}
	var canonical strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&canonical, "%d:%s", len(part), part)
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return fmt.Sprintf("mbt1_%x", sum[:])
}

func managedBuiltinPresentation(tool string, outcome ManagedBuiltinToolOutcome) (string, string, *ManagedHistoryPresentation, error) {
	if !validManagedBuiltinToolName(tool) {
		return "", "", nil, errors.New("managed builtin tool name is invalid")
	}
	switch outcome {
	case ManagedBuiltinToolActive:
		return "info", "Running " + tool, &ManagedHistoryPresentation{Kind: "tool", ToolName: tool, Outcome: "active", Summary: "Running"}, nil
	case ManagedBuiltinToolSucceeded:
		return "info", "Ran " + tool, &ManagedHistoryPresentation{Kind: "tool", ToolName: tool, Outcome: "success", Summary: "Completed successfully"}, nil
	case ManagedBuiltinToolFailed:
		return "error", strings.ToUpper(tool[:1]) + tool[1:] + " failed", &ManagedHistoryPresentation{Kind: "tool", ToolName: tool, Outcome: "failed", Summary: "The operation could not be completed.", Error: &ManagedHistoryError{Code: "TOOL_FAILED", Retryable: false, DetailAvailable: false}}, nil
	case ManagedBuiltinToolInterrupted:
		return "error", strings.ToUpper(tool[:1]) + tool[1:] + " interrupted", &ManagedHistoryPresentation{Kind: "tool", ToolName: tool, Outcome: "interrupted", Summary: "The operation could not be completed.", Error: &ManagedHistoryError{Code: "TOOL_INTERRUPTED", Retryable: false, DetailAvailable: false}}, nil
	default:
		return "", "", nil, errors.New("managed builtin tool outcome is invalid")
	}
}

func managedBuiltinInvocationEntryTx(ctx context.Context, db *sql.DB, key string) (ManagedHistoryEntry, ManagedBuiltinToolOutcome, error) {
	var entry ManagedHistoryEntry
	var category, presentation string
	var state string
	err := db.QueryRowContext(ctx, `SELECT h.sequence,h.agent_id,h.category,h.occurred_at,h.level,h.message,COALESCE(h.presentation_json,''),i.state
		FROM managed_builtin_tool_invocations i
		JOIN managed_complete_history h ON h.sequence=i.complete_sequence
		WHERE i.invocation_key=?`, key).Scan(&entry.ID, &entry.AgentID, &category, &entry.At, &entry.Level, &entry.Message, &presentation, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedHistoryEntry{}, "", ErrManagedBuiltinInvocationNotFound
	}
	if err != nil {
		return ManagedHistoryEntry{}, "", err
	}
	entry.ID = "complete:" + entry.ID
	entry.Presentation = effectiveManagedHistoryPresentation(category, entry.Level, entry.Message, presentation)
	return entry, ManagedBuiltinToolOutcome(state), nil
}

func managedBuiltinFenceIsCurrentTx(ctx context.Context, db *sql.DB, fence ManagedRuntimeFence) (bool, error) {
	var present int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// BeginManagedBuiltinInvocation creates exactly one active Complete entry. It
// stores no Pi arguments or output; the caller provides only the allowlisted
// builtin name and opaque tool-call correlation ID.
func (s *Store) BeginManagedBuiltinInvocation(ctx context.Context, fence ManagedRuntimeFence, toolCallID, toolName string) (ManagedHistoryEntry, bool, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) || !validManagedBuiltinToolCallID(toolCallID) || !validManagedBuiltinToolName(toolName) {
		return ManagedHistoryEntry{}, false, errors.New("managed builtin invocation is invalid")
	}
	key := managedBuiltinInvocationKey(fence, toolCallID)
	var entry ManagedHistoryEntry
	created := false
	err := s.runImmediate(ctx, func() error {
		current, err := managedBuiltinFenceIsCurrentTx(ctx, s.db, fence)
		if err != nil {
			return err
		}
		if !current {
			return errors.New("managed builtin invocation fence is stale")
		}
		if entry, _, err = managedBuiltinInvocationEntryTx(ctx, s.db, key); err == nil {
			return nil
		} else if !errors.Is(err, ErrManagedBuiltinInvocationNotFound) {
			return err
		}
		level, message, presentation, err := managedBuiltinPresentation(toolName, ManagedBuiltinToolActive)
		if err != nil {
			return err
		}
		encoded, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,?)`, fence.AgentID, "managed_builtin_tool", level, message, now, key, encoded)
		if err != nil {
			return err
		}
		sequence, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_builtin_tool_invocations(invocation_key,agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_sha256,tool_call_id,tool_name,state,error_code,started_at,terminal_at,complete_sequence)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'active',NULL,?,NULL,?)`, key, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash, toolCallID, toolName, now, sequence)
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
			return err
		}
		entry = ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: fence.AgentID, At: now, Level: level, Message: message, Presentation: presentation}
		created = true
		return nil
	})
	return entry, created, err
}

// FinishManagedBuiltinInvocation changes only the matching active Complete row.
// An exact replay is idempotent; any competing terminal claim is rejected.
func (s *Store) FinishManagedBuiltinInvocation(ctx context.Context, fence ManagedRuntimeFence, toolCallID string, outcome ManagedBuiltinToolOutcome) (ManagedHistoryEntry, bool, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) || !validManagedBuiltinToolCallID(toolCallID) || (outcome != ManagedBuiltinToolSucceeded && outcome != ManagedBuiltinToolFailed) {
		return ManagedHistoryEntry{}, false, errors.New("managed builtin terminal invocation is invalid")
	}
	key := managedBuiltinInvocationKey(fence, toolCallID)
	var entry ManagedHistoryEntry
	changed := false
	err := s.runImmediate(ctx, func() error {
		var tool, state string
		var sequence int64
		err := s.db.QueryRowContext(ctx, `SELECT tool_name,state,complete_sequence FROM managed_builtin_tool_invocations WHERE invocation_key=?`, key).Scan(&tool, &state, &sequence)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrManagedBuiltinInvocationNotFound
		}
		if err != nil {
			return err
		}
		if ManagedBuiltinToolOutcome(state) != ManagedBuiltinToolActive {
			entry, _, err = managedBuiltinInvocationEntryTx(ctx, s.db, key)
			if err != nil {
				return err
			}
			if ManagedBuiltinToolOutcome(state) != outcome {
				return ErrManagedBuiltinInvocationConflict
			}
			return nil
		}
		level, message, presentation, err := managedBuiltinPresentation(tool, outcome)
		if err != nil {
			return err
		}
		encoded, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_builtin_tool_invocations SET state=?,error_code=?,terminal_at=? WHERE invocation_key=? AND state='active'`, outcome, map[ManagedBuiltinToolOutcome]string{ManagedBuiltinToolSucceeded: "", ManagedBuiltinToolFailed: "TOOL_FAILED"}[outcome], now, key)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return ErrManagedBuiltinInvocationConflict
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level=?,message=?,presentation_json=? WHERE sequence=?`, level, message, encoded, sequence); err != nil {
			return err
		}
		entry = ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", sequence), AgentID: fence.AgentID, At: "", Level: level, Message: message, Presentation: presentation}
		var occurredAt string
		if err = s.db.QueryRowContext(ctx, `SELECT occurred_at FROM managed_complete_history WHERE sequence=?`, sequence).Scan(&occurredAt); err != nil {
			return err
		}
		entry.At = occurredAt
		changed = true
		return nil
	})
	return entry, changed, err
}

// InterruptActiveManagedBuiltinInvocations closes only an exact lost runtime
// fence. Process loss is not a success signal, so every still-active builtin
// gets the same safe interrupted presentation.
func (s *Store) InterruptActiveManagedBuiltinInvocations(ctx context.Context, fence ManagedRuntimeFence) ([]ManagedHistoryEntry, error) {
	if ctx == nil || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed builtin interruption fence is invalid")
	}
	entries := []ManagedHistoryEntry{}
	err := s.runImmediate(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT invocation_key,tool_name,complete_sequence FROM managed_builtin_tool_invocations WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_sha256=? AND state='active' ORDER BY complete_sequence ASC`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash)
		if err != nil {
			return err
		}
		type active struct {
			key, tool string
			sequence  int64
		}
		var activeRows []active
		for rows.Next() {
			var item active
			if err := rows.Scan(&item.key, &item.tool, &item.sequence); err != nil {
				_ = rows.Close()
				return err
			}
			activeRows = append(activeRows, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		for _, item := range activeRows {
			level, message, presentation, err := managedBuiltinPresentation(item.tool, ManagedBuiltinToolInterrupted)
			if err != nil {
				return err
			}
			encoded, err := managedHistoryPresentationJSON(presentation)
			if err != nil {
				return err
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE managed_builtin_tool_invocations SET state='interrupted',error_code='TOOL_INTERRUPTED',terminal_at=? WHERE invocation_key=? AND state='active'`, now, item.key); err != nil {
				return err
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level=?,message=?,presentation_json=? WHERE sequence=?`, level, message, encoded, item.sequence); err != nil {
				return err
			}
			var at string
			if err = s.db.QueryRowContext(ctx, `SELECT occurred_at FROM managed_complete_history WHERE sequence=?`, item.sequence).Scan(&at); err != nil {
				return err
			}
			entries = append(entries, ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", item.sequence), AgentID: fence.AgentID, At: at, Level: level, Message: message, Presentation: presentation})
		}
		return nil
	})
	return entries, err
}

// InterruptAllManagedBuiltinInvocations closes unreconstructable active rows
// during router startup before any history endpoint can expose them.
func (s *Store) InterruptAllManagedBuiltinInvocations(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_builtin_tool_invocations SET state='interrupted',error_code='TOOL_INTERRUPTED',terminal_at=? WHERE state='active'`, now)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_complete_history SET level='error',message=CASE (SELECT tool_name FROM managed_builtin_tool_invocations i WHERE i.complete_sequence=managed_complete_history.sequence) WHEN 'read' THEN 'Read interrupted' WHEN 'bash' THEN 'Bash interrupted' WHEN 'edit' THEN 'Edit interrupted' WHEN 'write' THEN 'Write interrupted' ELSE message END,presentation_json=CASE (SELECT tool_name FROM managed_builtin_tool_invocations i WHERE i.complete_sequence=managed_complete_history.sequence) WHEN 'read' THEN '{"kind":"tool","tool_name":"read","outcome":"interrupted","summary":"The operation could not be completed.","error":{"code":"TOOL_INTERRUPTED","retryable":false,"detail_available":false}}' WHEN 'bash' THEN '{"kind":"tool","tool_name":"bash","outcome":"interrupted","summary":"The operation could not be completed.","error":{"code":"TOOL_INTERRUPTED","retryable":false,"detail_available":false}}' WHEN 'edit' THEN '{"kind":"tool","tool_name":"edit","outcome":"interrupted","summary":"The operation could not be completed.","error":{"code":"TOOL_INTERRUPTED","retryable":false,"detail_available":false}}' WHEN 'write' THEN '{"kind":"tool","tool_name":"write","outcome":"interrupted","summary":"The operation could not be completed.","error":{"code":"TOOL_INTERRUPTED","retryable":false,"detail_available":false}}' ELSE presentation_json END WHERE sequence IN (SELECT complete_sequence FROM managed_builtin_tool_invocations WHERE state='interrupted' AND terminal_at=?)`, now)
		return err
	})
}

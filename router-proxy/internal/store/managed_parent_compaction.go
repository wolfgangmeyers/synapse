package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

// ManagedParentCompactionEvent is the closed, source-fenced form of one Pi
// compaction event. The router derives the parent identity from its binding;
// the extension never supplies an agent or session identifier.
type ManagedParentCompactionEvent struct {
	Sequence int64
	State    string
	Outcome  string
}

func validManagedParentCompactionEvent(event ManagedParentCompactionEvent) bool {
	if event.Sequence < 0 || (event.State != "compacting" && event.State != "idle") {
		return false
	}
	if event.State == "compacting" {
		return event.Outcome == ""
	}
	switch event.Outcome {
	case "completed", "failed", "aborted", "reconciled_unknown":
		return true
	default:
		return false
	}
}

func managedParentCompactionHistoryKey(fence ManagedRuntimeFence, event ManagedParentCompactionEvent) string {
	value := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%s\x00%s", fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, event.Sequence, event.State, event.Outcome)
	sum := sha256.Sum256([]byte(value))
	return "parent-compaction:" + hex.EncodeToString(sum[:])
}

// ApplyManagedParentCompaction records the authoritative installed-Pi event
// and updates the live badge atomically. A newer source sequence is required,
// so a delayed end cannot clear a later compaction start.
// ReconcileManagedParentCompaction repairs a missed start from get_state and
// marks a lost terminal event as unknown. It never upgrades a missing end to
// success. This is intentionally called only after the ready binding confirms
// the same Pi session that supplied get_state.
func (s *Store) ReconcileManagedParentCompaction(ctx context.Context, fence ManagedRuntimeFence, isCompacting bool) (bool, error) {
	if !validManagedRuntimeFence(fence) {
		return false, errors.New("managed parent compaction fence is invalid")
	}
	var event *ManagedParentCompactionEvent
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, errors.New("store is closed")
	}
	var sequence int64
	var compacting int
	err := s.db.QueryRowContext(ctx, `SELECT source_sequence,compacting FROM managed_parent_compaction_state
		WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
		fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&sequence, &compacting)
	s.mu.Unlock()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if isCompacting {
			event = &ManagedParentCompactionEvent{Sequence: 0, State: "compacting"}
		}
	} else if compacting != 0 && !isCompacting {
		event = &ManagedParentCompactionEvent{Sequence: sequence + 1, State: "idle", Outcome: "reconciled_unknown"}
	}
	if event == nil {
		return false, nil
	}
	return s.ApplyManagedParentCompaction(ctx, fence, *event)
}

func (s *Store) ApplyManagedParentCompaction(ctx context.Context, fence ManagedRuntimeFence, event ManagedParentCompactionEvent) (bool, error) {
	if !validManagedRuntimeFence(fence) || !validManagedParentCompactionEvent(event) {
		return false, errors.New("managed parent compaction event is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var lastSequence int64
		var active int
		err := s.db.QueryRowContext(ctx, `SELECT source_sequence,active FROM managed_parent_compaction_state
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&lastSequence, &active)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && event.Sequence <= lastSequence {
			return nil
		}

		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		compacting := boolInt(event.State == "compacting")
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_compaction_state(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,source_sequence,compacting,active,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation)
			DO UPDATE SET source_sequence=excluded.source_sequence, compacting=excluded.compacting, active=excluded.active, updated_at=excluded.updated_at`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, event.Sequence, compacting, active, now)
		if err != nil {
			return err
		}
		if err := s.recomputeManagedParentActivityTx(ctx, fence); err != nil {
			return err
		}
		key := managedParentCompactionHistoryKey(fence, event)
		message := "Compacting context"
		outcome := "compacting"
		if event.State == "idle" {
			outcome = event.Outcome
			switch event.Outcome {
			case "completed":
				message = "Context compacted"
			case "failed":
				message = "Context compaction failed"
			default:
				message = "Context compaction interrupted — outcome unknown"
			}
		}
		presentationJSON, presentationErr := managedHistoryPresentationJSON(&ManagedHistoryPresentation{Kind: "compaction", Outcome: outcome})
		if presentationErr != nil {
			return presentationErr
		}
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,?)`, fence.AgentID, "managed_parent_compaction", "info", message, now, key, presentationJSON)
		if err != nil {
			return err
		}
		completeSequence, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_compaction_transitions(event_key,complete_sequence,agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,source_sequence,state,outcome,occurred_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, key, completeSequence, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, event.Sequence, event.State, nullableString(event.Outcome), now)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		if err == nil {
			updated = true
		}
		return err
	})
	return updated, err
}

// ManagedParentCompactionHistoryEntry returns the one redacted Complete card
// written for a known native Pi transition. The manager uses it only to wake
// live subscribers after the same durable transaction has committed.
// RecordManagedParentCompactionNotification records the actual Pi handoff
// after a successful terminal transition. The visible custom message is the
// receipt; the root-control response alone never creates this card.
func (s *Store) RecordManagedParentCompactionNotification(ctx context.Context, fence ManagedRuntimeFence, sequence int64) (ManagedHistoryEntry, bool, error) {
	if !validManagedRuntimeFence(fence) || sequence < 1 {
		return ManagedHistoryEntry{}, false, errors.New("managed post-compaction notification is invalid")
	}
	key := managedParentCompactionNotificationHistoryKey(fence, sequence)
	entry := ManagedHistoryEntry{AgentID: fence.AgentID}
	created := false
	err := s.runImmediate(ctx, func() error {
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
			}
			return err
		}
		var completed int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_parent_compaction_transitions WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=? AND state='idle' AND outcome='completed' AND source_sequence<? ORDER BY source_sequence DESC LIMIT 1`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, sequence).Scan(&completed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("post-compaction notification has no completed compaction")
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success", Summary: "Post-compaction handoff delivered."}
		presentationJSON, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		var existing int64
		lookupErr := s.db.QueryRowContext(ctx, `SELECT sequence FROM managed_complete_history WHERE event_key=?`, key).Scan(&existing)
		if lookupErr == nil {
			return nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json) VALUES (?,?,?,?,?,?,?)`, fence.AgentID, "managed_parent_compaction_notification", "info", "Post-compaction notification delivered", now, key, presentationJSON)
		if err != nil {
			return err
		}
		completeSequence, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
			return err
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		entry = ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", completeSequence), AgentID: fence.AgentID, At: now, Level: "info", Message: "Post-compaction notification delivered", Presentation: presentation}
		created = true
		return nil
	})
	return entry, created, err
}

func managedParentCompactionNotificationHistoryKey(fence ManagedRuntimeFence, sequence int64) string {
	value := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00post-compaction-notification", fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, sequence)
	sum := sha256.Sum256([]byte(value))
	return "parent-compaction-notification:" + hex.EncodeToString(sum[:])
}

func (s *Store) ManagedParentCompactionHistoryEntry(ctx context.Context, fence ManagedRuntimeFence, event ManagedParentCompactionEvent) (ManagedHistoryEntry, error) {
	if !validManagedRuntimeFence(fence) || !validManagedParentCompactionEvent(event) {
		return ManagedHistoryEntry{}, errors.New("managed parent compaction history lookup is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedHistoryEntry{}, errors.New("store is closed")
	}
	var sequence int64
	var presentation sql.NullString
	entry := ManagedHistoryEntry{AgentID: fence.AgentID}
	err := s.db.QueryRowContext(ctx, `SELECT sequence,occurred_at,level,message,COALESCE(presentation_json,'') FROM managed_complete_history WHERE event_key=?`, managedParentCompactionHistoryKey(fence, event)).Scan(&sequence, &entry.At, &entry.Level, &entry.Message, &presentation)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	entry.ID = fmt.Sprintf("complete:%d", sequence)
	entry.Presentation = managedHistoryPresentation(presentation.String)
	return entry, nil
}

// managedParentProjectedActivity keeps compaction visible while Pi is paused,
// then returns the actual parent-turn state when compaction ends.
func managedParentProjectedActivity(compacting, active bool) string {
	if compacting {
		return "compacting"
	}
	if active {
		return "active"
	}
	return "idle"
}

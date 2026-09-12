package store

import (
	"context"
	"database/sql"
	"errors"
)

// ManagedParentActivityEvent is the closed, source-fenced form of Pi's native
// parent-turn lifecycle. Its sequence is assigned while decoding one Pi stdout
// stream, so a delayed settled event cannot clear a newer turn.
type ManagedParentActivityEvent struct {
	Sequence int64
	State    string
}

func validManagedParentActivityEvent(event ManagedParentActivityEvent) bool {
	return event.Sequence >= 0 && (event.State == "active" || event.State == "idle")
}

// ApplyManagedParentActivity retains the latest parent-turn state beside the
// compaction state. Both use the same stdout sequence, which lets compaction
// remain visible until Pi explicitly ends it even when the parent turn settles
// first. This intentionally records only router-authoritative Pi events; a
// prompt submission or a missing event is never treated as proof of activity.
func (s *Store) ApplyManagedParentActivity(ctx context.Context, fence ManagedRuntimeFence, event ManagedParentActivityEvent) (bool, error) {
	if !validManagedRuntimeFence(fence) || !validManagedParentActivityEvent(event) {
		return false, errors.New("managed parent activity event is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var lastSequence int64
		var compacting int
		err := s.db.QueryRowContext(ctx, `SELECT source_sequence,compacting FROM managed_parent_compaction_state
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND process_incarnation=? AND runtime_generation=?`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration).Scan(&lastSequence, &compacting)
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
		active := boolInt(event.State == "active")
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_parent_compaction_state(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,source_sequence,compacting,active,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT(agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation)
			DO UPDATE SET source_sequence=excluded.source_sequence, compacting=excluded.compacting, active=excluded.active, updated_at=excluded.updated_at`,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, event.Sequence, compacting, active, now)
		if err != nil {
			return err
		}
		activity := managedParentProjectedActivity(compacting != 0, active != 0)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND activity_state IN ('active','idle','compacting')`, append([]any{activity, now}, managedFenceValues(fence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		if err == nil {
			updated = true
		}
		return err
	})
	return updated, err
}

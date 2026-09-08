package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ManagedResetReservation is the reset-specific admission contract. Reset has
// two durable fences: SourceFence remains immutable lineage while LiveFence is
// used only to prove which managed row the transaction may change.
type ManagedResetReservation struct {
	AgentID, Method, CanonicalPath, RequestID, CanonicalBodyHash string
	SourceFence, LiveFence                                       ManagedRuntimeFence
	TargetFence                                                  *ManagedRuntimeFence
	PlannedTargetRuntimeGeneration                               string
	InitialState, InitialStage, RetryOf, TargetArtifactID        string
	Operation                                                    ManagedOperation
}

type ManagedResetAdmission struct {
	Operation *ManagedOperation
	Action    *ManagedLifecycleAction
	Agent     *ManagedAgentRecord
	Replayed  bool
}

type ManagedResetConflictError struct{ AgentID string }

func (e *ManagedResetConflictError) Error() string {
	return "managed reset conflicts with another lifecycle operation"
}
func (e *ManagedResetConflictError) Code() string { return "conflict" }

// ManagedSessionArtifactRecord is a read-only artifact ledger view. It keeps
// filesystem ownership out of the store while giving the artifact owner the
// captured device/inode tuple it must recheck before unlinking.
type ManagedSessionArtifactRecord struct{ ManagedSessionArtifact }

func scanManagedSessionArtifact(scanner interface{ Scan(...any) error }) (*ManagedSessionArtifactRecord, error) {
	var record ManagedSessionArtifactRecord
	var device, inode sql.NullString
	if err := scanner.Scan(&record.ArtifactID, &record.AgentID, &record.SessionEpoch, &record.PiSessionID,
		&record.RootRelativePath, &record.PathSHA256, &record.DiscoveryState, &device, &inode,
		&record.CleanupState, &record.DiscoveredAt, &record.CleanupUpdatedAt); err != nil {
		return nil, err
	}
	record.FileDevice, record.FileInode = device.String, inode.String
	return &record, nil
}

const managedSessionArtifactSelect = `SELECT artifact_id,agent_id,session_epoch,pi_session_id,root_relative_path,path_sha256,discovery_state,file_device,file_inode,cleanup_state,discovered_at,cleanup_updated_at FROM managed_session_artifacts`

func (s *Store) ManagedSessionArtifactForSession(ctx context.Context, agentID string, epoch int64, sessionID string) (*ManagedSessionArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedSessionArtifact(s.db.QueryRowContext(ctx, managedSessionArtifactSelect+` WHERE agent_id=? AND session_epoch=? AND pi_session_id=?`, agentID, epoch, sessionID))
}

// ManagedSessionArtifactForCandidate finds a session identity across every
// managed agent and epoch. A Pi session UUID is globally unique, so CreateNew
// must reject even a retained artifact that belongs to another epoch or agent.
func (s *Store) ManagedSessionArtifactForCandidate(ctx context.Context, sessionID string) (*ManagedSessionArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedSessionArtifact(s.db.QueryRowContext(ctx, managedSessionArtifactSelect+` WHERE pi_session_id=? LIMIT 1`, sessionID))
}

func (s *Store) ManagedSessionArtifact(ctx context.Context, artifactID string) (*ManagedSessionArtifactRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedSessionArtifact(s.db.QueryRowContext(ctx, managedSessionArtifactSelect+` WHERE artifact_id=?`, artifactID))
}

// MarkManagedSessionArtifactCleanup moves an artifact through retained,
// pending, and unlinked. The filesystem owner performs its check or unlink
// between pending and unlinked; the store never accepts an arbitrary path.
func (s *Store) MarkManagedSessionArtifactCleanup(ctx context.Context, artifactID, from, to string) error {
	if !managedCanonicalUUIDRE.MatchString(artifactID) || (from != "retained" && from != "pending") || (to != "retained" && to != "pending" && to != "unlinked") {
		return errors.New("managed session artifact cleanup transition is invalid")
	}
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_session_artifacts SET cleanup_state=?,cleanup_updated_at=? WHERE artifact_id=? AND cleanup_state=?`, to, formatTimestamp(s.clock.Now()), artifactID, from)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{}
		}
		return nil
	})
}

// refreshManagedSessionArtifactTx permits only stronger evidence for the same
// agent/session/epoch/path. In particular, an artifact can never be retargeted
// to a different file after a reset retry has retained it.
func (s *Store) RefreshManagedSessionArtifact(ctx context.Context, artifact ManagedSessionArtifact) (*ManagedSessionArtifactRecord, error) {
	current, err := s.ManagedSessionArtifactForSession(ctx, artifact.AgentID, artifact.SessionEpoch, artifact.PiSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := s.RecordManagedSessionArtifact(ctx, artifact); err != nil {
			return nil, err
		}
		return s.ManagedSessionArtifact(ctx, artifact.ArtifactID)
	}
	if err != nil {
		return nil, err
	}
	if current.RootRelativePath != artifact.RootRelativePath || current.PathSHA256 != artifact.PathSHA256 || current.CleanupState != "retained" {
		return nil, &ManagedResetConflictError{AgentID: artifact.AgentID}
	}
	if current.DiscoveryState == "materialized" || artifact.DiscoveryState != "materialized" {
		return current, nil
	}
	if artifact.FileDevice == "" || artifact.FileInode == "" {
		return nil, errors.New("managed session artifact materialization is invalid")
	}
	err = s.runImmediate(ctx, func() error {
		result, updateErr := s.db.ExecContext(ctx, `UPDATE managed_session_artifacts SET discovery_state='materialized',file_device=?,file_inode=?,discovered_at=?,cleanup_updated_at=? WHERE artifact_id=? AND agent_id=? AND session_epoch=? AND pi_session_id=? AND root_relative_path=? AND path_sha256=? AND discovery_state='not_materialized' AND cleanup_state='retained'`, artifact.FileDevice, artifact.FileInode, artifact.DiscoveredAt, artifact.CleanupUpdatedAt, current.ArtifactID, artifact.AgentID, artifact.SessionEpoch, artifact.PiSessionID, artifact.RootRelativePath, artifact.PathSHA256)
		if updateErr != nil {
			return updateErr
		}
		changed, updateErr := result.RowsAffected()
		if updateErr != nil {
			return updateErr
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: artifact.AgentID}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ManagedSessionArtifact(ctx, current.ArtifactID)
}

func resetLifecycleInitialState(reservation ManagedResetReservation) (string, string) {
	state, stage := reservation.InitialState, reservation.InitialStage
	if state == "" {
		state, stage = "admitted", "admission"
	}
	return state, stage
}

func resetActionInsertArgs(input ManagedLifecycleActionInput) []any {
	args := []any{input.ActionID, input.Operation.OperationID, input.AgentID, input.Kind, input.Source, nil, nullIfEmpty(input.RetryOf), input.ExecutionOwner, input.State, input.Stage}
	args = append(args, lifecycleFenceValues(input.SourceFence)...)
	args = append(args, lifecycleFenceValues(input.TargetFence)...)
	args = append(args, nullIfEmpty(input.PlannedTargetRuntimeGeneration), nullIfEmpty(input.TargetArtifactID), input.CreatedAt, input.UpdatedAt)
	return args
}

func resetActiveLifecycleActionTx(ctx context.Context, db *sql.DB, agentID string) (*ManagedLifecycleAction, error) {
	return scanManagedLifecycleAction(db.QueryRowContext(ctx, managedLifecycleActionSelect+` WHERE agent_id=? AND state IN ('admitted','awaiting_settle','stopping_source','starting_target','awaiting_target_ready','old_artifact_cleanup','terminating','cleanup')`, agentID))
}

// ActiveManagedLifecycleAction exposes the one durable active-lane row for a
// manager that has no live binding. Callers still pass their selected fence to
// the reservation, which repeats precedence under its immediate transaction.
func (s *Store) ActiveManagedLifecycleAction(ctx context.Context, agentID string) (*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return resetActiveLifecycleActionTx(ctx, s.db, agentID)
}

// ReserveManagedReset is intentionally separate from ReserveManagedLifecycle.
// Its source and live fences make stage-owned reset retries representable
// without weakening the normal one-fence lifecycle reservation seam.
func (s *Store) ReserveManagedReset(ctx context.Context, reservation ManagedResetReservation) (ManagedResetAdmission, error) {
	if reservation.Operation.OperationID == "" {
		reservation.Operation.OperationID = reservation.RequestID
	}
	if reservation.Operation.Method == "" {
		reservation.Operation.Method = reservation.Method
	}
	if reservation.Operation.CanonicalPath == "" {
		reservation.Operation.CanonicalPath = reservation.CanonicalPath
	}
	if reservation.Operation.RequestID == "" {
		reservation.Operation.RequestID = reservation.RequestID
	}
	if reservation.Operation.CanonicalBodyHash == "" {
		reservation.Operation.CanonicalBodyHash = reservation.CanonicalBodyHash
	}
	if reservation.Operation.State == "" {
		reservation.Operation.State = "pending"
	}
	if reservation.Operation.CreatedAt == "" {
		reservation.Operation.CreatedAt = formatTimestamp(s.clock.Now())
	}
	state, stage := resetLifecycleInitialState(reservation)
	if !managedCanonicalUUIDRE.MatchString(reservation.RequestID) || reservation.Operation.OperationID != reservation.RequestID || !isLifecycleText(reservation.AgentID) || reservation.Method == "" || reservation.CanonicalPath == "" || !managedSHA256RE.MatchString(reservation.CanonicalBodyHash) || !validManagedRuntimeFence(reservation.SourceFence) || reservation.SourceFence.AgentID != reservation.AgentID || !managedCanonicalUUIDRE.MatchString(reservation.PlannedTargetRuntimeGeneration) || !lifecycleStateStageValid(state, stage) {
		return ManagedResetAdmission{}, errors.New("managed reset reservation is invalid")
	}
	if reservation.LiveFence.AgentID != "" && (!validManagedRuntimeFence(reservation.LiveFence) || reservation.LiveFence.AgentID != reservation.AgentID) {
		return ManagedResetAdmission{}, errors.New("managed reset live fence is invalid")
	}
	var admission ManagedResetAdmission
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, reservation.Method, reservation.RequestID)
		if err == nil {
			if existing.CanonicalPath != reservation.CanonicalPath || existing.CanonicalBodyHash != reservation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			admission.Operation, admission.Replayed = existing, true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, reservation.AgentID), nil)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotFoundError{AgentID: reservation.AgentID}
		}
		if err != nil {
			return err
		}
		live := restartFenceFromAgent(agent)
		active, activeErr := resetActiveLifecycleActionTx(ctx, s.db, reservation.AgentID)
		if activeErr != nil && !errors.Is(activeErr, sql.ErrNoRows) {
			return activeErr
		}
		if activeErr == nil {
			if active.Kind != "restart" {
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
			if active.Stage == "admission" && active.State == "admitted" {
				if err := s.terminalizeLifecycleActionTx(ctx, active, "cancelled", "CANCELLED_BY_HIGHER_ACTION", "lifecycle action cancelled before external work"); err != nil {
					return err
				}
			} else {
				if err := s.terminalizeLifecycleActionTx(ctx, active, "superseded", "SUPERSEDED_BY_HIGHER_ACTION", "lifecycle action superseded by a higher priority action"); err != nil {
					return err
				}
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE operation_id=?`, active.OperationID); err != nil {
				return err
			}
			// A target already installed by the lower restart is the only possible
			// live source. Its exact durable fence replaces the old source lineage.
			if active.TargetFence != nil && sameManagedRestartFence(live, *active.TargetFence) {
				reservation.SourceFence = *active.TargetFence
			} else if active.SourceFence != nil && sameManagedRestartFence(live, *active.SourceFence) {
				reservation.SourceFence = *active.SourceFence
			} else {
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
		} else if reservation.RetryOf != "" {
			if !sameManagedRestartFence(live, reservation.LiveFence) || !agent.SlotHeld || agent.DesiredState != "running" || (reservation.TargetFence != nil && !sameManagedRestartFence(live, *reservation.TargetFence)) {
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
		} else {
			if !sameManagedRestartFence(live, reservation.LiveFence) || !sameManagedRestartFence(live, reservation.SourceFence) || agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld || (agent.ActivityState != "active" && agent.ActivityState != "idle") {
				return &ManagedAgentNotReadyError{AgentID: reservation.AgentID}
			}
		}
		if reservation.RetryOf != "" {
			predecessor, predecessorErr := managedLifecycleActionForTx(ctx, s.db, reservation.RetryOf)
			if predecessorErr != nil || predecessor.Kind != "reset" || predecessor.AgentID != reservation.AgentID || predecessor.State != "failed" || !predecessor.Retryable || predecessor.SourceFence == nil || !sameManagedRestartFence(*predecessor.SourceFence, reservation.SourceFence) {
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
			var successors int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE retry_of=?`, reservation.RetryOf).Scan(&successors); err != nil || successors != 0 {
				if err != nil {
					return err
				}
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
			switch {
			case state == "starting_target" && stage == "target_start":
				// Target-start retries resume the same verified session after the
				// manager has reaped/probed it and cleared the predecessor fence.
				// The predecessor therefore has no target fence, but keeps the
				// artifact and planned generation as immutable retry lineage.
				if predecessor.Stage != "target_start" || predecessor.TargetFence != nil || predecessor.TargetArtifactID == "" || reservation.TargetFence != nil || reservation.TargetArtifactID != predecessor.TargetArtifactID || reservation.PlannedTargetRuntimeGeneration != predecessor.PlannedTargetRuntimeGeneration {
					return &ManagedResetConflictError{AgentID: reservation.AgentID}
				}
				var candidateArtifactID string
				if err := s.db.QueryRowContext(ctx, `SELECT artifact_id FROM managed_session_artifacts WHERE artifact_id=? AND agent_id=? AND discovery_state='materialized' AND cleanup_state='retained'`, reservation.TargetArtifactID, reservation.AgentID).Scan(&candidateArtifactID); err != nil || candidateArtifactID != reservation.TargetArtifactID {
					if err != nil {
						return err
					}
					return &ManagedResetConflictError{AgentID: reservation.AgentID}
				}
			case state == "old_artifact_cleanup" && stage == "old_artifact_cleanup":
				if predecessor.Stage != "old_artifact_cleanup" || predecessor.TargetFence == nil || reservation.TargetFence == nil || predecessor.TargetArtifactID == "" || reservation.TargetArtifactID != predecessor.TargetArtifactID || !sameManagedRestartFence(*predecessor.TargetFence, *reservation.TargetFence) || reservation.PlannedTargetRuntimeGeneration != predecessor.PlannedTargetRuntimeGeneration {
					return &ManagedResetConflictError{AgentID: reservation.AgentID}
				}
			default:
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
		}
		if reservation.TargetArtifactID != "" {
			var artifactAgent string
			if err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_session_artifacts WHERE artifact_id=?`, reservation.TargetArtifactID).Scan(&artifactAgent); err != nil || artifactAgent != reservation.AgentID {
				if err != nil {
					return err
				}
				return &ManagedResetConflictError{AgentID: reservation.AgentID}
			}
		}
		if err := insertManagedOperation(ctx, s.db, reservation.Operation); err != nil {
			return err
		}
		input := ManagedLifecycleActionInput{ActionID: reservation.Operation.OperationID, AgentID: reservation.AgentID, Kind: "reset", Source: "direct", RetryOf: reservation.RetryOf, ExecutionOwner: "lifecycle", State: state, Stage: stage, SourceFence: &reservation.SourceFence, TargetFence: reservation.TargetFence, PlannedTargetRuntimeGeneration: reservation.PlannedTargetRuntimeGeneration, TargetArtifactID: reservation.TargetArtifactID, CreatedAt: reservation.Operation.CreatedAt, UpdatedAt: reservation.Operation.CreatedAt, Operation: reservation.Operation}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,planned_target_runtime_generation,target_artifact_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, resetActionInsertArgs(input)...); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if err := s.cancelManagedControlRequestsTx(ctx, reservation.AgentID, "CANCELLED_BY_RESET", now); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='resetting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere(""), append([]any{now}, managedFenceValues(live)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedResetConflictError{AgentID: reservation.AgentID}
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		action, err := managedLifecycleActionForTx(ctx, s.db, reservation.Operation.OperationID)
		if err != nil {
			return err
		}
		admission.Operation, admission.Action, admission.Agent = &reservation.Operation, action, agent
		return nil
	})
	return admission, err
}

func resetActionFence(action *ManagedLifecycleAction) (*ManagedRuntimeFence, string) {
	if action != nil && lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
		return action.TargetFence, "target_"
	}
	if action == nil {
		return nil, "source_"
	}
	return action.SourceFence, "source_"
}

// LatestRetryableManagedReset returns the newest failed reset that has not
// already been superseded by a retry. It is an advisory read; reservation
// repeats every lineage and live-fence check atomically.
func (s *Store) LatestRetryableManagedReset(ctx context.Context, agentID string) (*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedLifecycleAction(s.db.QueryRowContext(ctx, managedLifecycleActionSelect+` WHERE kind='reset' AND execution_owner='lifecycle' AND agent_id=? AND state='failed' AND retryable=1 AND NOT EXISTS (SELECT 1 FROM managed_lifecycle_actions successor WHERE successor.retry_of=managed_lifecycle_actions.action_id) ORDER BY completed_at DESC,action_id DESC LIMIT 1`, agentID))
}

func (s *Store) ManagedResetActions(ctx context.Context) ([]*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedLifecycleActionSelect+` WHERE kind='reset' AND execution_owner='lifecycle' AND state NOT IN ('succeeded','failed','cancelled','superseded') ORDER BY created_at,action_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []*ManagedLifecycleAction
	for rows.Next() {
		action, err := scanManagedLifecycleAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func (s *Store) resetUpdateActionTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction, nextState, nextStage string, fence ManagedRuntimeFence) error {
	if action == nil || action.Kind != "reset" || action.ExecutionOwner != "lifecycle" || !lifecycleStateStageValid(nextState, nextStage) {
		return errors.New("managed reset stage is invalid")
	}
	expected, prefix := resetActionFence(action)
	if expected == nil || !sameManagedRestartFence(*expected, fence) {
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	args := []any{nextState, nextStage, formatTimestamp(s.clock.Now()), action.ActionID, action.State, action.Stage}
	args = append(args, managedFenceValues(fence)[1:]...)
	result, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND kind='reset' AND execution_owner='lifecycle' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	return nil
}

func (s *Store) AdvanceManagedReset(ctx context.Context, actionID, fromState, fromStage, toState, toStage string, fence ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != fromState || action.Stage != fromStage {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		return s.resetUpdateActionTx(ctx, s.db, action, toState, toStage, fence)
	})
}

// MarkManagedResetSourceStopped records absence before a reset is allowed to
// create a new Pi session. The old fence remains in the row as immutable
// lineage, but the public projection stops advertising it as active.
func (s *Store) MarkManagedResetSourceStopped(ctx context.Context, actionID string, source ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "stopping_source" || action.Stage != "source_stop" {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := s.resetUpdateActionTx(ctx, s.db, action, "starting_target", "target_start", source); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='stopped',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='resetting' AND slot_held=1`, append([]any{formatTimestamp(s.clock.Now())}, managedFenceValues(source)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// BindManagedResetTarget atomically installs the only target process fence and
// rolls readiness sources. There is deliberately no caller-supplied path here;
// targetArtifactID must already be a controlled artifact ledger row.
func (s *Store) BindManagedResetTarget(ctx context.Context, actionID string, source, target ManagedRuntimeFence, artifactID string) error {
	if !validManagedRuntimeFence(source) || !validManagedRuntimeFence(target) || source.AgentID != target.AgentID || !managedCanonicalUUIDRE.MatchString(artifactID) {
		return errors.New("managed reset target is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "reset" || action.State != "starting_target" || action.Stage != "target_start" || action.TargetFence != nil || action.PlannedTargetRuntimeGeneration != target.RuntimeGeneration || action.SourceFence == nil || !sameManagedRestartFence(*action.SourceFence, source) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		var artifactAgent string
		if err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_session_artifacts WHERE artifact_id=?`, artifactID).Scan(&artifactAgent); err != nil || artifactAgent != source.AgentID {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, target.ManifestRevision, target.ManifestHash, artifactID, now, actionID}
		args = append(args, managedFenceValues(source)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_pi_session_id=?,target_session_epoch=?,target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,target_runtime_generation=?,target_manifest_revision=?,target_manifest_hash=?,target_artifact_id=?,state='awaiting_target_ready',stage='target_ready',updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND kind='reset' AND execution_owner='lifecycle' AND state='starting_target' AND stage='target_start' AND `+managedLifecycleFenceWhere("source_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,manifest_revision=?,manifest_hash=?,lifecycle_state='resetting',activity_state='starting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='resetting' AND activity_state='stopped' AND slot_held=1`, append([]any{target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, target.ManifestRevision, target.ManifestHash, now}, managedFenceValues(source)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, target); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) MarkManagedResetTargetReady(ctx context.Context, actionID string, target ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.State != "awaiting_target_ready" || action.Stage != "target_ready" || action.TargetFence == nil || !sameManagedRestartFence(*action.TargetFence, target) {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		if err := s.managedReadinessSourcesConfirmedForFenceTx(ctx, target); err != nil {
			return err
		}
		if err := s.resetUpdateActionTx(ctx, s.db, action, "old_artifact_cleanup", "old_artifact_cleanup", target); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='idle',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='resetting' AND activity_state='starting' AND slot_held=1`, append([]any{formatTimestamp(s.clock.Now())}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func resetFailureAllowed(code string, stage string, retryable bool) bool {
	switch code {
	case "RESET_RECOVERY_NEEDS_SETTLE":
		return stage == "source_settle" && !retryable
	case "RESET_TARGET_START_FAILED":
		return (stage == "target_start" || stage == "target_ready") && retryable
	case "RESET_TARGET_IDENTITY_UNSAFE":
		return stage == "target_start" && retryable
	case "RESET_OLD_ARTIFACT_CLEANUP_FAILED":
		return stage == "old_artifact_cleanup" && retryable
	case "RESET_PROCESS_IDENTITY_UNSAFE", "RESET_TARGET_CLEANUP_FAILED":
		return !retryable
	}
	return false
}

func resetFailureMessage(code string) string {
	return map[string]string{
		"RESET_RECOVERY_NEEDS_SETTLE":       "reset recovery requires a live settlement",
		"RESET_TARGET_START_FAILED":         "reset replacement did not become ready",
		"RESET_TARGET_IDENTITY_UNSAFE":      "reset replacement identity could not be verified",
		"RESET_OLD_ARTIFACT_CLEANUP_FAILED": "reset session cleanup did not complete",
		"RESET_PROCESS_IDENTITY_UNSAFE":     "reset process identity could not be verified",
		"RESET_TARGET_CLEANUP_FAILED":       "reset replacement cleanup did not complete",
	}[code]
}

// FailManagedReset leaves the exact failed stage durable. Target cleanup and
// old-artifact cleanup can then be retried without inventing a second session.
func (s *Store) FailManagedReset(ctx context.Context, actionID, code string, retryable bool, fence ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "reset" || lifecycleTerminalState(action.State) || !resetFailureAllowed(code, action.Stage, retryable) {
			return errors.New("managed reset failure is invalid")
		}
		expected, prefix := resetActionFence(action)
		if expected == nil || !sameManagedRestartFence(*expected, fence) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		// A target fence is cleared only by ClearResetTargetAfterVerifiedAbsence,
		// after its exact group was proven absent or safely reaped. Failure
		// terminalization itself must never rewrite a live fence.
		agentFence := fence
		args := []any{code, resetFailureMessage(code), boolInt(retryable), now, now, actionID, action.State, action.Stage}
		args = append(args, managedFenceValues(fence)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='failed',terminal_code=?,terminal_message=?,retryable=?,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND kind='reset' AND execution_owner='lifecycle' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state=CASE WHEN ?='RESET_OLD_ARTIFACT_CLEANUP_FAILED' THEN 'idle' ELSE 'failed' END,accepting_prompts=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND slot_held=1`, append([]any{code, code, resetFailureMessage(code), now, now}, managedFenceValues(agentFence)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, resetFailureMessage(code), action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) CompleteManagedReset(ctx context.Context, actionID string, target ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "reset" || action.State != "old_artifact_cleanup" || action.Stage != "old_artifact_cleanup" || action.TargetFence == nil || !sameManagedRestartFence(*action.TargetFence, target) {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, now, actionID}
		args = append(args, managedFenceValues(target)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='succeeded',stage='complete',terminal_code='RESET',terminal_message='managed agent reset',retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND kind='reset' AND execution_owner='lifecycle' AND state='old_artifact_cleanup' AND stage='old_artifact_cleanup' AND `+managedLifecycleFenceWhere("target_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND slot_held=1`, append([]any{now}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code='RESET',terminal_message='managed agent reset' WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: target.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ClearResetTargetAfterVerifiedAbsence is the retry/recovery fence. Its caller
// has already performed the exact group probe (and, for an owned group, reaped
// it). The transaction keeps source lineage and the retained materialized
// target artifact while clearing every target process field as one fence.
func (s *Store) ClearResetTargetAfterVerifiedAbsence(ctx context.Context, actionID string, source, target ManagedRuntimeFence) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "reset" || action.TargetFence == nil || action.SourceFence == nil || action.TargetArtifactID == "" || !sameManagedRestartFence(*action.SourceFence, source) || !sameManagedRestartFence(*action.TargetFence, target) {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		var artifactID string
		if err := s.db.QueryRowContext(ctx, `SELECT artifact_id FROM managed_session_artifacts WHERE artifact_id=? AND agent_id=? AND session_epoch=? AND pi_session_id=? AND discovery_state='materialized' AND cleanup_state='retained'`, action.TargetArtifactID, source.AgentID, target.SessionEpoch, target.PiSessionID).Scan(&artifactID); err != nil || artifactID != action.TargetArtifactID {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		state, stage := "starting_target", "target_start"
		if action.State == "failed" {
			if !action.Retryable || !resetFailureAllowed(action.TerminalCode, "target_start", true) {
				return &ManagedRestartStaleError{AgentID: source.AgentID}
			}
			state = "failed"
		} else if action.State != "awaiting_target_ready" && action.State != "old_artifact_cleanup" {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{state, stage, now, actionID, action.TargetArtifactID}
		args = append(args, managedFenceValues(target)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_pi_session_id=NULL,target_session_epoch=NULL,target_process_pid=NULL,target_process_start_marker=NULL,target_process_incarnation=NULL,target_runtime_generation=NULL,target_manifest_revision=NULL,target_manifest_hash=NULL,state=?,stage=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND kind='reset' AND target_artifact_id=? AND `+managedLifecycleFenceWhere("target_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='resetting',activity_state='stopped',accepting_prompts=0,pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,manifest_revision=?,manifest_hash=?,updated_at=? WHERE `+managedFenceWhere("")+` AND slot_held=1`, append([]any{source.PiSessionID, source.SessionEpoch, source.ProcessPID, source.ProcessStartMarker, source.ProcessIncarnation, source.RuntimeGeneration, source.ManifestRevision, source.ManifestHash, now}, managedFenceValues(target)...)...)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, source); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) ManagedResetRetryCandidate(ctx context.Context, agentID string) (*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedLifecycleAction(s.db.QueryRowContext(ctx, managedLifecycleActionSelect+` WHERE agent_id=? AND kind='reset' AND state='failed' AND retryable=1 AND NOT EXISTS (SELECT 1 FROM managed_lifecycle_actions child WHERE child.retry_of=managed_lifecycle_actions.action_id) ORDER BY completed_at DESC,action_id DESC LIMIT 1`, agentID))
}

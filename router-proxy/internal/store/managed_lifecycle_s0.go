package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// ManagedSessionArtifact is the durable, root-relative artifact identity. The
// process/artifact package owns discovery and unlinking; this store contract
// only accepts the result after that boundary has validated it.
type ManagedSessionArtifact struct {
	ArtifactID, AgentID, PiSessionID, RootRelativePath, PathSHA256 string
	SessionEpoch                                                   int64
	DiscoveryState, FileDevice, FileInode, CleanupState            string
	DiscoveredAt, CleanupUpdatedAt                                 string
}

// ManagedLifecycleActionInput is the storage-only admission shape. It has no
// process handle or external side effect, so S0 can validate durable ownership
// before executors are wired in later slices.
type ManagedLifecycleActionInput struct {
	ActionID, AgentID, Kind, Source, ParentActionID, RetryOf, ExecutionOwner string
	State, Stage                                                             string
	SourceFence, TargetFence                                                 *ManagedRuntimeFence
	PlannedTargetRuntimeGeneration, TargetArtifactID                         string
	TerminalCode, TerminalMessage                                            string
	Retryable                                                                bool
	CreatedAt, UpdatedAt                                                     string
	Operation                                                                ManagedOperation
}

// ManagedLifecycleTransition is an exact fenced state change. A caller must
// provide the action identity, current state/stage, and the durable source or
// target fence that owns the stage.
type ManagedLifecycleTransition struct {
	ActionID, FromState, FromStage, ToState, ToStage string
	Fence                                            ManagedRuntimeFence
}

// ManagedLifecycleTerminalization closes one lifecycle-owned action only when
// the caller still holds the action's recorded state, stage, and fence.
type ManagedLifecycleTerminalization struct {
	ActionID, ExpectedState, ExpectedStage, TerminalState, Code, Message string
	Fence                                                                *ManagedRuntimeFence
	Retryable                                                            bool
}

// ManagedLifecycleRecoveryClaim is a bounded startup ownership lease. It is
// durable so a second recovery loop cannot repeat an external action.
type ManagedLifecycleRecoveryClaim struct {
	ActionID, Owner, State, Stage, ClaimedAt string
	Fence                                    ManagedRuntimeFence
}

// ManagedLifecycleBatchItem is one immutable Restart All snapshot item. S0
// validates its graph links but does not enqueue a child executor.
type ManagedLifecycleBatchItem struct {
	SnapshotPosition                                 int
	AgentID, Status, ChildActionID, ChildOperationID string
	ErrorCode, ErrorMessage, CreatedAt               string
}

// ManagedControlRequestInput is the root-control ledger reservation shape.
// Execution, append, and exact-entry reconciliation remain later-slice work.
type ManagedControlRequestInput struct {
	RequestID, ResultID, AgentID, ToolCallID, CapabilityID string
	ToolName, DefinitionID, OperationID                    string
	CanonicalArgumentsSHA256, RequestFingerprint           string
	Fence                                                  ManagedRuntimeFence
	ReceivedAt                                             string
}

type ManagedControlReservation struct {
	RequestID string
	Replayed  bool
}

type ManagedControlConflictError struct{}

func (*ManagedControlConflictError) Error() string {
	return "managed control request conflicts with an existing reservation"
}
func (*ManagedControlConflictError) Code() string { return "conflict" }

func isCanonicalLifecycleTimestamp(value string) bool {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	return err == nil && formatTimestamp(parsed) == value
}

func isLifecycleText(value string) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= 128
}

func lifecycleTerminalState(state string) bool {
	switch state {
	case "succeeded", "failed", "cancelled", "superseded":
		return true
	default:
		return false
	}
}

func lifecycleStateStageValid(state, stage string) bool {
	allowed := map[string]map[string]bool{
		"admitted":              {"admission": true},
		"awaiting_settle":       {"source_settle": true},
		"stopping_source":       {"source_stop": true},
		"starting_target":       {"target_start": true},
		"awaiting_target_ready": {"target_ready": true},
		"old_artifact_cleanup":  {"old_artifact_cleanup": true},
		"terminating":           {"abort": true, "terminate_reap": true},
		"cleanup":               {"direct_mail_retire": true, "agent_cleanup": true, "durable_write": true},
	}
	if lifecycleTerminalState(state) {
		// A successful, cancelled, or superseded action has no failed work to
		// retain, so it must close at complete. Failed actions keep the exact
		// stage that failed so a retry has durable recovery context.
		if state == "failed" {
			return stage == "complete" || lifecycleStateStageValidNonterminal(stage)
		}
		return stage == "complete"
	}
	return allowed[state][stage]
}

func lifecycleStateStageValidNonterminal(stage string) bool {
	for _, candidate := range []string{"admission", "source_settle", "source_stop", "target_start", "target_ready", "old_artifact_cleanup", "abort", "terminate_reap", "direct_mail_retire", "agent_cleanup", "durable_write"} {
		if stage == candidate {
			return true
		}
	}
	return false
}

func lifecycleTargetStage(stage string) bool {
	return stage == "target_ready" || stage == "old_artifact_cleanup"
}

func lifecycleFenceValues(fence *ManagedRuntimeFence) []any {
	if fence == nil {
		return []any{nil, nil, nil, nil, nil, nil, nil, nil}
	}
	return []any{fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash}
}

func lifecycleFenceMatches(actionFence *ManagedRuntimeFence, input ManagedRuntimeFence) bool {
	return actionFence != nil && validManagedRuntimeFence(input) && sameManagedRestartFence(*actionFence, input)
}

func validateLifecycleActionInput(input ManagedLifecycleActionInput) error {
	if !managedCanonicalUUIDRE.MatchString(input.ActionID) || input.Operation.OperationID != input.ActionID || input.Operation.RequestID != input.ActionID || !managedCanonicalUUIDRE.MatchString(input.Operation.RequestID) {
		return errors.New("managed lifecycle action identity is invalid")
	}
	if input.ExecutionOwner == "" {
		input.ExecutionOwner = "lifecycle"
	}
	if input.ExecutionOwner != "lifecycle" {
		return errors.New("new managed lifecycle action must use lifecycle ownership")
	}
	if !isCanonicalLifecycleTimestamp(input.CreatedAt) || !isCanonicalLifecycleTimestamp(input.UpdatedAt) || !lifecycleStateStageValid(input.State, input.Stage) {
		return errors.New("managed lifecycle action state is invalid")
	}
	if input.Kind != "restart" && input.Kind != "reset" && input.Kind != "terminate" && input.Kind != "restart_all" {
		return errors.New("managed lifecycle action kind is invalid")
	}
	if input.Source != "direct" && input.Source != "restart_all_child" {
		return errors.New("managed lifecycle action source is invalid")
	}
	if input.Kind == "restart_all" {
		if input.AgentID != "" || input.Source != "direct" || input.ParentActionID != "" || input.SourceFence != nil || input.TargetFence != nil || input.PlannedTargetRuntimeGeneration != "" || input.RetryOf != "" {
			return errors.New("managed restart-all action shape is invalid")
		}
	} else {
		if !isLifecycleText(input.AgentID) {
			return errors.New("managed lifecycle action agent is invalid")
		}
		if input.Source == "restart_all_child" {
			if input.Kind != "restart" || input.ParentActionID == "" || !managedCanonicalUUIDRE.MatchString(input.ParentActionID) {
				return errors.New("managed restart-all child action shape is invalid")
			}
		} else if input.ParentActionID != "" {
			return errors.New("direct managed lifecycle action cannot have a parent")
		}
		if input.Kind == "restart" || input.Kind == "reset" {
			if input.SourceFence == nil || !validManagedRuntimeFence(*input.SourceFence) || input.SourceFence.AgentID != input.AgentID || !managedCanonicalUUIDRE.MatchString(input.PlannedTargetRuntimeGeneration) {
				return errors.New("managed lifecycle source fence is invalid")
			}
		}
		if input.Kind == "terminate" && input.SourceFence != nil && (!validManagedRuntimeFence(*input.SourceFence) || input.SourceFence.AgentID != input.AgentID) {
			return errors.New("managed termination source fence is invalid")
		}
	}
	if input.TargetFence != nil {
		if input.Kind != "restart" && input.Kind != "reset" || !validManagedRuntimeFence(*input.TargetFence) || input.TargetFence.AgentID != input.AgentID || input.TargetFence.RuntimeGeneration != input.PlannedTargetRuntimeGeneration {
			return errors.New("managed lifecycle target fence is invalid")
		}
	}
	if input.TargetArtifactID != "" && !managedCanonicalUUIDRE.MatchString(input.TargetArtifactID) {
		return errors.New("managed lifecycle target artifact identity is invalid")
	}
	if input.RetryOf != "" && (!managedCanonicalUUIDRE.MatchString(input.RetryOf) || (input.Kind != "reset" && input.Kind != "terminate")) {
		return errors.New("managed lifecycle retry shape is invalid")
	}
	if lifecycleTerminalState(input.State) {
		if !isLifecycleText(input.TerminalCode) || input.TerminalMessage == "" || len([]byte(input.TerminalMessage)) > 1024 {
			return errors.New("managed lifecycle terminal result is invalid")
		}
	} else if input.TerminalCode != "" || input.TerminalMessage != "" || input.Retryable {
		return errors.New("nonterminal managed lifecycle action cannot have an outcome")
	}
	if input.Operation.Method == "" || input.Operation.CanonicalPath == "" || !managedSHA256RE.MatchString(input.Operation.CanonicalBodyHash) || !isCanonicalLifecycleTimestamp(input.Operation.CreatedAt) {
		return errors.New("managed lifecycle operation is invalid")
	}
	if lifecycleTerminalState(input.State) != (input.Operation.State == "terminal") {
		return errors.New("managed lifecycle action and operation states disagree")
	}
	if input.Operation.State != "pending" && input.Operation.State != "terminal" {
		return errors.New("managed lifecycle operation state is invalid")
	}
	return nil
}

// RecordManagedSessionArtifact accepts only a validated root-relative identity.
func (s *Store) RecordManagedSessionArtifact(ctx context.Context, artifact ManagedSessionArtifact) error {
	if !managedCanonicalUUIDRE.MatchString(artifact.ArtifactID) || !isLifecycleText(artifact.AgentID) || !isLifecycleText(artifact.PiSessionID) || artifact.SessionEpoch < 1 || artifact.RootRelativePath == "" || len([]byte(artifact.RootRelativePath)) > 4096 || strings.HasPrefix(artifact.RootRelativePath, "/") || artifact.RootRelativePath == ".." || strings.HasPrefix(artifact.RootRelativePath, "../") || strings.Contains(artifact.RootRelativePath, "/../") || strings.HasSuffix(artifact.RootRelativePath, "/..") || !managedSHA256RE.MatchString(artifact.PathSHA256) || !isCanonicalLifecycleTimestamp(artifact.DiscoveredAt) || !isCanonicalLifecycleTimestamp(artifact.CleanupUpdatedAt) {
		return errors.New("managed session artifact is invalid")
	}
	if artifact.DiscoveryState != "materialized" && artifact.DiscoveryState != "not_materialized" && artifact.DiscoveryState != "ambiguous" && artifact.DiscoveryState != "missing" {
		return errors.New("managed session artifact discovery state is invalid")
	}
	if artifact.CleanupState != "retained" && artifact.CleanupState != "pending" && artifact.CleanupState != "unlinked" {
		return errors.New("managed session artifact cleanup state is invalid")
	}
	materialized := artifact.DiscoveryState == "materialized"
	if materialized != (artifact.FileDevice != "" && artifact.FileInode != "") || (!materialized && (artifact.FileDevice != "" || artifact.FileInode != "")) || (artifact.DiscoveryState == "ambiguous" && artifact.CleanupState != "retained") {
		return errors.New("managed session artifact ownership shape is invalid")
	}
	return s.runImmediate(ctx, func() error {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, artifact.AgentID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return &ManagedAgentNotFoundError{AgentID: artifact.AgentID}
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO managed_session_artifacts (artifact_id,agent_id,session_epoch,pi_session_id,root_relative_path,path_sha256,discovery_state,file_device,file_inode,cleanup_state,discovered_at,cleanup_updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, artifact.ArtifactID, artifact.AgentID, artifact.SessionEpoch, artifact.PiSessionID, artifact.RootRelativePath, artifact.PathSHA256, artifact.DiscoveryState, nullIfEmpty(artifact.FileDevice), nullIfEmpty(artifact.FileInode), artifact.CleanupState, artifact.DiscoveredAt, artifact.CleanupUpdatedAt)
		return err
	})
}

// CreateManagedLifecycleAction writes an operation and action together. It is
// intentionally storage-only: S0 establishes the durable graph before any
// executor is allowed to run.
func (s *Store) CreateManagedLifecycleAction(ctx context.Context, input ManagedLifecycleActionInput) error {
	if err := validateLifecycleActionInput(input); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if input.AgentID != "" {
			var agent int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, input.AgentID).Scan(&agent); err != nil {
				return err
			}
			if agent != 1 {
				return &ManagedAgentNotFoundError{AgentID: input.AgentID}
			}
		}
		if input.ParentActionID != "" {
			var kind string
			if err := s.db.QueryRowContext(ctx, `SELECT kind FROM managed_lifecycle_actions WHERE action_id=?`, input.ParentActionID).Scan(&kind); err != nil || kind != "restart_all" {
				if err != nil {
					return err
				}
				return errors.New("managed lifecycle parent is invalid")
			}
		}
		if input.RetryOf != "" {
			if err := validateManagedLifecycleRetryPredecessorTx(ctx, s.db, input); err != nil {
				return err
			}
		}
		if input.TargetArtifactID != "" {
			var artifactAgent string
			if err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_session_artifacts WHERE artifact_id=?`, input.TargetArtifactID).Scan(&artifactAgent); err != nil || artifactAgent != input.AgentID {
				if err != nil {
					return err
				}
				return errors.New("managed lifecycle artifact is not owned by the action agent")
			}
		}
		if err := insertManagedOperation(ctx, s.db, input.Operation); err != nil {
			return err
		}
		terminal := lifecycleTerminalState(input.State)
		args := []any{input.ActionID, input.Operation.OperationID, nullIfEmpty(input.AgentID), input.Kind, input.Source, nullIfEmpty(input.ParentActionID), nullIfEmpty(input.RetryOf), input.ExecutionOwner, input.State, input.Stage}
		args = append(args, lifecycleFenceValues(input.SourceFence)...)
		args = append(args, lifecycleFenceValues(input.TargetFence)...)
		args = append(args, nullIfEmpty(input.PlannedTargetRuntimeGeneration), nullIfEmpty(input.TargetArtifactID), nullIfEmpty(input.TerminalCode), nullIfEmpty(input.TerminalMessage))
		if terminal {
			args = append(args, boolInt(input.Retryable), input.UpdatedAt)
		} else {
			args = append(args, nil, nil)
		}
		args = append(args, input.CreatedAt, input.UpdatedAt)
		_, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
			action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
			source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
			target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,
			planned_target_runtime_generation,target_artifact_id,terminal_code,terminal_message,retryable,completed_at,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		return err
	})
}

func validateManagedLifecycleRetryPredecessorTx(ctx context.Context, db *sql.DB, input ManagedLifecycleActionInput) error {
	predecessor, err := managedLifecycleActionForTx(ctx, db, input.RetryOf)
	if err != nil {
		return err
	}
	if predecessor.Kind != input.Kind || predecessor.AgentID != input.AgentID || predecessor.State != "failed" || !predecessor.Retryable {
		return &ManagedLifecycleConflictError{AgentID: input.AgentID}
	}
	var successor int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE retry_of=?`, input.RetryOf).Scan(&successor); err != nil {
		return err
	}
	if successor != 0 || predecessor.SourceFence == nil || input.SourceFence == nil || !sameManagedRestartFence(*predecessor.SourceFence, *input.SourceFence) {
		return &ManagedLifecycleConflictError{AgentID: input.AgentID}
	}
	var newest string
	if err := db.QueryRowContext(ctx, `SELECT action_id FROM managed_lifecycle_actions WHERE agent_id=? AND kind=? AND state='failed' AND retryable=1 ORDER BY completed_at DESC,action_id DESC LIMIT 1`, input.AgentID, input.Kind).Scan(&newest); err != nil {
		return err
	}
	if newest != input.RetryOf {
		return &ManagedLifecycleConflictError{AgentID: input.AgentID}
	}
	if input.Kind == "reset" {
		if predecessor.TargetFence == nil || predecessor.TargetArtifactID == "" || input.TargetFence == nil || input.TargetArtifactID == "" || !sameManagedRestartFence(*predecessor.TargetFence, *input.TargetFence) || predecessor.TargetArtifactID != input.TargetArtifactID {
			return &ManagedLifecycleConflictError{AgentID: input.AgentID}
		}
	}
	if input.Kind == "terminate" && !lifecycleStateStageValid("cleanup", predecessor.Stage) && predecessor.Stage != "terminate_reap" && predecessor.Stage != "abort" {
		return &ManagedLifecycleConflictError{AgentID: input.AgentID}
	}
	return nil
}

// ClaimManagedLifecycleRecovery claims one lifecycle-owned, fenced row. Legacy
// 025 rows are deliberately excluded so their existing executor remains sole owner.
func (s *Store) ClaimManagedLifecycleRecovery(ctx context.Context, actionID, owner string) (*ManagedLifecycleRecoveryClaim, error) {
	if !managedCanonicalUUIDRE.MatchString(actionID) || !isLifecycleText(owner) {
		return nil, errors.New("managed lifecycle recovery claim is invalid")
	}
	var claim *ManagedLifecycleRecoveryClaim
	err := s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.ExecutionOwner != "lifecycle" || lifecycleTerminalState(action.State) || action.RecoveryOwner != "" {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		fence := action.SourceFence
		if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
			fence = action.TargetFence
		}
		if fence == nil || !validManagedRuntimeFence(*fence) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		prefix := "source_"
		if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
			prefix = "target_"
		}
		args := []any{owner, now, now, actionID, action.State, action.Stage}
		args = append(args, managedFenceValues(*fence)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET recovery_owner=?,recovery_claimed_at=?,updated_at=? WHERE action_id=? AND execution_owner='lifecycle' AND state=? AND stage=? AND recovery_owner IS NULL AND recovery_claimed_at IS NULL AND `+managedLifecycleFenceWhere(prefix), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		claim = &ManagedLifecycleRecoveryClaim{ActionID: actionID, Owner: owner, State: action.State, Stage: action.Stage, ClaimedAt: now, Fence: *fence}
		return nil
	})
	return claim, err
}

func managedLifecycleFenceWhere(prefix string) string {
	return prefix + `pi_session_id=? AND ` + prefix + `session_epoch=? AND ` + prefix + `process_pid=? AND ` + prefix + `process_start_marker=? AND ` + prefix + `process_incarnation=? AND ` + prefix + `runtime_generation=? AND ` + prefix + `manifest_revision=? AND ` + prefix + `manifest_hash=?`
}

// TransitionManagedLifecycleAction changes a single stage only when the caller
// still owns the exact durable action and fence. It clears a recovery claim in
// that same immediate transaction.
func (s *Store) TransitionManagedLifecycleAction(ctx context.Context, transition ManagedLifecycleTransition) error {
	if !managedCanonicalUUIDRE.MatchString(transition.ActionID) || lifecycleTerminalState(transition.ToState) || !lifecycleStateStageValid(transition.ToState, transition.ToStage) || !validManagedRuntimeFence(transition.Fence) {
		return errors.New("managed lifecycle transition is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, transition.ActionID)
		if err != nil {
			return err
		}
		if action.State != transition.FromState || action.Stage != transition.FromStage || lifecycleTerminalState(action.State) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		expected := action.SourceFence
		prefix := "source_"
		if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
			expected, prefix = action.TargetFence, "target_"
		}
		if !lifecycleFenceMatches(expected, transition.Fence) {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		args := []any{transition.ToState, transition.ToStage, formatTimestamp(s.clock.Now()), transition.ActionID, transition.FromState, transition.FromStage}
		args = append(args, managedFenceValues(transition.Fence)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND execution_owner='lifecycle' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		return nil
	})
}

// BindManagedLifecycleTarget persists an exact target fence before a later
// executor probes readiness. It is the storage handoff between source cleanup
// and target ownership, so a stale callback cannot attach a different process.
func (s *Store) BindManagedLifecycleTarget(ctx context.Context, actionID string, source, target ManagedRuntimeFence, artifactID string) error {
	if !managedCanonicalUUIDRE.MatchString(actionID) || !validManagedRuntimeFence(source) || !validManagedRuntimeFence(target) || source.AgentID != target.AgentID || !managedCanonicalUUIDRE.MatchString(artifactID) {
		return errors.New("managed lifecycle target binding is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "restart" && action.Kind != "reset" || action.State != "starting_target" || action.Stage != "target_start" || !lifecycleFenceMatches(action.SourceFence, source) || action.TargetFence != nil || action.PlannedTargetRuntimeGeneration != target.RuntimeGeneration {
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		var artifactAgent string
		if err := s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_session_artifacts WHERE artifact_id=?`, artifactID).Scan(&artifactAgent); err != nil || artifactAgent != source.AgentID {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		args := []any{target.PiSessionID, target.SessionEpoch, target.ProcessPID, target.ProcessStartMarker, target.ProcessIncarnation, target.RuntimeGeneration, target.ManifestRevision, target.ManifestHash, artifactID, formatTimestamp(s.clock.Now()), actionID, source.AgentID}
		args = append(args, managedFenceValues(source)[1:]...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET target_pi_session_id=?,target_session_epoch=?,target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,target_runtime_generation=?,target_manifest_revision=?,target_manifest_hash=?,target_artifact_id=?,state='awaiting_target_ready',stage='target_ready',updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND agent_id=? AND execution_owner='lifecycle' AND state='starting_target' AND stage='target_start' AND `+managedLifecycleFenceWhere("source_"), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: source.AgentID}
		}
		return nil
	})
}

func managedLifecycleNullFenceWhere(prefix string) string {
	return prefix + `pi_session_id IS NULL AND ` + prefix + `session_epoch IS NULL AND ` + prefix + `process_pid IS NULL AND ` + prefix + `process_start_marker IS NULL AND ` + prefix + `process_incarnation IS NULL AND ` + prefix + `runtime_generation IS NULL AND ` + prefix + `manifest_revision IS NULL AND ` + prefix + `manifest_hash IS NULL`
}

func lifecycleTerminalOutcomeValid(action *ManagedLifecycleAction, terminal ManagedLifecycleTerminalization, stage string) bool {
	if action == nil {
		return false
	}
	if terminal.TerminalState == "cancelled" {
		return !terminal.Retryable && terminal.Code == "CANCELLED_BY_HIGHER_ACTION" && terminal.Message == "lifecycle action cancelled before external work"
	}
	if terminal.TerminalState == "superseded" {
		return !terminal.Retryable && terminal.Code == "SUPERSEDED_BY_HIGHER_ACTION" && terminal.Message == "lifecycle action superseded by a higher priority action"
	}
	if terminal.TerminalState == "succeeded" {
		switch action.Kind {
		case "restart":
			return !terminal.Retryable && terminal.Code == "RESTARTED" && terminal.Message == "managed agent restarted"
		case "terminate":
			return !terminal.Retryable && terminal.Code == "TERMINATED" && terminal.Message == "managed agent terminated"
		case "restart_all":
			return !terminal.Retryable && terminal.Code == "BATCH_ADMITTED" && terminal.Message == "managed restart batch admitted"
		}
		return false
	}
	if terminal.TerminalState != "failed" {
		return false
	}
	switch action.Kind {
	case "reset":
		switch terminal.Code {
		case "RESET_RECOVERY_NEEDS_SETTLE":
			return !terminal.Retryable && stage == "source_settle" && terminal.Message == "reset recovery requires a live settlement"
		case "RESET_TARGET_START_FAILED":
			return terminal.Retryable && (stage == "target_start" || stage == "target_ready") && terminal.Message == "reset replacement did not become ready"
		case "RESET_TARGET_IDENTITY_UNSAFE":
			return terminal.Retryable && stage == "target_start" && terminal.Message == "reset replacement identity could not be verified"
		case "RESET_OLD_ARTIFACT_CLEANUP_FAILED":
			return terminal.Retryable && stage == "old_artifact_cleanup" && terminal.Message == "reset session cleanup did not complete"
		case "RESET_PROCESS_IDENTITY_UNSAFE":
			return !terminal.Retryable && terminal.Message == "reset process identity could not be verified"
		case "RESET_TARGET_CLEANUP_FAILED":
			return !terminal.Retryable && terminal.Message == "reset replacement cleanup did not complete"
		}
	case "terminate":
		return terminal.Code == "TERMINATION_CLEANUP_FAILED" && terminal.Message == "managed agent termination cleanup failed" && (stage == "terminate_reap" || stage == "direct_mail_retire" || stage == "agent_cleanup" || stage == "durable_write")
	}
	return false
}

// CompleteManagedLifecycleAction persists only contract-defined outcomes. It
// never accepts process/RPC text, and it does not infer a current stage from an
// action ID: callers supply the exact state/stage and the owning fence.
func (s *Store) CompleteManagedLifecycleAction(ctx context.Context, terminal ManagedLifecycleTerminalization) error {
	if !managedCanonicalUUIDRE.MatchString(terminal.ActionID) || !lifecycleStateStageValid(terminal.ExpectedState, terminal.ExpectedStage) || lifecycleTerminalState(terminal.ExpectedState) || !lifecycleTerminalState(terminal.TerminalState) {
		return errors.New("managed lifecycle terminalization is invalid")
	}
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, terminal.ActionID)
		if err != nil {
			return err
		}
		if action.ExecutionOwner != "lifecycle" || action.State != terminal.ExpectedState || action.Stage != terminal.ExpectedStage {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		stage := "complete"
		if terminal.TerminalState == "failed" {
			stage = terminal.ExpectedStage
		}
		if !lifecycleStateStageValid(terminal.TerminalState, stage) || !lifecycleTerminalOutcomeValid(action, terminal, stage) {
			return errors.New("managed lifecycle terminal result is invalid")
		}
		prefix := "source_"
		expected := action.SourceFence
		if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
			prefix, expected = "target_", action.TargetFence
		}
		where := `action_id=? AND operation_id=? AND execution_owner='lifecycle' AND state=? AND stage=? AND `
		args := []any{terminal.TerminalState, stage, terminal.Code, terminal.Message, boolInt(terminal.Retryable), formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()), action.ActionID, action.OperationID, terminal.ExpectedState, terminal.ExpectedStage}
		if expected == nil {
			if terminal.Fence != nil {
				return &ManagedRestartStaleError{AgentID: action.AgentID}
			}
			where += managedLifecycleNullFenceWhere(prefix)
		} else {
			if terminal.Fence == nil || !lifecycleFenceMatches(expected, *terminal.Fence) {
				return &ManagedRestartStaleError{AgentID: action.AgentID}
			}
			where += managedLifecycleFenceWhere(prefix)
			args = append(args, managedFenceValues(*terminal.Fence)[1:]...)
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,terminal_code=?,terminal_message=?,retryable=?,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE `+where, args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now().Add(24*time.Hour)), terminal.Code, terminal.Message, action.OperationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		return nil
	})
}

// terminalizeLifecycleActionTx is used by higher-priority reservation paths.
// It rechecks the full persisted fence in SQL before closing either owner, so a
// stale active-lane read cannot terminalize a replacement action or operation.
func (s *Store) terminalizeLifecycleActionTx(ctx context.Context, action *ManagedLifecycleAction, terminalState, code, message string) error {
	if action == nil || !lifecycleTerminalState(terminalState) || terminalState == "failed" || !lifecycleStateStageValid(action.State, action.Stage) || lifecycleTerminalState(action.State) {
		return errors.New("managed lifecycle terminalization is invalid")
	}
	prefix := "source_"
	expected := action.SourceFence
	if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
		prefix, expected = "target_", action.TargetFence
	}
	where := `action_id=? AND operation_id=? AND execution_owner=? AND state=? AND stage=? AND `
	args := []any{terminalState, code, message, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()), action.ActionID, action.OperationID, action.ExecutionOwner, action.State, action.Stage}
	if expected == nil {
		where += managedLifecycleNullFenceWhere(prefix)
	} else {
		where += managedLifecycleFenceWhere(prefix)
		args = append(args, managedFenceValues(*expected)[1:]...)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage='complete',terminal_code=?,terminal_message=?,retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE `+where, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, message, action.OperationID)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return err
		}
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	return nil
}

// ClearManagedLifecycleRecoveryClaims releases only lifecycle-owned recovery
// leases. Startup calls this before claiming rows one at a time; 025 bridge
// rows remain excluded by their execution owner.
func (s *Store) ClearManagedLifecycleRecoveryClaims(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET recovery_owner=NULL,recovery_claimed_at=NULL,updated_at=? WHERE execution_owner='lifecycle' AND state NOT IN ('succeeded','failed','cancelled','superseded') AND recovery_owner IS NOT NULL`, formatTimestamp(s.clock.Now()))
		return err
	})
}

// WriteManagedLifecycleBatchItems validates the durable parent/child graph.
// Admission/execution scheduling is intentionally owned by a later slice.
func (s *Store) WriteManagedLifecycleBatchItems(ctx context.Context, parentActionID string, items []ManagedLifecycleBatchItem) error {
	if !managedCanonicalUUIDRE.MatchString(parentActionID) || len(items) > 32 {
		return errors.New("managed lifecycle batch is invalid")
	}
	return s.runImmediate(ctx, func() error {
		parent, err := managedLifecycleActionForTx(ctx, s.db, parentActionID)
		if err != nil {
			return err
		}
		if parent.Kind != "restart_all" || parent.AgentID != "" || parent.SourceFence != nil {
			return errors.New("managed lifecycle batch parent is invalid")
		}
		seenPosition, seenAgent := make(map[int]bool), make(map[string]bool)
		for _, item := range items {
			if item.SnapshotPosition < 0 || item.SnapshotPosition > 31 || seenPosition[item.SnapshotPosition] || !isLifecycleText(item.AgentID) || seenAgent[item.AgentID] || !isCanonicalLifecycleTimestamp(item.CreatedAt) {
				return errors.New("managed lifecycle batch item is invalid")
			}
			seenPosition[item.SnapshotPosition], seenAgent[item.AgentID] = true, true
			switch item.Status {
			case "accepted", "coalesced":
				if !managedCanonicalUUIDRE.MatchString(item.ChildActionID) || item.ChildOperationID != item.ChildActionID || !managedCanonicalUUIDRE.MatchString(item.ChildOperationID) || item.ErrorCode != "" || item.ErrorMessage != "" {
					return errors.New("managed lifecycle batch child is invalid")
				}
				child, childErr := managedLifecycleActionForTx(ctx, s.db, item.ChildActionID)
				if childErr != nil || child.AgentID != item.AgentID || child.Kind != "restart" || child.OperationID != item.ChildOperationID {
					if childErr != nil {
						return childErr
					}
					return errors.New("managed lifecycle batch child does not match item")
				}
				if item.Status == "accepted" && (child.Source != "restart_all_child" || child.ParentActionID != parentActionID) {
					return errors.New("accepted managed lifecycle batch child is not parent-owned")
				}
			case "rejected":
				if item.ChildActionID != "" || item.ChildOperationID != "" || !isLifecycleText(item.ErrorCode) || item.ErrorMessage == "" || len([]byte(item.ErrorMessage)) > 1024 {
					return errors.New("rejected managed lifecycle batch item is invalid")
				}
			default:
				return errors.New("managed lifecycle batch status is invalid")
			}
			_, err = s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,error_code,error_message,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, parentActionID, item.SnapshotPosition, item.AgentID, item.Status, nullIfEmpty(item.ChildActionID), nullIfEmpty(item.ChildOperationID), nullIfEmpty(item.ErrorCode), nullIfEmpty(item.ErrorMessage), item.CreatedAt)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func validateManagedControlInput(input ManagedControlRequestInput) error {
	if !managedCanonicalUUIDRE.MatchString(input.RequestID) || !managedCanonicalUUIDRE.MatchString(input.ResultID) || !isLifecycleText(input.AgentID) || !isLifecycleText(input.ToolCallID) || !isLifecycleText(input.CapabilityID) || !isLifecycleText(input.ToolName) || !isLifecycleText(input.DefinitionID) || !isLifecycleText(input.OperationID) || !managedSHA256RE.MatchString(input.CanonicalArgumentsSHA256) || !managedSHA256RE.MatchString(input.RequestFingerprint) || !validManagedRuntimeFence(input.Fence) || input.Fence.AgentID != input.AgentID || !isCanonicalLifecycleTimestamp(input.ReceivedAt) {
		return errors.New("managed control request is invalid")
	}
	return nil
}

// ReserveManagedControlRequest is idempotent only for an identical request
// tuple and runtime fence. It bounds nonterminal work before a handler exists.
func (s *Store) ReserveManagedControlRequest(ctx context.Context, input ManagedControlRequestInput) (ManagedControlReservation, error) {
	if err := validateManagedControlInput(input); err != nil {
		return ManagedControlReservation{}, err
	}
	var reservation ManagedControlReservation
	err := s.runImmediate(ctx, func() error {
		var existing ManagedControlRequestInput
		var pi, marker, incarnation, generation, manifest string
		var epoch, pid, revision int64
		err := s.db.QueryRowContext(ctx, `SELECT request_id,result_id,agent_id,tool_call_id,capability_id,tool_name,definition_id,operation_id,canonical_arguments_sha256,request_fingerprint,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,received_at FROM managed_control_requests WHERE request_id=?`, input.RequestID).Scan(&existing.RequestID, &existing.ResultID, &existing.AgentID, &existing.ToolCallID, &existing.CapabilityID, &existing.ToolName, &existing.DefinitionID, &existing.OperationID, &existing.CanonicalArgumentsSHA256, &existing.RequestFingerprint, &pi, &epoch, &pid, &marker, &incarnation, &generation, &revision, &manifest, &existing.ReceivedAt)
		if err == nil {
			existing.Fence = ManagedRuntimeFence{AgentID: existing.AgentID, PiSessionID: pi, SessionEpoch: epoch, ProcessPID: int(pid), ProcessStartMarker: marker, ProcessIncarnation: incarnation, RuntimeGeneration: generation, ManifestRevision: int(revision), ManifestHash: manifest}
			if existing.ResultID != input.ResultID || existing.AgentID != input.AgentID || existing.ToolCallID != input.ToolCallID || existing.CapabilityID != input.CapabilityID || existing.ToolName != input.ToolName || existing.DefinitionID != input.DefinitionID || existing.OperationID != input.OperationID || existing.CanonicalArgumentsSHA256 != input.CanonicalArgumentsSHA256 || existing.RequestFingerprint != input.RequestFingerprint || !sameManagedRestartFence(existing.Fence, input.Fence) {
				return &ManagedControlConflictError{}
			}
			reservation = ManagedControlReservation{RequestID: input.RequestID, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var perAgent, global int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_control_requests WHERE agent_id=? AND state NOT IN ('delivered','failed','cancelled')`, input.AgentID).Scan(&perAgent); err != nil {
			return err
		}
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_control_requests WHERE state NOT IN ('delivered','failed','cancelled')`).Scan(&global); err != nil {
			return err
		}
		if perAgent >= 64 || global >= 512 {
			return &ManagedControlConflictError{}
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_control_requests (request_id,result_id,agent_id,tool_call_id,capability_id,tool_name,definition_id,operation_id,context_kind,canonical_arguments_sha256,request_fingerprint,state,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,received_at) VALUES (?,?,?,?,?,?,?,?, 'root',?,?, 'received',?,?,?,?,?,?,?,?,?)`, input.RequestID, input.ResultID, input.AgentID, input.ToolCallID, input.CapabilityID, input.ToolName, input.DefinitionID, input.OperationID, input.CanonicalArgumentsSHA256, input.RequestFingerprint, input.Fence.PiSessionID, input.Fence.SessionEpoch, input.Fence.ProcessPID, input.Fence.ProcessStartMarker, input.Fence.ProcessIncarnation, input.Fence.RuntimeGeneration, input.Fence.ManifestRevision, input.Fence.ManifestHash, input.ReceivedAt)
		if err == nil {
			reservation = ManagedControlReservation{RequestID: input.RequestID}
		}
		return err
	})
	return reservation, err
}

func (s *Store) CancelManagedControlRequests(ctx context.Context, agentID, code string) error {
	if !isLifecycleText(agentID) || (code != "CANCELLED_BY_RESET" && code != "CANCELLED_BY_TERMINATE") {
		return errors.New("managed control cancellation is invalid")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='cancelled',terminal_at=?,error_code=?,error_detail=NULL,retryable=0 WHERE agent_id=? AND state NOT IN ('delivered','failed','cancelled')`, now, code, agentID)
		return err
	})
}

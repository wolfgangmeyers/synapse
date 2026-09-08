package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	restartAllNotReadyCode    = "AGENT_NOT_READY"
	restartAllNotReadyMessage = "managed agent is not ready to restart"
	restartAllConflictCode    = "CONFLICT"
	restartAllConflictMessage = "a managed lifecycle operation is already in progress"
)

// ManagedRestartAllChildIdentity is allocated only after the store has taken
// its writer-locked snapshot and classified an item as accepted. Allocation is
// intentionally side-effect free: durable writes remain entirely store-owned.
type ManagedRestartAllChildIdentity struct {
	ActionID                       string
	PlannedTargetRuntimeGeneration string
}

// ManagedRestartAllReservation contains only canonical parent request metadata
// plus the bounded child ID source. The manager cannot supply graph rows or
// response bytes, which keeps admission and exact replay in one transaction.
type ManagedRestartAllReservation struct {
	Method, CanonicalPath, RequestID, CanonicalBodyHash string
	AllocateChild                                       func() (ManagedRestartAllChildIdentity, error)
}

// ManagedRestartAllAdmission is returned only after the parent and every item
// have committed. AcceptedChildActionIDs are ordered by the durable snapshot
// and are the sole input to the post-commit dispatcher.
type ManagedRestartAllAdmission struct {
	Operation              *ManagedOperation
	AcceptedChildActionIDs []string
	Replayed               bool
}

type managedRestartAllItem struct {
	agent      *ManagedAgentRecord
	status     string
	childID    string
	generation string
	code       string
	message    string
}

type managedRestartAllResponse struct {
	OperationID string                          `json:"operation_id"`
	Agents      []managedRestartAllResponseItem `json:"agents"`
}

type managedRestartAllResponseItem struct {
	AgentID     string  `json:"agent_id"`
	OperationID *string `json:"operation_id"`
	Status      string  `json:"status"`
}

type managedRestartAllChildResponse struct {
	OperationID string         `json:"operation_id"`
	Agent       map[string]any `json:"agent"`
}

func restartAllEmptyBodyHash() string {
	sum := sha256.Sum256([]byte("{}"))
	return hex.EncodeToString(sum[:])
}

func restartAllPublicAgent(agent *ManagedAgentRecord) map[string]any {
	if agent == nil {
		return nil
	}
	result := map[string]any{
		"agent_id": agent.AgentID, "source": "router-managed", "cwd": agent.Cwd,
		"model":         map[string]any{"provider": agent.SelectedModel.Provider, "id": agent.SelectedModel.ID},
		"desired_state": agent.DesiredState, "lifecycle_state": agent.LifecycleState, "activity_state": agent.ActivityState,
		"accepting_prompts": agent.AcceptingPrompts,
		"manifest":          map[string]any{"revision": agent.ManifestRevision, "sha256": agent.ManifestHash, "installed_tool_count": 16},
		"created_at":        agent.CreatedAt, "started_at": nil, "updated_at": agent.UpdatedAt, "last_error": nil,
	}
	if agent.StartedAt != "" {
		result["started_at"] = agent.StartedAt
	}
	if agent.LastErrorCode != "" {
		result["last_error"] = map[string]any{"code": agent.LastErrorCode, "message": agent.LastErrorMessage, "at": agent.LastErrorAt}
	}
	return result
}

func restartAllChildOperation(agent *ManagedAgentRecord, childID, parentID, now string) (ManagedOperation, error) {
	projection := *agent
	projection.LifecycleState = "restarting"
	projection.AcceptingPrompts = false
	projection.UpdatedAt = now
	body, err := json.Marshal(managedRestartAllChildResponse{OperationID: childID, Agent: restartAllPublicAgent(&projection)})
	if err != nil {
		return ManagedOperation{}, err
	}
	return ManagedOperation{
		OperationID: childID, Method: "POST",
		CanonicalPath:     "/internal/managed-lifecycle/restart-all/" + parentID + "/children/" + agent.AgentID,
		RequestID:         childID,
		CanonicalBodyHash: restartAllEmptyBodyHash(),
		State:             "pending",
		ResponseStatus:    202,
		ResponseHeaders:   map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": childID},
		ResponseBody:      body,
		CreatedAt:         now,
	}, nil
}

func managedRestartAllActiveActionsTx(ctx context.Context, db *sql.DB, agentID string) ([]*ManagedLifecycleAction, error) {
	rows, err := db.QueryContext(ctx, managedLifecycleActionSelect+` WHERE agent_id=? AND state IN ('admitted','awaiting_settle','stopping_source','starting_target','awaiting_target_ready','old_artifact_cleanup','terminating','cleanup') ORDER BY action_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var actions []*ManagedLifecycleAction
	for rows.Next() {
		action, scanErr := scanManagedLifecycleAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func validRestartAllCoalescedDirectTx(ctx context.Context, db *sql.DB, agent *ManagedAgentRecord, action *ManagedLifecycleAction) bool {
	if agent == nil || action == nil || action.ActionID != action.OperationID || action.Kind != "restart" || action.Source != "direct" || action.ParentActionID != "" || action.RetryOf != "" || (action.ExecutionOwner != "legacy025" && action.ExecutionOwner != "lifecycle") || action.SourceFence == nil || !sameManagedRestartFence(*action.SourceFence, restartFenceFromAgent(agent)) {
		return false
	}
	operation, err := managedOperationForTx(ctx, db, action.OperationID)
	if err != nil || operation.State != "pending" {
		return false
	}
	transition, transitionErr := managedRestartTransitionForTx(ctx, db, agent.AgentID)
	switch action.ExecutionOwner {
	case "legacy025":
		if transitionErr != nil || transition.OperationID != action.OperationID {
			return false
		}
		bridge, bridgeErr := legacyLifecycleBridgeActionTx(ctx, db, transition, *action.SourceFence)
		return bridgeErr == nil && bridge.ActionID == action.ActionID
	case "lifecycle":
		return errors.Is(transitionErr, sql.ErrNoRows)
	default:
		return false
	}
}

func managedRestartAllClassifyTx(ctx context.Context, db *sql.DB, agent *ManagedAgentRecord) (managedRestartAllItem, error) {
	item := managedRestartAllItem{agent: agent}
	actions, err := managedRestartAllActiveActionsTx(ctx, db, agent.AgentID)
	if err != nil {
		return item, err
	}
	if len(actions) != 0 {
		if len(actions) == 1 && validRestartAllCoalescedDirectTx(ctx, db, agent, actions[0]) {
			item.status, item.childID = "coalesced", actions[0].OperationID
			return item, nil
		}
		item.status, item.code, item.message = "rejected", restartAllConflictCode, restartAllConflictMessage
		return item, nil
	}
	if transition, transitionErr := managedRestartTransitionForTx(ctx, db, agent.AgentID); transitionErr == nil && transition != nil {
		item.status, item.code, item.message = "rejected", restartAllConflictCode, restartAllConflictMessage
		return item, nil
	} else if !errors.Is(transitionErr, sql.ErrNoRows) {
		return item, transitionErr
	}
	if agent.LaunchOperationID != "" {
		launch, launchErr := managedOperationForTx(ctx, db, agent.LaunchOperationID)
		if launchErr != nil {
			return item, launchErr
		}
		if launch.State != "terminal" {
			item.status, item.code, item.message = "rejected", restartAllNotReadyCode, restartAllNotReadyMessage
			return item, nil
		}
	}
	if agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld || (agent.ActivityState != "active" && agent.ActivityState != "idle") || !validManagedRuntimeFence(restartFenceFromAgent(agent)) {
		item.status, item.code, item.message = "rejected", restartAllNotReadyCode, restartAllNotReadyMessage
		return item, nil
	}
	item.status = "accepted"
	return item, nil
}

func insertManagedRestartAllActionTx(ctx context.Context, db *sql.DB, input ManagedLifecycleActionInput) error {
	if err := validateLifecycleActionInput(input); err != nil {
		return err
	}
	if input.ParentActionID != "" {
		parent, err := managedLifecycleActionForTx(ctx, db, input.ParentActionID)
		if err != nil || parent.Kind != "restart_all" {
			if err != nil {
				return err
			}
			return errors.New("managed restart-all child parent is invalid")
		}
	}
	args := []any{input.ActionID, input.Operation.OperationID, nullIfEmpty(input.AgentID), input.Kind, input.Source, nullIfEmpty(input.ParentActionID), nullIfEmpty(input.RetryOf), input.ExecutionOwner, input.State, input.Stage}
	args = append(args, lifecycleFenceValues(input.SourceFence)...)
	args = append(args, lifecycleFenceValues(input.TargetFence)...)
	args = append(args, nullIfEmpty(input.PlannedTargetRuntimeGeneration), nullIfEmpty(input.TargetArtifactID), nullIfEmpty(input.TerminalCode), nullIfEmpty(input.TerminalMessage), nil, nil, input.CreatedAt, input.UpdatedAt)
	_, err := db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
		action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
		source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
		target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,
		planned_target_runtime_generation,target_artifact_id,terminal_code,terminal_message,retryable,completed_at,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	return err
}

func (s *Store) restartAllFault(stage string) error {
	if s.managedRestartAllFault == nil {
		return nil
	}
	return s.managedRestartAllFault(stage)
}

// ReserveManagedRestartAll atomically creates the immutable batch-admission
// parent, every durable child/item, and all accepted-agent admission updates.
// The returned child IDs are not a queue. They are only post-commit handoff
// inputs; lifecycle restart execution remains owned by the S3 bridge.
func (s *Store) ReserveManagedRestartAll(ctx context.Context, reservation ManagedRestartAllReservation) (ManagedRestartAllAdmission, error) {
	if !managedCanonicalUUIDRE.MatchString(reservation.RequestID) || reservation.Method != "POST" || reservation.CanonicalPath == "" || !managedSHA256RE.MatchString(reservation.CanonicalBodyHash) || reservation.AllocateChild == nil {
		return ManagedRestartAllAdmission{}, errors.New("managed restart-all reservation is invalid")
	}
	var admission ManagedRestartAllAdmission
	err := s.runImmediate(ctx, func() error {
		existing, findErr := managedOperationForWorkerScopeTx(ctx, s.db, reservation.Method, reservation.RequestID)
		if findErr == nil {
			if existing.CanonicalPath != reservation.CanonicalPath || existing.CanonicalBodyHash != reservation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			admission.Operation, admission.Replayed = existing, true
			return nil
		}
		if !errors.Is(findErr, sql.ErrNoRows) {
			return findErr
		}

		rows, queryErr := s.db.QueryContext(ctx, `SELECT agent_id FROM managed_agents WHERE desired_state='running' ORDER BY agent_id ASC`)
		if queryErr != nil {
			return queryErr
		}
		var snapshotIDs []string
		for rows.Next() {
			var agentID string
			if scanErr := rows.Scan(&agentID); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			snapshotIDs = append(snapshotIDs, agentID)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		if len(snapshotIDs) > 32 {
			return errors.New("managed restart-all snapshot exceeds capacity")
		}

		items := make([]managedRestartAllItem, 0, len(snapshotIDs))
		seenIDs := map[string]bool{reservation.RequestID: true}
		seenGenerations := map[string]bool{reservation.RequestID: true}
		for _, agentID := range snapshotIDs {
			agent, agentErr := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, agentID), nil)
			if agentErr != nil {
				return agentErr
			}
			if linkErr := loadManagedLaunchOperationTx(ctx, s.db, agent); linkErr != nil {
				return linkErr
			}
			item, classifyErr := managedRestartAllClassifyTx(ctx, s.db, agent)
			if classifyErr != nil {
				return classifyErr
			}
			if item.status == "accepted" {
				identity, allocateErr := reservation.AllocateChild()
				if allocateErr != nil {
					return allocateErr
				}
				if !managedCanonicalUUIDRE.MatchString(identity.ActionID) || !managedCanonicalUUIDRE.MatchString(identity.PlannedTargetRuntimeGeneration) || identity.ActionID == identity.PlannedTargetRuntimeGeneration || seenIDs[identity.ActionID] || seenGenerations[identity.PlannedTargetRuntimeGeneration] {
					return errors.New("managed restart-all child allocation is invalid")
				}
				var collisions int
				if collisionErr := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM managed_http_operations WHERE operation_id=?) + (SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?)`, identity.ActionID, identity.ActionID).Scan(&collisions); collisionErr != nil {
					return collisionErr
				}
				if collisions != 0 {
					return errors.New("managed restart-all child allocation collides")
				}
				seenIDs[identity.ActionID], seenGenerations[identity.PlannedTargetRuntimeGeneration] = true, true
				item.childID, item.generation = identity.ActionID, identity.PlannedTargetRuntimeGeneration
			}
			items = append(items, item)
		}

		responseItems := make([]managedRestartAllResponseItem, 0, len(items))
		for _, item := range items {
			responseItem := managedRestartAllResponseItem{AgentID: item.agent.AgentID, Status: item.status}
			if item.status == "accepted" || item.status == "coalesced" {
				childID := item.childID
				responseItem.OperationID = &childID
			}
			responseItems = append(responseItems, responseItem)
		}
		parentBody, marshalErr := json.Marshal(managedRestartAllResponse{OperationID: reservation.RequestID, Agents: responseItems})
		if marshalErr != nil {
			return marshalErr
		}
		now := formatTimestamp(s.clock.Now())
		parentOperation := ManagedOperation{OperationID: reservation.RequestID, Method: reservation.Method, CanonicalPath: reservation.CanonicalPath, RequestID: reservation.RequestID, CanonicalBodyHash: reservation.CanonicalBodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": reservation.RequestID}, ResponseBody: parentBody, CreatedAt: now}
		if insertErr := insertManagedOperation(ctx, s.db, parentOperation); insertErr != nil {
			return insertErr
		}
		if faultErr := s.restartAllFault("after_parent_operation"); faultErr != nil {
			return faultErr
		}
		parentAction := ManagedLifecycleActionInput{ActionID: reservation.RequestID, Kind: "restart_all", Source: "direct", ExecutionOwner: "lifecycle", State: "admitted", Stage: "admission", CreatedAt: now, UpdatedAt: now, Operation: parentOperation}
		if insertErr := insertManagedRestartAllActionTx(ctx, s.db, parentAction); insertErr != nil {
			return insertErr
		}
		if faultErr := s.restartAllFault("after_parent_action"); faultErr != nil {
			return faultErr
		}

		for _, item := range items {
			if item.status != "accepted" {
				continue
			}
			childOperation, childErr := restartAllChildOperation(item.agent, item.childID, reservation.RequestID, now)
			if childErr != nil {
				return childErr
			}
			if insertErr := insertManagedOperation(ctx, s.db, childOperation); insertErr != nil {
				return insertErr
			}
			if faultErr := s.restartAllFault("after_child_operation"); faultErr != nil {
				return faultErr
			}
			source := restartFenceFromAgent(item.agent)
			childAction := ManagedLifecycleActionInput{ActionID: item.childID, AgentID: item.agent.AgentID, Kind: "restart", Source: "restart_all_child", ParentActionID: reservation.RequestID, ExecutionOwner: "lifecycle", State: "admitted", Stage: "admission", SourceFence: &source, PlannedTargetRuntimeGeneration: item.generation, CreatedAt: now, UpdatedAt: now, Operation: childOperation}
			if insertErr := insertManagedRestartAllActionTx(ctx, s.db, childAction); insertErr != nil {
				return insertErr
			}
			if faultErr := s.restartAllFault("after_child_action"); faultErr != nil {
				return faultErr
			}
		}

		accepted := 0
		for position, item := range items {
			if item.status == "accepted" {
				source := restartFenceFromAgent(item.agent)
				result, updateErr := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',accepting_prompts=0,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running' AND accepting_prompts=1 AND slot_held=1 AND activity_state IN ('active','idle')`, append([]any{now}, managedFenceValues(source)...)...)
				if updateErr != nil {
					return updateErr
				}
				changed, updateErr := result.RowsAffected()
				if updateErr != nil {
					return updateErr
				}
				if changed != 1 {
					return &ManagedRestartStaleError{AgentID: item.agent.AgentID}
				}
				accepted++
			}
			batch := ManagedLifecycleBatchItem{SnapshotPosition: position, AgentID: item.agent.AgentID, Status: item.status, CreatedAt: now}
			switch item.status {
			case "accepted", "coalesced":
				batch.ChildActionID, batch.ChildOperationID = item.childID, item.childID
			case "rejected":
				batch.ErrorCode, batch.ErrorMessage = item.code, item.message
			default:
				return fmt.Errorf("managed restart-all item status is invalid")
			}
			if _, itemErr := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,error_code,error_message,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, reservation.RequestID, batch.SnapshotPosition, batch.AgentID, batch.Status, nullIfEmpty(batch.ChildActionID), nullIfEmpty(batch.ChildOperationID), nullIfEmpty(batch.ErrorCode), nullIfEmpty(batch.ErrorMessage), batch.CreatedAt); itemErr != nil {
				return itemErr
			}
			if faultErr := s.restartAllFault("after_batch_item"); faultErr != nil {
				return faultErr
			}
		}
		if faultErr := s.restartAllFault("after_agent_updates"); faultErr != nil {
			return faultErr
		}
		if accepted > 0 {
			if _, bumpErr := s.bumpManagedProjectionVersion(ctx); bumpErr != nil {
				return bumpErr
			}
		}
		terminalAt := formatTimestamp(s.clock.Now())
		expiresAt := formatTimestamp(s.clock.Now().Add(24 * time.Hour))
		operationResult, terminalErr := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code='BATCH_ADMITTED',terminal_message='managed restart batch admitted' WHERE operation_id=? AND state='pending'`, terminalAt, expiresAt, reservation.RequestID)
		if terminalErr != nil {
			return terminalErr
		}
		changed, terminalErr := operationResult.RowsAffected()
		if terminalErr != nil || changed != 1 {
			if terminalErr != nil {
				return terminalErr
			}
			return errors.New("managed restart-all parent operation terminalization is stale")
		}
		if faultErr := s.restartAllFault("after_parent_operation_terminal"); faultErr != nil {
			return faultErr
		}
		actionResult, terminalErr := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='succeeded',stage='complete',terminal_code='BATCH_ADMITTED',terminal_message='managed restart batch admitted',retryable=0,completed_at=?,updated_at=? WHERE action_id=? AND operation_id=? AND execution_owner='lifecycle' AND state='admitted' AND stage='admission' AND `+managedLifecycleNullFenceWhere("source_")+` AND `+managedLifecycleNullFenceWhere("target_"), terminalAt, terminalAt, reservation.RequestID, reservation.RequestID)
		if terminalErr != nil {
			return terminalErr
		}
		changed, terminalErr = actionResult.RowsAffected()
		if terminalErr != nil || changed != 1 {
			if terminalErr != nil {
				return terminalErr
			}
			return errors.New("managed restart-all parent action terminalization is stale")
		}
		if faultErr := s.restartAllFault("after_parent_action_terminal"); faultErr != nil {
			return faultErr
		}
		stored, readErr := managedOperationForTx(ctx, s.db, reservation.RequestID)
		if readErr != nil {
			return readErr
		}
		admission.Operation = stored
		for _, item := range items {
			if item.status == "accepted" {
				admission.AcceptedChildActionIDs = append(admission.AcceptedChildActionIDs, item.childID)
			}
		}
		return nil
	})
	if err == nil {
		return admission, nil
	}
	var conflict *ManagedIdempotencyConflictError
	if !errors.As(err, &conflict) {
		return ManagedRestartAllAdmission{}, err
	}
	// A second store process can win the unique parent-operation insert after
	// this store observed no replay. Re-read only the parent operation; never
	// rebuild the snapshot or allocate children after that race.
	existing, readErr := s.FindManagedOperation(ctx, reservation.Method, reservation.RequestID)
	if readErr != nil {
		return ManagedRestartAllAdmission{}, err
	}
	if existing.CanonicalPath != reservation.CanonicalPath || existing.CanonicalBodyHash != reservation.CanonicalBodyHash {
		return ManagedRestartAllAdmission{}, &ManagedIdempotencyConflictError{}
	}
	return ManagedRestartAllAdmission{Operation: existing, Replayed: true}, nil
}

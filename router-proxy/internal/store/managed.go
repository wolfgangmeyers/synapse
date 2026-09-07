package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var managedIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ManagedModel is the immutable provider/model pair selected for a launch.
type ManagedModel struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
}

// ManagedAgentRecord is the internal durable managed-agent row. Private
// process details are deliberately kept in this type and omitted by the
// transport projection.
type ManagedAgentRecord struct {
	AgentID                   string
	Name                      string
	SlotHeld                  bool
	Cwd                       string
	SelectedModel             ManagedModel
	DesiredState              string
	LifecycleState            string
	ActivityState             string
	AcceptingPrompts          bool
	PiSessionID               string
	SessionEpoch              int64
	ProcessPID                int
	ProcessStartMarker        string
	ProcessIncarnation        string
	RuntimeGeneration         string
	ManifestRevision          int
	ManifestHash              string
	PendingInitialInstruction string
	InitialInstructionSent    bool
	LastErrorCode             string
	LastErrorMessage          string
	LastErrorAt               string
	CreatedAt                 string
	StartedAt                 string
	UpdatedAt                 string
}

// ManagedOperation is the durable replay record. Its storage scope is private
// to the store so callers cannot supply caller identity as part of the key.
type ManagedOperation struct {
	OperationID       string
	Method            string
	CanonicalPath     string
	RequestID         string
	CanonicalBodyHash string
	State             string
	ResponseStatus    int
	ResponseHeaders   map[string]string
	ResponseBody      []byte
	CreatedAt         string
	TerminalAt        string
	ExpiresAt         string
}

// managedWorkerOperationScope keeps migration 022 storage-compatible while
// making direct-worker idempotency independent of caller identity.
const managedWorkerOperationScope = "router-managed-worker-v1"

const managedOperationSelect = `SELECT operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at,terminal_at,expires_at FROM managed_http_operations`

// ManagedRestartFence identifies the exact live process that reservation may
// take out of prompt admission. Every restart write carries this old fence.
type ManagedRestartFence struct {
	AgentID            string
	PiSessionID        string
	SessionEpoch       int64
	ProcessPID         int
	ProcessStartMarker string
	ProcessIncarnation string
	RuntimeGeneration  string
}

// ManagedRestartTransition is the bounded durable handoff to a later
// replacement owner. It deliberately stores the pre-reservation projection so
// preparation can restore the still-live process without guessing its state.
type ManagedRestartTransition struct {
	OperationID              string
	Fence                    ManagedRestartFence
	TargetRuntimeGeneration  string
	DesiredState             string
	LifecycleState           string
	ActivityState            string
	AcceptingPrompts         bool
	SlotHeld                 bool
	ReservationActivityState string
	PlannedStop              bool
	Stage                    string
	CreatedAt                string
	UpdatedAt                string
}

// ManagedRestartReservation is the store-only input for the atomic durable
// reservation. The manager supplies the operation that owns its live binding
// so the store can check the same row under the immediate transaction.
type ManagedRestartReservation struct {
	Fence                  ManagedRestartFence
	CurrentOperationID     string
	CurrentLifecycleAction string
	Transition             ManagedRestartTransition
	Operation              ManagedOperation
}

// ManagedAgentSnapshot is the one-read-transaction projection boundary.
type ManagedAgentSnapshot struct {
	GeneratedAt       time.Time
	ProjectionVersion int64
	Agent             *ManagedAgentRecord
}

type ManagedNameConflictError struct{ Name string }

func (e *ManagedNameConflictError) Error() string { return "managed agent name is already reserved" }
func (e *ManagedNameConflictError) Code() string  { return "name_in_use" }

type ManagedSlotLimitError struct{}

func (e *ManagedSlotLimitError) Error() string { return "managed process limit reached" }
func (e *ManagedSlotLimitError) Code() string  { return "conflict" }

type ManagedIdempotencyConflictError struct{}

func (e *ManagedIdempotencyConflictError) Error() string {
	return "managed operation idempotency conflict"
}
func (e *ManagedIdempotencyConflictError) Code() string { return "idempotency_conflict" }

// ManagedAgentNotReadyError reports a durable or live eligibility failure.
type ManagedAgentNotReadyError struct{ AgentID string }

func (e *ManagedAgentNotReadyError) Error() string { return "managed agent is not ready" }
func (e *ManagedAgentNotReadyError) Code() string  { return "agent_not_ready" }

// ManagedRestartConflictError identifies another lifecycle owner that must
// finish before a new restart can reserve this agent.
type ManagedRestartConflictError struct{ AgentID string }

func (e *ManagedRestartConflictError) Error() string {
	return "managed restart conflicts with another lifecycle operation"
}
func (e *ManagedRestartConflictError) Code() string { return "conflict" }

// ManagedRestartStaleError is a fenced no-op. It is intentionally distinct
// from SQLite errors so callers never retry it as a transient database fault.
type ManagedRestartStaleError struct{ AgentID string }

func (e *ManagedRestartStaleError) Error() string { return "managed restart transition is stale" }
func (e *ManagedRestartStaleError) Code() string  { return "stale" }

func (s *Store) GetDefaultModel(ctx context.Context) (*ManagedModel, error) {
	var provider, id sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT default_model_provider, default_model_id FROM managed_settings WHERE singleton_id = 1`).Scan(&provider, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !provider.Valid || !id.Valid {
		return nil, nil
	}
	return &ManagedModel{Provider: provider.String, ID: id.String}, nil
}

func (s *Store) SetDefaultModel(ctx context.Context, model *ManagedModel) error {
	if model != nil && (!managedIdentifierRE.MatchString(model.Provider) || !managedIdentifierRE.MatchString(model.ID)) {
		return errors.New("invalid default model")
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		if model == nil {
			_, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET default_model_provider = NULL, default_model_id = NULL, updated_at = ? WHERE singleton_id = 1`, now)
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET default_model_provider = ?, default_model_id = ?, updated_at = ? WHERE singleton_id = 1`, model.Provider, model.ID, now)
		return err
	})
}

func (s *Store) GetCanonicalQuietRecipient(ctx context.Context) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT canonical_quiet_recipient_id FROM managed_settings WHERE singleton_id = 1`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetCanonicalQuietRecipient is a startup configuration boundary. It is not an
// HTTP setting route; changing it takes effect when the router is restarted.
func (s *Store) SetCanonicalQuietRecipient(ctx context.Context, recipient string) error {
	if !managedRecipientRE.MatchString(recipient) {
		return errors.New("invalid canonical quiet recipient")
	}
	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET canonical_quiet_recipient_id = ?, updated_at = ? WHERE singleton_id = 1`, recipient, formatTimestamp(s.clock.Now()))
		return err
	})
}

func (s *Store) GetProjectionVersion(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("store is closed")
	}
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT projection_version FROM managed_settings WHERE singleton_id = 1`).Scan(&version)
	return version, err
}

func (s *Store) bumpManagedProjectionVersion(ctx context.Context) (int64, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE managed_settings SET projection_version = projection_version + 1, updated_at = ? WHERE singleton_id = 1`, formatTimestamp(s.clock.Now())); err != nil {
		return 0, err
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT projection_version FROM managed_settings WHERE singleton_id = 1`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Store) ReserveProjectionVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.runImmediate(ctx, func() error { var err error; version, err = s.bumpManagedProjectionVersion(ctx); return err })
	return version, err
}

// ReserveManagedLaunch atomically reserves the name, one process slot, and
// operation before any child process is started.
func (s *Store) ReserveManagedLaunch(ctx context.Context, agent ManagedAgentRecord, operation ManagedOperation) error {
	return s.ReserveManagedLaunchWithLimit(ctx, agent, operation, 32)
}

func (s *Store) ReserveManagedLaunchWithLimit(ctx context.Context, agent ManagedAgentRecord, operation ManagedOperation, maxAgents int) error {
	if maxAgents < 1 {
		return errors.New("managed process limit must be positive")
	}
	if agent.AgentID == "" || agent.Name == "" || operation.OperationID == "" {
		return errors.New("managed launch identity is required")
	}
	if agent.CreatedAt == "" {
		agent.CreatedAt = formatTimestamp(s.clock.Now())
	}
	if agent.UpdatedAt == "" {
		agent.UpdatedAt = agent.CreatedAt
	}
	if agent.DesiredState == "" {
		agent.DesiredState = "running"
	}
	if agent.LifecycleState == "" {
		agent.LifecycleState = "starting"
	}
	if agent.ActivityState == "" {
		agent.ActivityState = "starting"
	}
	if agent.SessionEpoch < 1 {
		agent.SessionEpoch = 1
	}
	if operation.State == "" {
		operation.State = "pending"
	}
	return s.runImmediate(ctx, func() error {
		var existingOperation string
		err := s.db.QueryRowContext(ctx, `SELECT operation_id FROM managed_http_operations WHERE session_identity_hash = ? AND method = ? AND request_id = ?`, managedWorkerOperationScope, operation.Method, operation.RequestID).Scan(&existingOperation)
		if err == nil {
			return &ManagedIdempotencyConflictError{}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE slot_held = 1`).Scan(&count); err != nil {
			return err
		}
		if count >= maxAgents {
			return &ManagedSlotLimitError{}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_agents (
            agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
            desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
            session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
            manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,
            last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			agent.AgentID, agent.Name, boolInt(true), agent.Cwd,
			agent.SelectedModel.Provider, agent.SelectedModel.ID, agent.DesiredState,
			agent.LifecycleState, agent.ActivityState, boolInt(agent.AcceptingPrompts), nullIfEmpty(agent.PiSessionID),
			agent.SessionEpoch, nullIfZero(agent.ProcessPID), nullIfEmpty(agent.ProcessStartMarker), nullIfEmpty(agent.ProcessIncarnation), nullIfEmpty(agent.RuntimeGeneration),
			agent.ManifestRevision, agent.ManifestHash, nullIfEmpty(agent.PendingInitialInstruction), boolInt(agent.InitialInstructionSent),
			nullIfEmpty(agent.LastErrorCode), nullIfEmpty(agent.LastErrorMessage), nullIfEmpty(agent.LastErrorAt), agent.CreatedAt, nullIfEmpty(agent.StartedAt), agent.UpdatedAt); err != nil {
			if isConstraintError(err) {
				return &ManagedNameConflictError{Name: agent.Name}
			}
			return err
		}
		if err := insertManagedOperation(ctx, s.db, operation); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func insertManagedOperation(ctx context.Context, db *sql.DB, operation ManagedOperation) error {
	headers, err := json.Marshal(operation.ResponseHeaders)
	if err != nil {
		return err
	}
	if len(operation.ResponseBody) > 64*1024 {
		return errors.New("managed replay response is too large")
	}
	_, err = db.ExecContext(ctx, `INSERT INTO managed_http_operations (
        operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,
        response_status,response_headers_json,response_body,created_at,terminal_at,expires_at)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, operation.OperationID, managedWorkerOperationScope, operation.Method,
		operation.CanonicalPath, operation.RequestID, operation.CanonicalBodyHash, operation.State,
		operation.ResponseStatus, string(headers), string(operation.ResponseBody), operation.CreatedAt,
		nullIfEmpty(operation.TerminalAt), nullIfEmpty(operation.ExpiresAt))
	if err != nil && isConstraintError(err) {
		return &ManagedIdempotencyConflictError{}
	}
	return err
}

func (s *Store) PutManagedOperation(ctx context.Context, operation ManagedOperation) error {
	if operation.CreatedAt == "" {
		operation.CreatedAt = formatTimestamp(s.clock.Now())
	}
	return s.runImmediate(ctx, func() error { return insertManagedOperation(ctx, s.db, operation) })
}

func scanManagedOperation(scanner interface{ Scan(...any) error }) (*ManagedOperation, error) {
	var result ManagedOperation
	var scope, headers, body string
	var terminalAt, expiresAt sql.NullString
	if err := scanner.Scan(&result.OperationID, &scope, &result.Method, &result.CanonicalPath, &result.RequestID,
		&result.CanonicalBodyHash, &result.State, &result.ResponseStatus, &headers, &body, &result.CreatedAt, &terminalAt, &expiresAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(headers), &result.ResponseHeaders); err != nil {
		return nil, err
	}
	result.ResponseBody = []byte(body)
	if terminalAt.Valid {
		result.TerminalAt = terminalAt.String
	}
	if expiresAt.Valid {
		result.ExpiresAt = expiresAt.String
	}
	return &result, nil
}

// FindManagedOperation uses the one internal worker scope. Direct callers
// share request IDs, so an exact replay is independent of transport identity.
func (s *Store) FindManagedOperation(ctx context.Context, method, requestID string) (*ManagedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedOperation(s.db.QueryRowContext(ctx, managedOperationSelect+` WHERE session_identity_hash = ? AND method = ? AND request_id = ?`, managedWorkerOperationScope, method, requestID))
}

func (s *Store) ManagedOperation(ctx context.Context, operationID string) (*ManagedOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedOperation(s.db.QueryRowContext(ctx, managedOperationSelect+` WHERE operation_id = ?`, operationID))
}

func (s *Store) MarkManagedOperationTerminal(ctx context.Context, operationID string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state = 'terminal', terminal_at = ?, expires_at = ? WHERE operation_id = ? AND state = 'pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
		return err
	})
}

func restartFenceFromAgent(agent *ManagedAgentRecord) ManagedRestartFence {
	return ManagedRestartFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration}
}

func sameManagedRestartFence(left, right ManagedRestartFence) bool {
	return left.AgentID == right.AgentID && left.PiSessionID == right.PiSessionID && left.SessionEpoch == right.SessionEpoch && left.ProcessPID == right.ProcessPID && left.ProcessStartMarker == right.ProcessStartMarker && left.ProcessIncarnation == right.ProcessIncarnation && left.RuntimeGeneration == right.RuntimeGeneration
}

func restartActionHasHigherPriority(action string) bool {
	switch action {
	case "reset", "resetting", "terminate", "terminating", "stop", "stopping":
		return true
	default:
		return false
	}
}

func managedOperationForTx(ctx context.Context, db *sql.DB, operationID string) (*ManagedOperation, error) {
	return scanManagedOperation(db.QueryRowContext(ctx, managedOperationSelect+` WHERE operation_id = ?`, operationID))
}

func managedOperationForWorkerScopeTx(ctx context.Context, db *sql.DB, method, requestID string) (*ManagedOperation, error) {
	return scanManagedOperation(db.QueryRowContext(ctx, managedOperationSelect+` WHERE session_identity_hash = ? AND method = ? AND request_id = ?`, managedWorkerOperationScope, method, requestID))
}

// ReserveManagedRestart inserts the replay operation and one transition before
// changing the public projection. Every eligibility and fence predicate is
// checked while the immediate transaction holds the SQLite writer lock.
func (s *Store) ReserveManagedRestart(ctx context.Context, reservation ManagedRestartReservation) (*ManagedOperation, bool, error) {
	if reservation.Fence.AgentID == "" || reservation.CurrentOperationID == "" || reservation.Operation.OperationID == "" || reservation.Operation.RequestID == "" || reservation.Transition.TargetRuntimeGeneration == "" {
		return nil, false, errors.New("managed restart reservation identity is required")
	}
	operation := reservation.Operation
	if operation.State == "" {
		operation.State = "pending"
	}
	if operation.CreatedAt == "" {
		operation.CreatedAt = formatTimestamp(s.clock.Now())
	}
	var result *ManagedOperation
	replayed := false
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, operation.Method, operation.RequestID)
		if err == nil {
			if existing.CanonicalPath != operation.CanonicalPath || existing.CanonicalBodyHash != operation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			result, replayed = existing, true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, reservation.Fence.AgentID), nil)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotReadyError{AgentID: reservation.Fence.AgentID}
		}
		if err != nil {
			return err
		}
		if !sameManagedRestartFence(restartFenceFromAgent(agent), reservation.Fence) {
			return &ManagedAgentNotReadyError{AgentID: reservation.Fence.AgentID}
		}
		var transitionAgentID string
		err = s.db.QueryRowContext(ctx, `SELECT agent_id FROM managed_restart_transitions WHERE agent_id = ?`, reservation.Fence.AgentID).Scan(&transitionAgentID)
		if err == nil {
			return &ManagedRestartConflictError{AgentID: reservation.Fence.AgentID}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if agent.DesiredState != "running" || agent.LifecycleState != "running" || !agent.AcceptingPrompts || !agent.SlotHeld || (agent.ActivityState != "active" && agent.ActivityState != "idle") {
			return &ManagedAgentNotReadyError{AgentID: reservation.Fence.AgentID}
		}
		current, err := managedOperationForTx(ctx, s.db, reservation.CurrentOperationID)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedRestartConflictError{AgentID: reservation.Fence.AgentID}
		}
		if err != nil {
			return err
		}
		if current.State != "terminal" || restartActionHasHigherPriority(reservation.CurrentLifecycleAction) {
			return &ManagedRestartConflictError{AgentID: reservation.Fence.AgentID}
		}
		if err := insertManagedOperation(ctx, s.db, operation); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_restart_transitions (
			agent_id,operation_id,old_pi_session_id,old_session_epoch,old_process_pid,
			old_process_start_marker,old_process_incarnation,old_runtime_generation,target_runtime_generation,
			old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,
			reservation_activity_state,planned_stop,stage,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			reservation.Fence.AgentID, operation.OperationID, reservation.Fence.PiSessionID, reservation.Fence.SessionEpoch, reservation.Fence.ProcessPID,
			reservation.Fence.ProcessStartMarker, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration, reservation.Transition.TargetRuntimeGeneration,
			agent.DesiredState, agent.LifecycleState, agent.ActivityState, boolInt(agent.AcceptingPrompts), boolInt(agent.SlotHeld), agent.ActivityState, 0, "reserved", now, now); err != nil {
			return err
		}
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'restarting', accepting_prompts = 0, updated_at = ?
			WHERE agent_id = ? AND desired_state = 'running' AND lifecycle_state = 'running' AND accepting_prompts = 1 AND slot_held = 1
			AND activity_state IN ('active', 'idle') AND pi_session_id = ? AND session_epoch = ? AND process_pid = ?
			AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ?
			AND EXISTS (SELECT 1 FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ?)`,
			now, reservation.Fence.AgentID, reservation.Fence.PiSessionID, reservation.Fence.SessionEpoch, reservation.Fence.ProcessPID,
			reservation.Fence.ProcessStartMarker, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration,
			reservation.Fence.AgentID, operation.OperationID, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedAgentNotReadyError{AgentID: reservation.Fence.AgentID}
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		result = &operation
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, replayed, nil
}

func scanManagedRestartTransition(scanner interface{ Scan(...any) error }) (*ManagedRestartTransition, error) {
	var transition ManagedRestartTransition
	var accepting, slot, planned int
	if err := scanner.Scan(&transition.OperationID, &transition.Fence.AgentID, &transition.Fence.PiSessionID, &transition.Fence.SessionEpoch, &transition.Fence.ProcessPID,
		&transition.Fence.ProcessStartMarker, &transition.Fence.ProcessIncarnation, &transition.Fence.RuntimeGeneration, &transition.TargetRuntimeGeneration,
		&transition.DesiredState, &transition.LifecycleState, &transition.ActivityState, &accepting, &slot, &transition.ReservationActivityState,
		&planned, &transition.Stage, &transition.CreatedAt, &transition.UpdatedAt); err != nil {
		return nil, err
	}
	transition.AcceptingPrompts = accepting != 0
	transition.SlotHeld = slot != 0
	transition.PlannedStop = planned != 0
	return &transition, nil
}

const managedRestartTransitionSelect = `SELECT operation_id,agent_id,old_pi_session_id,old_session_epoch,old_process_pid,old_process_start_marker,old_process_incarnation,old_runtime_generation,target_runtime_generation,old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,reservation_activity_state,planned_stop,stage,created_at,updated_at FROM managed_restart_transitions`

// ManagedRestartTransition reads the one bounded transition for manager and
// store tests. It is not a public managed-agent projection.
func (s *Store) ManagedRestartTransition(ctx context.Context, agentID string) (*ManagedRestartTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedRestartTransition(s.db.QueryRowContext(ctx, managedRestartTransitionSelect+` WHERE agent_id = ?`, agentID))
}

func managedRestartTransitionForTx(ctx context.Context, db *sql.DB, agentID string) (*ManagedRestartTransition, error) {
	return scanManagedRestartTransition(db.QueryRowContext(ctx, managedRestartTransitionSelect+` WHERE agent_id = ?`, agentID))
}

// MarkManagedRestartPrepared advances only the private transition stage. It
// intentionally does not bump the public projection version.
func (s *Store) MarkManagedRestartPrepared(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage = 'prepared', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ? AND stage = 'reserved'
			AND EXISTS (SELECT 1 FROM managed_agents WHERE agent_id = ? AND process_incarnation = ? AND runtime_generation = ?)`,
			formatTimestamp(s.clock.Now()), fence.AgentID, operationID, fence.ProcessIncarnation, fence.RuntimeGeneration,
			fence.AgentID, fence.ProcessIncarnation, fence.RuntimeGeneration)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

// RollbackManagedRestartPreparation restores the saved projection and records
// the bounded preparation error before any stop or replacement work exists.
func (s *Store) RollbackManagedRestartPreparation(ctx context.Context, fence ManagedRestartFence, operationID, code, message string) error {
	return s.runImmediate(ctx, func() error {
		transition, err := managedRestartTransitionForTx(ctx, s.db, fence.AgentID)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		if err != nil {
			return err
		}
		if transition.OperationID != operationID || !sameManagedRestartFence(transition.Fence, fence) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state = ?, lifecycle_state = ?, activity_state = ?, accepting_prompts = ?, slot_held = ?, last_error_code = ?, last_error_message = ?, last_error_at = ?, updated_at = ?
			WHERE agent_id = ? AND lifecycle_state = 'restarting' AND accepting_prompts = 0 AND pi_session_id = ? AND session_epoch = ? AND process_pid = ?
			AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ?
			AND EXISTS (SELECT 1 FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ?)`,
			transition.DesiredState, transition.LifecycleState, transition.ActivityState, boolInt(transition.AcceptingPrompts), boolInt(transition.SlotHeld), code, message, now, now,
			fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration,
			fence.AgentID, operationID, fence.ProcessIncarnation, fence.RuntimeGeneration)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		deleted, err := s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ?`, fence.AgentID, operationID, fence.ProcessIncarnation, fence.RuntimeGeneration)
		if err != nil {
			return err
		}
		changed, err = deleted.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		terminalized, err := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state = 'terminal', terminal_at = ?, expires_at = ? WHERE operation_id = ? AND state = 'pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
		if err != nil {
			return err
		}
		changed, err = terminalized.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// RecordManagedRestartShutdown preserves a nonterminal transition after the
// manager has reaped the exact owned process group during router shutdown.
func (s *Store) RecordManagedRestartShutdown(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'stopped', updated_at = ?
			WHERE agent_id = ? AND desired_state = 'running' AND lifecycle_state = 'restarting' AND accepting_prompts = 0 AND slot_held = 1
			AND process_incarnation = ? AND runtime_generation = ?
			AND EXISTS (SELECT 1 FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ?)`,
			formatTimestamp(s.clock.Now()), fence.AgentID, fence.ProcessIncarnation, fence.RuntimeGeneration,
			fence.AgentID, operationID, fence.ProcessIncarnation, fence.RuntimeGeneration)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) UpdateManagedAgentStarting(ctx context.Context, agentID string, sessionID string, identity ProcessIdentityRecord, runtimeGeneration string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id = ?, process_pid = ?, process_start_marker = ?, process_incarnation = ?, runtime_generation = ?, updated_at = ? WHERE agent_id = ? AND lifecycle_state = 'starting'`, sessionID, nullIfZero(identity.PID), nullIfEmpty(identity.StartMarker), nullIfEmpty(identity.Incarnation), runtimeGeneration, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ProcessIdentityRecord keeps the store package independent from the process adapter.
type ProcessIdentityRecord struct {
	PID                      int
	StartMarker, Incarnation string
}

func (s *Store) MarkManagedReady(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'running', activity_state = 'idle', accepting_prompts = 1, started_at = COALESCE(started_at, ?), updated_at = ? WHERE agent_id = ? AND lifecycle_state = 'starting'`, now, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) MarkManagedFailed(ctx context.Context, agentID, code, message string) error {
	if len(code) > 128 {
		code = code[:128]
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'failed', activity_state = 'failed', accepting_prompts = 0, slot_held = 0, last_error_code = ?, last_error_message = ?, last_error_at = ?, updated_at = ? WHERE agent_id = ? AND lifecycle_state != 'failed'`, code, message, now, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// MarkManagedStopped records a planned process stop while retaining the
// desired-running record for later recovery. The row must stop advertising a
// live process as soon as its owned process has been reaped.
func (s *Store) MarkManagedStopped(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'stopped', accepting_prompts = 0, slot_held = 0, updated_at = ? WHERE agent_id = ? AND lifecycle_state != 'failed' AND activity_state != 'stopped'`, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) MarkManagedInitialInstructionSent(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET initial_instruction_sent = 1, pending_initial_instruction = NULL, updated_at = ? WHERE agent_id = ? AND initial_instruction_sent = 0`, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) ReleaseManagedSlot(ctx context.Context, agentID string) error {
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET slot_held = 0, updated_at = ? WHERE agent_id = ? AND slot_held = 1`, formatTimestamp(s.clock.Now()), agentID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) ManagedAgent(ctx context.Context, agentID string) (*ManagedAgentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, agentID), nil)
}

func (s *Store) ManagedAgents(ctx context.Context) ([]*ManagedAgentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedAgentSelect+` ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*ManagedAgentRecord
	for rows.Next() {
		item, scanErr := scanManagedAgent(rows, nil)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

const managedAgentSelect = `SELECT agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at FROM managed_agents`

func scanManagedAgent(scanner interface{ Scan(...any) error }, _ any) (*ManagedAgentRecord, error) {
	var result ManagedAgentRecord
	var slot, accepting, pid, sent sql.NullInt64
	var pi, marker, incarnation, runtime, pending, errorCode, errorMessage, errorAt, started sql.NullString
	if err := scanner.Scan(&result.AgentID, &result.Name, &slot, &result.Cwd, &result.SelectedModel.Provider, &result.SelectedModel.ID, &result.DesiredState, &result.LifecycleState, &result.ActivityState, &accepting, &pi, &result.SessionEpoch, &pid, &marker, &incarnation, &runtime, &result.ManifestRevision, &result.ManifestHash, &pending, &sent, &errorCode, &errorMessage, &errorAt, &result.CreatedAt, &started, &result.UpdatedAt); err != nil {
		return nil, err
	}
	result.SlotHeld = slot.Int64 != 0
	result.AcceptingPrompts = accepting.Int64 != 0
	result.InitialInstructionSent = sent.Int64 != 0
	result.ProcessPID = int(pid.Int64)
	result.PiSessionID = pi.String
	result.ProcessStartMarker = marker.String
	result.ProcessIncarnation = incarnation.String
	result.RuntimeGeneration = runtime.String
	result.PendingInitialInstruction = pending.String
	result.LastErrorCode = errorCode.String
	result.LastErrorMessage = errorMessage.String
	result.LastErrorAt = errorAt.String
	result.StartedAt = started.String
	return &result, nil
}

func (s *Store) ManagedAgentSnapshot(ctx context.Context, agentID string) (ManagedAgentSnapshot, error) {
	if ctx == nil {
		return ManagedAgentSnapshot{}, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedAgentSnapshot{}, errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN"); err != nil {
		return ManagedAgentSnapshot{}, err
	}
	rollback := func(err error) (ManagedAgentSnapshot, error) {
		_, _ = s.db.ExecContext(context.Background(), "ROLLBACK")
		return ManagedAgentSnapshot{}, err
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT projection_version FROM managed_settings WHERE singleton_id = 1`).Scan(&version); err != nil {
		return rollback(err)
	}
	agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, agentID), nil)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = s.db.ExecContext(ctx, "COMMIT")
		return ManagedAgentSnapshot{GeneratedAt: s.clock.Now().UTC(), ProjectionVersion: version}, nil
	}
	if err != nil {
		return rollback(err)
	}
	generated := s.clock.Now().UTC()
	if _, err := s.db.ExecContext(ctx, "COMMIT"); err != nil {
		return ManagedAgentSnapshot{}, err
	}
	return ManagedAgentSnapshot{GeneratedAt: generated, ProjectionVersion: version, Agent: agent}, nil
}

func (s *Store) sweepManagedOperations(ctx context.Context, now string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE state = 'terminal' AND expires_at IS NOT NULL AND expires_at <= ?`, now)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullIfZero(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
func isConstraintError(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) == false) && (len(err.Error()) > 0 && (containsText(err.Error(), "constraint") || containsText(err.Error(), "UNIQUE")))
}
func containsText(value, wanted string) bool {
	for i := 0; i+len(wanted) <= len(value); i++ {
		if equalFold(value[i:i+len(wanted)], wanted) {
			return true
		}
	}
	return false
}
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

var _ = fmt.Sprintf

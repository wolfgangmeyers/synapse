package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var managedIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var managedSHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

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

// ManagedRuntimeFence identifies the exact bound Pi runtime. Source writes,
// readiness, and restart reservation use this one fence so a manifest change
// cannot be mistaken for the same live process.
type ManagedRuntimeFence struct {
	AgentID            string
	PiSessionID        string
	SessionEpoch       int64
	ProcessPID         int
	ProcessStartMarker string
	ProcessIncarnation string
	RuntimeGeneration  string
	ManifestRevision   int
	ManifestHash       string
}

// ManagedRestartFence remains source-compatible for the closed restart slice.
type ManagedRestartFence = ManagedRuntimeFence

type ManagedDesiredPolicy struct {
	Canonical []byte
	SHA256    string
}

type ManagedChildSnapshot struct {
	SnapshotID     string
	OwnerSessionID string
	Sequence       int64
	Canonical      []byte
	SHA256         string
}

// ManagedReadinessSource is private durable state. It is deliberately absent
// from public projections and lasts until a future terminate transaction.
type ManagedReadinessSource struct {
	AgentID             string
	DesiredPolicy       ManagedDesiredPolicy
	Fence               *ManagedRuntimeFence
	AppliedPolicy       *ManagedDesiredPolicy
	PolicyConfirmedAt   string
	Snapshot            *ManagedChildSnapshot
	SnapshotConfirmedAt string
}

const defaultManagedReadinessPolicyCanonical = `{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{},"enabled":false},"recall":{"content":""},"settled":{"enabled":false}}`
const defaultManagedReadinessPolicySHA256 = "dcb17869020df00dd27a11e14cc0facada376f729a13002185a3d4b87f3c3648"

// DefaultManagedReadinessPolicy returns a copy because callers must not mutate
// the exact canonical bytes that identify the router's default projection.
func DefaultManagedReadinessPolicy() ManagedDesiredPolicy {
	return ManagedDesiredPolicy{Canonical: []byte(defaultManagedReadinessPolicyCanonical), SHA256: defaultManagedReadinessPolicySHA256}
}

// ManagedRestartTransition is the bounded durable handoff to a later
// replacement owner. It deliberately stores the pre-reservation projection so
// preparation can restore the still-live process without guessing its state.
type ManagedRestartTransition struct {
	OperationID              string
	Fence                    ManagedRestartFence
	TargetRuntimeGeneration  string
	TargetFence              *ManagedRuntimeFence
	DesiredState             string
	LifecycleState           string
	ActivityState            string
	AcceptingPrompts         bool
	SlotHeld                 bool
	ReservationActivityState string
	PlannedStop              bool
	TurnSettledAt            string
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

// RecipientSourceConflictError keeps a name from being selected by transport
// lookup order when a managed process and a tmux primary claim it.
type RecipientSourceConflictError struct{ Recipient string }

func (e *RecipientSourceConflictError) Error() string { return "recipient source is already owned" }
func (e *RecipientSourceConflictError) Code() string  { return "recipient_source_conflict" }

type RecipientSource struct {
	RecipientName string
	SourceKind    string
	OwnerIdentity string
	State         string
	ConflictKind  string
	ConflictOwner string
	CreatedAt     string
	UpdatedAt     string
}

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

// ManagedAgentNotFoundError distinguishes a durable absence from a live
// binding that has not reached the restart-ready state.
type ManagedAgentNotFoundError struct{ AgentID string }

func (e *ManagedAgentNotFoundError) Error() string { return "managed agent was not found" }
func (e *ManagedAgentNotFoundError) Code() string  { return "agent_not_found" }

// ManagedRestartAdmission is the durable half of direct restart admission.
// It deliberately contains no process handle; callers perform live binding
// checks only after this record has resolved replay and retained transitions.
type ManagedRestartAdmission struct {
	Operation  *ManagedOperation
	Agent      *ManagedAgentRecord
	Transition *ManagedRestartTransition
}

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
		if err := s.reserveRecipientSourceTx(ctx, agent.Name, "managed", agent.AgentID); err != nil {
			// Forward-migration fixtures deliberately stop before 026. Production
			// Open applies it before a managed launch can be reserved.
			if !strings.Contains(err.Error(), "no such table: recipient_sources") {
				return err
			}
		}
		defaultPolicy := DefaultManagedReadinessPolicy()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_readiness_sources (
			agent_id, desired_policy_canonical, desired_policy_sha256
		) VALUES (?, ?, ?)`, agent.AgentID, defaultPolicy.Canonical, defaultPolicy.SHA256); err != nil {
			// Only an explicit migration fixture opens the historical schema
			// without 024. Production Open always applies this migration first.
			if !strings.Contains(err.Error(), "no such table: managed_readiness_sources") {
				return err
			}
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

func restartFenceFromAgent(agent *ManagedAgentRecord) ManagedRuntimeFence {
	return ManagedRuntimeFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}
}

func sameManagedRestartFence(left, right ManagedRuntimeFence) bool {
	return left.AgentID == right.AgentID && left.PiSessionID == right.PiSessionID && left.SessionEpoch == right.SessionEpoch && left.ProcessPID == right.ProcessPID && left.ProcessStartMarker == right.ProcessStartMarker && left.ProcessIncarnation == right.ProcessIncarnation && left.RuntimeGeneration == right.RuntimeGeneration && left.ManifestRevision == right.ManifestRevision && left.ManifestHash == right.ManifestHash
}

func managedFenceValues(fence ManagedRuntimeFence) []any {
	return []any{fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash}
}

func managedFenceWhere(prefix string) string {
	return prefix + "agent_id = ? AND " + prefix + "pi_session_id = ? AND " + prefix + "session_epoch = ? AND " + prefix + "process_pid = ? AND " + prefix + "process_start_marker = ? AND " + prefix + "process_incarnation = ? AND " + prefix + "runtime_generation = ? AND " + prefix + "manifest_revision = ? AND " + prefix + "manifest_hash = ?"
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
			old_process_start_marker,old_process_incarnation,old_runtime_generation,old_manifest_revision,old_manifest_hash,target_runtime_generation,
			old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,
			reservation_activity_state,planned_stop,turn_settled_at,stage,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			reservation.Fence.AgentID, operation.OperationID, reservation.Fence.PiSessionID, reservation.Fence.SessionEpoch, reservation.Fence.ProcessPID,
			reservation.Fence.ProcessStartMarker, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration, reservation.Fence.ManifestRevision, reservation.Fence.ManifestHash, reservation.Transition.TargetRuntimeGeneration,
			agent.DesiredState, agent.LifecycleState, agent.ActivityState, boolInt(agent.AcceptingPrompts), boolInt(agent.SlotHeld), agent.ActivityState, 0, nil, "reserved", now, now); err != nil {
			return err
		}
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'restarting', accepting_prompts = 0, updated_at = ?
			WHERE agent_id = ? AND desired_state = 'running' AND lifecycle_state = 'running' AND accepting_prompts = 1 AND slot_held = 1
			AND activity_state IN ('active', 'idle') AND pi_session_id = ? AND session_epoch = ? AND process_pid = ?
			AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?
			AND EXISTS (SELECT 1 FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND old_process_incarnation = ? AND old_runtime_generation = ? AND old_manifest_revision = ? AND old_manifest_hash = ?)`,
			now, reservation.Fence.AgentID, reservation.Fence.PiSessionID, reservation.Fence.SessionEpoch, reservation.Fence.ProcessPID,
			reservation.Fence.ProcessStartMarker, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration, reservation.Fence.ManifestRevision, reservation.Fence.ManifestHash,
			reservation.Fence.AgentID, operation.OperationID, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration, reservation.Fence.ManifestRevision, reservation.Fence.ManifestHash)
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
	var turnSettled sql.NullString
	var targetSession, targetMarker, targetIncarnation, targetHash sql.NullString
	var targetEpoch, targetPID, targetRevision sql.NullInt64
	if err := scanner.Scan(&transition.OperationID, &transition.Fence.AgentID, &transition.Fence.PiSessionID, &transition.Fence.SessionEpoch, &transition.Fence.ProcessPID,
		&transition.Fence.ProcessStartMarker, &transition.Fence.ProcessIncarnation, &transition.Fence.RuntimeGeneration,
		&transition.Fence.ManifestRevision, &transition.Fence.ManifestHash, &transition.TargetRuntimeGeneration,
		&targetSession, &targetEpoch, &targetPID, &targetMarker, &targetIncarnation, &targetRevision, &targetHash,
		&transition.DesiredState, &transition.LifecycleState, &transition.ActivityState, &accepting, &slot, &transition.ReservationActivityState,
		&planned, &turnSettled, &transition.Stage, &transition.CreatedAt, &transition.UpdatedAt); err != nil {
		return nil, err
	}
	transition.AcceptingPrompts = accepting != 0
	transition.SlotHeld = slot != 0
	transition.PlannedStop = planned != 0
	transition.TurnSettledAt = turnSettled.String
	if targetSession.Valid {
		transition.TargetFence = &ManagedRuntimeFence{AgentID: transition.Fence.AgentID, PiSessionID: targetSession.String, SessionEpoch: targetEpoch.Int64, ProcessPID: int(targetPID.Int64), ProcessStartMarker: targetMarker.String, ProcessIncarnation: targetIncarnation.String, RuntimeGeneration: transition.TargetRuntimeGeneration, ManifestRevision: int(targetRevision.Int64), ManifestHash: targetHash.String}
	}
	return &transition, nil
}

const managedRestartTransitionSelect = `SELECT operation_id,agent_id,old_pi_session_id,old_session_epoch,old_process_pid,old_process_start_marker,old_process_incarnation,old_runtime_generation,old_manifest_revision,old_manifest_hash,target_runtime_generation,target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_manifest_revision,target_manifest_hash,old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,reservation_activity_state,planned_stop,turn_settled_at,stage,created_at,updated_at FROM managed_restart_transitions`

// ManagedRestartTransition reads the one bounded transition for manager and
// store tests. It is not a public managed-agent projection.
func (s *Store) ManagedRestartTransition(ctx context.Context, agentID string) (*ManagedRestartTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedRestartTransitionForTx(ctx, s.db, agentID)
}

func managedRestartTransitionForTx(ctx context.Context, db *sql.DB, agentID string) (*ManagedRestartTransition, error) {
	return scanManagedRestartTransition(db.QueryRowContext(ctx, managedRestartTransitionSelect+` WHERE agent_id = ?`, agentID))
}

// LookupManagedRestartAdmission resolves durable replay and conflicts before a
// caller can inspect a live process binding. The reservation transaction repeats
// these checks under the writer lock before it changes the projection.
func (s *Store) LookupManagedRestartAdmission(ctx context.Context, method, requestID, canonicalPath, bodyHash, agentID string) (ManagedRestartAdmission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedRestartAdmission{}, errors.New("store is closed")
	}
	existing, err := managedOperationForWorkerScopeTx(ctx, s.db, method, requestID)
	if err == nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != bodyHash {
			return ManagedRestartAdmission{}, &ManagedIdempotencyConflictError{}
		}
		return ManagedRestartAdmission{Operation: existing}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ManagedRestartAdmission{}, err
	}
	agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, agentID), nil)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedRestartAdmission{}, &ManagedAgentNotFoundError{AgentID: agentID}
	}
	if err != nil {
		return ManagedRestartAdmission{}, err
	}
	transition, err := managedRestartTransitionForTx(ctx, s.db, agentID)
	if err == nil {
		return ManagedRestartAdmission{Agent: agent, Transition: transition}, &ManagedRestartConflictError{AgentID: agentID}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ManagedRestartAdmission{}, err
	}
	return ManagedRestartAdmission{Agent: agent}, nil
}

// ListManagedRestartTransitions is intentionally narrow: ordinary desired
// running rows are not recovery candidates.
func (s *Store) ListManagedRestartTransitions(ctx context.Context) ([]*ManagedRestartTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedRestartTransitionSelect+` ORDER BY agent_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []*ManagedRestartTransition{}
	for rows.Next() {
		transition, scanErr := scanManagedRestartTransition(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, transition)
	}
	return result, rows.Err()
}

func (s *Store) RecoverableManagedRestartTransition(ctx context.Context, agentID string) (*ManagedRestartTransition, error) {
	return s.ManagedRestartTransition(ctx, agentID)
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

// MarkManagedRestartSettling arms the private active-turn drain boundary. It
// never changes public admission or the projection version.
func (s *Store) MarkManagedRestartSettling(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.restartStage(ctx, fence, operationID, "prepared", "settling", func(t *ManagedRestartTransition) bool {
		return t.ReservationActivityState == "active" && t.TurnSettledAt == ""
	})
}

// MarkManagedRestartTurnSettled records only a post-watermark observation for
// the exact old fence. The caller owns pipe identity and rechecks its immutable
// binding immediately before calling this transaction.
func (s *Store) MarkManagedRestartTurnSettled(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "settling")
		if err != nil {
			return err
		}
		if transition.ReservationActivityState != "active" || transition.TurnSettledAt != "" || agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET turn_settled_at = ?, updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = 'settling' AND turn_settled_at IS NULL`, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()), fence.AgentID, operationID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

// MarkManagedRestartStopping durably closes the old input path before the
// executor can touch stdin or send a signal.
func (s *Store) MarkManagedRestartStopping(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "prepared", "settling")
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld || transition.PlannedStop ||
			(transition.ReservationActivityState == "active" && (transition.Stage != "settling" || transition.TurnSettledAt == "")) ||
			(transition.ReservationActivityState == "idle" && transition.Stage != "prepared") {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop = 1, stage = 'stopping', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = ?`, formatTimestamp(s.clock.Now()), fence.AgentID, operationID, transition.Stage)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

// MarkManagedRestartOldStopped is legal only after the executor has proved the
// entire old process group absent. It preserves the held slot and closed
// admission for target creation.
func (s *Store) MarkManagedRestartOldStopped(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		_, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "stopping")
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'stopped', accepting_prompts = 0, updated_at = ?
			WHERE `+managedFenceWhere("")+` AND desired_state = 'running' AND lifecycle_state = 'restarting' AND accepting_prompts = 0 AND slot_held = 1`, append([]any{now}, managedFenceValues(fence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage = 'stopped', updated_at = ? WHERE agent_id = ? AND operation_id = ? AND stage = 'stopping' AND planned_stop = 1`, now, fence.AgentID, operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// MarkManagedRestartStoppingForShutdown closes the old input path before a
// router shutdown reaps its group. An active turn that is being shut down has
// no future target until recovery; the durable stop fence records that fact.
func (s *Store) MarkManagedRestartStoppingForShutdown(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "reserved", "prepared", "settling")
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld || transition.PlannedStop {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop = 1, turn_settled_at = CASE WHEN reservation_activity_state = 'active' THEN ? ELSE NULL END, stage = 'stopping', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = ?`, now, now, fence.AgentID, operationID, transition.Stage)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

// RecoverManagedRestartOldGone turns a retained prepared/settling transition
// into the stopped source for replacement only after an external reaper has
// proved the old group absent. An absent active group is its own drain proof:
// no old continuation remains to cross into the replacement.
func (s *Store) RecoverManagedRestartOldGone(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "prepared", "settling")
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'stopped', accepting_prompts = 0, last_error_code = 'RESTART_OLD_PROCESS_GONE', last_error_message = 'restart old process was already gone', last_error_at = ?, updated_at = ?
			WHERE `+managedFenceWhere("")+` AND desired_state = 'running' AND lifecycle_state = 'restarting' AND accepting_prompts = 0 AND slot_held = 1`, append([]any{now, now}, managedFenceValues(fence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop = 1, turn_settled_at = CASE WHEN reservation_activity_state = 'active' THEN ? ELSE turn_settled_at END, stage = 'stopped', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = ?`, now, now, fence.AgentID, operationID, transition.Stage)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ResetManagedRestartTargetToStopped is called after the target group is
// definitely absent. It restores the immutable old fence and leaves the
// retained operation available for one recovery owner to try another target.
func (s *Store) ResetManagedRestartTargetToStopped(ctx context.Context, targetFence ManagedRuntimeFence, operationID, code, message string) error {
	if code != "RESTART_REPLACEMENT_FAILED" || message != "replacement process did not become ready" {
		return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
	}
	return s.runImmediate(ctx, func() error {
		transition, err := managedRestartTransitionForTx(ctx, s.db, targetFence.AgentID)
		if err != nil || transition.OperationID != operationID || transition.Stage != "starting" || transition.TargetFence == nil || !sameManagedRestartFence(*transition.TargetFence, targetFence) {
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		oldFence := transition.Fence
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id = ?, session_epoch = ?, process_pid = ?, process_start_marker = ?, process_incarnation = ?, runtime_generation = ?, manifest_revision = ?, manifest_hash = ?, lifecycle_state = 'restarting', activity_state = 'stopped', accepting_prompts = 0, last_error_code = ?, last_error_message = ?, last_error_at = ?, updated_at = ?
			WHERE `+managedFenceWhere("")+` AND desired_state = 'running' AND lifecycle_state = 'restarting' AND activity_state = 'starting' AND accepting_prompts = 0 AND slot_held = 1`, append([]any{oldFence.PiSessionID, oldFence.SessionEpoch, oldFence.ProcessPID, oldFence.ProcessStartMarker, oldFence.ProcessIncarnation, oldFence.RuntimeGeneration, oldFence.ManifestRevision, oldFence.ManifestHash, code, message, now, now}, managedFenceValues(targetFence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET target_pi_session_id = NULL, target_session_epoch = NULL, target_process_pid = NULL, target_process_start_marker = NULL, target_process_incarnation = NULL, target_manifest_revision = NULL, target_manifest_hash = NULL, stage = 'stopped', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = 'starting'`, now, targetFence.AgentID, operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, oldFence); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// InstallManagedRestartTarget atomically swaps the durable runtime fence and
// rolls the source confirmations. A caller still owns the newborn process
// until this transaction succeeds.
func (s *Store) InstallManagedRestartTarget(ctx context.Context, oldFence ManagedRestartFence, targetFence ManagedRuntimeFence, operationID string) error {
	if !validManagedRuntimeFence(targetFence) || targetFence.AgentID != oldFence.AgentID || targetFence.RuntimeGeneration == oldFence.RuntimeGeneration || targetFence.ProcessIncarnation == oldFence.ProcessIncarnation {
		return &ManagedRestartStaleError{AgentID: oldFence.AgentID}
	}
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, oldFence, operationID, "stopped")
		if err != nil {
			return err
		}
		if transition.PlannedStop != true || transition.TargetRuntimeGeneration != targetFence.RuntimeGeneration || agent.ActivityState != "stopped" || agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedRestartStaleError{AgentID: oldFence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id = ?, session_epoch = ?, process_pid = ?, process_start_marker = ?, process_incarnation = ?, runtime_generation = ?, manifest_revision = ?, manifest_hash = ?, lifecycle_state = 'restarting', activity_state = 'starting', accepting_prompts = 0, updated_at = ?
			WHERE `+managedFenceWhere("")+` AND lifecycle_state = 'restarting' AND activity_state = 'stopped' AND accepting_prompts = 0 AND slot_held = 1`, append([]any{targetFence.PiSessionID, targetFence.SessionEpoch, targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation, targetFence.RuntimeGeneration, targetFence.ManifestRevision, targetFence.ManifestHash, now}, managedFenceValues(oldFence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: oldFence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET target_pi_session_id = ?, target_session_epoch = ?, target_process_pid = ?, target_process_start_marker = ?, target_process_incarnation = ?, target_manifest_revision = ?, target_manifest_hash = ?, stage = 'starting', updated_at = ?
			WHERE agent_id = ? AND operation_id = ? AND stage = 'stopped'`, targetFence.PiSessionID, targetFence.SessionEpoch, targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation, targetFence.ManifestRevision, targetFence.ManifestHash, now, oldFence.AgentID, operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: oldFence.AgentID}
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, targetFence); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// CompleteManagedRestartReady is the sole admission-opening transaction for a
// replacement runtime. It also terminalizes the retained operation for 24h.
func (s *Store) CompleteManagedRestartReady(ctx context.Context, targetFence ManagedRuntimeFence, operationID string) error {
	if !validManagedRuntimeFence(targetFence) {
		return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
	}
	return s.runImmediate(ctx, func() error {
		transition, err := managedRestartTransitionForTx(ctx, s.db, targetFence.AgentID)
		if err != nil || transition.OperationID != operationID || transition.Stage != "starting" || transition.TargetFence == nil || !sameManagedRestartFence(*transition.TargetFence, targetFence) {
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		if err := s.managedReadinessSourcesConfirmedForFenceTx(ctx, targetFence); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'running', activity_state = 'idle', accepting_prompts = 1, last_error_code = NULL, last_error_message = NULL, last_error_at = NULL, started_at = COALESCE(started_at, ?), updated_at = ?
			WHERE `+managedFenceWhere("")+` AND desired_state = 'running' AND lifecycle_state = 'restarting' AND activity_state = 'starting' AND accepting_prompts = 0 AND slot_held = 1`, append([]any{now, now}, managedFenceValues(targetFence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE agent_id = ? AND operation_id = ? AND stage = 'starting'`, targetFence.AgentID, operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state = 'terminal', terminal_at = ?, expires_at = ? WHERE operation_id = ? AND state = 'pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// RecordManagedRestartFailure accepts only the fixed public-safe outcomes.
// Raw process or RPC errors never reach a durable row.
func (s *Store) RecordManagedRestartFailure(ctx context.Context, fence ManagedRestartFence, operationID, code, message string) error {
	allowed := map[string]string{
		"RESTART_SETTLE_FAILED":         "restart turn did not settle",
		"RESTART_STATE_WRITE_FAILED":    "restart state write failed",
		"RESTART_IDENTITY_UNSAFE":       "restart process identity could not be verified",
		"RESTART_STOP_FAILED":           "restart process stop did not complete",
		"RESTART_OLD_PROCESS_GONE":      "restart old process was already gone",
		"RESTART_REPLACEMENT_FAILED":    "replacement process did not become ready",
		"RESTART_TARGET_REAP_FAILED":    "replacement process cleanup did not complete",
		"RESTART_RECOVERY_NEEDS_SETTLE": "restart requires a live turn settle",
	}
	if allowed[code] != message {
		return &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	return s.runImmediate(ctx, func() error {
		transition, err := managedRestartTransitionForTx(ctx, s.db, fence.AgentID)
		if err != nil || transition.OperationID != operationID {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		match := sameManagedRestartFence(transition.Fence, fence)
		if transition.TargetFence != nil && sameManagedRestartFence(*transition.TargetFence, fence) {
			match = true
		}
		if !match {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET last_error_code = ?, last_error_message = ?, last_error_at = ?, updated_at = ? WHERE agent_id = ? AND desired_state = 'running' AND lifecycle_state = 'restarting' AND accepting_prompts = 0 AND slot_held = 1`, code, message, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()), fence.AgentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

func (s *Store) restartStage(ctx context.Context, fence ManagedRestartFence, operationID, from, to string, extra func(*ManagedRestartTransition) bool) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, from)
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld || !extra(transition) {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage = ?, updated_at = ? WHERE agent_id = ? AND operation_id = ? AND stage = ?`, to, formatTimestamp(s.clock.Now()), fence.AgentID, operationID, from)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return nil
	})
}

func (s *Store) restartOldOwnedTx(ctx context.Context, fence ManagedRestartFence, operationID string, stages ...string) (*ManagedRestartTransition, *ManagedAgentRecord, error) {
	transition, err := managedRestartTransitionForTx(ctx, s.db, fence.AgentID)
	if err != nil || transition.OperationID != operationID || !sameManagedRestartFence(transition.Fence, fence) {
		return nil, nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	validStage := false
	for _, stage := range stages {
		validStage = validStage || transition.Stage == stage
	}
	if !validStage {
		return nil, nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, fence.AgentID), nil)
	if err != nil || !sameManagedRestartFence(restartFenceFromAgent(agent), fence) {
		return nil, nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	return transition, agent, nil
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
			if err != nil {
				return err
			}
			return &ManagedReadinessSourceStaleError{AgentID: agentID}
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, agentID), nil)
		if err != nil {
			return err
		}
		if err := s.rollManagedReadinessSourceFenceTx(ctx, restartFenceFromAgent(agent)); err != nil {
			// Open applies 024 before a production process can start. This only
			// permits the explicit 023-to-024 migration fixture to retain its
			// historical process identity long enough for backfill to run.
			if !strings.Contains(err.Error(), "no such table: managed_readiness_sources") {
				return err
			}
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

type ManagedReadinessSourceStaleError struct{ AgentID string }

func (e *ManagedReadinessSourceStaleError) Error() string {
	return "managed readiness source fence is stale"
}
func (e *ManagedReadinessSourceStaleError) Code() string { return "stale" }

func validManagedRuntimeFence(fence ManagedRuntimeFence) bool {
	return fence.AgentID != "" && fence.PiSessionID != "" && fence.SessionEpoch >= 1 && fence.ProcessPID > 0 && fence.ProcessStartMarker != "" && fence.ProcessIncarnation != "" && fence.RuntimeGeneration != "" && fence.ManifestRevision >= 1 && managedSHA256RE.MatchString(fence.ManifestHash)
}

func (s *Store) LoadManagedDesiredPolicy(ctx context.Context, agentID string) (ManagedDesiredPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedDesiredPolicy{}, errors.New("store is closed")
	}
	var policy ManagedDesiredPolicy
	if err := s.db.QueryRowContext(ctx, `SELECT desired_policy_canonical, desired_policy_sha256 FROM managed_readiness_sources WHERE agent_id = ?`, agentID).Scan(&policy.Canonical, &policy.SHA256); err != nil {
		return ManagedDesiredPolicy{}, err
	}
	policy.Canonical = append([]byte(nil), policy.Canonical...)
	return policy, nil
}

func (s *Store) ManagedReadinessSource(ctx context.Context, agentID string) (*ManagedReadinessSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return scanManagedReadinessSource(s.db.QueryRowContext(ctx, `SELECT agent_id, desired_policy_canonical, desired_policy_sha256,
		pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_hash,
		applied_policy_canonical, applied_policy_sha256, policy_confirmed_at,
		snapshot_id, snapshot_owner_session_id, snapshot_sequence, snapshot_canonical, snapshot_sha256, snapshot_confirmed_at
		FROM managed_readiness_sources WHERE agent_id = ?`, agentID))
}

func scanManagedReadinessSource(scanner interface{ Scan(...any) error }) (*ManagedReadinessSource, error) {
	var source ManagedReadinessSource
	var pi, marker, incarnation, generation, manifestHash sql.NullString
	var epoch, pid, revision sql.NullInt64
	var appliedCanonical []byte
	var appliedHash, policyAt sql.NullString
	var snapshotID, snapshotOwner, snapshotCanonical, snapshotHash, snapshotAt sql.NullString
	var snapshotSequence sql.NullInt64
	if err := scanner.Scan(&source.AgentID, &source.DesiredPolicy.Canonical, &source.DesiredPolicy.SHA256,
		&pi, &epoch, &pid, &marker, &incarnation, &generation, &revision, &manifestHash,
		&appliedCanonical, &appliedHash, &policyAt,
		&snapshotID, &snapshotOwner, &snapshotSequence, &snapshotCanonical, &snapshotHash, &snapshotAt); err != nil {
		return nil, err
	}
	source.DesiredPolicy.Canonical = append([]byte(nil), source.DesiredPolicy.Canonical...)
	if pi.Valid {
		source.Fence = &ManagedRuntimeFence{AgentID: source.AgentID, PiSessionID: pi.String, SessionEpoch: epoch.Int64, ProcessPID: int(pid.Int64), ProcessStartMarker: marker.String, ProcessIncarnation: incarnation.String, RuntimeGeneration: generation.String, ManifestRevision: int(revision.Int64), ManifestHash: manifestHash.String}
	}
	if appliedHash.Valid {
		source.AppliedPolicy = &ManagedDesiredPolicy{Canonical: append([]byte(nil), appliedCanonical...), SHA256: appliedHash.String}
		source.PolicyConfirmedAt = policyAt.String
	}
	if snapshotID.Valid {
		source.Snapshot = &ManagedChildSnapshot{SnapshotID: snapshotID.String, OwnerSessionID: snapshotOwner.String, Sequence: snapshotSequence.Int64, Canonical: []byte(snapshotCanonical.String), SHA256: snapshotHash.String}
		source.SnapshotConfirmedAt = snapshotAt.String
	}
	return &source, nil
}

// RollManagedReadinessSourceFence clears confirmations for a newly installed
// binding while retaining the router's desired policy. Replacement execution
// will call this same helper in its later target-install transaction.
func (s *Store) RollManagedReadinessSourceFence(ctx context.Context, fence ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(fence) {
		return errors.New("managed readiness source fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		return s.rollManagedReadinessSourceFenceTx(ctx, fence)
	})
}
func (s *Store) rollManagedReadinessSourceFenceTx(ctx context.Context, fence ManagedRuntimeFence) error {
	result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET
		pi_session_id = ?, session_epoch = ?, process_pid = ?, process_start_marker = ?, process_incarnation = ?, runtime_generation = ?, manifest_revision = ?, manifest_hash = ?,
		applied_policy_canonical = NULL, applied_policy_sha256 = NULL, policy_confirmed_at = NULL,
		snapshot_id = NULL, snapshot_owner_session_id = NULL, snapshot_sequence = NULL, snapshot_canonical = NULL, snapshot_sha256 = NULL, snapshot_confirmed_at = NULL
		WHERE agent_id = ? AND EXISTS (SELECT 1 FROM managed_agents WHERE agent_id = ?
			AND pi_session_id = ? AND session_epoch = ? AND process_pid = ? AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?)`,
		fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash,
		fence.AgentID, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash)
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
	return nil
}

func readinessSourceFenceWhere() string {
	return `agent_id = ? AND pi_session_id = ? AND session_epoch = ? AND process_pid = ? AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?
		AND EXISTS (SELECT 1 FROM managed_agents WHERE agent_id = ? AND pi_session_id = ? AND session_epoch = ? AND process_pid = ? AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?)`
}
func readinessSourceFenceArgs(fence ManagedRuntimeFence) []any {
	return []any{fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash,
		fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash}
}

func (s *Store) ConfirmManagedPolicyApplied(ctx context.Context, fence ManagedRuntimeFence, policy ManagedDesiredPolicy) error {
	if !validManagedRuntimeFence(fence) || len(policy.Canonical) < 2 || len(policy.Canonical) > 32768 || !managedSHA256RE.MatchString(policy.SHA256) {
		return errors.New("managed policy confirmation is invalid")
	}
	return s.runImmediate(ctx, func() error {
		args := append([]any{policy.Canonical, policy.SHA256, formatTimestamp(s.clock.Now())}, readinessSourceFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET applied_policy_canonical = ?, applied_policy_sha256 = ?, policy_confirmed_at = ? WHERE `+readinessSourceFenceWhere()+`
			AND snapshot_id IS NULL AND snapshot_owner_session_id IS NULL AND snapshot_sequence IS NULL AND snapshot_canonical IS NULL AND snapshot_sha256 IS NULL AND snapshot_confirmed_at IS NULL`, args...)
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
		return nil
	})
}

func (s *Store) ConfirmManagedChildSnapshot(ctx context.Context, fence ManagedRuntimeFence, snapshot ManagedChildSnapshot) error {
	if !validManagedRuntimeFence(fence) || snapshot.SnapshotID == "" || snapshot.OwnerSessionID == "" || !utf8.ValidString(snapshot.SnapshotID) || !utf8.ValidString(snapshot.OwnerSessionID) || len([]byte(snapshot.SnapshotID)) > 8192 || len([]byte(snapshot.OwnerSessionID)) > 8192 || snapshot.Sequence < 0 || len(snapshot.Canonical) < 2 || len(snapshot.Canonical) > 32768 || !managedSHA256RE.MatchString(snapshot.SHA256) {
		return errors.New("managed child snapshot confirmation is invalid")
	}
	return s.runImmediate(ctx, func() error {
		args := append([]any{snapshot.SnapshotID, snapshot.OwnerSessionID, snapshot.Sequence, snapshot.Canonical, snapshot.SHA256, formatTimestamp(s.clock.Now())}, readinessSourceFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET snapshot_id = ?, snapshot_owner_session_id = ?, snapshot_sequence = ?, snapshot_canonical = ?, snapshot_sha256 = ?, snapshot_confirmed_at = ? WHERE `+readinessSourceFenceWhere()+`
			AND applied_policy_canonical IS NOT NULL AND applied_policy_sha256 IS NOT NULL AND policy_confirmed_at IS NOT NULL`, args...)
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
		return nil
	})
}

func (s *Store) managedReadinessSourcesConfirmedForFenceTx(ctx context.Context, fence ManagedRuntimeFence) error {
	var desired, desiredHash, applied, appliedHash []byte
	err := s.db.QueryRowContext(ctx, `SELECT desired_policy_canonical, desired_policy_sha256, applied_policy_canonical, applied_policy_sha256 FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere()+`
		AND applied_policy_canonical IS NOT NULL AND applied_policy_sha256 IS NOT NULL AND policy_confirmed_at IS NOT NULL
		AND snapshot_id IS NOT NULL AND snapshot_owner_session_id IS NOT NULL AND snapshot_sequence IS NOT NULL AND snapshot_canonical IS NOT NULL AND snapshot_sha256 IS NOT NULL AND snapshot_confirmed_at IS NOT NULL`, readinessSourceFenceArgs(fence)...).Scan(&desired, &desiredHash, &applied, &appliedHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
		}
		return err
	}
	if !bytes.Equal(desired, applied) || string(desiredHash) != string(appliedHash) {
		return &ManagedReadinessSourceStaleError{AgentID: fence.AgentID}
	}
	return nil
}

// MarkManagedReadyFromSources performs source confirmation and the public ready
// transition in one transaction. Confirmations never add a projection bump;
// this existing readiness transition remains the only one.
func (s *Store) MarkManagedReadyFromSources(ctx context.Context, fence ManagedRuntimeFence) error {
	if !validManagedRuntimeFence(fence) {
		return errors.New("managed readiness source fence is invalid")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.managedReadinessSourcesConfirmedForFenceTx(ctx, fence); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state = 'running', activity_state = 'idle', accepting_prompts = 1, started_at = COALESCE(started_at, ?), updated_at = ?
			WHERE agent_id = ? AND lifecycle_state = 'starting' AND pi_session_id = ? AND session_epoch = ? AND process_pid = ? AND process_start_marker = ? AND process_incarnation = ? AND runtime_generation = ? AND manifest_revision = ? AND manifest_hash = ?`, now, now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash)
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
		if _, err := s.db.ExecContext(ctx, `UPDATE recipient_sources SET state = 'active', updated_at = ?
			WHERE source_kind = 'managed' AND owner_identity = ? AND state = 'reserved'`, now, fence.AgentID); err != nil {
			return err
		}
		if err := s.reconcileMessageTargetsTx(ctx); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
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

// ReserveTmuxRecipientSource returns a durable source identity. A reconnect
// reads and keeps the existing owner instead of using its WebSocket instance.
func (s *Store) ReserveTmuxRecipientSource(ctx context.Context, recipient, proposedOwner string) (string, error) {
	if !managedRecipientRE.MatchString(recipient) || proposedOwner == "" {
		return "", errors.New("recipient source identity is invalid")
	}
	owner := ""
	err := s.runImmediate(ctx, func() error {
		existing, err := recipientSourceForTx(ctx, s.db, recipient)
		if errors.Is(err, sql.ErrNoRows) {
			if err := s.reserveRecipientSourceTx(ctx, recipient, "tmux", proposedOwner); err != nil {
				return err
			}
			owner = proposedOwner
			return nil
		}
		if err != nil {
			return err
		}
		if existing.SourceKind != "tmux" || existing.State == "conflicted" || existing.State == "retired" {
			return &RecipientSourceConflictError{Recipient: recipient}
		}
		owner = existing.OwnerIdentity
		return nil
	})
	return owner, err
}

func (s *Store) ActivateTmuxRecipientSource(ctx context.Context, recipient, owner string) error {
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `UPDATE recipient_sources SET state = 'active', updated_at = ?
			WHERE recipient_name = ? AND source_kind = 'tmux' AND owner_identity = ? AND state IN ('reserved','active')`, formatTimestamp(s.clock.Now()), recipient, owner)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &RecipientSourceConflictError{Recipient: recipient}
		}
		return nil
	})
}

// RetireManagedRecipientSource removes a managed contender explicitly. It is
// separate from ordinary stop/restart, which deliberately retain a managed
// source so queued snapshots have a stable owner during recovery.
func (s *Store) RetireManagedRecipientSource(ctx context.Context, recipient, owner string) error {
	return s.retireRecipientSource(ctx, recipient, "managed", owner)
}

func (s *Store) RetireTmuxRecipientSource(ctx context.Context, recipient, owner string) error {
	return s.retireRecipientSource(ctx, recipient, "tmux", owner)
}

func (s *Store) retireRecipientSource(ctx context.Context, recipient, kind, owner string) error {
	if recipient == "" || owner == "" || (kind != "managed" && kind != "tmux") {
		return errors.New("recipient source identity is invalid")
	}
	return s.runImmediate(ctx, func() error {
		source, err := recipientSourceForTx(ctx, s.db, recipient)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if source.SourceKind == kind && source.OwnerIdentity == owner {
			if err := s.terminalizeRetiredRecipientSnapshotsTx(ctx, source.SourceKind, source.OwnerIdentity, now); err != nil {
				return err
			}
			if source.State == "conflicted" {
				// The recorded secondary becomes the active primary. Existing
				// snapshots keep their original owner and the retired primary's
				// rows were terminalized above; no snapshot is retargeted.
				_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
					SET source_kind=?,owner_identity=?,state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
					WHERE recipient_name=?`, source.ConflictKind, source.ConflictOwner, now, recipient)
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
				SET state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
				WHERE recipient_name=? AND source_kind=? AND owner_identity=?`, now, recipient, kind, owner)
			return err
		}
		if source.State == "conflicted" && source.ConflictKind == kind && source.ConflictOwner == owner {
			if err := s.terminalizeRetiredRecipientSnapshotsTx(ctx, kind, owner, now); err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
				SET state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
				WHERE recipient_name=?`, now, recipient)
			return err
		}
		return &RecipientSourceConflictError{Recipient: recipient}
	})
}

func (s *Store) terminalizeRetiredRecipientSnapshotsTx(ctx context.Context, kind, owner, now string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT m.id FROM messages m
		JOIN message_target_snapshots target ON target.message_id=m.id
		WHERE m.state='queued' AND target.source_kind=? AND target.owner_identity=?`, kind, owner)
	if err != nil {
		return err
	}
	var messageIDs []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			_ = rows.Close()
			return err
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, messageID := range messageIDs {
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_direct_delivery_attempts
			SET state='failed',released_at=?,next_attempt_at=NULL,failure_code='retired_target',failure_detail=NULL
			WHERE message_id=? AND state IN ('direct_pending','direct_prompted','released')`, now, messageID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE messages SET state='dead_letter' WHERE id=? AND state='queued'`, messageID); err != nil {
			return err
		}
		if err := s.rearmFailedReminder(ctx, messageID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RecipientSource(ctx context.Context, recipient string) (*RecipientSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return recipientSourceForTx(ctx, s.db, recipient)
}

func recipientSourceForTx(ctx context.Context, db *sql.DB, recipient string) (*RecipientSource, error) {
	result := &RecipientSource{}
	var conflictKind, conflictOwner sql.NullString
	err := db.QueryRowContext(ctx, `SELECT recipient_name,source_kind,owner_identity,state,conflict_source_kind,conflict_owner_identity,created_at,updated_at
		FROM recipient_sources WHERE recipient_name = ?`, recipient).Scan(&result.RecipientName, &result.SourceKind, &result.OwnerIdentity, &result.State, &conflictKind, &conflictOwner, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, err
	}
	result.ConflictKind, result.ConflictOwner = conflictKind.String, conflictOwner.String
	return result, nil
}

func (s *Store) reserveRecipientSourceTx(ctx context.Context, recipient, kind, owner string) error {
	if recipient == "" || owner == "" || (kind != "managed" && kind != "tmux") {
		return errors.New("recipient source identity is invalid")
	}
	existing, err := recipientSourceForTx(ctx, s.db, recipient)
	if errors.Is(err, sql.ErrNoRows) {
		now := formatTimestamp(s.clock.Now())
		_, err = s.db.ExecContext(ctx, `INSERT INTO recipient_sources (recipient_name,source_kind,owner_identity,state,created_at,updated_at)
			VALUES (?, ?, ?, 'reserved', ?, ?)`, recipient, kind, owner, now, now)
		return err
	}
	if err != nil {
		return err
	}
	if existing.SourceKind == kind && existing.OwnerIdentity == owner && existing.State != "retired" && existing.State != "conflicted" {
		return nil
	}
	return &RecipientSourceConflictError{Recipient: recipient}
}

// ReconcileManagedRecipientSources is intentionally idempotent. It hydrates
// records left by pre-026 launches without guessing a tmux owner.
func (s *Store) ReconcileManagedRecipientSources(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT name,agent_id,lifecycle_state FROM managed_agents ORDER BY name ASC`)
		if err != nil {
			return err
		}
		type candidate struct{ name, id, lifecycle string }
		var candidates []candidate
		for rows.Next() {
			var value candidate
			if err := rows.Scan(&value.name, &value.id, &value.lifecycle); err != nil {
				_ = rows.Close()
				return err
			}
			candidates = append(candidates, value)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, value := range candidates {
			existing, lookupErr := recipientSourceForTx(ctx, s.db, value.name)
			if errors.Is(lookupErr, sql.ErrNoRows) {
				now := formatTimestamp(s.clock.Now())
				state := "active"
				if value.lifecycle != "running" {
					state = "reserved"
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO recipient_sources (recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES (?,?,?,?,?,?)`, value.name, "managed", value.id, state, now, now); err != nil {
					return err
				}
				continue
			}
			if lookupErr != nil {
				return lookupErr
			}
			if existing.SourceKind != "managed" || existing.OwnerIdentity != value.id {
				if _, err := s.db.ExecContext(ctx, `UPDATE recipient_sources SET state = 'conflicted', conflict_source_kind = ?, conflict_owner_identity = ?, updated_at = ? WHERE recipient_name = ?`, "managed", value.id, formatTimestamp(s.clock.Now()), value.name); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

var _ = fmt.Sprintf

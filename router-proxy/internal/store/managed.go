package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var managedIdentifierRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var managedRecipientRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var managedSHA256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var managedCanonicalUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

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
	// LaunchOperationID is a private 027 reverse-protection link. It is never
	// exposed by the managed HTTP projection.
	LaunchOperationID string
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

// ManagedChildDelta is the prescribed post-readiness mutation contract. The
// store merges its safe, normalized fields into the current ready snapshot in
// the same transaction that advances the committed projection version.
type ManagedChildDelta struct {
	OwnerSessionID, TaskID, RunID, ParentEntryID string
	Sequence                                     int64
	Changes                                      map[string]any
}

// ManagedChildRecovery preserves a last known active source row when a parent
// runtime is replaced before that row can be confirmed. The full runtime fence
// stays private; the transport hashes it before rendering the bounded recovery
// object to the browser.
type ManagedChildRecovery struct {
	PriorSessionID, PriorProcessIncarnation, PriorRuntimeGeneration string
	PriorSessionEpoch                                               int64
	ParentEntryID, TaskID, RunID                                    string
	Sequence                                                        int64
	RunCanonical                                                    []byte
	Cause                                                           string
	ObservedAt                                                      string
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
	Recoveries          []ManagedChildRecovery
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

// ManagedAgentsSnapshot is an atomic whole-projection read for UI-live and
// direct list clients. It intentionally exposes only durable records; callers
// turn them into the redacted public projection at the transport boundary.
type ManagedAgentsSnapshot struct {
	GeneratedAt       time.Time
	ProjectionVersion int64
	Agents            []*ManagedAgentRecord
	// ReadinessSources are read in the same transaction as Agents so child
	// telemetry can never mix a newer runtime snapshot with an older agent row.
	ReadinessSources map[string]*ManagedReadinessSource
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

// ManagedLifecycleAction is the private durable result of lifecycle admission.
// The HTTP response remains the immutable managed operation.
type ManagedLifecycleAction struct {
	ActionID                       string
	OperationID                    string
	AgentID                        string
	Kind                           string
	Source                         string
	ParentActionID                 string
	RetryOf                        string
	ExecutionOwner                 string
	State                          string
	Stage                          string
	SourceFence                    *ManagedRuntimeFence
	TargetFence                    *ManagedRuntimeFence
	PlannedTargetRuntimeGeneration string
	TargetArtifactID               string
	TerminalCode                   string
	TerminalMessage                string
	Retryable                      bool
	CompletedAt                    string
	RecoveryOwner                  string
	RecoveryClaimedAt              string
	CreatedAt                      string
	UpdatedAt                      string
}

type ManagedLifecycleReservation struct {
	AgentID                        string
	Kind                           string
	Source                         string
	Method                         string
	CanonicalPath                  string
	RequestID                      string
	CanonicalBodyHash              string
	Fence                          ManagedRuntimeFence
	PlannedTargetRuntimeGeneration string
	TargetFence                    *ManagedRuntimeFence
	TargetArtifactID               string
	ParentActionID                 string
	RetryOf                        string
	Operation                      ManagedOperation
}

type ManagedLifecycleAdmission struct {
	Operation *ManagedOperation
	Action    *ManagedLifecycleAction
	Agent     *ManagedAgentRecord
	Replayed  bool
}

type ManagedLifecycleConflictError struct{ AgentID string }

func (e *ManagedLifecycleConflictError) Error() string {
	return "managed lifecycle operation conflicts with another action"
}
func (e *ManagedLifecycleConflictError) Code() string { return "conflict" }

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
		// Post-027 rows retain this pending operation as a reverse protection
		// edge. Inserting it first lets the parent link be FK-valid in the same
		// transaction; historical migration fixtures still use the old shape.
		if err := insertManagedOperation(ctx, s.db, operation); err != nil {
			return err
		}
		if err := s.insertManagedLaunchAgentTx(ctx, agent, operation.OperationID); err != nil {
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
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// insertManagedLaunchAgentTx keeps the 027 launch link private while allowing
// focused historical migration fixtures to open schemas that intentionally end
// before 027.
func (s *Store) insertManagedLaunchAgentTx(ctx context.Context, agent ManagedAgentRecord, operationID string) error {
	columns := `agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
		desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
		session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
		manifest_revision,manifest_hash,pending_initial_instruction,initial_instruction_sent,
		last_error_code,last_error_message,last_error_at,created_at,started_at,updated_at`
	values := []any{
		agent.AgentID, agent.Name, boolInt(true), agent.Cwd,
		agent.SelectedModel.Provider, agent.SelectedModel.ID, agent.DesiredState,
		agent.LifecycleState, agent.ActivityState, boolInt(agent.AcceptingPrompts), nullIfEmpty(agent.PiSessionID),
		agent.SessionEpoch, nullIfZero(agent.ProcessPID), nullIfEmpty(agent.ProcessStartMarker), nullIfEmpty(agent.ProcessIncarnation), nullIfEmpty(agent.RuntimeGeneration),
		agent.ManifestRevision, agent.ManifestHash, nullIfEmpty(agent.PendingInitialInstruction), boolInt(agent.InitialInstructionSent),
		nullIfEmpty(agent.LastErrorCode), nullIfEmpty(agent.LastErrorMessage), nullIfEmpty(agent.LastErrorAt), agent.CreatedAt, nullIfEmpty(agent.StartedAt), agent.UpdatedAt,
	}
	hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
	if err != nil {
		return err
	}
	if hasLink {
		columns += `,launch_operation_id`
		values = append(values, operationID)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(values)), ",")
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_agents (`+columns+`) VALUES (`+placeholders+`)`, values...)
	return err
}

func managedLaunchOperationColumnTx(ctx context.Context, db *sql.DB) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(managed_agents)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primary int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primary); err != nil {
			return false, err
		}
		if name == "launch_operation_id" {
			return true, nil
		}
	}
	return false, rows.Err()
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

const managedLifecycleActionSelect = `SELECT action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,planned_target_runtime_generation,target_artifact_id,terminal_code,terminal_message,retryable,completed_at,recovery_owner,recovery_claimed_at,created_at,updated_at FROM managed_lifecycle_actions`

func scanManagedLifecycleAction(scanner interface{ Scan(...any) error }) (*ManagedLifecycleAction, error) {
	var action ManagedLifecycleAction
	var agentID, parent, retry, sourceSession, sourceMarker, sourceIncarnation, sourceGeneration, sourceHash, targetSession, targetMarker, targetIncarnation, targetGeneration, targetHash, planned, artifact, code, message, completed, recoveryOwner, recoveryClaimedAt sql.NullString
	var sourceEpoch, sourcePID, sourceRevision, targetEpoch, targetPID, targetRevision sql.NullInt64
	var retryable sql.NullInt64
	if err := scanner.Scan(&action.ActionID, &action.OperationID, &agentID, &action.Kind, &action.Source, &parent, &retry, &action.ExecutionOwner, &action.State, &action.Stage,
		&sourceSession, &sourceEpoch, &sourcePID, &sourceMarker, &sourceIncarnation, &sourceGeneration, &sourceRevision, &sourceHash,
		&targetSession, &targetEpoch, &targetPID, &targetMarker, &targetIncarnation, &targetGeneration, &targetRevision, &targetHash,
		&planned, &artifact, &code, &message, &retryable, &completed, &recoveryOwner, &recoveryClaimedAt, &action.CreatedAt, &action.UpdatedAt); err != nil {
		return nil, err
	}
	action.AgentID, action.ParentActionID, action.RetryOf = agentID.String, parent.String, retry.String
	action.PlannedTargetRuntimeGeneration, action.TargetArtifactID, action.TerminalCode, action.TerminalMessage, action.CompletedAt, action.RecoveryOwner, action.RecoveryClaimedAt = planned.String, artifact.String, code.String, message.String, completed.String, recoveryOwner.String, recoveryClaimedAt.String
	action.Retryable = retryable.Int64 != 0
	if sourceSession.Valid {
		action.SourceFence = &ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: sourceSession.String, SessionEpoch: sourceEpoch.Int64, ProcessPID: int(sourcePID.Int64), ProcessStartMarker: sourceMarker.String, ProcessIncarnation: sourceIncarnation.String, RuntimeGeneration: sourceGeneration.String, ManifestRevision: int(sourceRevision.Int64), ManifestHash: sourceHash.String}
	}
	if targetSession.Valid {
		action.TargetFence = &ManagedRuntimeFence{AgentID: action.AgentID, PiSessionID: targetSession.String, SessionEpoch: targetEpoch.Int64, ProcessPID: int(targetPID.Int64), ProcessStartMarker: targetMarker.String, ProcessIncarnation: targetIncarnation.String, RuntimeGeneration: targetGeneration.String, ManifestRevision: int(targetRevision.Int64), ManifestHash: targetHash.String}
	}
	return &action, nil
}

func managedLifecycleActionForTx(ctx context.Context, db *sql.DB, actionID string) (*ManagedLifecycleAction, error) {
	return scanManagedLifecycleAction(db.QueryRowContext(ctx, managedLifecycleActionSelect+` WHERE action_id=?`, actionID))
}

// ReserveManagedLifecycle resolves exact replay before inspecting an agent or
// its binding. The caller supplies a complete live fence only after that lookup.
func (s *Store) ReserveManagedLifecycle(ctx context.Context, reservation ManagedLifecycleReservation) (ManagedLifecycleAdmission, error) {
	if reservation.Source == "" {
		reservation.Source = "direct"
	}
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
	// Request identity is the only admission check before replay lookup. The
	// complete lifecycle shape is validated only for a new reservation below.
	if !managedCanonicalUUIDRE.MatchString(reservation.RequestID) || reservation.Operation.OperationID != reservation.RequestID || reservation.Method == "" || reservation.CanonicalPath == "" || !managedSHA256RE.MatchString(reservation.CanonicalBodyHash) {
		return ManagedLifecycleAdmission{}, errors.New("managed lifecycle reservation is invalid")
	}
	var result ManagedLifecycleAdmission
	err := s.runImmediate(ctx, func() error {
		existing, err := managedOperationForWorkerScopeTx(ctx, s.db, reservation.Method, reservation.RequestID)
		if err == nil {
			if existing.CanonicalPath != reservation.CanonicalPath || existing.CanonicalBodyHash != reservation.CanonicalBodyHash {
				return &ManagedIdempotencyConflictError{}
			}
			result.Operation, result.Replayed = existing, true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		state, stage := "admitted", "admission"
		if reservation.Kind == "terminate" {
			state, stage = "terminating", "abort"
			if reservation.RetryOf != "" {
				predecessor, predecessorErr := managedLifecycleActionForTx(ctx, s.db, reservation.RetryOf)
				if predecessorErr != nil {
					return predecessorErr
				}
				// A retry resumes the exact safe cleanup boundary. It must not send a
				// second Abort after a file/direct-mail/durable-write failure.
				switch predecessor.Stage {
				case "abort", "terminate_reap":
					state = "terminating"
					stage = predecessor.Stage
				case "direct_mail_retire", "agent_cleanup", "durable_write":
					state = "cleanup"
					stage = predecessor.Stage
				default:
					return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
				}
			}
		}
		var sourceFence *ManagedRuntimeFence
		if validManagedRuntimeFence(reservation.Fence) {
			sourceFence = &reservation.Fence
		}
		input := ManagedLifecycleActionInput{
			ActionID: reservation.Operation.OperationID, AgentID: reservation.AgentID,
			Kind: reservation.Kind, Source: reservation.Source, ParentActionID: reservation.ParentActionID,
			RetryOf: reservation.RetryOf, ExecutionOwner: "lifecycle", State: state, Stage: stage,
			SourceFence: sourceFence, TargetFence: reservation.TargetFence, PlannedTargetRuntimeGeneration: reservation.PlannedTargetRuntimeGeneration,
			TargetArtifactID: reservation.TargetArtifactID, CreatedAt: reservation.Operation.CreatedAt, UpdatedAt: reservation.Operation.CreatedAt, Operation: reservation.Operation,
		}
		if err := validateLifecycleActionInput(input); err != nil {
			return err
		}
		if input.RetryOf != "" {
			if err := validateManagedLifecycleRetryPredecessorTx(ctx, s.db, input); err != nil {
				return err
			}
		}
		agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id=?`, reservation.AgentID), nil)
		if errors.Is(err, sql.ErrNoRows) {
			return &ManagedAgentNotFoundError{AgentID: reservation.AgentID}
		}
		if err != nil {
			return err
		}
		if reservation.Kind != "terminate" && !sameManagedRestartFence(restartFenceFromAgent(agent), reservation.Fence) {
			return &ManagedAgentNotReadyError{AgentID: reservation.AgentID}
		}
		// A completed termination removes the agent row. A retained failed
		// tombstone may only be resumed through its recorded retry lineage.
		if reservation.Kind == "terminate" && agent.DesiredState == "terminated" && reservation.RetryOf == "" {
			return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
		}

		var activeID, activeKind string
		err = s.db.QueryRowContext(ctx, `SELECT action_id,kind FROM managed_lifecycle_actions WHERE agent_id=? AND state IN ('admitted','awaiting_settle','stopping_source','starting_target','awaiting_target_ready','old_artifact_cleanup','terminating','cleanup')`, reservation.AgentID).Scan(&activeID, &activeKind)
		if err == nil {
			active, activeErr := managedLifecycleActionForTx(ctx, s.db, activeID)
			if activeErr != nil {
				return activeErr
			}
			if reservation.Kind == "terminate" && (activeKind == "restart" || activeKind == "reset") {
				terminalState, code, message := "superseded", "SUPERSEDED_BY_HIGHER_ACTION", "lifecycle action superseded by a higher priority action"
				if activeKind == "reset" && active.State == "admitted" && active.Stage == "admission" {
					terminalState, code, message = "cancelled", "CANCELLED_BY_HIGHER_ACTION", "lifecycle action cancelled before external work"
				}
				if err := s.terminalizeLifecycleActionTx(ctx, active, terminalState, code, message); err != nil {
					return err
				}
				// A legacy bridge cannot remain live after its linked operation becomes
				// terminal; this also makes late 025 callbacks durable no-ops.
				if activeKind == "restart" {
					if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE operation_id=?`, active.OperationID); err != nil {
						return err
					}
				}
			} else if reservation.Kind == "reset" && activeKind == "restart" {
				if err := s.terminalizeLifecycleActionTx(ctx, active, "superseded", "SUPERSEDED_BY_HIGHER_ACTION", "lifecycle action superseded by a higher priority action"); err != nil {
					return err
				}
				if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE operation_id=?`, active.OperationID); err != nil {
					return err
				}
			} else {
				return &ManagedLifecycleConflictError{AgentID: reservation.AgentID}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := insertManagedOperation(ctx, s.db, reservation.Operation); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{input.ActionID, input.Operation.OperationID, input.AgentID, input.Kind, input.Source, nullIfEmpty(input.ParentActionID), nullIfEmpty(input.RetryOf), input.ExecutionOwner, input.State, input.Stage}
		args = append(args, lifecycleFenceValues(input.SourceFence)...)
		args = append(args, lifecycleFenceValues(input.TargetFence)...)
		args = append(args, nullIfEmpty(input.PlannedTargetRuntimeGeneration), nullIfEmpty(input.TargetArtifactID), input.CreatedAt, input.UpdatedAt)
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
			action_id,operation_id,agent_id,kind,source,parent_action_id,retry_of,execution_owner,state,stage,
			source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
			target_pi_session_id,target_session_epoch,target_process_pid,target_process_start_marker,target_process_incarnation,target_runtime_generation,target_manifest_revision,target_manifest_hash,
			planned_target_runtime_generation,target_artifact_id,created_at,updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
		if err != nil {
			return err
		}
		if reservation.Kind == "terminate" {
			// This transaction is the cancellation fence. Root-control workers
			// inspect their durable rows before doing any external append, so they
			// cannot publish a result after termination has been admitted.
			if err = s.cancelManagedControlRequestsTx(ctx, reservation.AgentID, "CANCELLED_BY_TERMINATE", now); err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated',lifecycle_state='terminating',accepting_prompts=0,updated_at=? WHERE agent_id=?`, now, reservation.AgentID)
			if err == nil {
				hasLink, linkErr := managedLaunchOperationColumnTx(ctx, s.db)
				if linkErr != nil {
					return linkErr
				}
				if hasLink {
					var launch sql.NullString
					if linkErr = s.db.QueryRowContext(ctx, `SELECT launch_operation_id FROM managed_agents WHERE agent_id=?`, reservation.AgentID).Scan(&launch); linkErr != nil {
						return linkErr
					}
					if launch.Valid {
						_, linkErr = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code='LAUNCH_CANCELLED_BY_TERMINATE',terminal_message='managed launch cancelled by termination' WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), launch.String)
						if linkErr != nil {
							return linkErr
						}
					}
				}
			}
		} else {
			lifecycle := map[string]string{"restart": "restarting", "reset": "resetting"}[reservation.Kind]
			args := []any{lifecycle, now, reservation.AgentID}
			where := `agent_id=?`
			if validManagedRuntimeFence(reservation.Fence) {
				where += ` AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?`
				args = append(args, managedFenceValues(reservation.Fence)[1:]...)
			}
			resultUpdate, updateErr := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state=?,accepting_prompts=0,updated_at=? WHERE `+where, args...)
			if updateErr != nil {
				return updateErr
			}
			changed, updateErr := resultUpdate.RowsAffected()
			if updateErr != nil {
				return updateErr
			}
			if changed != 1 {
				return &ManagedAgentNotReadyError{AgentID: reservation.AgentID}
			}
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		action, err := managedLifecycleActionForTx(ctx, s.db, reservation.Operation.OperationID)
		if err != nil {
			return err
		}
		result.Operation, result.Action, result.Agent = &reservation.Operation, action, agent
		return nil
	})
	return result, err
}

func (s *Store) ManagedLifecycleAction(ctx context.Context, actionID string) (*ManagedLifecycleAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedLifecycleActionForTx(ctx, s.db, actionID)
}

// CompleteManagedTermination is the final all-or-nothing cleanup transaction.
// The executor reaches it only after it has retired direct-mail attempts and
// unlinked every owned session artifact; ordinary mailbox history is purposely
// not touched by this managed-runtime cleanup.
func (s *Store) CompleteManagedTermination(ctx context.Context, actionID string) error {
	return s.runImmediate(ctx, func() error {
		action, err := managedLifecycleActionForTx(ctx, s.db, actionID)
		if err != nil {
			return err
		}
		if action.Kind != "terminate" || action.State != "cleanup" || action.Stage != "durable_write" || action.AgentID == "" {
			return &ManagedRestartStaleError{AgentID: action.AgentID}
		}
		if err := s.completeManagedTerminationFault("before_artifact_check"); err != nil {
			return err
		}
		var pending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_session_artifacts WHERE agent_id=? AND cleanup_state<>'unlinked'`, action.AgentID).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("managed termination has session artifacts that were not safely unlinked")
		}
		if err := s.completeManagedTerminationFault("before_lifecycle_cleanup"); err != nil {
			return err
		}
		// Batch references point across agent actions, so clear them before
		// deleting their child operations. The batch parent response survives.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_lifecycle_batch_items WHERE child_action_id IN (SELECT action_id FROM managed_lifecycle_actions WHERE agent_id=?) OR child_operation_id IN (SELECT operation_id FROM managed_lifecycle_actions WHERE agent_id=?)`, action.AgentID, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_readiness_sources WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_control_requests WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM goal_control_revision_history WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM goal_control_desired_state WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_events WHERE parent_agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_diagnostics WHERE parent_agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_state WHERE parent_agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM subagent_lifecycle_producers WHERE parent_agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if err := s.completeManagedTerminationFault("before_operation_cleanup"); err != nil {
			return err
		}
		// Break retry links first: they use RESTRICT so a failed predecessor can
		// never leave the final cleanup half-applied.
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET retry_of=NULL WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id IN (SELECT operation_id FROM managed_lifecycle_actions WHERE agent_id=? AND action_id<>?)`, action.AgentID, actionID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_session_artifacts WHERE agent_id=?`, action.AgentID); err != nil {
			return err
		}
		if err := s.completeManagedTerminationFault("before_agent_delete"); err != nil {
			return err
		}
		hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
		if err != nil {
			return err
		}
		if hasLink {
			if _, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET launch_operation_id=NULL WHERE agent_id=?`, action.AgentID); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id=? AND desired_state='terminated'`, action.AgentID); err != nil {
			return err
		}
		if err := s.completeManagedTerminationFault("before_terminalize"); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state='succeeded',stage='complete',terminal_code='TERMINATED',terminal_message='managed agent terminated',retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND state='cleanup' AND stage='durable_write'`, now, now, actionID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code='TERMINATED',terminal_message='managed agent terminated' WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), actionID); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func (s *Store) completeManagedTerminationFault(stage string) error {
	if s.managedTerminationFault == nil {
		return nil
	}
	return s.managedTerminationFault(stage)
}

// ManagedLaunchStillPending is the pre-start fence for an asynchronous launch
// callback. A terminate reservation closes the linked operation in the same
// transaction as its tombstone, so this check prevents a callback that lost
// that race from starting a new external process.
func (s *Store) ManagedLaunchStillPending(ctx context.Context, agentID, operationID string) (bool, error) {
	if !isLifecycleText(agentID) || operationID == "" {
		return false, errors.New("managed launch pending check is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("store is closed")
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents a JOIN managed_http_operations o ON o.operation_id=a.launch_operation_id WHERE a.agent_id=? AND a.desired_state='running' AND a.lifecycle_state='starting' AND a.launch_operation_id=? AND o.state='pending'`, agentID, operationID).Scan(&count)
	return count == 1, err
}

// PendingManagedLaunches returns only post-027 launch links. It never treats a
// desired-running row as recovery work.
func (s *Store) PendingManagedLaunches(ctx context.Context) ([]*ManagedAgentRecord, error) {
	agents, err := s.ManagedAgents(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]*ManagedAgentRecord, 0, len(agents))
	for _, agent := range agents {
		if agent.LifecycleState != "starting" || agent.LaunchOperationID == "" {
			continue
		}
		op, err := s.ManagedOperation(ctx, agent.LaunchOperationID)
		if err == nil && op.State == "pending" {
			result = append(result, agent)
		}
	}
	return result, nil
}

func (s *Store) TerminalizeRecoveredManagedLaunch(ctx context.Context, agentID, operationID, code, message string) error {
	if (code != "LAUNCH_RECOVERY_CANCELLED" && code != "LAUNCH_RECOVERY_IDENTITY_UNSAFE") || message == "" {
		return errors.New("managed launch recovery outcome is invalid")
	}
	return s.runImmediate(ctx, func() error {
		// The reverse launch link makes this exact ownership check durable. A
		// stale callback cannot terminalize a replacement agent or operation.
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE agent_id=? AND lifecycle_state='starting' AND launch_operation_id=?`, code, message, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()), agentID, operationID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: agentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, message, operationID)
		if err != nil {
			return err
		}
		changed, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedRestartStaleError{AgentID: agentID}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

func managedLifecycleActionsTableTx(ctx context.Context, db *sql.DB) (bool, error) {
	var present int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_actions'`).Scan(&present); err != nil {
		return false, err
	}
	return present == 1, nil
}

func legacyLifecycleStateStage(transitionStage string) (string, string, bool) {
	switch transitionStage {
	case "reserved", "prepared":
		return "admitted", "admission", true
	case "settling":
		return "awaiting_settle", "source_settle", true
	case "stopping":
		return "stopping_source", "source_stop", true
	case "stopped":
		return "starting_target", "target_start", true
	case "starting":
		return "awaiting_target_ready", "target_ready", true
	default:
		return "", "", false
	}
}

// legacyLifecycleBridgeActionTx binds an existing 025 transition to its 027
// backfill row. It is deliberately optional before migration 027 so historical
// migration fixtures still exercise the original 025 schema.
func legacyLifecycleBridgeActionTx(ctx context.Context, db *sql.DB, transition *ManagedRestartTransition, fence ManagedRuntimeFence) (*ManagedLifecycleAction, error) {
	present, err := managedLifecycleActionsTableTx(ctx, db)
	if err != nil || !present {
		return nil, err
	}
	if transition == nil || transition.OperationID == "" {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	state, stage, ok := legacyLifecycleStateStage(transition.Stage)
	if !ok {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	action, err := managedLifecycleActionForTx(ctx, db, transition.OperationID)
	if err != nil {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	if action.OperationID != transition.OperationID || action.AgentID != fence.AgentID || action.ExecutionOwner != "legacy025" || action.State != state || action.Stage != stage {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	expected := transition.Fence
	if transition.Stage == "starting" {
		if transition.TargetFence == nil {
			return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		expected = *transition.TargetFence
	}
	if !sameManagedRestartFence(expected, fence) {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	actionFence := action.SourceFence
	if transition.Stage == "starting" {
		actionFence = action.TargetFence
	}
	if !lifecycleFenceMatches(actionFence, fence) {
		return nil, &ManagedRestartStaleError{AgentID: fence.AgentID}
	}
	return action, nil
}

func updateLegacyLifecycleBridgeTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction, nextState, nextStage string, target *ManagedRuntimeFence, replaceTarget bool, updatedAt string) error {
	if action == nil {
		return nil
	}
	if !lifecycleStateStageValid(nextState, nextStage) {
		return errors.New("managed legacy lifecycle bridge state is invalid")
	}
	prefix := "source_"
	expected := action.SourceFence
	if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
		prefix, expected = "target_", action.TargetFence
	}
	if expected == nil {
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	set := `state=?,stage=?,updated_at=?`
	args := []any{nextState, nextStage, updatedAt}
	if replaceTarget {
		set += `,target_pi_session_id=?,target_session_epoch=?,target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,target_runtime_generation=?,target_manifest_revision=?,target_manifest_hash=?`
		args = append(args, lifecycleFenceValues(target)...)
	}
	args = append(args, action.ActionID, action.OperationID, action.State, action.Stage)
	args = append(args, managedFenceValues(*expected)[1:]...)
	result, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET `+set+` WHERE action_id=? AND operation_id=? AND execution_owner='legacy025' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
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
}

func (s *Store) terminalizeLegacyLifecycleBridgeTx(ctx context.Context, db *sql.DB, action *ManagedLifecycleAction, terminalState, code, message string) error {
	if action == nil {
		return nil
	}
	if (terminalState == "succeeded" && (code != "RESTARTED" || message != "managed agent restarted")) || (terminalState == "failed" && (code != "RESTART_PREPARATION_FAILED" || message != "restart preparation failed")) || (terminalState != "succeeded" && terminalState != "failed") {
		return errors.New("managed legacy lifecycle terminal result is invalid")
	}
	stage := action.Stage
	if terminalState == "succeeded" {
		stage = "complete"
	}
	prefix := "source_"
	expected := action.SourceFence
	if lifecycleTargetStage(action.Stage) && action.TargetFence != nil {
		prefix, expected = "target_", action.TargetFence
	}
	if expected == nil {
		return &ManagedRestartStaleError{AgentID: action.AgentID}
	}
	now := formatTimestamp(s.clock.Now())
	args := []any{terminalState, stage, code, message, now, now, action.ActionID, action.OperationID, action.State, action.Stage}
	args = append(args, managedFenceValues(*expected)[1:]...)
	result, err := db.ExecContext(ctx, `UPDATE managed_lifecycle_actions SET state=?,stage=?,terminal_code=?,terminal_message=?,retryable=0,completed_at=?,updated_at=?,recovery_owner=NULL,recovery_claimed_at=NULL WHERE action_id=? AND operation_id=? AND execution_owner='legacy025' AND state=? AND stage=? AND `+managedLifecycleFenceWhere(prefix), args...)
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
	result, err = db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=?,terminal_code=?,terminal_message=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), code, message, action.OperationID)
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
		present, err := managedLifecycleActionsTableTx(ctx, s.db)
		if err != nil {
			return err
		}
		if present {
			// This compatibility reservation remains the old 025 executor's lane
			// until its manager caller moves to ReserveManagedLifecycle. The
			// bridge is created atomically so every old callback is fenced after
			// migration 027.
			if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_lifecycle_actions (
				action_id,operation_id,agent_id,kind,source,execution_owner,state,stage,
				source_pi_session_id,source_session_epoch,source_process_pid,source_process_start_marker,source_process_incarnation,source_runtime_generation,source_manifest_revision,source_manifest_hash,
				planned_target_runtime_generation,created_at,updated_at
			) VALUES (?,?,?,'restart','direct','legacy025','admitted','admission',?,?,?,?,?,?,?,?,?,?,?)`, append([]any{operation.OperationID, operation.OperationID, reservation.Fence.AgentID}, append(managedFenceValues(reservation.Fence)[1:], reservation.Transition.TargetRuntimeGeneration, now, now)...)...); err != nil {
				return err
			}
		}
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
	present, err := managedLifecycleActionsTableTx(ctx, s.db)
	if err != nil {
		return nil, err
	}
	query := managedRestartTransitionSelect + ` ORDER BY agent_id ASC`
	if present {
		// After 027, the old executor owns only a complete, matching legacy
		// bridge. An orphaned 025 row is not recoverable work.
		query = managedRestartTransitionSelect + ` WHERE EXISTS (
			SELECT 1 FROM managed_lifecycle_actions action
			WHERE action.action_id=managed_restart_transitions.operation_id
				AND action.operation_id=managed_restart_transitions.operation_id
				AND action.execution_owner='legacy025'
				AND action.state=CASE managed_restart_transitions.stage
					WHEN 'reserved' THEN 'admitted' WHEN 'prepared' THEN 'admitted' WHEN 'settling' THEN 'awaiting_settle'
					WHEN 'stopping' THEN 'stopping_source' WHEN 'stopped' THEN 'starting_target' WHEN 'starting' THEN 'awaiting_target_ready' END
				AND action.stage=CASE managed_restart_transitions.stage
					WHEN 'reserved' THEN 'admission' WHEN 'prepared' THEN 'admission' WHEN 'settling' THEN 'source_settle'
					WHEN 'stopping' THEN 'source_stop' WHEN 'stopped' THEN 'target_start' WHEN 'starting' THEN 'target_ready' END
				AND action.source_pi_session_id=managed_restart_transitions.old_pi_session_id
				AND action.source_session_epoch=managed_restart_transitions.old_session_epoch
				AND action.source_process_pid=managed_restart_transitions.old_process_pid
				AND action.source_process_start_marker=managed_restart_transitions.old_process_start_marker
				AND action.source_process_incarnation=managed_restart_transitions.old_process_incarnation
				AND action.source_runtime_generation=managed_restart_transitions.old_runtime_generation
				AND action.source_manifest_revision=managed_restart_transitions.old_manifest_revision
				AND action.source_manifest_hash=managed_restart_transitions.old_manifest_hash
				AND (managed_restart_transitions.stage<>'starting' OR (
					action.target_pi_session_id=managed_restart_transitions.target_pi_session_id
					AND action.target_session_epoch=managed_restart_transitions.target_session_epoch
					AND action.target_process_pid=managed_restart_transitions.target_process_pid
					AND action.target_process_start_marker=managed_restart_transitions.target_process_start_marker
					AND action.target_process_incarnation=managed_restart_transitions.target_process_incarnation
					AND action.target_runtime_generation=managed_restart_transitions.target_runtime_generation
					AND action.target_manifest_revision=managed_restart_transitions.target_manifest_revision
					AND action.target_manifest_hash=managed_restart_transitions.target_manifest_hash
				))
		) ORDER BY agent_id ASC`
	}
	rows, err := s.db.QueryContext(ctx, query)
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
		transition, _, err := s.restartOldOwnedTx(ctx, fence, operationID, "reserved")
		if err != nil {
			return err
		}
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage='prepared',updated_at=? WHERE agent_id=? AND operation_id=? AND stage='reserved' AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, fence.AgentID, operationID}, managedFenceValues(fence)[1:]...)...)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "admitted", "admission", nil, false, now)
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
		if transition.OperationID != operationID || !sameManagedRestartFence(transition.Fence, fence) || (transition.Stage != "reserved" && transition.Stage != "prepared") {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
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
		if bridge != nil {
			if err := s.terminalizeLegacyLifecycleBridgeTx(ctx, s.db, bridge, "failed", code, message); err != nil {
				return err
			}
		} else {
			terminalized, terminalErr := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
			if terminalErr != nil {
				return terminalErr
			}
			changed, terminalErr = terminalized.RowsAffected()
			if terminalErr != nil || changed != 1 {
				if terminalErr != nil {
					return terminalErr
				}
				return &ManagedRestartStaleError{AgentID: fence.AgentID}
			}
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// RecordManagedRestartShutdown preserves a nonterminal transition after the
// manager has reaped the exact owned process group during router shutdown.
func (s *Store) RecordManagedRestartShutdown(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "reserved", "prepared", "settling", "stopping", "stopped")
		if err != nil {
			return err
		}
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		updated, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='stopped',updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND accepting_prompts=0 AND slot_held=1`, append([]any{now}, managedFenceValues(restartFenceFromAgent(agent))...)...)
		if err != nil {
			return err
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		state, stage, _ := legacyLifecycleStateStage(transition.Stage)
		if err := updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, state, stage, nil, false, now); err != nil {
			return err
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET turn_settled_at=?,updated_at=? WHERE agent_id=? AND operation_id=? AND stage='settling' AND turn_settled_at IS NULL AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, now, fence.AgentID, operationID}, managedFenceValues(fence)[1:]...)...)
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
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "awaiting_settle", "source_settle", nil, false, now)
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop=1,stage='stopping',updated_at=? WHERE agent_id=? AND operation_id=? AND stage=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, fence.AgentID, operationID, transition.Stage}, managedFenceValues(fence)[1:]...)...)
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
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "stopping_source", "source_stop", nil, false, now)
	})
}

// MarkManagedRestartOldStopped is legal only after the executor has proved the
// entire old process group absent. It preserves the held slot and closed
// admission for target creation.
func (s *Store) MarkManagedRestartOldStopped(ctx context.Context, fence ManagedRestartFence, operationID string) error {
	return s.runImmediate(ctx, func() error {
		transition, agent, err := s.restartOldOwnedTx(ctx, fence, operationID, "stopping")
		if err != nil {
			return err
		}
		if agent.LifecycleState != "restarting" || agent.AcceptingPrompts || !agent.SlotHeld {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
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
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage='stopped',updated_at=? WHERE agent_id=? AND operation_id=? AND stage='stopping' AND planned_stop=1 AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, fence.AgentID, operationID}, managedFenceValues(fence)[1:]...)...)
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
		if err := updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "starting_target", "target_start", nil, false, now); err != nil {
			return err
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop=1,turn_settled_at=CASE WHEN reservation_activity_state='active' THEN ? ELSE NULL END,stage='stopping',updated_at=? WHERE agent_id=? AND operation_id=? AND stage=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, now, fence.AgentID, operationID, transition.Stage}, managedFenceValues(fence)[1:]...)...)
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
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "stopping_source", "source_stop", nil, false, now)
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
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
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET planned_stop=1,turn_settled_at=CASE WHEN reservation_activity_state='active' THEN ? ELSE turn_settled_at END,stage='stopped',updated_at=? WHERE agent_id=? AND operation_id=? AND stage=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{now, now, fence.AgentID, operationID, transition.Stage}, managedFenceValues(fence)[1:]...)...)
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
		if err := updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "starting_target", "target_start", nil, false, now); err != nil {
			return err
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, targetFence)
		if err != nil {
			return err
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
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET target_pi_session_id=NULL,target_session_epoch=NULL,target_process_pid=NULL,target_process_start_marker=NULL,target_process_incarnation=NULL,target_manifest_revision=NULL,target_manifest_hash=NULL,stage='stopped',updated_at=? WHERE agent_id=? AND operation_id=? AND stage='starting' AND target_pi_session_id=? AND target_session_epoch=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=? AND target_manifest_revision=? AND target_manifest_hash=?`, now, targetFence.AgentID, operationID, targetFence.PiSessionID, targetFence.SessionEpoch, targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation, targetFence.ManifestRevision, targetFence.ManifestHash)
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
		if err := updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "starting_target", "target_start", nil, true, now); err != nil {
			return err
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, oldFence)
		if err != nil {
			return err
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
		result, err = s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET target_pi_session_id=?,target_session_epoch=?,target_process_pid=?,target_process_start_marker=?,target_process_incarnation=?,target_manifest_revision=?,target_manifest_hash=?,stage='starting',updated_at=? WHERE agent_id=? AND operation_id=? AND stage='stopped' AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{targetFence.PiSessionID, targetFence.SessionEpoch, targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation, targetFence.ManifestRevision, targetFence.ManifestHash, now, oldFence.AgentID, operationID}, managedFenceValues(oldFence)[1:]...)...)
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
		if err := updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, "awaiting_target_ready", "target_ready", &targetFence, true, now); err != nil {
			return err
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, targetFence)
		if err != nil {
			return err
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
		result, err = s.db.ExecContext(ctx, `DELETE FROM managed_restart_transitions WHERE agent_id=? AND operation_id=? AND stage='starting' AND target_pi_session_id=? AND target_session_epoch=? AND target_process_pid=? AND target_process_start_marker=? AND target_process_incarnation=? AND target_manifest_revision=? AND target_manifest_hash=?`, targetFence.AgentID, operationID, targetFence.PiSessionID, targetFence.SessionEpoch, targetFence.ProcessPID, targetFence.ProcessStartMarker, targetFence.ProcessIncarnation, targetFence.ManifestRevision, targetFence.ManifestHash)
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
		if bridge != nil {
			if err := s.terminalizeLegacyLifecycleBridgeTx(ctx, s.db, bridge, "succeeded", "RESTARTED", "managed agent restarted"); err != nil {
				return err
			}
		} else {
			result, terminalErr := s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
			if terminalErr != nil {
				return terminalErr
			}
			changed, terminalErr = result.RowsAffected()
			if terminalErr != nil || changed != 1 {
				if terminalErr != nil {
					return terminalErr
				}
				return &ManagedRestartStaleError{AgentID: targetFence.AgentID}
			}
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='restarting' AND accepting_prompts=0 AND slot_held=1`, append([]any{code, message, now, now}, managedFenceValues(fence)...)...)
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
		state, stage, _ := legacyLifecycleStateStage(transition.Stage)
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, state, stage, nil, false, now)
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
		bridge, err := legacyLifecycleBridgeActionTx(ctx, s.db, transition, fence)
		if err != nil {
			return err
		}
		nextState, nextStage, ok := legacyLifecycleStateStage(to)
		if !ok {
			return &ManagedRestartStaleError{AgentID: fence.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_restart_transitions SET stage=?,updated_at=? WHERE agent_id=? AND operation_id=? AND stage=? AND old_pi_session_id=? AND old_session_epoch=? AND old_process_pid=? AND old_process_start_marker=? AND old_process_incarnation=? AND old_runtime_generation=? AND old_manifest_revision=? AND old_manifest_hash=?`, append([]any{to, now, fence.AgentID, operationID, from}, managedFenceValues(fence)[1:]...)...)
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
		return updateLegacyLifecycleBridgeTx(ctx, s.db, bridge, nextState, nextStage, nil, false, now)
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
		hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
		if err != nil {
			return err
		}
		where := `agent_id=? AND lifecycle_state='starting'`
		if hasLink {
			// The link and pending operation are a write-side ownership fence. A
			// terminate tombstone closes the operation, so an old callback cannot
			// install a process identity after that reservation wins.
			where += ` AND launch_operation_id IS NOT NULL AND EXISTS (SELECT 1 FROM managed_http_operations WHERE operation_id=managed_agents.launch_operation_id AND state='pending')`
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,updated_at=? WHERE `+where, sessionID, nullIfZero(identity.PID), nullIfEmpty(identity.StartMarker), nullIfEmpty(identity.Incarnation), runtimeGeneration, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
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
	source, err := scanManagedReadinessSource(s.db.QueryRowContext(ctx, `SELECT agent_id, desired_policy_canonical, desired_policy_sha256,
		pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_hash,
		applied_policy_canonical, applied_policy_sha256, policy_confirmed_at,
		snapshot_id, snapshot_owner_session_id, snapshot_sequence, snapshot_canonical, snapshot_sha256, snapshot_confirmed_at
		FROM managed_readiness_sources WHERE agent_id = ?`, agentID))
	if err != nil {
		return nil, err
	}
	recoveries, err := managedChildRecoveriesForTx(ctx, s.db, agentID)
	if err != nil {
		return nil, err
	}
	source.Recoveries = recoveries
	return source, nil
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

func managedChildRecoveriesForTx(ctx context.Context, db *sql.DB, agentID string) ([]ManagedChildRecovery, error) {
	rows, err := db.QueryContext(ctx, `SELECT prior_pi_session_id, prior_session_epoch, prior_process_incarnation, prior_runtime_generation,
		parent_entry_id, task_id, run_id, source_sequence, run_json, recovery_cause, updated_at
		FROM managed_child_recoveries WHERE agent_id = ? ORDER BY updated_at ASC, rowid ASC`, agentID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table: managed_child_recoveries") {
			return []ManagedChildRecovery{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	recoveries := []ManagedChildRecovery{}
	for rows.Next() {
		var recovery ManagedChildRecovery
		if err := rows.Scan(&recovery.PriorSessionID, &recovery.PriorSessionEpoch, &recovery.PriorProcessIncarnation, &recovery.PriorRuntimeGeneration,
			&recovery.ParentEntryID, &recovery.TaskID, &recovery.RunID, &recovery.Sequence, &recovery.RunCanonical, &recovery.Cause, &recovery.ObservedAt); err != nil {
			return nil, err
		}
		recovery.RunCanonical = append([]byte(nil), recovery.RunCanonical...)
		recoveries = append(recoveries, recovery)
	}
	return recoveries, rows.Err()
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
	// The new parent fence is already durable when this runs. Preserve every
	// old active child before replacing its source row so a crash or an omitted
	// replacement snapshot cannot turn absence into a terminal lifecycle state.
	if err := s.archiveActiveManagedChildrenForFenceTx(ctx, fence); err != nil {
		return err
	}
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

type managedChildArchiveSnapshot struct {
	Runs []map[string]any `json:"runs"`
}

func activeManagedChildRuns(canonical []byte) ([]map[string]any, error) {
	var snapshot managedChildArchiveSnapshot
	if err := json.Unmarshal(canonical, &snapshot); err != nil {
		return nil, errors.New("stored managed child snapshot is unreadable")
	}
	active := make([]map[string]any, 0, len(snapshot.Runs))
	for _, run := range snapshot.Runs {
		state, _ := run["lifecycle_state"].(string)
		if state != "queued" && state != "running" {
			continue
		}
		if !managedChildRunIdentityValid(run) {
			return nil, errors.New("stored managed child snapshot has invalid active run")
		}
		active = append(active, run)
	}
	return active, nil
}

func managedChildRunIdentityValid(run map[string]any) bool {
	for _, key := range []string{"parent_entry_id", "task_id", "run_id"} {
		value, ok := run[key].(string)
		if !ok || value == "" || !utf8.ValidString(value) || len([]byte(value)) > 8192 {
			return false
		}
	}
	return true
}

func managedChildRunSequence(run map[string]any) (int64, bool) {
	value, ok := run["sequence"]
	if !ok {
		return 0, false
	}
	switch sequence := value.(type) {
	case float64:
		if sequence != float64(int64(sequence)) || sequence < 0 {
			return 0, false
		}
		return int64(sequence), true
	case int64:
		return sequence, sequence >= 0
	case int:
		return int64(sequence), sequence >= 0
	default:
		return 0, false
	}
}

// archiveActiveManagedChildrenForFenceTx creates an interrupted_unknown row
// before discarding the old readiness snapshot. A session epoch is a hard
// boundary; a same-session process replacement starts as parent_process_lost
// until the new snapshot can confirm or omit each old active run.
func (s *Store) archiveActiveManagedChildrenForFenceTx(ctx context.Context, next ManagedRuntimeFence) error {
	var prior ManagedRuntimeFence
	var canonical []byte
	err := s.db.QueryRowContext(ctx, `SELECT pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_hash, snapshot_canonical
		FROM managed_readiness_sources WHERE agent_id = ? AND snapshot_canonical IS NOT NULL`, next.AgentID).Scan(
		&prior.PiSessionID, &prior.SessionEpoch, &prior.ProcessPID, &prior.ProcessStartMarker, &prior.ProcessIncarnation, &prior.RuntimeGeneration, &prior.ManifestRevision, &prior.ManifestHash, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		// Historical migration fixtures intentionally stop before this Phase 3
		// table. They have no runtime recovery contract to preserve.
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	prior.AgentID = next.AgentID
	if !validManagedRuntimeFence(prior) || sameManagedRestartFence(prior, next) {
		return nil
	}
	runs, err := activeManagedChildRuns(canonical)
	if err != nil {
		return err
	}
	cause := "parent_process_lost"
	if prior.PiSessionID != next.PiSessionID || prior.SessionEpoch != next.SessionEpoch {
		cause = "session_changed"
	}
	now := formatTimestamp(s.clock.Now())
	for _, run := range runs {
		sequence, valid := managedChildRunSequence(run)
		if !valid {
			return errors.New("stored managed child snapshot has invalid active sequence")
		}
		runCanonical, err := json.Marshal(run)
		if err != nil || len(runCanonical) < 2 || len(runCanonical) > 8192 {
			return errors.New("stored managed child run cannot be archived")
		}
		parentEntryID, taskID, runID := run["parent_entry_id"].(string), run["task_id"].(string), run["run_id"].(string)
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_child_recoveries(
			agent_id,prior_pi_session_id,prior_session_epoch,prior_process_incarnation,prior_runtime_generation,parent_entry_id,task_id,run_id,source_sequence,run_json,recovery_state,recovery_cause,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?, 'interrupted_unknown',?,?,?)
			ON CONFLICT(agent_id,prior_pi_session_id,prior_session_epoch,prior_process_incarnation,parent_entry_id,task_id,run_id)
			DO UPDATE SET recovery_cause=excluded.recovery_cause, run_json=excluded.run_json, source_sequence=excluded.source_sequence, updated_at=excluded.updated_at`,
			next.AgentID, prior.PiSessionID, prior.SessionEpoch, prior.ProcessIncarnation, prior.RuntimeGeneration, parentEntryID, taskID, runID, sequence, runCanonical, cause, now, now)
		if err != nil {
			return err
		}
		if _, err := s.recordManagedChildHistoryTx(ctx, prior, parentEntryID, taskID, runID, sequence, "recovery", run["lifecycle_state"].(string), "interrupted_unknown", cause, []string{"recovery"}); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM managed_child_recoveries WHERE rowid NOT IN (SELECT rowid FROM managed_child_recoveries ORDER BY updated_at DESC, rowid DESC LIMIT ?)`, maxManagedHistoryEntries)
	return err
}

// RecordManagedParentProcessLost fences the one failure boundary that cannot
// wait for a replacement runtime. Once the owned parent has exited, each live
// child is retained as interrupted_unknown and the old source snapshot is
// cleared so the browser cannot keep presenting it as current. A later
// same-session snapshot may confirm or omit these rows; a new session cannot.
func (s *Store) RecordManagedParentProcessLost(ctx context.Context, fence ManagedRuntimeFence) (bool, error) {
	if !validManagedRuntimeFence(fence) {
		return false, errors.New("managed parent-process-loss fence is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+` AND desired_state='running' AND lifecycle_state='running'`, managedFenceValues(fence)...).Scan(&present); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		var canonical []byte
		err := s.db.QueryRowContext(ctx, `SELECT snapshot_canonical FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere()+` AND snapshot_canonical IS NOT NULL`, readinessSourceFenceArgs(fence)...).Scan(&canonical)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		runs, err := activeManagedChildRuns(canonical)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		for _, run := range runs {
			sequence, valid := managedChildRunSequence(run)
			if !valid {
				return errors.New("stored managed child snapshot has invalid active sequence")
			}
			runCanonical, marshalErr := json.Marshal(run)
			if marshalErr != nil || len(runCanonical) < 2 || len(runCanonical) > 8192 {
				return errors.New("stored managed child run cannot be archived")
			}
			parentEntryID, taskID, runID := run["parent_entry_id"].(string), run["task_id"].(string), run["run_id"].(string)
			_, err = s.db.ExecContext(ctx, `INSERT INTO managed_child_recoveries(
				agent_id,prior_pi_session_id,prior_session_epoch,prior_process_incarnation,prior_runtime_generation,parent_entry_id,task_id,run_id,source_sequence,run_json,recovery_state,recovery_cause,created_at,updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?, 'interrupted_unknown','parent_process_lost',?,?)
				ON CONFLICT(agent_id,prior_pi_session_id,prior_session_epoch,prior_process_incarnation,parent_entry_id,task_id,run_id)
				DO UPDATE SET recovery_cause=excluded.recovery_cause,run_json=excluded.run_json,source_sequence=excluded.source_sequence,updated_at=excluded.updated_at`,
				fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, parentEntryID, taskID, runID, sequence, runCanonical, now, now)
			if err != nil {
				return err
			}
			if _, err := s.recordManagedChildHistoryTx(ctx, fence, parentEntryID, taskID, runID, sequence, "recovery", run["lifecycle_state"].(string), "interrupted_unknown", "parent_process_lost", []string{"recovery"}); err != nil {
				return err
			}
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET snapshot_id=NULL,snapshot_owner_session_id=NULL,snapshot_sequence=NULL,snapshot_canonical=NULL,snapshot_sha256=NULL,snapshot_confirmed_at=NULL WHERE `+readinessSourceFenceWhere()+` AND snapshot_canonical IS NOT NULL`, readinessSourceFenceArgs(fence)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return nil
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_child_recoveries WHERE rowid NOT IN (SELECT rowid FROM managed_child_recoveries ORDER BY updated_at DESC, rowid DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
			return err
		}
		if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		updated = true
		return nil
	})
	return updated, err
}

func managedChildHistoryKey(fence ManagedRuntimeFence, parentEntryID, taskID, runID string, sequence int64, kind, recoveryState, recoveryCause string, changedFields []string) string {
	identity := strings.Join([]string{fence.AgentID, fence.PiSessionID, fmt.Sprint(fence.SessionEpoch), fence.ProcessIncarnation, fence.RuntimeGeneration, parentEntryID, taskID, runID, fmt.Sprint(sequence), kind, recoveryState, recoveryCause, strings.Join(changedFields, ",")}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "child:" + hex.EncodeToString(sum[:])
}

// recordManagedChildHistoryTx is the authoritative complete-history writer for
// child transitions. It commits the visible row and its fenced, bounded audit
// record in the same transaction as the source projection, so retries and UI
// projection publication cannot create a second lifecycle entry.
func (s *Store) recordManagedChildHistoryTx(ctx context.Context, fence ManagedRuntimeFence, parentEntryID, taskID, runID string, sequence int64, kind, lifecycleState, recoveryState, recoveryCause string, changedFields []string) (ManagedHistoryEntry, error) {
	if !validManagedRuntimeFence(fence) || parentEntryID == "" || taskID == "" || runID == "" || sequence < 0 || (kind != "transition" && kind != "recovery") || (recoveryState != "confirmed" && recoveryState != "interrupted_unknown") || (recoveryState == "confirmed" && recoveryCause != "") || (recoveryState == "interrupted_unknown" && recoveryCause != "snapshot_omitted" && recoveryCause != "parent_process_lost" && recoveryCause != "session_changed") {
		return ManagedHistoryEntry{}, errors.New("managed child history is invalid")
	}
	key := managedChildHistoryKey(fence, parentEntryID, taskID, runID, sequence, kind, recoveryState, recoveryCause, changedFields)
	var existingSequence int64
	var existingAt, existingLevel, existingMessage string
	err := s.db.QueryRowContext(ctx, `SELECT h.sequence,h.occurred_at,h.level,h.message FROM managed_child_transitions t JOIN managed_complete_history h ON h.sequence=t.complete_sequence WHERE t.event_key=?`, key).Scan(&existingSequence, &existingAt, &existingLevel, &existingMessage)
	if err == nil {
		return ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", existingSequence), At: existingAt, Level: existingLevel, Message: existingMessage}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ManagedHistoryEntry{}, err
	}
	details := map[string]any{
		"session_epoch":       fence.SessionEpoch,
		"process_incarnation": fence.ProcessIncarnation,
		"runtime_generation":  fence.RuntimeGeneration,
		"parent_entry_id":     parentEntryID,
		"task_id":             taskID,
		"run_id":              runID,
		"source_sequence":     sequence,
		"kind":                kind,
		"lifecycle_state":     lifecycleState,
		"recovery":            map[string]any{"state": recoveryState},
		"changed_fields":      append([]string(nil), changedFields...),
	}
	if recoveryCause != "" {
		details["recovery"] = map[string]any{"state": recoveryState, "cause": recoveryCause}
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil || len(detailsJSON) < 2 || len(detailsJSON) > 8192 {
		return ManagedHistoryEntry{}, errors.New("managed child history details are invalid")
	}
	message := "Managed child transition recorded"
	if kind == "recovery" && recoveryState == "interrupted_unknown" {
		message = "Managed child recovery is interrupted and unknown"
	}
	occurredAt := formatTimestamp(s.clock.Now())
	result, err := s.db.ExecContext(ctx, `INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key) VALUES (?,?,?,?,?,?)`, fence.AgentID, "managed_child", "info", message, occurredAt, key)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	completeSequence, err := result.LastInsertId()
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_child_transitions(event_key,complete_sequence,agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation,parent_entry_id,task_id,run_id,source_sequence,kind,lifecycle_state,recovery_state,recovery_cause,details_json,occurred_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, key, completeSequence, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessIncarnation, fence.RuntimeGeneration, parentEntryID, taskID, runID, sequence, kind, lifecycleState, recoveryState, nullableString(recoveryCause), string(detailsJSON), occurredAt)
	if err != nil {
		return ManagedHistoryEntry{}, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_complete_history WHERE sequence NOT IN (SELECT sequence FROM managed_complete_history ORDER BY sequence DESC LIMIT ?)`, maxManagedHistoryEntries); err != nil {
		return ManagedHistoryEntry{}, err
	}
	return ManagedHistoryEntry{ID: fmt.Sprintf("complete:%d", completeSequence), At: occurredAt, Level: "info", Message: message}, nil
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

// reconcileManagedChildRecoveriesForSnapshotTx resolves only prior runs from
// the same parent session. A new session is a hard fence: identical-looking
// old IDs remain interrupted_unknown and can never attach to its new epoch.
func (s *Store) reconcileManagedChildRecoveriesForSnapshotTx(ctx context.Context, fence ManagedRuntimeFence, snapshot ManagedChildSnapshot) error {
	var source managedChildArchiveSnapshot
	if err := json.Unmarshal(snapshot.Canonical, &source); err != nil {
		return errors.New("managed child recovery snapshot is unreadable")
	}
	current := make(map[string]map[string]any, len(source.Runs))
	for _, run := range source.Runs {
		if !managedChildRunIdentityValid(run) {
			return errors.New("managed child recovery snapshot has invalid run")
		}
		current[managedChildRecoveryIdentity(run["parent_entry_id"].(string), run["task_id"].(string), run["run_id"].(string))] = run
	}
	recoveries, err := managedChildRecoveriesForTx(ctx, s.db, fence.AgentID)
	if err != nil {
		return err
	}
	for _, recovery := range recoveries {
		if recovery.PriorSessionID != fence.PiSessionID || recovery.PriorSessionEpoch != fence.SessionEpoch {
			continue
		}
		identity := managedChildRecoveryIdentity(recovery.ParentEntryID, recovery.TaskID, recovery.RunID)
		if run := current[identity]; run != nil {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_child_recoveries WHERE agent_id=? AND prior_pi_session_id=? AND prior_session_epoch=? AND prior_process_incarnation=? AND parent_entry_id=? AND task_id=? AND run_id=?`,
				fence.AgentID, recovery.PriorSessionID, recovery.PriorSessionEpoch, recovery.PriorProcessIncarnation, recovery.ParentEntryID, recovery.TaskID, recovery.RunID); err != nil {
				return err
			}
			sequence, valid := managedChildRunSequence(run)
			if !valid {
				return errors.New("managed child recovery confirmation has invalid sequence")
			}
			if _, err := s.recordManagedChildHistoryTx(ctx, fence, recovery.ParentEntryID, recovery.TaskID, recovery.RunID, sequence, "recovery", run["lifecycle_state"].(string), "confirmed", "", []string{"snapshot"}); err != nil {
				return err
			}
			continue
		}
		if recovery.Cause == "snapshot_omitted" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_child_recoveries SET recovery_cause='snapshot_omitted',updated_at=? WHERE agent_id=? AND prior_pi_session_id=? AND prior_session_epoch=? AND prior_process_incarnation=? AND parent_entry_id=? AND task_id=? AND run_id=?`,
			formatTimestamp(s.clock.Now()), fence.AgentID, recovery.PriorSessionID, recovery.PriorSessionEpoch, recovery.PriorProcessIncarnation, recovery.ParentEntryID, recovery.TaskID, recovery.RunID); err != nil {
			return err
		}
		var run map[string]any
		if err := json.Unmarshal(recovery.RunCanonical, &run); err != nil {
			return errors.New("managed child recovery row is unreadable")
		}
		state, _ := run["lifecycle_state"].(string)
		if _, err := s.recordManagedChildHistoryTx(ctx, fence, recovery.ParentEntryID, recovery.TaskID, recovery.RunID, recovery.Sequence, "recovery", state, "interrupted_unknown", "snapshot_omitted", []string{"snapshot"}); err != nil {
			return err
		}
	}
	return nil
}

func managedChildRecoveryIdentity(parentEntryID, taskID, runID string) string {
	return parentEntryID + "\x00" + taskID + "\x00" + runID
}

type managedChildCompactionReconciliation struct {
	ParentEntryID, TaskID, RunID, LifecycleState string
	Sequence                                     int64
}

// reconcileManagedChildCompactionSnapshot applies the one honest repair that
// a new authoritative snapshot can establish: a formerly compacting child is
// now idle but supplies no explicit ending outcome. It cannot call that
// success, failure, or a terminal lifecycle result.
func reconcileManagedChildCompactionSnapshot(previousCanonical, incomingCanonical []byte) ([]byte, []managedChildCompactionReconciliation, error) {
	var previous, incoming map[string]any
	if err := json.Unmarshal(previousCanonical, &previous); err != nil {
		return nil, nil, errors.New("stored managed child snapshot is unreadable")
	}
	if err := json.Unmarshal(incomingCanonical, &incoming); err != nil {
		return nil, nil, errors.New("managed child snapshot confirmation is unreadable")
	}
	previousRuns, previousValid := previous["runs"].([]any)
	incomingRuns, incomingValid := incoming["runs"].([]any)
	if !previousValid || !incomingValid {
		// Historical readiness fixtures predate the child payload contract. They
		// contain no source run that can be reconciled.
		return incomingCanonical, []managedChildCompactionReconciliation{}, nil
	}
	previousByIdentity := make(map[string]map[string]any, len(previousRuns))
	for _, value := range previousRuns {
		run, valid := value.(map[string]any)
		if valid && managedChildRunIdentityValid(run) {
			previousByIdentity[managedChildRecoveryIdentity(run["parent_entry_id"].(string), run["task_id"].(string), run["run_id"].(string))] = run
		}
	}
	reconciliations := []managedChildCompactionReconciliation{}
	for _, value := range incomingRuns {
		run, valid := value.(map[string]any)
		if !valid || !managedChildRunIdentityValid(run) {
			continue
		}
		prior := previousByIdentity[managedChildRecoveryIdentity(run["parent_entry_id"].(string), run["task_id"].(string), run["run_id"].(string))]
		if prior == nil {
			continue
		}
		priorCompaction, _ := prior["compaction"].(map[string]any)
		compaction, _ := run["compaction"].(map[string]any)
		if priorCompaction == nil || compaction == nil || priorCompaction["state"] != "compacting" || compaction["state"] == "compacting" || compaction["last_outcome"] != nil {
			continue
		}
		sequence, valid := managedChildRunSequence(run)
		state, stateValid := run["lifecycle_state"].(string)
		if !valid || !stateValid {
			return nil, nil, errors.New("managed child compaction reconciliation is invalid")
		}
		compaction["last_outcome"] = "reconciled_unknown"
		reconciliations = append(reconciliations, managedChildCompactionReconciliation{ParentEntryID: run["parent_entry_id"].(string), TaskID: run["task_id"].(string), RunID: run["run_id"].(string), LifecycleState: state, Sequence: sequence})
	}
	if len(reconciliations) == 0 {
		return incomingCanonical, reconciliations, nil
	}
	reconciled, err := json.Marshal(incoming)
	if err != nil || len(reconciled) < 2 || len(reconciled) > 32768 {
		return nil, nil, errors.New("managed child compaction reconciliation is invalid")
	}
	return reconciled, reconciliations, nil
}

func (s *Store) ConfirmManagedChildSnapshot(ctx context.Context, fence ManagedRuntimeFence, snapshot ManagedChildSnapshot) error {
	if !validManagedRuntimeFence(fence) || snapshot.SnapshotID == "" || snapshot.OwnerSessionID == "" || !utf8.ValidString(snapshot.SnapshotID) || !utf8.ValidString(snapshot.OwnerSessionID) || len([]byte(snapshot.SnapshotID)) > 8192 || len([]byte(snapshot.OwnerSessionID)) > 8192 || snapshot.Sequence < 0 || len(snapshot.Canonical) < 2 || len(snapshot.Canonical) > 32768 || !managedSHA256RE.MatchString(snapshot.SHA256) {
		return errors.New("managed child snapshot confirmation is invalid")
	}
	return s.runImmediate(ctx, func() error {
		var previousCanonical []byte
		previousErr := s.db.QueryRowContext(ctx, `SELECT snapshot_canonical FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere()+` AND snapshot_canonical IS NOT NULL`, readinessSourceFenceArgs(fence)...).Scan(&previousCanonical)
		if previousErr != nil && !errors.Is(previousErr, sql.ErrNoRows) {
			return previousErr
		}
		reconciliations := []managedChildCompactionReconciliation{}
		if previousErr == nil {
			var err error
			snapshot.Canonical, reconciliations, err = reconcileManagedChildCompactionSnapshot(previousCanonical, snapshot.Canonical)
			if err != nil {
				return err
			}
			if len(reconciliations) > 0 {
				sum := sha256.Sum256(snapshot.Canonical)
				snapshot.SHA256 = hex.EncodeToString(sum[:])
			}
		}
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
		for _, reconciliation := range reconciliations {
			if _, err := s.recordManagedChildHistoryTx(ctx, fence, reconciliation.ParentEntryID, reconciliation.TaskID, reconciliation.RunID, reconciliation.Sequence, "transition", reconciliation.LifecycleState, "confirmed", "", []string{"compaction", "reconciled_unknown"}); err != nil {
				return err
			}
		}
		return s.reconcileManagedChildRecoveriesForSnapshotTx(ctx, fence, snapshot)
	})
}

// ApplyManagedChildDelta merges the existing child-delta protocol into the
// ready snapshot. The initial readiness snapshot remains a separate gate: an
// event is accepted only for its current fence, owning session, and a strictly
// newer source sequence, so an old process cannot fabricate child state.
// RecordStaleManagedChildDelta preserves a fenced audit fact without changing
// the current projection. It is intentionally a no-op after termination,
// because terminate removes that agent's complete history as one transaction.
func (s *Store) RecordStaleManagedChildDelta(ctx context.Context, fence ManagedRuntimeFence, delta ManagedChildDelta, reason string) error {
	if !validManagedRuntimeFence(fence) || !validManagedChildDelta(delta) || reason == "" || !utf8.ValidString(reason) || len(reason) > 128 {
		return errors.New("stale managed child delta is invalid")
	}
	return s.runImmediate(ctx, func() error {
		var present int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id=?`, fence.AgentID).Scan(&present); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		state, _ := delta.Changes["lifecycle_state"].(string)
		if state == "" {
			state = "unknown"
		}
		_, err := s.recordManagedChildHistoryTx(ctx, fence, delta.ParentEntryID, delta.TaskID, delta.RunID, delta.Sequence, "transition", state, "confirmed", "", []string{"stale", reason})
		return err
	})
}

func (s *Store) ApplyManagedChildDelta(ctx context.Context, fence ManagedRuntimeFence, delta ManagedChildDelta) (bool, error) {
	if !validManagedRuntimeFence(fence) || !validManagedChildDelta(delta) {
		return false, errors.New("managed child delta is invalid")
	}
	updated := false
	err := s.runImmediate(ctx, func() error {
		var snapshotID, owner string
		var sequence int64
		var canonical []byte
		err := s.db.QueryRowContext(ctx, `SELECT snapshot_id, snapshot_owner_session_id, snapshot_sequence, snapshot_canonical FROM managed_readiness_sources WHERE `+readinessSourceFenceWhere()+`
			AND snapshot_id IS NOT NULL AND snapshot_owner_session_id = ?`, append(readinessSourceFenceArgs(fence), fence.PiSessionID)...).Scan(&snapshotID, &owner, &sequence, &canonical)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if owner != delta.OwnerSessionID {
			_, err := s.recordManagedChildHistoryTx(ctx, fence, delta.ParentEntryID, delta.TaskID, delta.RunID, delta.Sequence, "transition", "unknown", "confirmed", "", []string{"stale", "owner_session_mismatch"})
			return err
		}
		if delta.Sequence <= sequence {
			state, _ := delta.Changes["lifecycle_state"].(string)
			if state == "" {
				state = "unknown"
			}
			_, err := s.recordManagedChildHistoryTx(ctx, fence, delta.ParentEntryID, delta.TaskID, delta.RunID, delta.Sequence, "transition", state, "confirmed", "", []string{"stale", "sequence_regression"})
			return err
		}
		next, err := mergeManagedChildDelta(canonical, delta)
		if err != nil {
			return err
		}
		nextCanonical, err := json.Marshal(next)
		if err != nil || len(nextCanonical) < 2 || len(nextCanonical) > 32768 {
			return errors.New("merged managed child snapshot is invalid")
		}
		sum := sha256.Sum256(nextCanonical)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources SET snapshot_sequence = ?, snapshot_canonical = ?, snapshot_sha256 = ?, snapshot_confirmed_at = ? WHERE `+readinessSourceFenceWhere()+`
			AND snapshot_id = ? AND snapshot_owner_session_id = ? AND snapshot_sequence < ?`, append([]any{delta.Sequence, nextCanonical, hex.EncodeToString(sum[:]), formatTimestamp(s.clock.Now())}, append(readinessSourceFenceArgs(fence), snapshotID, fence.PiSessionID, delta.Sequence)...)...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return nil
		}
		state, err := managedChildLifecycleState(next, delta)
		if err != nil {
			return err
		}
		if _, err := s.recordManagedChildHistoryTx(ctx, fence, delta.ParentEntryID, delta.TaskID, delta.RunID, delta.Sequence, "transition", state, "confirmed", "", managedChildChangedFields(delta.Changes)); err != nil {
			return err
		}
		if err := s.confirmManagedChildRecoveryForDeltaTx(ctx, fence, delta, state); err != nil {
			return err
		}
		updated = true
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	return updated, err
}

func managedChildChangedFields(changes map[string]any) []string {
	fields := make([]string, 0, len(changes))
	for field := range changes {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func managedChildLifecycleState(snapshot map[string]any, delta ManagedChildDelta) (string, error) {
	runs, ok := snapshot["runs"].([]any)
	if !ok {
		return "", errors.New("merged managed child snapshot has invalid runs")
	}
	for _, value := range runs {
		run, ok := value.(map[string]any)
		if !ok || run["parent_entry_id"] != delta.ParentEntryID || run["task_id"] != delta.TaskID || run["run_id"] != delta.RunID {
			continue
		}
		state, ok := run["lifecycle_state"].(string)
		if !ok {
			return "", errors.New("merged managed child snapshot has invalid state")
		}
		return state, nil
	}
	return "", errors.New("merged managed child snapshot is missing its delta run")
}

// A matching current-session delta is the only live event that may clear a
// same-session uncertainty. Session-changed rows stay visible: their old event
// cannot attach to the new epoch even when task/run text happens to match.
func (s *Store) confirmManagedChildRecoveryForDeltaTx(ctx context.Context, fence ManagedRuntimeFence, delta ManagedChildDelta, lifecycleState string) error {
	recoveries, err := managedChildRecoveriesForTx(ctx, s.db, fence.AgentID)
	if err != nil {
		return err
	}
	for _, recovery := range recoveries {
		if recovery.PriorSessionID != fence.PiSessionID || recovery.PriorSessionEpoch != fence.SessionEpoch || recovery.ParentEntryID != delta.ParentEntryID || recovery.TaskID != delta.TaskID || recovery.RunID != delta.RunID {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_child_recoveries WHERE agent_id=? AND prior_pi_session_id=? AND prior_session_epoch=? AND prior_process_incarnation=? AND parent_entry_id=? AND task_id=? AND run_id=?`,
			fence.AgentID, recovery.PriorSessionID, recovery.PriorSessionEpoch, recovery.PriorProcessIncarnation, recovery.ParentEntryID, recovery.TaskID, recovery.RunID); err != nil {
			return err
		}
		if _, err := s.recordManagedChildHistoryTx(ctx, fence, delta.ParentEntryID, delta.TaskID, delta.RunID, delta.Sequence, "recovery", lifecycleState, "confirmed", "", []string{"delta"}); err != nil {
			return err
		}
	}
	return nil
}

func validManagedChildDelta(delta ManagedChildDelta) bool {
	if delta.OwnerSessionID == "" || delta.TaskID == "" || delta.RunID == "" || delta.ParentEntryID == "" || delta.Sequence < 0 || len(delta.Changes) == 0 || len(delta.Changes) > 7 {
		return false
	}
	for _, value := range []string{delta.OwnerSessionID, delta.TaskID, delta.RunID, delta.ParentEntryID} {
		if !utf8.ValidString(value) || len([]byte(value)) > 8192 {
			return false
		}
	}
	for key := range delta.Changes {
		switch key {
		case "description", "model", "lifecycle_state", "started_at", "finished_at", "duration_ms", "compaction":
		default:
			return false
		}
	}
	return true
}

func mergeManagedChildDelta(canonical []byte, delta ManagedChildDelta) (map[string]any, error) {
	var snapshot map[string]any
	if err := json.Unmarshal(canonical, &snapshot); err != nil {
		return nil, errors.New("stored managed child snapshot is unreadable")
	}
	runs, ok := snapshot["runs"].([]any)
	if !ok {
		return nil, errors.New("stored managed child snapshot has invalid runs")
	}
	var target map[string]any
	for _, value := range runs {
		run, valid := value.(map[string]any)
		if !valid {
			return nil, errors.New("stored managed child snapshot has invalid run")
		}
		if run["task_id"] == delta.TaskID && run["run_id"] == delta.RunID && run["parent_entry_id"] == delta.ParentEntryID {
			target = run
			break
		}
	}
	if target == nil {
		if len(delta.Changes) != 7 {
			return nil, errors.New("first managed child delta must provide a complete run")
		}
		target = map[string]any{"task_id": delta.TaskID, "run_id": delta.RunID, "parent_entry_id": delta.ParentEntryID}
		runs = append(runs, target)
		snapshot["runs"] = runs
	}
	for key, value := range delta.Changes {
		target[key] = value
	}
	target["sequence"] = delta.Sequence
	snapshot["sequence"] = delta.Sequence
	return snapshot, nil
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
		hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
		if err != nil {
			return err
		}
		where := `agent_id=? AND lifecycle_state='starting' AND pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?`
		if hasLink {
			where += ` AND launch_operation_id IS NOT NULL AND EXISTS (SELECT 1 FROM managed_http_operations WHERE operation_id=managed_agents.launch_operation_id AND state='pending')`
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,started_at=COALESCE(started_at,?),updated_at=? WHERE `+where, now, now, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash)
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
		hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
		if err != nil {
			return err
		}
		where := `agent_id=? AND lifecycle_state='starting'`
		if hasLink {
			where += ` AND launch_operation_id IS NOT NULL AND EXISTS (SELECT 1 FROM managed_http_operations WHERE operation_id=managed_agents.launch_operation_id AND state='pending')`
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,started_at=COALESCE(started_at,?),updated_at=? WHERE `+where, now, now, agentID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedReadinessSourceStaleError{AgentID: agentID}
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
	agent, err := scanManagedAgent(s.db.QueryRowContext(ctx, managedAgentSelect+` WHERE agent_id = ?`, agentID), nil)
	if err != nil {
		return nil, err
	}
	if err := loadManagedLaunchOperationTx(ctx, s.db, agent); err != nil {
		return nil, err
	}
	return agent, nil
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
	var result []*ManagedAgentRecord
	for rows.Next() {
		item, scanErr := scanManagedAgent(rows, nil)
		if scanErr != nil {
			_ = rows.Close()
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, item := range result {
		if err := loadManagedLaunchOperationTx(ctx, s.db, item); err != nil {
			return nil, err
		}
	}
	return result, nil
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

func loadManagedLaunchOperationTx(ctx context.Context, db *sql.DB, agent *ManagedAgentRecord) error {
	if agent == nil {
		return nil
	}
	hasLink, err := managedLaunchOperationColumnTx(ctx, db)
	if err != nil || !hasLink {
		return err
	}
	var link sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT launch_operation_id FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&link); err != nil {
		return err
	}
	agent.LaunchOperationID = link.String
	return nil
}

// ManagedAgentsProjectionSnapshot reads the current version and every durable
// managed row in one transaction. A UI client can therefore treat its result as
// an authoritative snapshot without combining rows from different versions.
func (s *Store) ManagedAgentsProjectionSnapshot(ctx context.Context) (ManagedAgentsSnapshot, error) {
	if ctx == nil {
		return ManagedAgentsSnapshot{}, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedAgentsSnapshot{}, errors.New("store is closed")
	}
	if _, err := s.db.ExecContext(ctx, "BEGIN"); err != nil {
		return ManagedAgentsSnapshot{}, err
	}
	rollback := func(err error) (ManagedAgentsSnapshot, error) {
		_, _ = s.db.ExecContext(context.Background(), "ROLLBACK")
		return ManagedAgentsSnapshot{}, err
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT projection_version FROM managed_settings WHERE singleton_id = 1`).Scan(&version); err != nil {
		return rollback(err)
	}
	rows, err := s.db.QueryContext(ctx, managedAgentSelect+` ORDER BY name ASC`)
	if err != nil {
		return rollback(err)
	}
	agents := make([]*ManagedAgentRecord, 0)
	for rows.Next() {
		agent, scanErr := scanManagedAgent(rows, nil)
		if scanErr != nil {
			_ = rows.Close()
			return rollback(scanErr)
		}
		agents = append(agents, agent)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	sourceRows, err := s.db.QueryContext(ctx, `SELECT agent_id, desired_policy_canonical, desired_policy_sha256,
		pi_session_id, session_epoch, process_pid, process_start_marker, process_incarnation, runtime_generation, manifest_revision, manifest_hash,
		applied_policy_canonical, applied_policy_sha256, policy_confirmed_at,
		snapshot_id, snapshot_owner_session_id, snapshot_sequence, snapshot_canonical, snapshot_sha256, snapshot_confirmed_at
		FROM managed_readiness_sources`)
	if err != nil {
		return rollback(err)
	}
	sources := make(map[string]*ManagedReadinessSource)
	for sourceRows.Next() {
		source, scanErr := scanManagedReadinessSource(sourceRows)
		if scanErr != nil {
			_ = sourceRows.Close()
			return rollback(scanErr)
		}
		sources[source.AgentID] = source
	}
	if err := sourceRows.Close(); err != nil {
		return rollback(err)
	}
	recoveryRows, err := s.db.QueryContext(ctx, `SELECT agent_id, prior_pi_session_id, prior_session_epoch, prior_process_incarnation, prior_runtime_generation,
		parent_entry_id, task_id, run_id, source_sequence, run_json, recovery_cause, updated_at
		FROM managed_child_recoveries ORDER BY updated_at ASC, rowid ASC`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table: managed_child_recoveries") {
			if _, commitErr := s.db.ExecContext(ctx, "COMMIT"); commitErr != nil {
				return ManagedAgentsSnapshot{}, commitErr
			}
			return ManagedAgentsSnapshot{GeneratedAt: s.clock.Now().UTC(), ProjectionVersion: version, Agents: agents, ReadinessSources: sources}, nil
		}
		return rollback(err)
	}
	for recoveryRows.Next() {
		var agentID string
		var recovery ManagedChildRecovery
		if err := recoveryRows.Scan(&agentID, &recovery.PriorSessionID, &recovery.PriorSessionEpoch, &recovery.PriorProcessIncarnation, &recovery.PriorRuntimeGeneration,
			&recovery.ParentEntryID, &recovery.TaskID, &recovery.RunID, &recovery.Sequence, &recovery.RunCanonical, &recovery.Cause, &recovery.ObservedAt); err != nil {
			_ = recoveryRows.Close()
			return rollback(err)
		}
		if source := sources[agentID]; source != nil {
			recovery.RunCanonical = append([]byte(nil), recovery.RunCanonical...)
			source.Recoveries = append(source.Recoveries, recovery)
		}
	}
	if err := recoveryRows.Close(); err != nil {
		return rollback(err)
	}
	if _, err := s.db.ExecContext(ctx, "COMMIT"); err != nil {
		return ManagedAgentsSnapshot{}, err
	}
	return ManagedAgentsSnapshot{GeneratedAt: s.clock.Now().UTC(), ProjectionVersion: version, Agents: agents, ReadinessSources: sources}, nil
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

// sweepManagedOperations preserves a terminal operation graph as one unit.
// expires_at is an eligibility floor, never permission to cascade through a
// nonterminal child, retained batch parent, 025 bridge, or launch protection.
func (s *Store) sweepManagedOperations(ctx context.Context, now string) (int, error) {
	var lifecycleTable int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_actions'`).Scan(&lifecycleTable); err != nil {
		return 0, err
	}
	if lifecycleTable == 0 {
		result, err := s.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE state='terminal' AND expires_at IS NOT NULL AND expires_at<=?`, now)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		return int(count), err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_id FROM managed_http_operations WHERE state='terminal' AND expires_at IS NOT NULL AND expires_at<=? ORDER BY expires_at,operation_id`, now)
	if err != nil {
		return 0, err
	}
	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, id)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	deleted := 0
	for _, candidate := range candidates {
		count, err := s.sweepManagedOperationClosureTx(ctx, candidate, now)
		if err != nil {
			return 0, err
		}
		deleted += count
	}
	return deleted, nil
}

type managedOperationGraphAction struct{ id, operationID, parent, retryOf, state string }

func (s *Store) sweepManagedOperationClosureTx(ctx context.Context, candidate, now string) (int, error) {
	// Candidates are captured before graph deletion. A prior closure can remove
	// a later candidate, which is a completed sweep outcome, not an invariant.
	var present int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_http_operations WHERE operation_id=?`, candidate).Scan(&present); err != nil {
		return 0, err
	}
	if present == 0 {
		return 0, nil
	}
	actions := map[string]managedOperationGraphAction{}
	operations := map[string]struct{}{}
	operationQueue := []string{}
	actionQueue := []string{}
	addOperation := func(id string) {
		if id != "" {
			if _, ok := operations[id]; !ok {
				operations[id] = struct{}{}
				operationQueue = append(operationQueue, id)
			}
		}
	}
	addAction := func(id string) error {
		if id == "" {
			return nil
		}
		if _, ok := actions[id]; ok {
			return nil
		}
		var value managedOperationGraphAction
		var parent, retry sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT action_id,operation_id,parent_action_id,retry_of,state FROM managed_lifecycle_actions WHERE action_id=?`, id).Scan(&value.id, &value.operationID, &parent, &retry, &value.state)
		if err != nil {
			return err
		}
		value.parent, value.retryOf = parent.String, retry.String
		actions[id] = value
		actionQueue = append(actionQueue, id)
		return nil
	}
	addOperation(candidate)
	for operationIndex, actionIndex := 0, 0; operationIndex < len(operationQueue) || actionIndex < len(actionQueue); {
		if operationIndex < len(operationQueue) {
			op := operationQueue[operationIndex]
			operationIndex++
			var actionID string
			err := s.db.QueryRowContext(ctx, `SELECT action_id FROM managed_lifecycle_actions WHERE operation_id=?`, op).Scan(&actionID)
			if err == nil {
				if err := addAction(actionID); err != nil {
					return 0, err
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			if err := s.addManagedBatchOperationReferencesTx(ctx, op, addAction, addOperation); err != nil {
				return 0, err
			}
			continue
		}
		action := actions[actionQueue[actionIndex]]
		actionIndex++
		addOperation(action.operationID)
		if err := addAction(action.parent); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if err := addAction(action.retryOf); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		for _, query := range []string{`SELECT action_id FROM managed_lifecycle_actions WHERE retry_of=?`, `SELECT action_id FROM managed_lifecycle_actions WHERE parent_action_id=?`} {
			rows, err := s.db.QueryContext(ctx, query, action.id)
			if err != nil {
				return 0, err
			}
			var childIDs []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return 0, err
				}
				childIDs = append(childIDs, id)
			}
			if err := rows.Close(); err != nil {
				return 0, err
			}
			for _, id := range childIDs {
				if err := addAction(id); err != nil {
					return 0, err
				}
			}
		}
		if err := s.addManagedBatchActionReferencesTx(ctx, action.id, addAction, addOperation); err != nil {
			return 0, err
		}
	}
	prunable, err := s.validateManagedOperationClosureTx(ctx, operations, actions, now)
	if err != nil {
		return 0, err
	}
	if !prunable {
		return 0, nil
	}
	postorder, err := s.managedActionPostorderTx(ctx, actions)
	if err != nil {
		return 0, err
	}
	if err := s.deleteManagedBatchReferencesTx(ctx, actions, operations); err != nil {
		return 0, err
	}
	if s.managedGraphFault != nil {
		if err := s.managedGraphFault("after_first_batch_item_delete"); err != nil {
			return 0, err
		}
	}
	for _, actionID := range postorder {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_lifecycle_actions WHERE action_id=?`, actionID); err != nil {
			return 0, err
		}
	}
	for _, actionID := range postorder {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, actions[actionID].operationID); err != nil {
			return 0, err
		}
		delete(operations, actions[actionID].operationID)
	}
	remaining := make([]string, 0, len(operations))
	for id := range operations {
		remaining = append(remaining, id)
	}
	sort.Strings(remaining)
	for _, operationID := range remaining {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, operationID); err != nil {
			return 0, err
		}
	}
	return len(postorder) + len(remaining), nil
}

func (s *Store) addManagedBatchOperationReferencesTx(ctx context.Context, operationID string, addAction func(string) error, addOperation func(string)) error {
	rows, err := s.db.QueryContext(ctx, `SELECT parent_action_id,child_action_id,child_operation_id FROM managed_lifecycle_batch_items WHERE child_operation_id=?`, operationID)
	if err != nil {
		return err
	}
	type batchReference struct{ parent, childAction, childOperation string }
	var references []batchReference
	for rows.Next() {
		var reference batchReference
		var childAction, childOperation sql.NullString
		if err := rows.Scan(&reference.parent, &childAction, &childOperation); err != nil {
			_ = rows.Close()
			return err
		}
		reference.childAction, reference.childOperation = childAction.String, childOperation.String
		references = append(references, reference)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, reference := range references {
		if err := addAction(reference.parent); err != nil {
			return err
		}
		if err := addAction(reference.childAction); err != nil {
			return err
		}
		addOperation(reference.childOperation)
	}
	return nil
}
func (s *Store) addManagedBatchActionReferencesTx(ctx context.Context, actionID string, addAction func(string) error, addOperation func(string)) error {
	rows, err := s.db.QueryContext(ctx, `SELECT parent_action_id,child_action_id,child_operation_id FROM managed_lifecycle_batch_items WHERE parent_action_id=? OR child_action_id=?`, actionID, actionID)
	if err != nil {
		return err
	}
	type batchReference struct{ parent, childAction, childOperation string }
	var references []batchReference
	for rows.Next() {
		var reference batchReference
		var childAction, childOperation sql.NullString
		if err := rows.Scan(&reference.parent, &childAction, &childOperation); err != nil {
			_ = rows.Close()
			return err
		}
		reference.childAction, reference.childOperation = childAction.String, childOperation.String
		references = append(references, reference)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, reference := range references {
		if err := addAction(reference.parent); err != nil {
			return err
		}
		if err := addAction(reference.childAction); err != nil {
			return err
		}
		addOperation(reference.childOperation)
	}
	return nil
}
func (s *Store) validateManagedOperationClosureTx(ctx context.Context, operations map[string]struct{}, actions map[string]managedOperationGraphAction, now string) (bool, error) {
	for operationID := range operations {
		var state string
		var expiry sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT state,expires_at FROM managed_http_operations WHERE operation_id=?`, operationID).Scan(&state, &expiry); err != nil {
			return false, err
		}
		if state != "terminal" || !expiry.Valid || expiry.String > now {
			return false, nil
		}
		var transitions int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_restart_transitions WHERE operation_id=?`, operationID).Scan(&transitions); err != nil {
			return false, err
		}
		if transitions != 0 {
			return false, errors.New("terminal managed operation retains a 025 transition")
		}
		var links int
		hasLink, err := managedLaunchOperationColumnTx(ctx, s.db)
		if err != nil {
			return false, err
		}
		if hasLink {
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE launch_operation_id=?`, operationID).Scan(&links); err != nil {
				return false, err
			}
			if links != 0 {
				return false, nil
			}
		}
	}
	for _, action := range actions {
		if action.state != "succeeded" && action.state != "failed" && action.state != "cancelled" && action.state != "superseded" {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) managedActionPostorderTx(ctx context.Context, actions map[string]managedOperationGraphAction) ([]string, error) {
	ids := make([]string, 0, len(actions))
	for id := range actions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	visiting := map[string]bool{}
	result := []string{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		if visiting[id] {
			return errors.New("managed lifecycle graph cycle")
		}
		visiting[id] = true
		rows, err := s.db.QueryContext(ctx, `SELECT action_id FROM managed_lifecycle_actions WHERE retry_of=? OR parent_action_id=?`, id, id)
		if err != nil {
			return err
		}
		var children []string
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				_ = rows.Close()
				return err
			}
			if _, ok := actions[child]; ok {
				children = append(children, child)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		sort.Strings(children)
		for _, child := range children {
			if err := visit(child); err != nil {
				return err
			}
		}
		visiting[id] = false
		seen[id] = true
		result = append(result, id)
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *Store) deleteManagedBatchReferencesTx(ctx context.Context, actions map[string]managedOperationGraphAction, operations map[string]struct{}) error {
	if len(actions) == 0 && len(operations) == 0 {
		return nil
	}
	ids := make([]string, 0, len(actions))
	for id := range actions {
		ids = append(ids, id)
	}
	ops := make([]string, 0, len(operations))
	for id := range operations {
		ops = append(ops, id)
	}
	clauses := []string{}
	args := []any{}
	if len(ids) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		clauses = append(clauses, `parent_action_id IN (`+placeholders+`)`, `child_action_id IN (`+placeholders+`)`)
		for _, id := range ids {
			args = append(args, id)
		}
		for _, id := range ids {
			args = append(args, id)
		}
	}
	if len(ops) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ops)), ",")
		clauses = append(clauses, `child_operation_id IN (`+placeholders+`)`)
		for _, id := range ops {
			args = append(args, id)
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM managed_lifecycle_batch_items WHERE `+strings.Join(clauses, " OR "), args...)
	return err
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
	if existing.State == "retired" {
		// Keep the source row stable because immutable snapshots reference it.
		// The terminated managed owner cannot reclaim its own retired route.
		if existing.SourceKind == "managed" && existing.OwnerIdentity == retiredManagedRecipientOwner(owner) {
			return &RecipientSourceConflictError{Recipient: recipient}
		}
		_, err = s.db.ExecContext(ctx, `UPDATE recipient_sources
			SET source_kind=?,owner_identity=?,state='reserved',conflict_source_kind=NULL,conflict_owner_identity=NULL,updated_at=?
			WHERE recipient_name=? AND state='retired'`, kind, owner, formatTimestamp(s.clock.Now()), recipient)
		return err
	}
	if existing.SourceKind == kind && existing.OwnerIdentity == owner && existing.State != "conflicted" {
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

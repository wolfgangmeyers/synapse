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
	"strings"
)

// LifecycleChild is the native subagent projection carried by a lifecycle
// frame. State is derived from the lifecycle event; status is native metadata.
type LifecycleChild struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// LifecycleCandidate is the validated lifecycle payload passed by transport.
type LifecycleCandidate struct {
	ParentAgentID   string
	InstanceID      string
	ParentSessionID string
	LeaseID         string
	ProducerEpoch   string
	ClientSequence  int64
	EventID         string
	EventKind       string
	TerminalReason  string
	PayloadHash     string
	Snapshot        bool
	Subagent        LifecycleChild
	Subagents       []LifecycleChild
}

type LifecycleApplyResult struct {
	Disposition       string
	RouterVersion     int64
	AppliedVersion    int64
	HasAppliedVersion bool
	Snapshot          LifecycleSnapshot
}

type LifecycleReplayUpdate struct {
	RouterVersion  int64
	ParentAgentID  string
	SubagentID     string
	State          string
	Type           string
	Description    string
	Status         string
	TerminalReason string
	HasType        bool
	HasDescription bool
	HasStatus      bool
	HasReason      bool
}

type LifecycleReplay struct {
	Mode          string
	RouterVersion int64
	Updates       []LifecycleReplayUpdate
	Projection    LifecycleProjection
}

type LifecycleAgent struct {
	AgentID         string
	SubagentVersion int64
	Subagents       []LifecycleChild
}

type LifecycleProjection struct {
	RouterVersion int64
	Agents        []LifecycleAgent
}

// LifecycleProducerInfo exposes the durable authority marker needed by the
// transport when a v2 producer has only terminal children left.
type LifecycleProducerInfo struct {
	ParentAgentID   string
	InstanceID      string
	ParentSessionID string
	ProducerEpoch   string
	LeaseID         string
	Authority       string
}

type lifecycleProducer struct {
	ParentAgentID         string
	InstanceID            string
	ParentSessionID       string
	ProducerEpoch         string
	LeaseID               string
	HighestClientSequence int64
	LastEventID           string
	LastEventHash         string
	LastSnapshotSequence  int64
	Authority             string
}

type lifecycleStateRow struct {
	ParentAgentID                                    string
	SubagentID                                       string
	InstanceID                                       string
	ParentSessionID                                  string
	ProducerEpoch                                    string
	Type, Description, Status, State, TerminalReason sql.NullString
	StartedAt, UpdatedAt, TerminalAt                 sql.NullString
	RouterVersion                                    int64
}

type lifecycleTransition struct {
	child  LifecycleChild
	state  string
	reason string
	kind   string
}

// ApplySubagentLifecycle atomically applies one validated Node-compatible
// lifecycle candidate and returns its disposition and current projection.
func (s *Store) ApplySubagentLifecycle(ctx context.Context, candidate LifecycleCandidate, allowLeaseRebind bool) (LifecycleApplyResult, error) {
	if ctx == nil {
		return LifecycleApplyResult{}, errors.New("context is required")
	}
	candidate = normalizeLifecycleCandidate(candidate)
	if candidate.ParentAgentID == "" || candidate.InstanceID == "" || candidate.ParentSessionID == "" || candidate.LeaseID == "" || candidate.ProducerEpoch == "" || candidate.EventID == "" || candidate.ClientSequence < 1 {
		return LifecycleApplyResult{}, errors.New("invalid lifecycle candidate")
	}
	candidate.PayloadHash = lifecyclePayloadHash(candidate)
	var result LifecycleApplyResult
	if err := s.runImmediate(ctx, func() error {
		return s.applyLifecycleCandidate(ctx, candidate, allowLeaseRebind, &result)
	}); err != nil {
		return LifecycleApplyResult{}, err
	}
	return result, nil
}

func (s *Store) applyLifecycleCandidate(ctx context.Context, candidate LifecycleCandidate, allowLeaseRebind bool, result *LifecycleApplyResult) error {
	producer, err := s.selectLifecycleProducer(ctx, candidate.ParentAgentID)
	if err != nil {
		return err
	}
	replacement, disposition := reconcileLifecycleProducer(producer, candidate, allowLeaseRebind)
	if disposition != "" {
		return s.lifecycleNoMutation(ctx, candidate.ParentAgentID, disposition, result, 0)
	}

	var priorVersion sql.NullInt64
	var priorHash string
	err = s.db.QueryRowContext(ctx, `SELECT router_version, payload_hash
		FROM subagent_lifecycle_events
		WHERE parent_agent_id = ? AND producer_epoch = ? AND event_id = ?`, candidate.ParentAgentID, candidate.ProducerEpoch, candidate.EventID).Scan(&priorVersion, &priorHash)
	if err == nil {
		disposition = "conflict"
		if priorHash == candidate.PayloadHash {
			disposition = "duplicate"
		}
		return s.lifecycleNoMutation(ctx, candidate.ParentAgentID, disposition, result, priorVersion.Int64)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if producer != nil && !replacement {
		if candidate.ClientSequence < producer.HighestClientSequence {
			return s.lifecycleNoMutation(ctx, candidate.ParentAgentID, "stale", result, 0)
		}
		if candidate.ClientSequence == producer.HighestClientSequence {
			disposition = "conflict"
			if producer.LastEventID == candidate.EventID && producer.LastEventHash == candidate.PayloadHash {
				disposition = "duplicate"
			}
			return s.lifecycleNoMutation(ctx, candidate.ParentAgentID, disposition, result, 0)
		}
	}

	transitions := make([]lifecycleTransition, 0)
	if replacement {
		rows, err := s.lifecycleStateRows(ctx, candidate.ParentAgentID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.State.String == "running" {
				transitions = append(transitions, lifecycleTransition{child: LifecycleChild{ID: row.SubagentID}, state: "terminal", reason: "producer_replaced", kind: "snapshot"})
			}
		}
	}
	if candidate.Snapshot {
		if err := s.snapshotTransitions(ctx, candidate, &transitions); err != nil {
			return err
		}
	} else {
		prior, err := s.lifecycleStateRow(ctx, candidate.ParentAgentID, candidate.Subagent.ID)
		if err != nil {
			return err
		}
		if !(prior != nil && prior.State.String == "terminal" && prior.ProducerEpoch == candidate.ProducerEpoch) &&
			!(prior != nil && prior.State.String == "running" && candidate.EventKind == "started" && prior.ProducerEpoch == candidate.ProducerEpoch && lifecycleMetadataEqual(prior, candidate.Subagent)) {
			transitions = append(transitions, lifecycleTransition{child: candidate.Subagent, state: map[bool]string{true: "running", false: "terminal"}[candidate.EventKind == "started"], reason: candidate.TerminalReason, kind: candidate.EventKind})
		}
	}

	lastVersion := int64(0)
	lastVersion, err = s.lifecycleVersion(ctx)
	if err != nil {
		return err
	}
	for index, transition := range transitions {
		eventID := candidate.EventID
		if index > 0 {
			eventID = derivedLifecycleEventID(candidate.EventID, index)
		}
		lastVersion, err = s.writeLifecycleTransition(ctx, candidate, transition, eventID, index)
		if err != nil {
			return err
		}
	}
	lastSnapshotSequence := int64(0)
	if producer != nil {
		lastSnapshotSequence = producer.LastSnapshotSequence
	}
	if candidate.Snapshot {
		lastSnapshotSequence = candidate.ClientSequence
	}
	if err := s.upsertLifecycleProducer(ctx, candidate, lastSnapshotSequence); err != nil {
		return err
	}
	if len(transitions) == 0 {
		return s.lifecycleNoMutation(ctx, candidate.ParentAgentID, "accepted_noop", result, lastVersion)
	}
	result.Disposition = "applied"
	result.RouterVersion = lastVersion
	result.Snapshot, err = s.subagentLifecycleSnapshotLocked(ctx, candidate.ParentAgentID)
	return err
}

func reconcileLifecycleProducer(producer *lifecycleProducer, candidate LifecycleCandidate, allowLeaseRebind bool) (bool, string) {
	if producer == nil {
		if candidate.Snapshot {
			return false, ""
		}
		return false, "conflict"
	}
	sameIdentity := producer.InstanceID == candidate.InstanceID && producer.ParentSessionID == candidate.ParentSessionID
	if producer.ProducerEpoch != candidate.ProducerEpoch {
		if sameIdentity {
			return false, "conflict"
		}
		if candidate.Snapshot {
			return true, ""
		}
		return false, "conflict"
	}
	if !sameIdentity {
		return false, "conflict"
	}
	if producer.LeaseID != candidate.LeaseID && !(candidate.Snapshot && allowLeaseRebind) {
		return false, "stale"
	}
	return false, ""
}

func (s *Store) snapshotTransitions(ctx context.Context, candidate LifecycleCandidate, transitions *[]lifecycleTransition) error {
	supplied := make(map[string]LifecycleChild, len(candidate.Subagents))
	for _, child := range candidate.Subagents {
		supplied[child.ID] = child
	}
	existing, err := s.lifecycleStateRows(ctx, candidate.ParentAgentID)
	if err != nil {
		return err
	}
	for _, child := range candidate.Subagents {
		prior := findLifecycleState(existing, child.ID)
		if prior != nil && prior.State.String == "terminal" && prior.ProducerEpoch == candidate.ProducerEpoch {
			continue
		}
		if prior != nil && prior.State.String == "running" && prior.ProducerEpoch == candidate.ProducerEpoch && lifecycleMetadataEqual(prior, child) {
			continue
		}
		*transitions = append(*transitions, lifecycleTransition{child: child, state: "running", kind: "snapshot"})
	}
	for _, row := range existing {
		if row.State.String == "running" && row.ProducerEpoch == candidate.ProducerEpoch {
			if _, ok := supplied[row.SubagentID]; !ok {
				*transitions = append(*transitions, lifecycleTransition{child: LifecycleChild{ID: row.SubagentID}, state: "terminal", reason: "snapshot_omitted", kind: "snapshot"})
			}
		}
	}
	return nil
}

func lifecycleMetadataEqual(row *lifecycleStateRow, child LifecycleChild) bool {
	return lifecycleNullableString(row.Type) == child.Type && lifecycleNullableString(row.Description) == child.Description && lifecycleNullableString(row.Status) == child.Status
}
func lifecycleNullableString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}
func findLifecycleState(rows []lifecycleStateRow, id string) *lifecycleStateRow {
	for index := range rows {
		if rows[index].SubagentID == id {
			return &rows[index]
		}
	}
	return nil
}

func (s *Store) writeLifecycleTransition(ctx context.Context, candidate LifecycleCandidate, transition lifecycleTransition, eventID string, index int) (int64, error) {
	child := transition.child
	var typ, description, status any
	if transition.state == "running" {
		typ, description, status = nullableAny(child.Type), nullableAny(child.Description), nullableAny(child.Status)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_events (
		parent_agent_id, instance_id, parent_session_id, producer_epoch, subagent_id,
		event_id, client_sequence, event_kind, state, type, description, status,
		terminal_reason, payload_hash, received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.ParentAgentID, candidate.InstanceID, candidate.ParentSessionID, candidate.ProducerEpoch, child.ID,
		eventID, candidate.ClientSequence*1000+int64(index), transition.kind, transition.state, typ, description, status,
		nullableAny(transition.reason), candidate.PayloadHash, formatTimestamp(s.clock.Now()))
	if err != nil {
		return 0, err
	}
	version, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_state (
		parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
		type, description, status, state, started_at, updated_at, terminal_at,
		terminal_reason, router_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (parent_agent_id, subagent_id) DO UPDATE SET
		instance_id = excluded.instance_id, parent_session_id = excluded.parent_session_id,
		producer_epoch = excluded.producer_epoch, type = excluded.type, description = excluded.description, status = excluded.status,
		state = excluded.state, started_at = CASE WHEN excluded.state = 'running' THEN excluded.started_at ELSE subagent_lifecycle_state.started_at END,
		updated_at = excluded.updated_at, terminal_at = excluded.terminal_at,
		terminal_reason = excluded.terminal_reason, router_version = excluded.router_version`,
		candidate.ParentAgentID, child.ID, candidate.InstanceID, candidate.ParentSessionID, candidate.ProducerEpoch,
		typ, description, status, transition.state, nullableAny(map[bool]string{true: formatTimestamp(s.clock.Now()), false: ""}[transition.state == "running"]), formatTimestamp(s.clock.Now()),
		nullableAny(map[bool]string{true: formatTimestamp(s.clock.Now()), false: ""}[transition.state == "terminal"]), nullableAny(transition.reason), version)
	return version, err
}

func nullableAny(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) upsertLifecycleProducer(ctx context.Context, candidate LifecycleCandidate, lastSnapshotSequence int64) error {
	now := formatTimestamp(s.clock.Now())
	_, err := s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_producers (
		parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id,
		highest_client_sequence, last_event_id, last_event_hash, last_snapshot_sequence,
		authority, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'v2', ?, ?, ?)
		ON CONFLICT (parent_agent_id) DO UPDATE SET
		instance_id = excluded.instance_id, parent_session_id = excluded.parent_session_id,
		producer_epoch = excluded.producer_epoch, lease_id = excluded.lease_id,
		highest_client_sequence = excluded.highest_client_sequence, last_event_id = excluded.last_event_id,
		last_event_hash = excluded.last_event_hash, last_snapshot_sequence = excluded.last_snapshot_sequence,
		authority = 'v2', last_seen_at = excluded.last_seen_at, updated_at = excluded.updated_at`,
		candidate.ParentAgentID, candidate.InstanceID, candidate.ParentSessionID, candidate.ProducerEpoch, candidate.LeaseID,
		candidate.ClientSequence, candidate.EventID, candidate.PayloadHash, lastSnapshotSequence, now, now, now)
	return err
}

func (s *Store) lifecycleNoMutation(ctx context.Context, parent, disposition string, result *LifecycleApplyResult, appliedVersion int64) error {
	if err := s.incrementLifecycleDiagnostic(ctx, parent, disposition); err != nil {
		return err
	}
	version, err := s.lifecycleVersion(ctx)
	if err != nil {
		return err
	}
	result.Disposition, result.RouterVersion = disposition, version
	if appliedVersion > 0 {
		result.AppliedVersion, result.HasAppliedVersion = appliedVersion, true
	}
	result.Snapshot, err = s.subagentLifecycleSnapshotLocked(ctx, parent)
	return err
}

func (s *Store) incrementLifecycleDiagnostic(ctx context.Context, parent, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO subagent_lifecycle_diagnostics (parent_agent_id, reason_code, count, last_router_version, updated_at)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT (parent_agent_id, reason_code) DO UPDATE SET
		count = MIN(10000, subagent_lifecycle_diagnostics.count + 1),
		last_router_version = excluded.last_router_version, updated_at = excluded.updated_at`, parent, reason, mustLifecycleVersion(ctx, s), formatTimestamp(s.clock.Now()))
	return err
}

func mustLifecycleVersion(ctx context.Context, s *Store) int64 {
	version, _ := s.lifecycleVersion(ctx)
	return version
}

type lifecycleHashBase struct {
	ParentAgentID   string `json:"parentAgentId"`
	InstanceID      string `json:"instanceId"`
	ParentSessionID string `json:"parentSessionId"`
	ProducerEpoch   string `json:"epoch"`
	ClientSequence  int64  `json:"sequence"`
	EventID         string `json:"eventId"`
}

type lifecycleHashSnapshot struct {
	lifecycleHashBase
	Subagents []LifecycleChild `json:"subagents"`
	EventKind string           `json:"eventKind"`
}

type lifecycleHashEvent struct {
	lifecycleHashBase
	Subagent       LifecycleChild `json:"subagent"`
	EventKind      string         `json:"eventKind"`
	TerminalReason string         `json:"terminalReason,omitempty"`
}

func normalizeLifecycleChild(child LifecycleChild) LifecycleChild {
	// Lifecycle payloads are normalized with JavaScript String.trim semantics.
	// Go's strings.TrimSpace removes U+0085, which JavaScript preserves.
	child.ID = strings.TrimFunc(child.ID, isECMAScriptTrimSpace)
	child.Type = strings.TrimFunc(child.Type, isECMAScriptTrimSpace)
	child.Description = strings.TrimFunc(child.Description, isECMAScriptTrimSpace)
	return child
}

func normalizeLifecycleCandidate(candidate LifecycleCandidate) LifecycleCandidate {
	if candidate.Snapshot {
		for index := range candidate.Subagents {
			candidate.Subagents[index] = normalizeLifecycleChild(candidate.Subagents[index])
		}
		candidate.TerminalReason = ""
		return candidate
	}
	candidate.Subagent = normalizeLifecycleChild(candidate.Subagent)
	if candidate.EventKind != "terminal" {
		// Node keeps terminal_reason out of started payloads and storage.
		candidate.TerminalReason = ""
	}
	return candidate
}

func lifecycleHashJSON(value any) []byte {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimSuffix(encoded.Bytes(), []byte{'\n'})
}

func lifecyclePayloadHash(candidate LifecycleCandidate) string {
	candidate = normalizeLifecycleCandidate(candidate)
	base := lifecycleHashBase{
		ParentAgentID: candidate.ParentAgentID, InstanceID: candidate.InstanceID, ParentSessionID: candidate.ParentSessionID,
		ProducerEpoch: candidate.ProducerEpoch, ClientSequence: candidate.ClientSequence, EventID: candidate.EventID,
	}
	var value any
	if candidate.Snapshot {
		value = lifecycleHashSnapshot{lifecycleHashBase: base, Subagents: candidate.Subagents, EventKind: "snapshot"}
	} else {
		value = lifecycleHashEvent{lifecycleHashBase: base, Subagent: candidate.Subagent, EventKind: candidate.EventKind, TerminalReason: candidate.TerminalReason}
	}
	sum := sha256.Sum256(lifecycleHashJSON(value))
	return hex.EncodeToString(sum[:])
}

func derivedLifecycleEventID(eventID string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", eventID, index)))
	return hex.EncodeToString(sum[:])
}

func (s *Store) selectLifecycleProducer(ctx context.Context, parent string) (*lifecycleProducer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id,
		highest_client_sequence, last_event_id, last_event_hash, last_snapshot_sequence, authority
		FROM subagent_lifecycle_producers WHERE parent_agent_id = ?`, parent)
	var producer lifecycleProducer
	var eventID, eventHash sql.NullString
	if err := row.Scan(&producer.ParentAgentID, &producer.InstanceID, &producer.ParentSessionID, &producer.ProducerEpoch, &producer.LeaseID, &producer.HighestClientSequence, &eventID, &eventHash, &producer.LastSnapshotSequence, &producer.Authority); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	producer.LastEventID, producer.LastEventHash = lifecycleNullableString(eventID), lifecycleNullableString(eventHash)
	return &producer, nil
}

// LifecycleProducerFor returns the durable producer identity and authority.
// A v2 producer remains meaningful when every child has reached terminal state.
func (s *Store) LifecycleProducerFor(ctx context.Context, parent string) (*LifecycleProducerInfo, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if parent == "" {
		return nil, errors.New("parent agent ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	producer, err := s.selectLifecycleProducer(ctx, parent)
	if err != nil || producer == nil {
		return nil, err
	}
	return &LifecycleProducerInfo{
		ParentAgentID: producer.ParentAgentID, InstanceID: producer.InstanceID,
		ParentSessionID: producer.ParentSessionID, ProducerEpoch: producer.ProducerEpoch,
		LeaseID: producer.LeaseID, Authority: producer.Authority,
	}, nil
}

// LifecycleAgentProjection reads one parent projection, including a zero-child
// agent with its durable version when the v2 producer has only tombstones.
func (s *Store) LifecycleAgentProjection(ctx context.Context, parent string) (LifecycleAgent, error) {
	if ctx == nil {
		return LifecycleAgent{}, errors.New("context is required")
	}
	if parent == "" {
		return LifecycleAgent{}, errors.New("parent agent ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return LifecycleAgent{}, errors.New("store is closed")
	}
	var latestState sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(router_version) FROM subagent_lifecycle_state WHERE parent_agent_id = ?`, parent).Scan(&latestState); err != nil {
		return LifecycleAgent{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subagent_id, type, description, status, router_version
		FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND state = 'running' ORDER BY subagent_id ASC`, parent)
	if err != nil {
		return LifecycleAgent{}, err
	}
	defer rows.Close()
	agent := LifecycleAgent{AgentID: parent, Subagents: []LifecycleChild{}}
	for rows.Next() {
		var id string
		var typ, description, status sql.NullString
		var version int64
		if err := rows.Scan(&id, &typ, &description, &status, &version); err != nil {
			return LifecycleAgent{}, err
		}
		if version > agent.SubagentVersion {
			agent.SubagentVersion = version
		}
		child := LifecycleChild{ID: id}
		if typ.Valid {
			child.Type = typ.String
		}
		if description.Valid {
			child.Description = description.String
		}
		if status.Valid {
			child.Status = status.String
		}
		agent.Subagents = append(agent.Subagents, child)
	}
	if err := rows.Err(); err != nil {
		return LifecycleAgent{}, err
	}
	if latestState.Valid && latestState.Int64 > agent.SubagentVersion {
		agent.SubagentVersion = latestState.Int64
	}
	if !latestState.Valid {
		version, err := s.lifecycleVersion(ctx)
		if err != nil {
			return LifecycleAgent{}, err
		}
		agent.SubagentVersion = version
	}
	return agent, nil
}

func (s *Store) lifecycleVersion(ctx context.Context) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'subagent_lifecycle_events'), 0)").Scan(&version)
	return version, err
}

func (s *Store) lifecycleStateRows(ctx context.Context, parent string) ([]lifecycleStateRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch,
		type, description, status, state, started_at, updated_at, terminal_at, terminal_reason, router_version
		FROM subagent_lifecycle_state WHERE parent_agent_id = ? ORDER BY subagent_id ASC`, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []lifecycleStateRow
	for rows.Next() {
		var row lifecycleStateRow
		if err := rows.Scan(&row.ParentAgentID, &row.SubagentID, &row.InstanceID, &row.ParentSessionID, &row.ProducerEpoch, &row.Type, &row.Description, &row.Status, &row.State, &row.StartedAt, &row.UpdatedAt, &row.TerminalAt, &row.TerminalReason, &row.RouterVersion); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
func (s *Store) lifecycleStateRow(ctx context.Context, parent, child string) (*lifecycleStateRow, error) {
	rows, err := s.lifecycleStateRows(ctx, parent)
	if err != nil {
		return nil, err
	}
	return findLifecycleState(rows, child), nil
}

func (s *Store) subagentLifecycleSnapshotLocked(ctx context.Context, parent string) (LifecycleSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subagent_id, type, description, status FROM subagent_lifecycle_state WHERE parent_agent_id = ? AND state = 'running' ORDER BY subagent_id ASC`, parent)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	defer rows.Close()
	snapshot := LifecycleSnapshot{Subagents: []map[string]any{}}
	for rows.Next() {
		var id, typ, desc, status sql.NullString
		if err := rows.Scan(&id, &typ, &desc, &status); err != nil {
			return LifecycleSnapshot{}, err
		}
		child := map[string]any{"id": id.String}
		if typ.Valid {
			child["type"] = typ.String
		}
		if desc.Valid {
			child["description"] = desc.String
		}
		if status.Valid {
			child["status"] = status.String
		}
		snapshot.Subagents = append(snapshot.Subagents, child)
	}
	return snapshot, rows.Err()
}

func (s *Store) LifecycleProjection(ctx context.Context) (LifecycleProjection, error) {
	if ctx == nil {
		return LifecycleProjection{}, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return LifecycleProjection{}, errors.New("store is closed")
	}
	return s.lifecycleProjectionRead(ctx)
}

func (s *Store) lifecycleProjectionRead(ctx context.Context) (LifecycleProjection, error) {
	version, err := s.lifecycleVersion(ctx)
	if err != nil {
		return LifecycleProjection{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT parent_agent_id, subagent_id, type, description, status, router_version
		FROM subagent_lifecycle_state WHERE state = 'running' ORDER BY parent_agent_id ASC, subagent_id ASC`)
	if err != nil {
		return LifecycleProjection{}, err
	}
	defer rows.Close()
	projection := LifecycleProjection{RouterVersion: version, Agents: []LifecycleAgent{}}
	byParent := map[string]int{}
	for rows.Next() {
		var parent, id string
		var typ, desc, status sql.NullString
		var rowVersion int64
		if err := rows.Scan(&parent, &id, &typ, &desc, &status, &rowVersion); err != nil {
			return LifecycleProjection{}, err
		}
		index, ok := byParent[parent]
		if !ok {
			projection.Agents = append(projection.Agents, LifecycleAgent{AgentID: parent, Subagents: []LifecycleChild{}})
			index = len(projection.Agents) - 1
			byParent[parent] = index
		}
		agent := &projection.Agents[index]
		if rowVersion > agent.SubagentVersion {
			agent.SubagentVersion = rowVersion
		}
		child := LifecycleChild{ID: id}
		if typ.Valid {
			child.Type = typ.String
		}
		if desc.Valid {
			child.Description = desc.String
		}
		if status.Valid {
			child.Status = status.String
		}
		agent.Subagents = append(agent.Subagents, child)
	}
	return projection, rows.Err()
}

func (s *Store) ReplaySubagentLifecycle(ctx context.Context, afterVersion int64) (LifecycleReplay, error) {
	if ctx == nil {
		return LifecycleReplay{}, errors.New("context is required")
	}
	if afterVersion < 0 {
		return LifecycleReplay{}, errors.New("after_version must be non-negative")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return LifecycleReplay{}, errors.New("store is closed")
	}
	latest, err := s.lifecycleVersion(ctx)
	if err != nil {
		return LifecycleReplay{}, err
	}
	var oldest sql.NullInt64
	err = s.db.QueryRowContext(ctx, "SELECT MIN(router_version) FROM subagent_lifecycle_events").Scan(&oldest)
	if err != nil {
		return LifecycleReplay{}, err
	}
	fallback := (oldest.Valid && afterVersion < oldest.Int64-1) || (!oldest.Valid && afterVersion < latest)
	if fallback {
		projection, err := s.lifecycleProjectionRead(ctx)
		projection.RouterVersion = latest
		return LifecycleReplay{Mode: "snapshot", RouterVersion: latest, Projection: projection}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT router_version, parent_agent_id, subagent_id, state, type, description, status, terminal_reason FROM subagent_lifecycle_events WHERE router_version > ? ORDER BY router_version ASC`, afterVersion)
	if err != nil {
		return LifecycleReplay{}, err
	}
	var updates []LifecycleReplayUpdate
	for rows.Next() {
		var update LifecycleReplayUpdate
		var typ, desc, status, reason sql.NullString
		if err := rows.Scan(&update.RouterVersion, &update.ParentAgentID, &update.SubagentID, &update.State, &typ, &desc, &status, &reason); err != nil {
			_ = rows.Close()
			return LifecycleReplay{}, err
		}
		update.Type, update.Description, update.Status, update.TerminalReason = lifecycleNullableString(typ), lifecycleNullableString(desc), lifecycleNullableString(status), lifecycleNullableString(reason)
		update.HasType, update.HasDescription, update.HasStatus, update.HasReason = typ.Valid, desc.Valid, status.Valid, reason.Valid
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return LifecycleReplay{}, err
	}
	if err := rows.Close(); err != nil {
		return LifecycleReplay{}, err
	}
	projection, err := s.lifecycleProjectionRead(ctx)
	if err != nil {
		return LifecycleReplay{}, err
	}
	return LifecycleReplay{Mode: "replay", RouterVersion: latest, Updates: updates, Projection: projection}, nil
}

// PayloadHash is exposed for deterministic differential fixtures and does not
// include the transport lease, matching the Node idempotency boundary.
func PayloadHash(candidate LifecycleCandidate) string { return lifecyclePayloadHash(candidate) }

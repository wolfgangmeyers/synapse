package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
)

// Phase 2A keeps the bridge ledger bounded before any bridge transport exists.
const (
	ManagedBridgeRetentionAge   = 24 * time.Hour
	ManagedBridgeRetentionCount = 50_000
	ManagedBridgeRetentionBytes = 64 * 1024 * 1024
	ManagedBridgePayloadLimit   = managedbridge.MaxPayloadBytes
	managedBridgeIdentitySelect = `SELECT bridge_id,agent_id,verifier,enabled,revoked,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,command_next_seq,command_acked_seq,command_floor,event_admitted_next,event_admitted_floor,event_applied_next,created_at,updated_at FROM managed_bridges`
	managedBridgeCommandSelect  = `SELECT bridge_id,seq,request_id,command_name,payload_hash,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,state,created_at,updated_at,acked_at,size_bytes FROM managed_bridge_commands`
	managedBridgeEventSelect    = `SELECT bridge_id,seq,event_id,event_name,payload_hash,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,state,created_at,updated_at,admitted_at,applied_at,size_bytes FROM managed_bridge_events`
)

var (
	ErrManagedBridgeInvalid          = errors.New("managed bridge input is invalid")
	ErrManagedBridgeAuthRejected     = errors.New("managed bridge authentication was rejected")
	ErrManagedBridgeStale            = errors.New("managed bridge fence is stale")
	ErrManagedBridgeConflict         = errors.New("managed bridge identity conflicts")
	ErrManagedBridgeSequenceGap      = errors.New("managed bridge sequence gap")
	ErrManagedBridgeSequenceConflict = errors.New("managed bridge sequence conflicts")
	ErrManagedBridgeReplayGap        = errors.New("managed bridge replay gap")
	ErrManagedBridgeSequenceOverflow = errors.New("managed bridge sequence exhausted")
	ErrManagedBridgeCapacity         = errors.New("managed bridge retention capacity is live")
	ErrManagedBridgeCommandNotFound  = errors.New("managed bridge command was not found")
	ErrManagedBridgeEventNotFound    = errors.New("managed bridge event was not found")
	ErrManagedBridgeRevoked          = errors.New("managed bridge is revoked")
)

// ManagedBridgeReservation is the one-time bridge identity reservation. The
// verifier is a derived 32-byte secret; a plaintext token is never accepted.
type ManagedBridgeReservation struct {
	AgentID  string
	BridgeID string
	Verifier []byte
	Fence    ManagedRuntimeFence
}

// ManagedBridgeHandshake is checked in one SQLite transaction against both
// the bridge reservation and the current managed-agent fence.
type ManagedBridgeHandshake struct {
	AgentID  string
	BridgeID string
	Verifier []byte
	Fence    ManagedRuntimeFence
}

// ManagedBridgeIdentity is safe to return to callers. It deliberately omits
// the verifier and all command/event payloads.
type ManagedBridgeIdentity struct {
	AgentID      string
	BridgeID     string
	Enabled      bool
	Revoked      bool
	Fence        ManagedRuntimeFence
	CommandNext  uint64
	CommandAcked uint64
	CommandFloor uint64
	EventNext    uint64
	EventFloor   uint64
	EventApplied uint64
	CreatedAt    string
	UpdatedAt    string
}

// ManagedBridgeCursors keeps command allocation, event admission, and event
// application as separate cursor domains.
type ManagedBridgeCursors struct {
	CommandNext        uint64
	CommandAcked       uint64
	CommandFloor       uint64
	EventAdmittedNext  uint64
	EventAdmittedFloor uint64
	EventAppliedNext   uint64
}

// ManagedBridgeCommandInput is validated before its payload reaches SQLite.
type ManagedBridgeCommandInput struct {
	BridgeID    string
	RequestID   string
	Name        string
	Payload     []byte
	PayloadHash string
	Fence       ManagedRuntimeFence
}

// ManagedBridgeCommand excludes payload bytes while retaining the metadata
// needed by a later bridge sender and safe diagnostics.
type ManagedBridgeCommand struct {
	BridgeID    string
	Seq         uint64
	RequestID   string
	Name        string
	PayloadHash string
	Fence       ManagedRuntimeFence
	FenceHash   string
	State       string
	CreatedAt   string
	UpdatedAt   string
	AckedAt     string
	SizeBytes   int
}

// ManagedBridgeEventInput is the inbound Phase 0 event boundary.
type ManagedBridgeEventInput struct {
	BridgeID    string
	Seq         uint64
	EventID     string
	Name        string
	Payload     []byte
	PayloadHash string
	Fence       ManagedRuntimeFence
}

// ManagedBridgeEvent excludes payload bytes while exposing admission state.
type ManagedBridgeEvent struct {
	BridgeID    string
	Seq         uint64
	EventID     string
	Name        string
	PayloadHash string
	Fence       ManagedRuntimeFence
	FenceHash   string
	State       string
	CreatedAt   string
	UpdatedAt   string
	AdmittedAt  string
	AppliedAt   string
	SizeBytes   int
}

type ManagedBridgeRetentionResult struct {
	CommandsRemoved int
	EventsRemoved   int
	CommandFloor    uint64
	CommandAcked    uint64
	EventFloor      uint64
	CapacityLive    bool
}

func (ManagedBridgeReservation) String() string    { return "managed bridge reservation [redacted]" }
func (ManagedBridgeReservation) GoString() string  { return "managed bridge reservation [redacted]" }
func (ManagedBridgeHandshake) String() string      { return "managed bridge handshake [redacted]" }
func (ManagedBridgeHandshake) GoString() string    { return "managed bridge handshake [redacted]" }
func (ManagedBridgeIdentity) String() string       { return "managed bridge identity [redacted]" }
func (ManagedBridgeIdentity) GoString() string     { return "managed bridge identity [redacted]" }
func (ManagedBridgeCommandInput) String() string   { return "managed bridge command input [redacted]" }
func (ManagedBridgeCommandInput) GoString() string { return "managed bridge command input [redacted]" }
func (ManagedBridgeCommand) String() string        { return "managed bridge command [redacted]" }
func (ManagedBridgeCommand) GoString() string      { return "managed bridge command [redacted]" }
func (ManagedBridgeEventInput) String() string     { return "managed bridge event input [redacted]" }
func (ManagedBridgeEventInput) GoString() string   { return "managed bridge event input [redacted]" }
func (ManagedBridgeEvent) String() string          { return "managed bridge event [redacted]" }
func (ManagedBridgeEvent) GoString() string        { return "managed bridge event [redacted]" }
func (ManagedBridgeCursors) String() string        { return "managed bridge cursors [redacted]" }
func (ManagedBridgeCursors) GoString() string      { return "managed bridge cursors [redacted]" }
func (ManagedBridgeRetentionResult) String() string {
	return "managed bridge retention result [redacted]"
}
func (ManagedBridgeRetentionResult) GoString() string {
	return "managed bridge retention result [redacted]"
}
func (ManagedRuntimeFence) String() string   { return "managed runtime fence [redacted]" }
func (ManagedRuntimeFence) GoString() string { return "managed runtime fence [redacted]" }

// ReserveManagedBridge binds one bridge ID to the current durable managed
// runtime. A stale runtime or an existing conflicting reservation is a
// no-mutation failure.
func (s *Store) ReserveManagedBridge(ctx context.Context, reservation ManagedBridgeReservation) (ManagedBridgeIdentity, error) {
	if err := validateBridgeReservation(reservation); err != nil {
		return ManagedBridgeIdentity{}, err
	}
	var result ManagedBridgeIdentity
	err := s.runImmediate(ctx, func() error {
		if err := s.requireCurrentManagedFenceTx(ctx, reservation.AgentID, reservation.Fence); err != nil {
			return err
		}
		row, err := managedBridgeForTx(ctx, s.db, reservation.BridgeID)
		if errors.Is(err, sql.ErrNoRows) {
			var existingID string
			err = s.db.QueryRowContext(ctx, `SELECT bridge_id FROM managed_bridges WHERE agent_id=?`, reservation.AgentID).Scan(&existingID)
			if err == nil {
				return ErrManagedBridgeConflict
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			now := formatTimestamp(s.clock.Now())
			fenceHash, hashErr := managedBridgeFenceHash(reservation.Fence)
			if hashErr != nil {
				return hashErr
			}
			_, err = s.db.ExecContext(ctx, `INSERT INTO managed_bridges (bridge_id,agent_id,verifier,enabled,revoked,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,created_at,updated_at) VALUES (?,?,?,1,0,?,?,?,?,?,?,?,?,?,?,?,?)`, reservation.BridgeID, reservation.AgentID, append([]byte(nil), reservation.Verifier...), reservation.Fence.AgentID, reservation.Fence.PiSessionID, reservation.Fence.SessionEpoch, reservation.Fence.ProcessPID, reservation.Fence.ProcessStartMarker, reservation.Fence.ProcessIncarnation, reservation.Fence.RuntimeGeneration, reservation.Fence.ManifestRevision, reservation.Fence.ManifestHash, fenceHash, now, now)
			if err != nil {
				if isConstraintError(err) {
					return ErrManagedBridgeConflict
				}
				return err
			}
			row, err = managedBridgeForTx(ctx, s.db, reservation.BridgeID)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			if row.AgentID != reservation.AgentID || row.Revoked || !row.Enabled || !sameManagedRestartFence(row.Fence, reservation.Fence) || subtle.ConstantTimeCompare(row.Verifier, reservation.Verifier) != 1 {
				return ErrManagedBridgeConflict
			}
			if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_bridges WHERE agent_id=? AND bridge_id<>?`, reservation.AgentID, reservation.BridgeID).Scan(new(int)); err == nil {
				return ErrManagedBridgeConflict
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		result, err = row.identity()
		return err
	})
	return result, err
}

// AuthenticateManagedBridge performs the handshake fence and verifier check
// without changing state on any rejected or stale input.
// ReserveManagedBridgeWithVerifier persists the derived verifier held by the
// Phase 0 authentication package. The original token never crosses this API.
func (s *Store) ReserveManagedBridgeWithVerifier(ctx context.Context, agentID, bridgeID string, verifier managedbridge.Verifier, fence ManagedRuntimeFence) (ManagedBridgeIdentity, error) {
	bytes, err := verifier.Bytes()
	if err != nil {
		return ManagedBridgeIdentity{}, ErrManagedBridgeInvalid
	}
	return s.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: agentID, BridgeID: bridgeID, Verifier: bytes, Fence: fence})
}

func (s *Store) AuthenticateManagedBridge(ctx context.Context, handshake ManagedBridgeHandshake) (ManagedBridgeIdentity, error) {
	if err := validateBridgeHandshake(handshake); err != nil {
		return ManagedBridgeIdentity{}, err
	}
	var result ManagedBridgeIdentity
	err := s.runImmediate(ctx, func() error {
		row, err := managedBridgeForTx(ctx, s.db, handshake.BridgeID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrManagedBridgeAuthRejected
		}
		if err != nil {
			return err
		}
		if row.AgentID != handshake.AgentID || row.Revoked || !row.Enabled || subtle.ConstantTimeCompare(row.Verifier, handshake.Verifier) != 1 || !sameManagedRestartFence(row.Fence, handshake.Fence) {
			return ErrManagedBridgeAuthRejected
		}
		if err := s.requireCurrentManagedFenceTx(ctx, handshake.AgentID, handshake.Fence); err != nil {
			if errors.Is(err, ErrManagedBridgeStale) {
				return ErrManagedBridgeAuthRejected
			}
			return err
		}
		result, err = row.identity()
		return err
	})
	return result, err
}

func (s *Store) AuthenticateManagedBridgeWithVerifier(ctx context.Context, agentID, bridgeID string, verifier managedbridge.Verifier, fence ManagedRuntimeFence) (ManagedBridgeIdentity, error) {
	bytes, err := verifier.Bytes()
	if err != nil {
		return ManagedBridgeIdentity{}, ErrManagedBridgeAuthRejected
	}
	return s.AuthenticateManagedBridge(ctx, ManagedBridgeHandshake{AgentID: agentID, BridgeID: bridgeID, Verifier: bytes, Fence: fence})
}

// RevokeManagedBridge is a private manager-store mutation. The full fence is
// required so a stale manager cannot revoke a bridge owned by a later runtime.
func (s *Store) RevokeManagedBridge(ctx context.Context, bridgeID string, fence ManagedRuntimeFence) error {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) {
		return ErrManagedBridgeInvalid
	}
	return s.runImmediate(ctx, func() error {
		if _, err := s.requireBridgeFenceTx(ctx, bridgeID, fence); err != nil {
			return err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE managed_bridges SET enabled=0,revoked=1,updated_at=? WHERE bridge_id=?`, formatTimestamp(s.clock.Now()), bridgeID)
		return err
	})
}

// ManagedBridgeCursors is a private, read-only manager-store projection. It
// intentionally has no fence argument because it mutates no bridge state.
func (s *Store) ManagedBridgeCursors(ctx context.Context, bridgeID string) (ManagedBridgeCursors, error) {
	if !validBridgeID(bridgeID) {
		return ManagedBridgeCursors{}, ErrManagedBridgeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManagedBridgeCursors{}, errors.New("store is closed")
	}
	row, err := managedBridgeForTx(ctx, s.db, bridgeID)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedBridgeCursors{}, ErrManagedBridgeAuthRejected
	}
	if err != nil {
		return ManagedBridgeCursors{}, err
	}
	return row.cursors()
}

// QueueManagedBridgeCommand allocates the next command sequence in the
// command domain and commits the command plus watermark atomically.
func (s *Store) QueueManagedBridgeCommand(ctx context.Context, input ManagedBridgeCommandInput) (ManagedBridgeCommand, error) {
	if err := validateBridgeCommandInput(input); err != nil {
		return ManagedBridgeCommand{}, err
	}
	if input.PayloadHash == "" {
		input.PayloadHash, _ = managedbridge.HashPayload(input.Payload)
	}
	var result ManagedBridgeCommand
	err := s.runImmediate(ctx, func() error {
		row, err := s.requireBridgeFenceTx(ctx, input.BridgeID, input.Fence)
		if err != nil {
			return err
		}
		var existing managedBridgeCommandRow
		existingErr := scanManagedBridgeCommand(s.db.QueryRowContext(ctx, managedBridgeCommandSelect+` WHERE bridge_id=? AND request_id=? AND command_name=?`, input.BridgeID, input.RequestID, input.Name), &existing)
		if existingErr == nil {
			if existing.Name != input.Name || existing.PayloadHash != input.PayloadHash || !sameManagedRestartFence(existing.Fence, input.Fence) {
				return ErrManagedBridgeSequenceConflict
			}
			result = existing.public()
			return nil
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return existingErr
		}
		seq, err := parseBridgeSeq(row.CommandNext)
		if err != nil {
			return err
		}
		if seq == math.MaxUint64 {
			return ErrManagedBridgeSequenceOverflow
		}
		if _, err := pruneBridgeDirectionTx(ctx, s.db, input.BridgeID, true, s.clock.Now(), len(input.Payload)); err != nil {
			return err
		}
		live, err := bridgeDirectionCapacityTx(ctx, s.db, input.BridgeID, true, len(input.Payload))
		if err != nil {
			return err
		}
		if live {
			return ErrManagedBridgeCapacity
		}
		now := formatTimestamp(s.clock.Now())
		payloadHash := input.PayloadHash
		if payloadHash == "" {
			payloadHash, err = managedbridge.HashPayload(input.Payload)
			if err != nil {
				return ErrManagedBridgeInvalid
			}
		}
		fenceHash, err := managedBridgeFenceHash(input.Fence)
		if err != nil {
			return err
		}
		seqText := formatBridgeSeq(seq)
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_bridge_commands (bridge_id,seq,request_id,command_name,payload,payload_hash,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,state,created_at,updated_at,size_bytes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.BridgeID, seqText, input.RequestID, input.Name, append([]byte(nil), input.Payload...), payloadHash, input.Fence.AgentID, input.Fence.PiSessionID, input.Fence.SessionEpoch, input.Fence.ProcessPID, input.Fence.ProcessStartMarker, input.Fence.ProcessIncarnation, input.Fence.RuntimeGeneration, input.Fence.ManifestRevision, input.Fence.ManifestHash, fenceHash, "queued", now, now, len(input.Payload))
		if err != nil {
			return err
		}
		if err := s.injectManagedBridgeFault("command_before_cursor"); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_bridges SET command_next_seq=?,updated_at=? WHERE bridge_id=?`, formatBridgeSeq(seq+1), now, input.BridgeID)
		if err != nil {
			return err
		}
		if err := scanManagedBridgeCommand(s.db.QueryRowContext(ctx, managedBridgeCommandSelect+` WHERE bridge_id=? AND seq=?`, input.BridgeID, seqText), &existing); err != nil {
			return err
		}
		result = existing.public()
		return nil
	})
	return result, err
}

// QueueManagedBridgeCommandFrame is the strict Phase 0 frame boundary used by
// the future bridge server. The store persists only its bounded payload data.
func (s *Store) QueueManagedBridgeCommandFrame(ctx context.Context, frame managedbridge.Command) (ManagedBridgeCommand, error) {
	if frame.Validate() != nil {
		return ManagedBridgeCommand{}, ErrManagedBridgeInvalid
	}
	return s.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: frame.BridgeInstance, RequestID: frame.ID, Name: frame.Name, Payload: append([]byte(nil), frame.Payload...), PayloadHash: frame.Hash, Fence: managedBridgeFence(frame.Fence)})
}

func (s *Store) ReadManagedBridgeCommandPayload(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence) ([]byte, error) {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) || seq == 0 {
		return nil, ErrManagedBridgeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if _, err := s.requireBridgeFenceLocked(ctx, bridgeID, fence); err != nil {
		return nil, err
	}
	var payload []byte
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM managed_bridge_commands WHERE bridge_id=? AND seq=?`, bridgeID, formatBridgeSeq(seq)).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrManagedBridgeCommandNotFound
	} else if err != nil {
		return nil, err
	}
	return append([]byte(nil), payload...), nil
}

// ReplayManagedBridgeCommands returns metadata for retained commands at or
// after fromSeq. Payload bytes are fetched through the separate bounded read.
func (s *Store) ReplayManagedBridgeCommands(ctx context.Context, bridgeID string, fromSeq uint64, fence ManagedRuntimeFence) ([]ManagedBridgeCommand, error) {
	if !validBridgeID(bridgeID) || fromSeq == 0 || !validManagedRuntimeFence(fence) {
		return nil, ErrManagedBridgeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	row, err := s.requireBridgeFenceLocked(ctx, bridgeID, fence)
	if err != nil {
		return nil, err
	}
	floor, err := parseBridgeSeq(row.CommandFloor)
	if err != nil {
		return nil, err
	}
	if fromSeq < floor {
		return nil, ErrManagedBridgeReplayGap
	}
	rows, err := s.db.QueryContext(ctx, managedBridgeCommandSelect+` WHERE bridge_id=? ORDER BY rowid ASC`, bridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedBridgeCommand, 0)
	for rows.Next() {
		var value managedBridgeCommandRow
		if err := scanManagedBridgeCommand(rows, &value); err != nil {
			return nil, err
		}
		if value.Seq >= fromSeq {
			result = append(result, value.public())
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// ReplayManagedBridgeEvents is the event counterpart to command replay. It
// reports a replay gap once the requested sequence is below the retained floor.
func (s *Store) ReplayManagedBridgeEvents(ctx context.Context, bridgeID string, fromSeq uint64, fence ManagedRuntimeFence) ([]ManagedBridgeEvent, error) {
	if !validBridgeID(bridgeID) || fromSeq == 0 || !validManagedRuntimeFence(fence) {
		return nil, ErrManagedBridgeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	row, err := s.requireBridgeFenceLocked(ctx, bridgeID, fence)
	if err != nil {
		return nil, err
	}
	floor, err := parseBridgeSeq(row.EventAdmittedFloor)
	if err != nil {
		return nil, err
	}
	if fromSeq < floor {
		return nil, ErrManagedBridgeReplayGap
	}
	rows, err := s.db.QueryContext(ctx, managedBridgeEventSelect+` WHERE bridge_id=? ORDER BY rowid ASC`, bridgeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ManagedBridgeEvent, 0)
	for rows.Next() {
		var value managedBridgeEventRow
		if err := scanManagedBridgeEvent(rows, &value); err != nil {
			return nil, err
		}
		if value.Seq >= fromSeq {
			result = append(result, value.public())
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) ReadManagedBridgeEventPayload(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence) ([]byte, error) {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) || seq == 0 {
		return nil, ErrManagedBridgeInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if _, err := s.requireBridgeFenceLocked(ctx, bridgeID, fence); err != nil {
		return nil, err
	}
	var payload []byte
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM managed_bridge_events WHERE bridge_id=? AND seq=?`, bridgeID, formatBridgeSeq(seq)).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrManagedBridgeEventNotFound
	} else if err != nil {
		return nil, err
	}
	return append([]byte(nil), payload...), nil
}

// AcknowledgeManagedBridgeCommand advances the durable cumulative ACK cursor.
// Every command from sequence one through seq must still exist and be queued or
// already acked. The validation, state updates, and cursor advance are atomic.
func (s *Store) AcknowledgeManagedBridgeCommand(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence) error {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) || seq == 0 {
		return ErrManagedBridgeInvalid
	}
	return s.runImmediate(ctx, func() error {
		row, err := s.requireBridgeFenceTx(ctx, bridgeID, fence)
		if err != nil {
			return err
		}
		next, err := parseBridgeSeq(row.CommandNext)
		if err != nil {
			return err
		}
		acked, err := parseBridgeCursor(row.CommandAcked)
		if err != nil {
			return err
		}
		if seq >= next {
			return ErrManagedBridgeSequenceGap
		}
		if seq <= acked {
			return nil
		}

		rows, err := s.db.QueryContext(ctx, `SELECT seq,state FROM managed_bridge_commands WHERE bridge_id=? ORDER BY length(seq),seq`, bridgeID)
		if err != nil {
			return err
		}
		type commandState struct {
			seq   uint64
			state string
		}
		states := make([]commandState, 0)
		for rows.Next() {
			var seqText, state string
			if err := rows.Scan(&seqText, &state); err != nil {
				_ = rows.Close()
				return err
			}
			current, err := parseBridgeSeq(seqText)
			if err != nil {
				_ = rows.Close()
				return err
			}
			if current > seq {
				break
			}
			states = append(states, commandState{seq: current, state: state})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		expected := acked + 1
		for _, current := range states {
			if current.seq <= acked {
				continue
			}
			if current.seq != expected {
				return ErrManagedBridgeSequenceGap
			}
			if current.state == "unknown" {
				return ErrManagedBridgeSequenceConflict
			}
			if current.state != "queued" && current.state != "acked" {
				return ErrManagedBridgeSequenceConflict
			}
			expected++
		}
		if expected != seq+1 {
			return ErrManagedBridgeSequenceGap
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_bridge_commands SET state='acked',acked_at=COALESCE(acked_at,?),updated_at=? WHERE bridge_id=? AND state='queued' AND (length(seq) < length(?) OR (length(seq)=length(?) AND seq <= ?))`, now, now, bridgeID, formatBridgeSeq(seq), formatBridgeSeq(seq), formatBridgeSeq(seq)); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_bridges SET command_acked_seq=?,updated_at=? WHERE bridge_id=?`, formatBridgeSeq(seq), now, bridgeID)
		return err
	})
}

func (s *Store) MarkManagedBridgeCommandUnknown(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence) error {
	return s.transitionManagedBridgeCommand(ctx, bridgeID, seq, fence, "unknown")
}

func (s *Store) transitionManagedBridgeCommand(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence, state string) error {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) || seq == 0 || (state != "acked" && state != "unknown") {
		return ErrManagedBridgeInvalid
	}
	return s.runImmediate(ctx, func() error {
		row, err := s.requireBridgeFenceTx(ctx, bridgeID, fence)
		if err != nil {
			return err
		}
		next, err := parseBridgeSeq(row.CommandNext)
		if err != nil {
			return err
		}
		if seq >= next {
			return ErrManagedBridgeSequenceGap
		}
		var current string
		if err := s.db.QueryRowContext(ctx, `SELECT state FROM managed_bridge_commands WHERE bridge_id=? AND seq=?`, bridgeID, formatBridgeSeq(seq)).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			if floor, floorErr := parseBridgeSeq(row.CommandFloor); floorErr == nil && seq < floor {
				return ErrManagedBridgeReplayGap
			}
			return ErrManagedBridgeCommandNotFound
		} else if err != nil {
			return err
		}
		if current == state || (state == "acked" && current == "acked") {
			return nil
		}
		if current != "queued" {
			return ErrManagedBridgeSequenceConflict
		}
		ackedAt := any(nil)
		if state == "acked" {
			ackedAt = formatTimestamp(s.clock.Now())
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_bridge_commands SET state=?,acked_at=?,updated_at=? WHERE bridge_id=? AND seq=?`, state, ackedAt, formatTimestamp(s.clock.Now()), bridgeID, formatBridgeSeq(seq))
		return err
	})
}

// AdmitManagedBridgeEvent stores the event and advances the admitted cursor in
// one transaction. The returned event is ACK-eligible only after that commit.
func (s *Store) AdmitManagedBridgeEvent(ctx context.Context, input ManagedBridgeEventInput) (ManagedBridgeEvent, error) {
	if err := validateBridgeEventInput(input); err != nil {
		return ManagedBridgeEvent{}, err
	}
	if input.PayloadHash == "" {
		input.PayloadHash, _ = managedbridge.HashPayload(input.Payload)
	}
	var result ManagedBridgeEvent
	err := s.runImmediate(ctx, func() error {
		row, err := s.requireBridgeFenceTx(ctx, input.BridgeID, input.Fence)
		if err != nil {
			return err
		}
		next, err := parseBridgeSeq(row.EventAdmittedNext)
		if err != nil {
			return err
		}
		floor, err := parseBridgeSeq(row.EventAdmittedFloor)
		if err != nil {
			return err
		}
		if input.Seq < next {
			if input.Seq < floor {
				return ErrManagedBridgeReplayGap
			}
			var existing managedBridgeEventRow
			if scanManagedBridgeEvent(s.db.QueryRowContext(ctx, managedBridgeEventSelect+` WHERE bridge_id=? AND seq=?`, input.BridgeID, formatBridgeSeq(input.Seq)), &existing) != nil {
				return ErrManagedBridgeReplayGap
			}
			if existing.EventID != input.EventID || existing.Name != input.Name || existing.PayloadHash != input.PayloadHash || !sameManagedRestartFence(existing.Fence, input.Fence) {
				return ErrManagedBridgeSequenceConflict
			}
			result = existing.public()
			return nil
		}
		if input.Seq > next {
			return ErrManagedBridgeSequenceGap
		}
		if input.Seq == math.MaxUint64 {
			return ErrManagedBridgeSequenceOverflow
		}
		var byID managedBridgeEventRow
		if err := scanManagedBridgeEvent(s.db.QueryRowContext(ctx, managedBridgeEventSelect+` WHERE bridge_id=? AND event_id=?`, input.BridgeID, input.EventID), &byID); err == nil {
			if byID.Seq == input.Seq && byID.Name == input.Name && byID.PayloadHash == input.PayloadHash {
				result = byID.public()
				return nil
			}
			return ErrManagedBridgeSequenceConflict
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := pruneBridgeDirectionTx(ctx, s.db, input.BridgeID, false, s.clock.Now(), len(input.Payload)); err != nil {
			return err
		}
		live, err := bridgeDirectionCapacityTx(ctx, s.db, input.BridgeID, false, len(input.Payload))
		if err != nil {
			return err
		}
		if live {
			return ErrManagedBridgeCapacity
		}
		now := formatTimestamp(s.clock.Now())
		payloadHash := input.PayloadHash
		if payloadHash == "" {
			payloadHash, err = managedbridge.HashPayload(input.Payload)
			if err != nil {
				return ErrManagedBridgeInvalid
			}
		}
		fenceHash, err := managedBridgeFenceHash(input.Fence)
		if err != nil {
			return err
		}
		seqText := formatBridgeSeq(input.Seq)
		_, err = s.db.ExecContext(ctx, `INSERT INTO managed_bridge_events (bridge_id,seq,event_id,event_name,payload,payload_hash,fence_agent_id,fence_pi_session_id,fence_session_epoch,fence_process_pid,fence_process_start_marker,fence_process_incarnation,fence_runtime_generation,fence_manifest_revision,fence_manifest_hash,fence_hash,state,created_at,updated_at,admitted_at,size_bytes) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.BridgeID, seqText, input.EventID, input.Name, append([]byte(nil), input.Payload...), payloadHash, input.Fence.AgentID, input.Fence.PiSessionID, input.Fence.SessionEpoch, input.Fence.ProcessPID, input.Fence.ProcessStartMarker, input.Fence.ProcessIncarnation, input.Fence.RuntimeGeneration, input.Fence.ManifestRevision, input.Fence.ManifestHash, fenceHash, "admitted", now, now, now, len(input.Payload))
		if err != nil {
			return err
		}
		if err := s.injectManagedBridgeFault("event_before_cursor"); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_bridges SET event_admitted_next=?,updated_at=? WHERE bridge_id=?`, formatBridgeSeq(input.Seq+1), now, input.BridgeID)
		if err != nil {
			return err
		}
		if err := scanManagedBridgeEvent(s.db.QueryRowContext(ctx, managedBridgeEventSelect+` WHERE bridge_id=? AND seq=?`, input.BridgeID, seqText), &byID); err != nil {
			return err
		}
		result = byID.public()
		return nil
	})
	return result, err
}

func (s *Store) AdmitManagedBridgeEventFrame(ctx context.Context, frame managedbridge.Event) (ManagedBridgeEvent, error) {
	if frame.Validate() != nil {
		return ManagedBridgeEvent{}, ErrManagedBridgeInvalid
	}
	return s.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: frame.BridgeInstance, Seq: frame.Seq, EventID: frame.ID, Name: frame.Name, Payload: append([]byte(nil), frame.Payload...), PayloadHash: frame.Hash, Fence: managedBridgeFence(frame.Fence)})
}

// ApplyManagedBridgeEvent advances the applied cursor only for the next
// contiguous event. A future event cannot be marked applied out of order.
func (s *Store) ApplyManagedBridgeEvent(ctx context.Context, bridgeID string, seq uint64, fence ManagedRuntimeFence) error {
	if !validBridgeID(bridgeID) || !validManagedRuntimeFence(fence) || seq == 0 {
		return ErrManagedBridgeInvalid
	}
	return s.runImmediate(ctx, func() error {
		row, err := s.requireBridgeFenceTx(ctx, bridgeID, fence)
		if err != nil {
			return err
		}
		next, err := parseBridgeSeq(row.EventAppliedNext)
		if err != nil {
			return err
		}
		if seq < next {
			var state string
			if err := s.db.QueryRowContext(ctx, `SELECT state FROM managed_bridge_events WHERE bridge_id=? AND seq=?`, bridgeID, formatBridgeSeq(seq)).Scan(&state); errors.Is(err, sql.ErrNoRows) {
				return ErrManagedBridgeReplayGap
			} else if err != nil {
				return err
			} else if state == "applied" {
				return nil
			}
			return ErrManagedBridgeSequenceConflict
		}
		if seq > next {
			return ErrManagedBridgeSequenceGap
		}
		if seq == math.MaxUint64 {
			return ErrManagedBridgeSequenceOverflow
		}
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT state FROM managed_bridge_events WHERE bridge_id=? AND seq=?`, bridgeID, formatBridgeSeq(seq)).Scan(&state); errors.Is(err, sql.ErrNoRows) {
			return ErrManagedBridgeEventNotFound
		} else if err != nil {
			return err
		}
		if state != "admitted" {
			return ErrManagedBridgeSequenceConflict
		}
		if err := s.injectManagedBridgeFault("event_apply_before_cursor"); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_bridge_events SET state='applied',applied_at=?,updated_at=? WHERE bridge_id=? AND seq=? AND state='admitted'`, now, now, bridgeID, formatBridgeSeq(seq)); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_bridges SET event_applied_next=?,updated_at=? WHERE bridge_id=?`, formatBridgeSeq(seq+1), now, bridgeID)
		return err
	})
}

// RetainManagedBridge is kept for callers using the original Phase 2A name.
func (s *Store) RetainManagedBridge(ctx context.Context, bridgeID string) (ManagedBridgeRetentionResult, error) {
	return s.SweepManagedBridgeRetention(ctx, bridgeID)
}

// SweepManagedBridgeRetention deterministically applies age/count/byte bounds.
// Only acked commands and applied events are candidates. Live rows cause an
// explicit capacity result after every eligible terminal row is pruned.
func (s *Store) SweepManagedBridgeRetention(ctx context.Context, bridgeID string) (ManagedBridgeRetentionResult, error) {
	if !validBridgeID(bridgeID) {
		return ManagedBridgeRetentionResult{}, ErrManagedBridgeInvalid
	}
	var result ManagedBridgeRetentionResult
	err := s.runImmediate(ctx, func() error {
		var err error
		result.CommandsRemoved, err = pruneBridgeDirectionTx(ctx, s.db, bridgeID, true, s.clock.Now(), 0)
		if err != nil {
			return err
		}
		result.EventsRemoved, err = pruneBridgeDirectionTx(ctx, s.db, bridgeID, false, s.clock.Now(), 0)
		if err != nil {
			return err
		}
		row, err := managedBridgeForTx(ctx, s.db, bridgeID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrManagedBridgeAuthRejected
		}
		if err != nil {
			return err
		}
		cursors, err := row.cursors()
		if err != nil {
			return err
		}
		result.CommandFloor = cursors.CommandFloor
		result.CommandAcked = cursors.CommandAcked
		result.EventFloor = cursors.EventAdmittedFloor
		result.CapacityLive, err = bridgeDirectionCapacityTx(ctx, s.db, bridgeID, true, 0)
		if err != nil {
			return err
		}
		eventLive, err := bridgeDirectionCapacityTx(ctx, s.db, bridgeID, false, 0)
		if err != nil {
			return err
		}
		result.CapacityLive = result.CapacityLive || eventLive
		if result.CapacityLive {
			return ErrManagedBridgeCapacity
		}
		return nil
	})
	return result, err
}

func validateBridgeReservation(value ManagedBridgeReservation) error {
	if !validBridgeID(value.BridgeID) || !validManagedRuntimeFence(value.Fence) || value.Fence.AgentID != value.AgentID || len(value.Verifier) != 32 || subtle.ConstantTimeCompare(value.Verifier, make([]byte, 32)) == 1 {
		return ErrManagedBridgeInvalid
	}
	return nil
}
func validateBridgeHandshake(value ManagedBridgeHandshake) error {
	if !validBridgeID(value.BridgeID) || !validManagedRuntimeFence(value.Fence) || value.Fence.AgentID != value.AgentID || len(value.Verifier) != 32 {
		return ErrManagedBridgeInvalid
	}
	return nil
}
func validateBridgeCommandInput(value ManagedBridgeCommandInput) error {
	if !validBridgeID(value.BridgeID) || !managedCanonicalUUIDRE.MatchString(value.RequestID) || !validManagedRuntimeFence(value.Fence) || value.Fence.AgentID == "" || !validBridgeCommandName(value.Name) || !validBridgePayload(value.Payload) {
		return ErrManagedBridgeInvalid
	}
	hash, err := managedbridge.HashPayload(value.Payload)
	if err != nil || (value.PayloadHash != "" && value.PayloadHash != hash) {
		return ErrManagedBridgeInvalid
	}
	return nil
}
func validateBridgeEventInput(value ManagedBridgeEventInput) error {
	if !validBridgeID(value.BridgeID) || value.Seq == 0 || !managedCanonicalUUIDRE.MatchString(value.EventID) || !validManagedRuntimeFence(value.Fence) || !validBridgeEventName(value.Name) || !validBridgePayload(value.Payload) {
		return ErrManagedBridgeInvalid
	}
	hash, err := managedbridge.HashPayload(value.Payload)
	returnError := err != nil || (value.PayloadHash != "" && value.PayloadHash != hash)
	if returnError {
		return ErrManagedBridgeInvalid
	}
	return nil
}
func validBridgePayload(payload []byte) bool {
	return len(payload) > 0 && len(payload) <= ManagedBridgePayloadLimit && managedbridgeHashValid(payload)
}
func managedbridgeHashValid(payload []byte) bool {
	_, err := managedbridge.HashPayload(payload)
	return err == nil
}
func validBridgeCommandName(name string) bool {
	return name == managedbridge.CommandPrompt || name == managedbridge.CommandAbort || name == managedbridge.CommandShutdown || name == managedbridge.CommandManifest
}
func validBridgeEventName(name string) bool {
	return name == managedbridge.EventReady || name == managedbridge.EventState || name == managedbridge.EventMessage || name == managedbridge.EventTool || name == managedbridge.EventError || name == managedbridge.EventStopped
}
func validBridgeID(value string) bool { return managedCanonicalUUIDRE.MatchString(value) }

func (s *Store) requireBridgeFenceTx(ctx context.Context, bridgeID string, fence ManagedRuntimeFence) (managedBridgeRow, error) {
	row, err := managedBridgeForTx(ctx, s.db, bridgeID)
	if errors.Is(err, sql.ErrNoRows) {
		return managedBridgeRow{}, ErrManagedBridgeAuthRejected
	}
	if err != nil {
		return managedBridgeRow{}, err
	}
	if row.Revoked {
		return managedBridgeRow{}, ErrManagedBridgeRevoked
	}
	if !row.Enabled || row.AgentID != fence.AgentID || !sameManagedRestartFence(row.Fence, fence) {
		return managedBridgeRow{}, ErrManagedBridgeStale
	}
	if err := s.requireCurrentManagedFenceTx(ctx, row.AgentID, fence); err != nil {
		return managedBridgeRow{}, err
	}
	return row, nil
}
func (s *Store) requireBridgeFenceLocked(ctx context.Context, bridgeID string, fence ManagedRuntimeFence) (managedBridgeRow, error) {
	return s.requireBridgeFenceTx(ctx, bridgeID, fence)
}
func (s *Store) requireCurrentManagedFenceTx(ctx context.Context, agentID string, fence ManagedRuntimeFence) error {
	var current ManagedRuntimeFence
	var pid, epoch, revision sql.NullInt64
	var session, marker, incarnation, generation, hash sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash FROM managed_agents WHERE agent_id=?`, agentID).Scan(&current.AgentID, &session, &epoch, &pid, &marker, &incarnation, &generation, &revision, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrManagedBridgeAuthRejected
	}
	if err != nil {
		return err
	}
	current.PiSessionID, current.SessionEpoch, current.ProcessPID, current.ProcessStartMarker, current.ProcessIncarnation, current.RuntimeGeneration, current.ManifestRevision, current.ManifestHash = session.String, epoch.Int64, int(pid.Int64), marker.String, incarnation.String, generation.String, int(revision.Int64), hash.String
	if !validManagedRuntimeFence(current) || !sameManagedRestartFence(current, fence) {
		return ErrManagedBridgeStale
	}
	return nil
}

type managedBridgeRow struct {
	BridgeID, AgentID                                                                                string
	Verifier                                                                                         []byte
	Enabled, Revoked                                                                                 bool
	Fence                                                                                            ManagedRuntimeFence
	FenceHash                                                                                        string
	CommandNext, CommandAcked, CommandFloor, EventAdmittedNext, EventAdmittedFloor, EventAppliedNext string
	CreatedAt, UpdatedAt                                                                             string
}

func managedBridgeForTx(ctx context.Context, db *sql.DB, bridgeID string) (managedBridgeRow, error) {
	var row managedBridgeRow
	var enabled, revoked int
	err := db.QueryRowContext(ctx, managedBridgeIdentitySelect+` WHERE bridge_id=?`, bridgeID).Scan(&row.BridgeID, &row.AgentID, &row.Verifier, &enabled, &revoked, &row.Fence.AgentID, &row.Fence.PiSessionID, &row.Fence.SessionEpoch, &row.Fence.ProcessPID, &row.Fence.ProcessStartMarker, &row.Fence.ProcessIncarnation, &row.Fence.RuntimeGeneration, &row.Fence.ManifestRevision, &row.Fence.ManifestHash, &row.FenceHash, &row.CommandNext, &row.CommandAcked, &row.CommandFloor, &row.EventAdmittedNext, &row.EventAdmittedFloor, &row.EventAppliedNext, &row.CreatedAt, &row.UpdatedAt)
	if err != nil {
		return row, err
	}
	row.Enabled, row.Revoked = enabled == 1, revoked == 1
	return row, nil
}
func (r managedBridgeRow) cursors() (ManagedBridgeCursors, error) {
	commandNext, err := parseBridgeSeq(r.CommandNext)
	if err != nil {
		return ManagedBridgeCursors{}, err
	}
	commandAcked, err := parseBridgeCursor(r.CommandAcked)
	if err != nil || commandAcked >= commandNext {
		return ManagedBridgeCursors{}, ErrManagedBridgeInvalid
	}
	commandFloor, err := parseBridgeSeq(r.CommandFloor)
	if err != nil || commandFloor > commandNext {
		return ManagedBridgeCursors{}, ErrManagedBridgeInvalid
	}
	eventNext, err := parseBridgeSeq(r.EventAdmittedNext)
	if err != nil {
		return ManagedBridgeCursors{}, err
	}
	eventFloor, err := parseBridgeSeq(r.EventAdmittedFloor)
	if err != nil || eventFloor > eventNext {
		return ManagedBridgeCursors{}, ErrManagedBridgeInvalid
	}
	eventApplied, err := parseBridgeSeq(r.EventAppliedNext)
	if err != nil || eventApplied > eventNext {
		return ManagedBridgeCursors{}, ErrManagedBridgeInvalid
	}
	return ManagedBridgeCursors{CommandNext: commandNext, CommandAcked: commandAcked, CommandFloor: commandFloor, EventAdmittedNext: eventNext, EventAdmittedFloor: eventFloor, EventAppliedNext: eventApplied}, nil
}
func (r managedBridgeRow) identity() (ManagedBridgeIdentity, error) {
	c, err := r.cursors()
	if err != nil {
		return ManagedBridgeIdentity{}, err
	}
	return ManagedBridgeIdentity{AgentID: r.AgentID, BridgeID: r.BridgeID, Enabled: r.Enabled, Revoked: r.Revoked, Fence: r.Fence, CommandNext: c.CommandNext, CommandAcked: c.CommandAcked, CommandFloor: c.CommandFloor, EventNext: c.EventAdmittedNext, EventFloor: c.EventAdmittedFloor, EventApplied: c.EventAppliedNext, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, nil
}

func managedBridgeFenceHash(fence ManagedRuntimeFence) (string, error) {
	if !validManagedRuntimeFence(fence) {
		return "", ErrManagedBridgeInvalid
	}
	data, err := json.Marshal(fence)
	if err != nil {
		return "", ErrManagedBridgeInvalid
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func managedBridgeFence(f managedbridge.RuntimeFence) ManagedRuntimeFence {
	return ManagedRuntimeFence{AgentID: f.Agent, PiSessionID: f.PiSessionID, SessionEpoch: int64(f.PiSessionEpoch), ProcessPID: int(f.PiPID), ProcessStartMarker: f.PiStartMarker, ProcessIncarnation: f.PiIncarnation, RuntimeGeneration: strconv.FormatUint(f.RuntimeGeneration, 10), ManifestRevision: int(f.ManifestRevision), ManifestHash: f.ManifestHash}
}
func formatBridgeSeq(value uint64) string { return strconv.FormatUint(value, 10) }
func parseBridgeSeq(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, ErrManagedBridgeInvalid
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return 0, ErrManagedBridgeInvalid
	}
	return parsed, nil
}
func parseBridgeCursor(value string) (uint64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, ErrManagedBridgeInvalid
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, ErrManagedBridgeInvalid
	}
	return parsed, nil
}

// managedBridgeCommandRow is private because it contains no payload but keeps
// scanning separate from the redacted public result.
type managedBridgeCommandRow struct {
	BridgeID, RequestID, Name, PayloadHash          string
	Seq                                             uint64
	Fence                                           ManagedRuntimeFence
	FenceHash, State, CreatedAt, UpdatedAt, AckedAt string
	SizeBytes                                       int
}

func scanManagedBridgeCommand(scanner interface{ Scan(...any) error }, row *managedBridgeCommandRow) error {
	var seq string
	var epoch, pid, revision sql.NullInt64
	var ackedAt sql.NullString
	if err := scanner.Scan(&row.BridgeID, &seq, &row.RequestID, &row.Name, &row.PayloadHash, &row.Fence.AgentID, &row.Fence.PiSessionID, &epoch, &pid, &row.Fence.ProcessStartMarker, &row.Fence.ProcessIncarnation, &row.Fence.RuntimeGeneration, &revision, &row.Fence.ManifestHash, &row.FenceHash, &row.State, &row.CreatedAt, &row.UpdatedAt, &ackedAt, &row.SizeBytes); err != nil {
		return err
	}
	row.AckedAt = ackedAt.String
	var err error
	row.Seq, err = parseBridgeSeq(seq)
	row.Fence.SessionEpoch, row.Fence.ProcessPID, row.Fence.ManifestRevision = epoch.Int64, int(pid.Int64), int(revision.Int64)
	return err
}
func (r managedBridgeCommandRow) public() ManagedBridgeCommand {
	return ManagedBridgeCommand{BridgeID: r.BridgeID, Seq: r.Seq, RequestID: r.RequestID, Name: r.Name, PayloadHash: r.PayloadHash, Fence: r.Fence, FenceHash: r.FenceHash, State: r.State, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, AckedAt: r.AckedAt, SizeBytes: r.SizeBytes}
}

type managedBridgeEventRow struct {
	BridgeID, EventID, Name, PayloadHash                          string
	Seq                                                           uint64
	Fence                                                         ManagedRuntimeFence
	FenceHash, State, CreatedAt, UpdatedAt, AdmittedAt, AppliedAt string
	SizeBytes                                                     int
}

func scanManagedBridgeEvent(scanner interface{ Scan(...any) error }, row *managedBridgeEventRow) error {
	var seq string
	var epoch, pid, revision sql.NullInt64
	var appliedAt sql.NullString
	if err := scanner.Scan(&row.BridgeID, &seq, &row.EventID, &row.Name, &row.PayloadHash, &row.Fence.AgentID, &row.Fence.PiSessionID, &epoch, &pid, &row.Fence.ProcessStartMarker, &row.Fence.ProcessIncarnation, &row.Fence.RuntimeGeneration, &revision, &row.Fence.ManifestHash, &row.FenceHash, &row.State, &row.CreatedAt, &row.UpdatedAt, &row.AdmittedAt, &appliedAt, &row.SizeBytes); err != nil {
		return err
	}
	row.AppliedAt = appliedAt.String
	var err error
	row.Seq, err = parseBridgeSeq(seq)
	row.Fence.SessionEpoch, row.Fence.ProcessPID, row.Fence.ManifestRevision = epoch.Int64, int(pid.Int64), int(revision.Int64)
	return err
}
func (r managedBridgeEventRow) public() ManagedBridgeEvent {
	return ManagedBridgeEvent{BridgeID: r.BridgeID, Seq: r.Seq, EventID: r.EventID, Name: r.Name, PayloadHash: r.PayloadHash, Fence: r.Fence, FenceHash: r.FenceHash, State: r.State, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, AdmittedAt: r.AdmittedAt, AppliedAt: r.AppliedAt, SizeBytes: r.SizeBytes}
}

// pruneBridgeDirectionTx removes only terminal rows and then moves the
// replay floor to the oldest retained sequence (or the next cursor).
func pruneBridgeDirectionTx(ctx context.Context, db *sql.DB, bridgeID string, commands bool, now time.Time, additional int) (int, error) {
	table, state, floorColumn, nextColumn := "managed_bridge_events", "applied", "event_admitted_floor", "event_admitted_next"
	if commands {
		table, state, floorColumn, nextColumn = "managed_bridge_commands", "acked", "command_floor", "command_next_seq"
	}
	rows, err := db.QueryContext(ctx, `SELECT seq,created_at,size_bytes FROM `+table+` WHERE bridge_id=? AND state=?`, bridgeID, state)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		seq     uint64
		created string
		size    int
	}
	var candidates []candidate
	for rows.Next() {
		var seq, created string
		var size int
		if err := rows.Scan(&seq, &created, &size); err != nil {
			_ = rows.Close()
			return 0, err
		}
		parsed, parseErr := parseBridgeSeq(seq)
		if parseErr != nil {
			_ = rows.Close()
			return 0, parseErr
		}
		candidates = append(candidates, candidate{parsed, created, size})
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].seq < candidates[j].seq })
	var totalCount, totalBytes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size_bytes),0) FROM `+table+` WHERE bridge_id=?`, bridgeID).Scan(&totalCount, &totalBytes); err != nil {
		return 0, err
	}
	cutoff := formatTimestamp(now.Add(-ManagedBridgeRetentionAge))
	removed := make([]uint64, 0)
	for _, item := range candidates {
		if item.created <= cutoff || totalCount >= ManagedBridgeRetentionCount || totalBytes+additional > ManagedBridgeRetentionBytes {
			removed = append(removed, item.seq)
			totalCount--
			totalBytes -= item.size
		}
	}
	for _, seq := range removed {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE bridge_id=? AND seq=? AND state=?`, bridgeID, formatBridgeSeq(seq), state); err != nil {
			return 0, err
		}
	}
	var nextText string
	if err := db.QueryRowContext(ctx, `SELECT `+nextColumn+` FROM managed_bridges WHERE bridge_id=?`, bridgeID).Scan(&nextText); err != nil {
		return 0, err
	}
	next, err := parseBridgeSeq(nextText)
	if err != nil {
		return 0, err
	}
	var minText sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT seq FROM `+table+` WHERE bridge_id=? ORDER BY length(seq),seq LIMIT 1`, bridgeID).Scan(&minText); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	floor := next
	if minText.Valid {
		floor, err = parseBridgeSeq(minText.String)
		if err != nil {
			return 0, err
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE managed_bridges SET `+floorColumn+`=?,updated_at=? WHERE bridge_id=?`, formatBridgeSeq(floor), formatTimestamp(now), bridgeID); err != nil {
		return 0, err
	}
	return len(removed), nil
}

func bridgeDirectionCapacityTx(ctx context.Context, db *sql.DB, bridgeID string, commands bool, additional int) (bool, error) {
	table := "managed_bridge_events"
	if commands {
		table = "managed_bridge_commands"
	}
	var count, bytes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size_bytes),0) FROM `+table+` WHERE bridge_id=?`, bridgeID).Scan(&count, &bytes); err != nil {
		return false, err
	}
	if count >= ManagedBridgeRetentionCount {
		return true, nil
	}
	if additional == 0 {
		return bytes >= ManagedBridgeRetentionBytes, nil
	}
	return bytes+additional > ManagedBridgeRetentionBytes, nil
}

func (s *Store) injectManagedBridgeFault(stage string) error {
	if s.managedBridgeFault == nil {
		return nil
	}
	return s.managedBridgeFault(stage)
}

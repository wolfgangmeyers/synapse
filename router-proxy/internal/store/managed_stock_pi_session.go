package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type ManagedPiSessionReservation struct {
	ReservationID     string
	AgentID           string
	SessionEpoch      int64
	LaunchOperationID string
	RootRelativePath  string
}

// ReserveManagedSessionPath creates the one stock-Pi path reservation owned by
// an admitted fresh launch. The pending launch operation remains the authority
// until the child process identity is installed.
func (s *Store) ReserveManagedSessionPath(ctx context.Context, reservation ManagedPiSessionReservation) error {
	path := filepath.Clean(reservation.RootRelativePath)
	if ctx == nil || !validManagedPiSessionBindText(reservation.ReservationID, 256) || !validManagedPiSessionBindText(reservation.AgentID, 128) || reservation.SessionEpoch < 1 || !validManagedPiSessionBindText(reservation.LaunchOperationID, 256) || !validManagedPiSessionBindText(path, 4096) || filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return errors.New("managed Pi session reservation is invalid")
	}
	digest := sha256.Sum256([]byte(path))
	now := formatTimestamp(s.clock.Now())
	return s.runImmediate(ctx, func() error {
		result, err := s.db.ExecContext(ctx, `INSERT INTO managed_stock_pi_session_reservations (
			reservation_id,agent_id,session_epoch,launch_operation_id,root_relative_path,path_sha256,pi_session_id,state,session_artifact_id,created_at,updated_at
		) SELECT ?,?,?,?,?,?,NULL,'reserved',NULL,?,?
		WHERE EXISTS (
			SELECT 1 FROM managed_agents AS agent
			JOIN managed_http_operations AS operation ON operation.operation_id=agent.launch_operation_id
			WHERE agent.agent_id=? AND agent.session_epoch=? AND agent.launch_operation_id=?
				AND agent.desired_state='running' AND agent.lifecycle_state='starting'
				AND operation.state='pending'
		)`, reservation.ReservationID, reservation.AgentID, reservation.SessionEpoch, reservation.LaunchOperationID, path, hex.EncodeToString(digest[:]), now, now, reservation.AgentID, reservation.SessionEpoch, reservation.LaunchOperationID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("managed Pi session reservation is stale")
		}
		return nil
	})
}

// ReserveManagedPiSession retains compatibility for existing callers.
func (s *Store) ReserveManagedPiSession(ctx context.Context, reservation ManagedPiSessionReservation) error {
	return s.ReserveManagedSessionPath(ctx, reservation)
}

// ManagedPiSessionBindStaleError reports a session callback that no longer owns
// the exact reservation and managed runtime fence it was started for.
type ManagedPiSessionBindStaleError struct {
	ReservationID string
}

func (e *ManagedPiSessionBindStaleError) Error() string {
	return "managed Pi session binding is stale"
}

func (e *ManagedPiSessionBindStaleError) Code() string { return "stale" }

// BindManagedPiSession atomically binds Pi's generated session ID to a reserved
// stock-session row. Parameters are ordered as reservationID,
// launchOperationID, piSessionID, exact rootRelativePath, and fence.
//
// The reservation and current managed-agent binding must match every supplied
// value. The only successful transition is reserved -> bound together with
// pi_session_id NULL -> piSessionID. Every lost CAS returns a typed stale error.
func (s *Store) BindManagedPiSession(ctx context.Context, reservationID, launchOperationID, piSessionID, rootRelativePath string, fence ManagedRuntimeFence) error {
	if ctx == nil || !validManagedPiSessionBindText(reservationID, 256) || !validManagedPiSessionBindText(launchOperationID, 256) || !validManagedPiSessionBindText(piSessionID, 256) || !validManagedPiSessionBindText(rootRelativePath, 4096) || !validManagedRuntimeFence(fence) {
		return errors.New("managed Pi session binding is invalid")
	}

	return s.runImmediate(ctx, func() error {
		args := []any{piSessionID, formatTimestamp(s.clock.Now()), reservationID, launchOperationID, fence.AgentID, fence.SessionEpoch, rootRelativePath}
		args = append(args, managedFenceValues(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_stock_pi_session_reservations
			SET pi_session_id=?,state='bound',updated_at=?
			WHERE reservation_id=? AND launch_operation_id=? AND agent_id=? AND session_epoch=?
				AND root_relative_path=? AND state='reserved' AND pi_session_id IS NULL
				AND EXISTS (SELECT 1 FROM managed_agents WHERE `+managedFenceWhere("")+`)`, args...)
		if err != nil {
			if isConstraintError(err) {
				return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
			}
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
		}
		// The reservation CAS proves the full current agent fence. The readiness
		// source must still belong to that same process and runtime generation;
		// its manifest may have advanced during RPC negotiation.
		sourceResult, err := s.db.ExecContext(ctx, `UPDATE managed_readiness_sources
			SET pi_session_id=?
			WHERE agent_id=? AND pi_session_id=? AND session_epoch=?
				AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=?`,
			piSessionID, fence.AgentID, fence.PiSessionID, fence.SessionEpoch,
			fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration)
		if err != nil {
			if isConstraintError(err) {
				return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
			}
			return err
		}
		sourceChanged, err := sourceResult.RowsAffected()
		if err != nil {
			return err
		}
		if sourceChanged != 1 {
			return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
		}
		agentArgs := []any{piSessionID, formatTimestamp(s.clock.Now())}
		agentArgs = append(agentArgs, managedFenceValues(fence)...)
		agentResult, err := s.db.ExecContext(ctx, `UPDATE managed_agents
			SET pi_session_id=?,updated_at=?
			WHERE `+managedFenceWhere(""), agentArgs...)
		if err != nil {
			if isConstraintError(err) {
				return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
			}
			return err
		}
		agentChanged, err := agentResult.RowsAffected()
		if err != nil {
			return err
		}
		if agentChanged != 1 {
			return &ManagedPiSessionBindStaleError{ReservationID: reservationID}
		}
		return nil
	})
}

// BoundManagedPiSessionPath returns the exact root-relative stock-Pi session
// path already bound to the durable managed runtime. Recovery must not create
// or rebind a reservation.
func (s *Store) BoundManagedPiSessionPath(ctx context.Context, agentID string, sessionEpoch int64, piSessionID string) (string, error) {
	if ctx == nil || !validManagedPiSessionBindText(agentID, 128) || sessionEpoch < 1 || !validManagedPiSessionBindText(piSessionID, 256) {
		return "", errors.New("managed Pi session lookup is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT root_relative_path,state FROM managed_stock_pi_session_reservations WHERE agent_id=? AND session_epoch=? AND pi_session_id=?`, agentID, sessionEpoch, piSessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var path, state string
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return "", errors.New("managed Pi session binding is duplicated")
		}
		if err := rows.Scan(&path, &state); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	clean := filepath.Clean(path)
	if count != 1 || state != "bound" || !validManagedPiSessionBindText(path, 4096) || filepath.IsAbs(path) || clean != path || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("managed Pi session binding does not match the durable runtime")
	}
	return path, nil
}

func validManagedPiSessionBindText(value string, maxBytes int) bool {
	return value != "" && utf8.ValidString(value) && len([]byte(value)) <= maxBytes
}

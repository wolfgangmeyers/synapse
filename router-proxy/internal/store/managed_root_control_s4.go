package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	managedControlReceived          = "received"
	managedControlExecuting         = "executing"
	managedControlEffectCommitted   = "side_effect_committed"
	managedControlCompleted         = "completed"
	managedControlAppendPending     = "append_pending"
	managedControlAwaitingAppend    = "awaiting_append"
	managedControlDelivered         = "delivered"
	managedControlFailed            = "failed"
	managedControlCancelled         = "cancelled"
	managedControlPayloadLimit      = 64 * 1024
	managedControlContentLimit      = 16 * 1024
	managedControlDetailsLimit      = 16 * 1024
	managedControlTerminalPerAgent  = 200
	managedControlTerminalRetention = 30 * 24 * time.Hour
)

// ManagedControlRequest is the immutable request/result ledger projection.
// Callers receive a copy and must use fenced transition methods to change it.
type ManagedControlRequest struct {
	RequestID, ResultID, AgentID, ToolCallID, CapabilityID string
	ToolName, DefinitionID, OperationID                    string
	CanonicalArgumentsSHA256, RequestFingerprint           string
	Fence                                                  ManagedRuntimeFence
	State                                                  string
	ResultStatus, ResultContent, ResultDetailsJSON         string
	ResultPayloadSHA256, ResultEntryID                     string
	DecodedPayloadBytes, AppendAttempt                     int
	NextAppendAt, ReceivedAt                               string
	HandlerOwner, HandlerStartedAt, HandlerDeadlineAt      string
	SideEffectCommittedAt, HandlerCompletedAt              string
	AppendOwner, AppendStartedAt, AppendWatermark          string
	AppendSubmittedAt, ReceiptEntryIdentity                string
	DeliveredAt, TerminalAt, ErrorCode, ErrorDetail        string
	Retryable                                              bool
}

type ManagedControlHandlerClaim struct {
	Request ManagedControlRequest
	Owner   string
}

type ManagedControlResult struct {
	Status, Content, DetailsJSON, PayloadSHA256 string
	DecodedPayloadBytes                         int
	ErrorCode, ErrorMessage                     string
	Retryable                                   bool
}

type ManagedControlAppendClaim struct {
	Request ManagedControlRequest
	Owner   string
}

type ManagedControlReceipt struct {
	RequestID, AppendOwner, EntryIdentity string
	Attempt                               int
	Fence                                 ManagedRuntimeFence
}

type ManagedControlStaleError struct{ AgentID string }

func (e *ManagedControlStaleError) Error() string { return "managed root control is stale" }
func (e *ManagedControlStaleError) Code() string  { return "stale" }

func managedControlTerminal(state string) bool {
	return state == managedControlDelivered || state == managedControlFailed || state == managedControlCancelled
}

func validManagedControlResult(value ManagedControlResult) error {
	if (value.Status != "ok" && value.Status != "error") || len([]byte(value.Content)) > managedControlContentLimit || len([]byte(value.DetailsJSON)) > managedControlDetailsLimit || len([]byte(value.Content))+len([]byte(value.DetailsJSON)) > managedControlPayloadLimit || !managedSHA256RE.MatchString(value.PayloadSHA256) || value.DecodedPayloadBytes < 1 || value.DecodedPayloadBytes > managedControlPayloadLimit {
		return errors.New("managed control result is invalid")
	}
	var details any
	decoder := json.NewDecoder(strings.NewReader(value.DetailsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&details); err != nil || details == nil {
		return errors.New("managed control result details are invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("managed control result details are invalid")
	}
	if value.Status == "ok" {
		if value.ErrorCode != "" || value.ErrorMessage != "" || value.Retryable {
			return errors.New("successful managed control result has an error")
		}
		return nil
	}
	if value.Retryable || !isLifecycleText(value.ErrorCode) || len([]byte(value.ErrorMessage)) > 2000 || !utf8Bounded(value.ErrorMessage, 2000, true) {
		return errors.New("managed control error result is invalid")
	}
	return nil
}

func utf8Bounded(value string, maximum int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && utf8.ValidString(value) && len([]byte(value)) <= maximum
}

func managedControlFenceWhere() string {
	return `pi_session_id=? AND session_epoch=? AND process_pid=? AND process_start_marker=? AND process_incarnation=? AND runtime_generation=? AND manifest_revision=? AND manifest_hash=?`
}

func managedControlFenceArgs(fence ManagedRuntimeFence) []any {
	return []any{fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash}
}

func managedControlForTx(ctx context.Context, db *sql.DB, requestID string) (*ManagedControlRequest, error) {
	if requestID == "" {
		return nil, errors.New("managed control request ID is required")
	}
	row := &ManagedControlRequest{}
	var epoch, pid, revision int64
	var decoded, attempt int
	var retryable int
	err := db.QueryRowContext(ctx, `SELECT request_id,result_id,agent_id,tool_call_id,capability_id,tool_name,definition_id,operation_id,canonical_arguments_sha256,request_fingerprint,state,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,COALESCE(result_status,''),COALESCE(result_content,''),COALESCE(result_details_json,''),COALESCE(result_payload_sha256,''),COALESCE(result_entry_id,''),COALESCE(decoded_payload_bytes,0),append_attempt,COALESCE(next_append_at,''),received_at,COALESCE(handler_owner,''),COALESCE(handler_started_at,''),COALESCE(handler_deadline_at,''),COALESCE(side_effect_committed_at,''),COALESCE(handler_completed_at,''),COALESCE(append_owner,''),COALESCE(append_started_at,''),COALESCE(append_watermark,''),COALESCE(append_submitted_at,''),COALESCE(receipt_entry_identity,''),COALESCE(delivered_at,''),COALESCE(terminal_at,''),COALESCE(error_code,''),COALESCE(error_detail,''),COALESCE(retryable,0) FROM managed_control_requests WHERE request_id=?`, requestID).Scan(
		&row.RequestID, &row.ResultID, &row.AgentID, &row.ToolCallID, &row.CapabilityID, &row.ToolName, &row.DefinitionID, &row.OperationID, &row.CanonicalArgumentsSHA256, &row.RequestFingerprint, &row.State,
		&row.Fence.PiSessionID, &epoch, &pid, &row.Fence.ProcessStartMarker, &row.Fence.ProcessIncarnation, &row.Fence.RuntimeGeneration, &revision, &row.Fence.ManifestHash,
		&row.ResultStatus, &row.ResultContent, &row.ResultDetailsJSON, &row.ResultPayloadSHA256, &row.ResultEntryID, &decoded, &attempt, &row.NextAppendAt, &row.ReceivedAt,
		&row.HandlerOwner, &row.HandlerStartedAt, &row.HandlerDeadlineAt, &row.SideEffectCommittedAt, &row.HandlerCompletedAt,
		&row.AppendOwner, &row.AppendStartedAt, &row.AppendWatermark, &row.AppendSubmittedAt, &row.ReceiptEntryIdentity, &row.DeliveredAt, &row.TerminalAt, &row.ErrorCode, &row.ErrorDetail, &retryable,
	)
	if err != nil {
		return nil, err
	}
	row.Fence.AgentID, row.Fence.SessionEpoch, row.Fence.ProcessPID, row.Fence.ManifestRevision = row.AgentID, epoch, int(pid), int(revision)
	row.DecodedPayloadBytes, row.AppendAttempt, row.Retryable = decoded, attempt, retryable == 1
	return row, nil
}

func sameManagedControlFence(left, right ManagedRuntimeFence) bool {
	return validManagedRuntimeFence(left) && validManagedRuntimeFence(right) && sameManagedRestartFence(left, right)
}

func (s *Store) ManagedControlRequest(ctx context.Context, requestID string) (*ManagedControlRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	return managedControlForTx(ctx, s.db, requestID)
}

// ClaimManagedControlHandler is the one transition that authorizes a handler.
// A duplicate request cannot acquire it after the original claim commits.
func (s *Store) ClaimManagedControlHandler(ctx context.Context, requestID, owner string, fence ManagedRuntimeFence) (*ManagedControlHandlerClaim, error) {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed control handler claim is invalid")
	}
	var claim *ManagedControlHandlerClaim
	err := s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if row.State != managedControlReceived || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		deadline := formatTimestamp(s.clock.Now().Add(30 * time.Second))
		args := []any{owner, now, deadline, requestID, managedControlReceived}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='executing',handler_owner=?,handler_started_at=?,handler_deadline_at=?,next_append_at=NULL WHERE request_id=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		row.State, row.HandlerOwner, row.HandlerStartedAt, row.HandlerDeadlineAt = managedControlExecuting, owner, now, deadline
		claim = &ManagedControlHandlerClaim{Request: *row, Owner: owner}
		return nil
	})
	return claim, err
}

// RecoverManagedControlHandler transfers an expired handler lease after a
// rebind. It permits reconciliation only; callers still need the handler's
// request-ID idempotency boundary before committing a previously uncertain
// effect.
func (s *Store) RecoverManagedControlHandler(ctx context.Context, requestID, owner string, fence ManagedRuntimeFence) (*ManagedControlHandlerClaim, error) {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed control recovery claim is invalid")
	}
	var claim *ManagedControlHandlerClaim
	err := s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if (row.State != managedControlExecuting && row.State != managedControlEffectCommitted) || !sameManagedControlFence(row.Fence, fence) || row.HandlerDeadlineAt == "" || row.HandlerDeadlineAt > now {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		deadline := formatTimestamp(s.clock.Now().Add(30 * time.Second))
		args := []any{owner, now, deadline, requestID}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET handler_owner=?,handler_started_at=?,handler_deadline_at=? WHERE request_id=? AND state IN ('executing','side_effect_committed') AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		row.HandlerOwner, row.HandlerStartedAt, row.HandlerDeadlineAt = owner, now, deadline
		claim = &ManagedControlHandlerClaim{Request: *row, Owner: owner}
		return nil
	})
	return claim, err
}

// CommitManagedControlSideEffect records the only point from which recovery may
// generate a result. Adapters must make their external effect idempotent on the
// request ID before calling this transition.
func (s *Store) CommitManagedControlSideEffect(ctx context.Context, requestID, owner string, fence ManagedRuntimeFence) error {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !validManagedRuntimeFence(fence) {
		return errors.New("managed control side effect is invalid")
	}
	return s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if row.State != managedControlExecuting || row.HandlerOwner != owner || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, requestID, owner, managedControlExecuting}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='side_effect_committed',side_effect_committed_at=? WHERE request_id=? AND handler_owner=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		return nil
	})
}

// CompleteManagedControl commits an immutable result and its deterministic ID
// before any append reservation exists.
func (s *Store) CompleteManagedControl(ctx context.Context, requestID, owner string, fence ManagedRuntimeFence, value ManagedControlResult) (*ManagedControlRequest, error) {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed control completion is invalid")
	}
	if err := validManagedControlResult(value); err != nil {
		return nil, err
	}
	var completed *ManagedControlRequest
	err := s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if row.State != managedControlEffectCommitted || row.HandlerOwner != owner || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		entryID := "mcr1_" + strings.ReplaceAll(row.ResultID, "-", "")
		now := formatTimestamp(s.clock.Now())
		args := []any{value.Status, value.Content, value.DetailsJSON, value.PayloadSHA256, value.DecodedPayloadBytes, entryID, nullIfEmpty(value.ErrorCode), nullIfEmpty(value.ErrorMessage), now, requestID, owner, managedControlEffectCommitted}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='completed',result_status=?,result_content=?,result_details_json=?,result_payload_sha256=?,decoded_payload_bytes=?,result_entry_id=?,error_code=?,error_detail=?,handler_completed_at=?,next_append_at=NULL WHERE request_id=? AND handler_owner=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		row.State, row.ResultStatus, row.ResultContent, row.ResultDetailsJSON, row.ResultPayloadSHA256, row.DecodedPayloadBytes, row.ResultEntryID, row.ErrorCode, row.ErrorDetail, row.HandlerCompletedAt = managedControlCompleted, value.Status, value.Content, value.DetailsJSON, value.PayloadSHA256, value.DecodedPayloadBytes, entryID, value.ErrorCode, value.ErrorMessage, now
		completed = row
		return nil
	})
	return completed, err
}

// ClaimManagedControlAppend fixes the attempt, owner, and reader watermark in
// SQLite before the private prompt write. A retry must have reached its durable
// due time; it cannot be allocated by an arbitrary callback.
func (s *Store) ClaimManagedControlAppend(ctx context.Context, requestID, owner, watermark string, fence ManagedRuntimeFence) (*ManagedControlAppendClaim, error) {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !isLifecycleText(watermark) || !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed control append claim is invalid")
	}
	var claim *ManagedControlAppendClaim
	err := s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if row.State != managedControlCompleted || !sameManagedControlFence(row.Fence, fence) || row.ResultEntryID == "" || (row.NextAppendAt != "" && row.NextAppendAt > now) || row.AppendAttempt >= 5 {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		attempt := row.AppendAttempt + 1
		args := []any{attempt, owner, now, watermark, requestID, managedControlCompleted}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='append_pending',append_attempt=?,append_owner=?,append_started_at=?,append_watermark=?,append_submitted_at=NULL,next_append_at=NULL WHERE request_id=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		row.State, row.AppendAttempt, row.AppendOwner, row.AppendStartedAt, row.AppendWatermark, row.NextAppendAt = managedControlAppendPending, attempt, owner, now, watermark, ""
		claim = &ManagedControlAppendClaim{Request: *row, Owner: owner}
		return nil
	})
	return claim, err
}

func (s *Store) MarkManagedControlAppendSubmitted(ctx context.Context, requestID, owner string, attempt int, fence ManagedRuntimeFence) error {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || attempt < 1 || attempt > 5 || !validManagedRuntimeFence(fence) {
		return errors.New("managed control append submission is invalid")
	}
	return s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if row.State != managedControlAppendPending || row.AppendOwner != owner || row.AppendAttempt != attempt || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, requestID, owner, attempt, managedControlAppendPending}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='awaiting_append',append_submitted_at=? WHERE request_id=? AND append_owner=? AND append_attempt=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		return nil
	})
}

func (s *Store) DeliverManagedControlReceipt(ctx context.Context, receipt ManagedControlReceipt) error {
	if !managedCanonicalUUIDRE.MatchString(receipt.RequestID) || !managedCanonicalUUIDRE.MatchString(receipt.AppendOwner) || receipt.Attempt < 1 || receipt.Attempt > 5 || !isLifecycleText(receipt.EntryIdentity) || !validManagedRuntimeFence(receipt.Fence) {
		return errors.New("managed control receipt is invalid")
	}
	return s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, receipt.RequestID)
		if err != nil {
			return err
		}
		if row.State != managedControlAwaitingAppend || row.AppendOwner != receipt.AppendOwner || row.AppendAttempt != receipt.Attempt || !sameManagedControlFence(row.Fence, receipt.Fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{receipt.EntryIdentity, now, now, receipt.RequestID, receipt.AppendOwner, receipt.Attempt, managedControlAwaitingAppend}
		args = append(args, managedControlFenceArgs(receipt.Fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='delivered',receipt_entry_identity=?,delivered_at=?,terminal_at=?,retryable=0,next_append_at=NULL WHERE request_id=? AND append_owner=? AND append_attempt=? AND state=? AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		_, err = s.pruneManagedControlTerminalTx(ctx, row.AgentID, s.clock.Now())
		return err
	})
}

// RetryManagedControlAppend records one of the four fixed backoff times. The
// fifth failed observation is terminal and can never allocate a sixth append.
func (s *Store) RetryManagedControlAppend(ctx context.Context, requestID, owner string, attempt int, fence ManagedRuntimeFence, detail string) error {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || attempt < 1 || attempt > 5 || !validManagedRuntimeFence(fence) || !utf8Bounded(detail, 2000, true) {
		return errors.New("managed control append retry is invalid")
	}
	return s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if (row.State != managedControlAppendPending && row.State != managedControlAwaitingAppend) || row.AppendOwner != owner || row.AppendAttempt != attempt || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := s.clock.Now()
		nowText := formatTimestamp(now)
		if attempt == 5 {
			args := []any{nowText, "RESULT_DELIVERY_FAILED", nullIfEmpty(detail), requestID, owner, attempt}
			args = append(args, managedControlFenceArgs(fence)...)
			result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='failed',terminal_at=?,error_code=?,error_detail=?,retryable=0,next_append_at=NULL WHERE request_id=? AND append_owner=? AND append_attempt=? AND state IN ('append_pending','awaiting_append') AND `+managedControlFenceWhere(), args...)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				if err != nil {
					return err
				}
				return &ManagedControlStaleError{AgentID: row.AgentID}
			}
			_, err = s.pruneManagedControlTerminalTx(ctx, row.AgentID, now)
			return err
		}
		delay := []time.Duration{0, time.Second, 5 * time.Second, 30 * time.Second, 120 * time.Second}[attempt]
		due := formatTimestamp(now.Add(delay))
		args := []any{nullIfEmpty(detail), requestID, owner, attempt}
		args = append(args, managedControlFenceArgs(fence)...)
		// SQLite checks the 027 append-state invariant at each constrained
		// transition. Clear the old reservation before publishing its due retry.
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='completed',next_append_at=NULL,error_detail=? WHERE request_id=? AND append_owner=? AND append_attempt=? AND state IN ('append_pending','awaiting_append') AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return fmt.Errorf("clear append retry reservation: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		args = []any{due, requestID, attempt}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err = s.db.ExecContext(ctx, `UPDATE managed_control_requests SET next_append_at=? WHERE request_id=? AND append_attempt=? AND state='completed' AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return fmt.Errorf("publish append retry due time: %w", err)
		}
		changed, err = result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		return nil
	})
}

func (s *Store) FailManagedControl(ctx context.Context, requestID, owner string, fence ManagedRuntimeFence, code, detail string) error {
	if !managedCanonicalUUIDRE.MatchString(requestID) || !managedCanonicalUUIDRE.MatchString(owner) || !validManagedRuntimeFence(fence) || !isLifecycleText(code) || !utf8Bounded(detail, 2000, true) {
		return errors.New("managed control failure is invalid")
	}
	return s.runImmediate(ctx, func() error {
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return err
		}
		if (row.State != managedControlExecuting && row.State != managedControlEffectCommitted) || row.HandlerOwner != owner || !sameManagedControlFence(row.Fence, fence) {
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		now := formatTimestamp(s.clock.Now())
		args := []any{now, code, nullIfEmpty(detail), requestID, owner}
		args = append(args, managedControlFenceArgs(fence)...)
		result, err := s.db.ExecContext(ctx, `UPDATE managed_control_requests SET state='failed',terminal_at=?,error_code=?,error_detail=?,retryable=0,next_append_at=NULL WHERE request_id=? AND handler_owner=? AND state IN ('executing','side_effect_committed') AND `+managedControlFenceWhere(), args...)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return &ManagedControlStaleError{AgentID: row.AgentID}
		}
		_, err = s.pruneManagedControlTerminalTx(ctx, row.AgentID, s.clock.Now())
		return err
	})
}

func (s *Store) ListRestorableManagedControlRequests(ctx context.Context, fence ManagedRuntimeFence) ([]ManagedControlRequest, error) {
	if !validManagedRuntimeFence(fence) {
		return nil, errors.New("managed control recovery fence is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT request_id FROM managed_control_requests WHERE agent_id=? AND state NOT IN ('delivered','failed','cancelled') AND `+managedControlFenceWhere()+` ORDER BY received_at,request_id`, append([]any{fence.AgentID}, managedControlFenceArgs(fence)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ManagedControlRequest
	for rows.Next() {
		var requestID string
		if err := rows.Scan(&requestID); err != nil {
			return nil, err
		}
		row, err := managedControlForTx(ctx, s.db, requestID)
		if err != nil {
			return nil, err
		}
		result = append(result, *row)
	}
	return result, rows.Err()
}

func (s *Store) PruneManagedControlTerminal(ctx context.Context) (int, error) {
	var count int
	err := s.runImmediate(ctx, func() error {
		var err error
		count, err = s.pruneManagedControlTerminalTx(ctx, "", s.clock.Now())
		return err
	})
	return count, err
}

func (s *Store) pruneManagedControlTerminalTx(ctx context.Context, agentID string, now time.Time) (int, error) {
	cutoff := formatTimestamp(now.Add(-managedControlTerminalRetention))
	where, args := `state IN ('delivered','failed','cancelled') AND terminal_at <= ?`, []any{cutoff}
	if agentID != "" {
		where += ` AND agent_id=?`
		args = append(args, agentID)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM managed_control_requests WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if agentID != "" {
		result, err = s.db.ExecContext(ctx, `DELETE FROM managed_control_requests WHERE agent_id=? AND request_id IN (SELECT request_id FROM managed_control_requests WHERE agent_id=? AND state IN ('delivered','failed','cancelled') ORDER BY terminal_at DESC,request_id DESC LIMIT -1 OFFSET ?)`, agentID, agentID, managedControlTerminalPerAgent)
		if err != nil {
			return 0, err
		}
		more, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		return int(deleted + more), nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT agent_id FROM managed_control_requests WHERE state IN ('delivered','failed','cancelled')`)
	if err != nil {
		return 0, err
	}
	var agents []string
	for rows.Next() {
		var current string
		if err := rows.Scan(&current); err != nil {
			_ = rows.Close()
			return 0, err
		}
		agents = append(agents, current)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, current := range agents {
		result, err = s.db.ExecContext(ctx, `DELETE FROM managed_control_requests WHERE agent_id=? AND request_id IN (SELECT request_id FROM managed_control_requests WHERE agent_id=? AND state IN ('delivered','failed','cancelled') ORDER BY terminal_at DESC,request_id DESC LIMIT -1 OFFSET ?)`, current, current, managedControlTerminalPerAgent)
		if err != nil {
			return 0, err
		}
		more, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += more
	}
	return int(deleted), nil
}

// ManagedControlResultEnvelope reconstructs exactly the immutable private
// result payload. It is intentionally scoped to one known entry, not history.
func (s *Store) ManagedControlResultEnvelope(ctx context.Context, requestID string) (map[string]any, error) {
	row, err := s.ManagedControlRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	payload, err := managedControlPayloadJSON(*row)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func managedControlPayloadJSON(row ManagedControlRequest) ([]byte, error) {
	if row.ResultStatus != "ok" && row.ResultStatus != "error" {
		return nil, errors.New("managed control result is incomplete")
	}
	var details any
	decoder := json.NewDecoder(strings.NewReader(row.ResultDetailsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&details); err != nil {
		return nil, err
	}
	payload := map[string]any{"protocol": "mecha.control/v1", "result_id": row.ResultID, "request_id": row.RequestID, "target_session_epoch": row.Fence.SessionEpoch, "runtime_generation": row.Fence.RuntimeGeneration, "manifest_sha256": row.Fence.ManifestHash, "status": row.ResultStatus, "content": row.ResultContent, "details": details}
	if row.ResultStatus == "error" {
		payload["error"] = map[string]any{"code": row.ErrorCode, "message": row.ErrorDetail, "retryable": false}
	}
	return json.Marshal(payload)
}

func ManagedControlRetryDelay(attempt int) (time.Duration, error) {
	if attempt < 1 || attempt > 4 {
		return 0, fmt.Errorf("managed control retry attempt is invalid")
	}
	return []time.Duration{0, time.Second, 5 * time.Second, 30 * time.Second, 120 * time.Second}[attempt], nil
}

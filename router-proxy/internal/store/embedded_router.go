package store

// Router-owned embedded state uses migration 065 and the managed-agent runtime fence.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"errors"
	"fmt"
	"github.com/sky-valley/pi/ai"
	"time"
)

var ErrStaleEmbeddedFence = errors.New("stale embedded fence")

const embeddedMaxBody = 1 << 20
const embeddedGenesis = "genesis"

type EmbeddedBinding struct {
	AgentID, SessionID string
	Epoch              int64
	Generation         string
	OwnerToken         int64
}
type EmbeddedEntry struct {
	Sequence           int64
	ParentDigest, Kind string
	Body               []byte
	Digest             string
}
type EmbeddedCheckpoint struct {
	Sequence       int64
	AncestorDigest string
	Body           []byte
	Digest         string
}
type EmbeddedSnapshot struct {
	Entries    []EmbeddedEntry
	Checkpoint *EmbeddedCheckpoint
}
type EmbeddedEffect struct {
	State          string
	Intent, Result []byte
	ShouldReplay   bool
}

func validateEmbedded(b EmbeddedBinding) error {
	if b.AgentID == "" || b.SessionID == "" || b.Generation == "" || b.Epoch <= 0 {
		return errors.New("invalid embedded binding")
	}
	return nil
}

// embeddedManagedFence is read inside the caller's immediate transaction (or
// serialized read lock). An embedded current bit alone is never authority.
func (s *Store) embeddedManagedFence(ctx context.Context, b EmbeddedBinding) error {
	if err := validateEmbedded(b); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded' AND desired_state='running' AND (lifecycle_state='running' OR (lifecycle_state='starting' AND EXISTS (SELECT 1 FROM managed_http_operations o WHERE o.operation_id=managed_agents.launch_operation_id AND o.state='pending')))`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: stale managed embedded binding", ErrStaleEmbeddedFence)
	}
	return nil
}

// embeddedOwnerFence must be called while holding runImmediate or the Store
// read lock. It validates that the session row exists and is current.
// Stage 2: Stripped owner token and lease expiry requirements.
func (s *Store) embeddedOwnerFence(ctx context.Context, b EmbeddedBinding) error {
	if err := validateEmbedded(b); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: stale embedded owner", ErrStaleEmbeddedFence)
	}
	return nil
}

func (s *Store) embeddedFence(ctx context.Context, b EmbeddedBinding) error {
	if err := s.embeddedOwnerFence(ctx, b); err != nil {
		return err
	}
	// Runtime identities carry a positive durable token. Token-zero router control
	// and session discovery remain supported; no lease/expiry authority is used.
	if b.OwnerToken > 0 {
		var token int64
		if err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&token); err != nil {
			return err
		}
		if token != b.OwnerToken {
			return fmt.Errorf("%w: stale embedded owner token", ErrStaleEmbeddedFence)
		}
	}
	if err := s.embeddedManagedFence(ctx, b); err != nil {
		return err
	}
	return nil
}

// EmbeddedSessionRole reads the exact saved reservation; a missing or stale
// binding cannot be promoted to a root during recovery.
func (s *Store) EmbeddedSessionRole(ctx context.Context, b EmbeddedBinding) (string, error) {
	if err := validateEmbedded(b); err != nil {
		return "", err
	}
	if err := s.embeddedManagedFence(ctx, b); err != nil {
		return "", err
	}
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role)
	if err != nil {
		return "", err
	}
	if !validEmbeddedRole(role) {
		return "", errors.New("invalid saved embedded role")
	}
	return role, nil
}

// SavedEmbeddedSessionRole reads the current reservation without a live owner
// fence. Read-only HTTP views must still work after a router restart or while
// the saved agent is inactive. The runtime and full binding come from storage.
func (s *Store) SavedEmbeddedSessionRole(ctx context.Context, b EmbeddedBinding) (string, error) {
	if err := validateEmbedded(b); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("store is closed")
	}
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT e.role FROM embedded_sessions e JOIN managed_agents a ON a.agent_id=e.agent_id WHERE a.runtime='embedded' AND a.pi_session_id=e.session_id AND a.session_epoch=e.epoch AND a.runtime_generation=e.generation AND e.agent_id=? AND e.session_id=? AND e.epoch=? AND e.generation=? AND e.current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role)
	if err != nil {
		return "", err
	}
	if !validEmbeddedRole(role) {
		return "", errors.New("invalid saved embedded role")
	}
	return role, nil
}

// EnsureEmbeddedSession bootstraps only an absent root launch row. An existing
// binding is inspected read-only; authority is obtained exclusively by claim.
func (s *Store) EnsureEmbeddedSession(ctx context.Context, b EmbeddedBinding) error {
	if err := validateEmbedded(b); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedManagedFence(ctx, b); err != nil {
			return err
		}
		if present, err := s.managedSubagentSchemaPresent(ctx); err != nil {
			return err
		} else if present {
			var child bool
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_subagents WHERE agent_id=?)`, b.AgentID).Scan(&child); err != nil {
				return err
			}
			if child {
				return errors.New("subagent cannot bootstrap a root session")
			}
		}
		var session, generation, role string
		var epoch int64
		err := s.db.QueryRowContext(ctx, `SELECT session_id,epoch,generation,role FROM embedded_sessions WHERE agent_id=? AND current=1`, b.AgentID).Scan(&session, &epoch, &generation, &role)
		if err == nil {
			if session == b.SessionID && generation == b.Generation && epoch == b.Epoch && role == "root" {
				return nil
			}
			return errors.New("conflicting embedded binding")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?, 'root',1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		return err
	})
}

// MarkEmbeddedOwnerFailed changes lifecycle only while the exact lease is
// still live. This also protects the operation from a stale runner's failure.
func (s *Store) MarkEmbeddedOwnerFailed(ctx context.Context, b EmbeddedBinding, operationID, code, message string) error {
	if len(code) > 128 {
		code = code[:128]
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if b.OwnerToken > 0 {
			var currentToken int64
			err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&currentToken)
			if err != nil {
				return err
			}
			if currentToken != b.OwnerToken {
				return errors.New("stale embedded failure")
			}
		}
		now := formatTimestamp(s.clock.Now())
		r, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='failed',activity_state='failed',accepting_prompts=0,slot_held=0,last_error_code=?,last_error_message=?,last_error_at=?,updated_at=? WHERE agent_id=? AND launch_operation_id=? AND lifecycle_state IN ('starting','running')`, code, message, now, now, b.AgentID, operationID)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale embedded failure")
		}
		if _, err = s.bumpManagedProjectionVersion(ctx); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_http_operations SET state='terminal',terminal_at=?,expires_at=? WHERE operation_id=? AND state='pending'`, now, formatTimestamp(s.clock.Now().Add(24*time.Hour)), operationID)
		return err
	})
}

// EmbeddedOwnerLeaseHeld reports the remaining persisted claim duration. It is
// returned only for the exact current binding, never for a stale incarnation.
type EmbeddedOwnerLeaseHeld struct{ RetryAfter time.Duration }

func (e *EmbeddedOwnerLeaseHeld) Error() string { return "embedded owner lease held" }

// AcquireEmbeddedOwner installs or confirms an owner claim on an already
// bound session. Same-owner confirmation preserves its token; changing owner
// advances the durable execution fence. This does not restore lease exclusion.
func (s *Store) AcquireEmbeddedOwner(ctx context.Context, b EmbeddedBinding, owner string, ttl time.Duration) (int64, error) {
	if owner == "" || len(owner) > 256 {
		return 0, errors.New("invalid embedded owner lease")
	}
	var token int64
	err := s.runImmediate(ctx, func() error {
		// Acquisition is a control operation: validate session/runtime, not the
		// prior runtime token. It is the operation that mints the successor fence.
		control := b
		control.OwnerToken = 0
		if err := s.embeddedFence(ctx, control); err != nil {
			return err
		}
		var oldOwner int64
		_ = s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&oldOwner)
		expires := formatTimestamp(s.clock.Now().Add(100 * 365 * 24 * time.Hour))
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET owner_id=?,owner_token=CASE WHEN owner_token<=0 THEN 1 WHEN owner_id IS NOT ? THEN owner_token+1 ELSE owner_token END,owner_expires_at=? WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, owner, owner, expires, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale embedded owner binding")
		}
		if err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&token); err != nil {
			return err
		}
		return nil
	})
	return token, err
}

// RenewEmbeddedOwner is a harmless identity confirmation that validates the session
// binding remains valid, current, and active.
func (s *Store) RenewEmbeddedOwner(ctx context.Context, b EmbeddedBinding, owner string, ttl time.Duration) error {
	if owner == "" || len(owner) > 256 {
		return errors.New("invalid embedded owner lease")
	}
	if err := s.lockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return err
	}
	var currentOwner sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT owner_id FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&currentOwner)
	if err != nil {
		return err
	}
	if currentOwner.Valid && currentOwner.String != "" && currentOwner.String != owner {
		return fmt.Errorf("%w: stale embedded owner", ErrStaleEmbeddedFence)
	}
	return nil
}

// RefreshEmbeddedOwner is an alias for RenewEmbeddedOwner for interface compatibility.
func (s *Store) RefreshEmbeddedOwner(ctx context.Context, b EmbeddedBinding, owner string, ttl time.Duration) error {
	return s.RenewEmbeddedOwner(ctx, b, owner, ttl)
}

// ReleaseEmbeddedOwner is a harmless confirmation in Stage 2.
func (s *Store) ReleaseEmbeddedOwner(ctx context.Context, b EmbeddedBinding, ownerID string) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if ownerID == "" || len(ownerID) > 256 {
		return errors.New("invalid embedded owner lease")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	return s.embeddedFence(ctx, b)
}

func validEmbeddedRole(role string) bool {
	return role == "root" || role == "worker" || role == "subagent"
}

// BindEmbeddedSession bootstraps the first unowned current row for the exact
// managed runtime. After claim, even an exact retry needs the live owner token.
func (s *Store) BindEmbeddedSession(ctx context.Context, b EmbeddedBinding, role string) error {
	if err := validateEmbedded(b); err != nil {
		return err
	}
	if !validEmbeddedRole(role) {
		return errors.New("invalid embedded role")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedManagedFence(ctx, b); err != nil {
			return err
		}
		var child bool
		if present, err := s.managedSubagentSchemaPresent(ctx); err != nil {
			return err
		} else if present {
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM managed_subagents WHERE agent_id=?)`, b.AgentID).Scan(&child); err != nil {
				return err
			}
		}
		if child != (role == "subagent") {
			return errors.New("embedded role does not match managed identity")
		}
		var session, generation, savedRole string
		var epoch int64
		err := s.db.QueryRowContext(ctx, `SELECT session_id,epoch,generation,role FROM embedded_sessions WHERE agent_id=? AND current=1`, b.AgentID).Scan(&session, &epoch, &generation, &savedRole)
		if err == nil {
			if session == b.SessionID && epoch == b.Epoch && generation == b.Generation && savedRole == role {
				return s.embeddedOwnerFence(ctx, b)
			}
			return errors.New("conflicting embedded binding")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, role)
		return err
	})
}

// ReadyEmbeddedRoot is the starting -> bound -> ready transition. The bound
// row and exact managed identity are checked in one transaction; a crash after
// bind can retry, while a stale generation cannot publish readiness.
func (s *Store) ReadyEmbeddedRoot(ctx context.Context, b EmbeddedBinding, operationID string) error {
	if err := validateEmbedded(b); err != nil || operationID == "" {
		return errors.New("invalid embedded readiness identity")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var state string
		if err := s.db.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_agents WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded' AND desired_state='running' AND launch_operation_id=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, operationID).Scan(&state); err != nil {
			return err
		}
		if state == "running" {
			return nil
		}
		if state != "starting" {
			return errors.New("stale embedded readiness")
		}
		now := formatTimestamp(s.clock.Now())
		// Preserve 'active' if already executing an initial turn, otherwise default to 'idle'.
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='running',activity_state=CASE WHEN activity_state='active' THEN 'active' ELSE 'idle' END,accepting_prompts=1,started_at=COALESCE(started_at,?),updated_at=? WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded' AND desired_state='running' AND lifecycle_state='starting' AND launch_operation_id=? AND EXISTS (SELECT 1 FROM managed_http_operations WHERE operation_id=? AND state='pending')`, now, now, b.AgentID, b.SessionID, b.Epoch, b.Generation, operationID, operationID)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("stale embedded readiness")
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE recipient_sources SET state='active',updated_at=? WHERE source_kind='managed' AND owner_identity=? AND state='reserved'`, now, b.AgentID); err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// SetEmbeddedActivityState updates activity_state on managed_agents for an embedded agent
// ("active" or "idle") and bumps projection_version if the state changed.
func (s *Store) SetEmbeddedActivityState(ctx context.Context, b EmbeddedBinding, activityState string) error {
	if err := validateEmbedded(b); err != nil {
		return err
	}
	if activityState != "active" && activityState != "idle" {
		return errors.New("invalid embedded activity state")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if b.OwnerToken > 0 {
			var currentToken int64
			err := s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&currentToken)
			if err != nil {
				return err
			}
			if currentToken != b.OwnerToken {
				return fmt.Errorf("%w: stale embedded owner", ErrStaleEmbeddedFence)
			}
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state=?,updated_at=? WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=? AND runtime='embedded' AND desired_state='running' AND lifecycle_state IN ('starting','running') AND activity_state != ?`, activityState, now, b.AgentID, b.SessionID, b.Epoch, b.Generation, activityState)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			_, err = s.bumpManagedProjectionVersion(ctx)
			return err
		}
		return nil
	})
}

// ResetEmbeddedAgentInterrupt resets an embedded agent to running and idle after
// a clean intentional interrupt (context cancellation), ensuring accepting_prompts=1
// and slot_held=1.
func (s *Store) ResetEmbeddedAgentInterrupt(ctx context.Context, agentID string) error {
	if agentID == "" {
		return errors.New("empty agent id")
	}
	return s.runImmediate(ctx, func() error {
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_restart_wake_slots SET state='unknown' WHERE state='claimed' AND agent_id=?`, agentID); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state='idle', lifecycle_state='running', accepting_prompts=1, slot_held=1, last_error_code=NULL, last_error_message=NULL, last_error_at=NULL, updated_at=? WHERE agent_id=? AND runtime='embedded' AND desired_state='running'`, now, agentID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n > 0 {
			_, err = s.bumpManagedProjectionVersion(ctx)
			return err
		}
		return nil
	})
}

// ResetActiveEmbeddedAgents resets any running embedded agents that were left in active state
// to idle. At router startup / new process generation, no turn can be active since the
// previous process was killed. It also reconciles dangling active tool history rows.
func (s *Store) ResetActiveEmbeddedAgents(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET activity_state = 'idle', updated_at = ? WHERE lifecycle_state = 'running' AND activity_state = 'active' AND runtime = 'embedded'`, now)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		var historyExists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_complete_history'`).Scan(&historyExists); err != nil {
			return err
		}
		var histN int64
		if historyExists > 0 {
			histResult, err := s.db.ExecContext(ctx, `UPDATE managed_complete_history SET presentation_json=json_set(presentation_json, '$.outcome', 'failed', '$.summary', 'Interrupted by restart') WHERE json_extract(presentation_json, '$.outcome') = 'active'`)
			if err != nil {
				return err
			}
			histN, err = histResult.RowsAffected()
			if err != nil {
				return err
			}
		}
		danglingN, err := s.reconcileDanglingEmbeddedToolCallsTx(ctx)
		if err != nil {
			return err
		}
		if n > 0 || histN > 0 || danglingN > 0 {
			_, err = s.bumpManagedProjectionVersion(ctx)
			return err
		}
		return nil
	})
}

// reconcileDanglingEmbeddedToolCallsTx checks all current embedded sessions for
// in-flight tool calls that never received a toolResult before the router restarted.
// It appends a synthetic error toolResult so the transcript remains valid for the model.
func (s *Store) reconcileDanglingEmbeddedToolCallsTx(ctx context.Context) (int64, error) {
	var sessionsExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='embedded_sessions'`).Scan(&sessionsExists); err != nil {
		return 0, err
	}
	var entriesExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='embedded_entries'`).Scan(&entriesExists); err != nil {
		return 0, err
	}
	if sessionsExists == 0 || entriesExists == 0 {
		return 0, nil
	}

	rows, err := s.db.QueryContext(ctx, `SELECT agent_id, session_id, epoch, generation FROM embedded_sessions WHERE current=1`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type currentSession struct {
		binding EmbeddedBinding
	}
	var sessions []currentSession
	for rows.Next() {
		var cs currentSession
		if err := rows.Scan(&cs.binding.AgentID, &cs.binding.SessionID, &cs.binding.Epoch, &cs.binding.Generation); err != nil {
			return 0, err
		}
		sessions = append(sessions, cs)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	_ = rows.Close()

	var totalCleaned int64
	for _, cs := range sessions {
		tailRows, err := s.db.QueryContext(ctx, `SELECT seq, kind, body, digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq >= (SELECT COALESCE(MAX(seq), 0) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND kind != 'toolResult' AND kind != 'tool_result') ORDER BY seq ASC`, cs.binding.AgentID, cs.binding.SessionID, cs.binding.Epoch, cs.binding.AgentID, cs.binding.SessionID, cs.binding.Epoch)
		if err != nil {
			return totalCleaned, err
		}
		type tailEntry struct {
			seq    int64
			kind   string
			body   []byte
			digest string
		}
		var tail []tailEntry
		for tailRows.Next() {
			var te tailEntry
			if err := tailRows.Scan(&te.seq, &te.kind, &te.body, &te.digest); err != nil {
				_ = tailRows.Close()
				return totalCleaned, err
			}
			tail = append(tail, te)
		}
		if err := tailRows.Err(); err != nil {
			_ = tailRows.Close()
			return totalCleaned, err
		}
		_ = tailRows.Close()

		if len(tail) == 0 {
			continue
		}

		first := tail[0]
		if first.kind != "assistant" {
			continue
		}

		msg, err := ai.UnmarshalMessage(first.body)
		if err != nil {
			continue
		}

		resolvedToolCallIDs := make(map[string]bool)
		for _, entry := range tail[1:] {
			if entry.kind == "toolResult" || entry.kind == "tool_result" {
				resultMsg, err := ai.UnmarshalMessage(entry.body)
				if err == nil {
					if tr, ok := resultMsg.(ai.ToolResultMessage); ok {
						resolvedToolCallIDs[tr.ToolCallID] = true
					} else if tr, ok := resultMsg.(*ai.ToolResultMessage); ok && tr != nil {
						resolvedToolCallIDs[tr.ToolCallID] = true
					}
				}
			}
		}

		var candidateToolCalls []ai.ToolCall
		switch a := msg.(type) {
		case ai.AssistantMessage:
			for _, part := range a.Content {
				if tc, ok := part.(ai.ToolCall); ok {
					if !resolvedToolCallIDs[tc.ID] {
						candidateToolCalls = append(candidateToolCalls, tc)
					}
				} else if tc, ok := part.(*ai.ToolCall); ok && tc != nil {
					if !resolvedToolCallIDs[tc.ID] {
						candidateToolCalls = append(candidateToolCalls, *tc)
					}
				}
			}
		case *ai.AssistantMessage:
			if a != nil {
				for _, part := range a.Content {
					if tc, ok := part.(ai.ToolCall); ok {
						if !resolvedToolCallIDs[tc.ID] {
							candidateToolCalls = append(candidateToolCalls, tc)
						}
					} else if tc, ok := part.(*ai.ToolCall); ok && tc != nil {
						if !resolvedToolCallIDs[tc.ID] {
							candidateToolCalls = append(candidateToolCalls, *tc)
						}
					}
				}
			}
		}

		var danglingToolCalls []ai.ToolCall
		for _, tc := range candidateToolCalls {
			var isEffect bool
			_ = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND effect_id=?)`, cs.binding.AgentID, cs.binding.SessionID, cs.binding.Epoch, tc.ID).Scan(&isEffect)
			if !isEffect {
				danglingToolCalls = append(danglingToolCalls, tc)
			}
		}

		if len(danglingToolCalls) == 0 {
			continue
		}

		lastEntry := tail[len(tail)-1]
		headSeq := lastEntry.seq
		headDigest := lastEntry.digest

		for _, tc := range danglingToolCalls {
			synthetic := ai.ToolResultMessage{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    ai.ContentList{ai.TextContent{Text: "Command interrupted by router restart"}},
				IsError:    true,
				Timestamp:  s.clock.Now().UnixMilli(),
			}
			body, err := json.Marshal(synthetic)
			if err != nil {
				return totalCleaned, err
			}
			entry, err := s.appendEmbeddedTx(ctx, cs.binding, "toolResult", body, headSeq, headDigest)
			if err != nil {
				return totalCleaned, err
			}
			headSeq = entry.Sequence
			headDigest = entry.Digest
			totalCleaned++
		}
	}

	return totalCleaned, nil
}

// ResetEmbeddedSession accepts only the managed row's replacement epoch. The
// old row remains historical, but loses its current bit atomically with insert.
func (s *Store) ResetEmbeddedSession(ctx context.Context, old, next EmbeddedBinding, role string) error {
	if err := validateEmbedded(old); err != nil {
		return err
	}
	if err := validateEmbedded(next); err != nil {
		return err
	}
	if old.AgentID != next.AgentID || next.Epoch <= old.Epoch || !validEmbeddedRole(role) {
		return errors.New("invalid embedded reset")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedManagedFence(ctx, next); err != nil {
			return err
		}
		var session, generation, savedRole string
		var epoch int64
		err := s.db.QueryRowContext(ctx, `SELECT session_id,epoch,generation,role FROM embedded_sessions WHERE agent_id=? AND current=1`, old.AgentID).Scan(&session, &epoch, &generation, &savedRole)
		if err != nil {
			return err
		}
		if session == next.SessionID && epoch == next.Epoch && generation == next.Generation && savedRole == role {
			return s.embeddedOwnerFence(ctx, next)
		}
		if err := s.embeddedOwnerFence(ctx, old); err != nil {
			return err
		}
		if session != old.SessionID || epoch != old.Epoch || generation != old.Generation || savedRole != role {
			return errors.New("stale embedded reset")
		}
		// A replacement epoch retires recovery authority for the old identity.
		// Drop wake slots first: they reference notices and own checked origins.
		if err := s.clearEmbeddedLifecycleRecoveryTx(ctx, old.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET current=0 WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, old.AgentID, old.SessionID, old.Epoch, old.Generation); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, next.AgentID, next.SessionID, next.Epoch, next.Generation, role)
		return err
	})
}

// ResetEmbeddedSessionEpoch fences the old owner, releases only its unconsumed
// mail reservations, and advances both epoch records in one immediate transaction.
// Callers must drain callbacks before snapshotting preserved context and resetting.
func (s *Store) ResetEmbeddedSessionEpoch(ctx context.Context, old, next EmbeddedBinding, role string) error {
	_, _, err := s.resetEmbeddedSessionEpoch(ctx, old, next, role, "", nil)
	return err
}

// ErrEmbeddedResetRolledBack marks a conclusively uncommitted reset claim.
// An unknown outcome must never be treated as a rollback.
var ErrEmbeddedResetRolledBack = errors.New("embedded reset claim rolled back")

// ResetEmbeddedSessionEpochAndClaim atomically rotates the epoch and installs
// identity as its first owner. Reuse the same identity and bindings on retry.
// A retry succeeds only while that exact claim remains authoritative.
func (s *Store) ResetEmbeddedSessionEpochAndClaim(ctx context.Context, old, next EmbeddedBinding, role, identity string) (EmbeddedBinding, error) {
	if old.OwnerToken <= 0 || next.OwnerToken != 0 || identity == "" || len(identity) > 256 {
		return EmbeddedBinding{}, errors.New("invalid embedded reset claim")
	}
	return s.resetEmbeddedSessionEpochAndClaim(ctx, old, next, role, identity, nil)
}

// ResetEmbeddedSessionEpochClaimWithSeed commits the replacement owner and its context together.
func (s *Store) ResetEmbeddedSessionEpochClaimWithSeed(ctx context.Context, old, next EmbeddedBinding, role, identity string, bodies [][]byte, imports []EmbeddedEntry) (EmbeddedBinding, error) {
	return s.resetEmbeddedSessionEpochAndClaim(ctx, old, next, role, identity, func(target EmbeddedBinding) error {
		return s.installEmbeddedResetSeedTx(ctx, target, old, identity, bodies, imports)
	})
}

func (s *Store) resetEmbeddedSessionEpochAndClaim(ctx context.Context, old, next EmbeddedBinding, role, identity string, seed func(EmbeddedBinding) error) (EmbeddedBinding, error) {
	if old.OwnerToken <= 0 || next.OwnerToken != 0 || identity == "" || len(identity) > 256 {
		return EmbeddedBinding{}, errors.New("invalid embedded reset claim")
	}
	committed, conclusive, err := s.resetEmbeddedSessionEpoch(ctx, old, next, role, identity, seed)
	if err != nil {
		if committed {
			if seed == nil {
				return EmbeddedBinding{}, err
			}
			next.OwnerToken = 1
			return next, err
		}
		if conclusive {
			return EmbeddedBinding{}, errors.Join(ErrEmbeddedResetRolledBack, err)
		}
		// Resolve only an unknown transaction outcome. A post-commit error remains an error.
		resolveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resolved, resolveErr := s.ResolveEmbeddedResetClaim(resolveCtx, old, next, role, identity)
		if resolveErr != nil {
			return EmbeddedBinding{}, errors.Join(err, fmt.Errorf("resolve reset claim: %w", resolveErr))
		}
		if seed == nil {
			return resolved, nil
		}
		return resolved, err
	}
	next.OwnerToken = 1
	return next, nil
}

// ResolveEmbeddedResetClaim only observes a previously committed operation; it
// never starts a reset. A replaced claim is not authority for publication.
func (s *Store) ResolveEmbeddedResetClaim(ctx context.Context, old, next EmbeddedBinding, role, identity string) (EmbeddedBinding, error) {
	if old.OwnerToken <= 0 || next.OwnerToken != 0 || old.AgentID != next.AgentID || identity == "" || len(identity) > 256 {
		return EmbeddedBinding{}, errors.New("invalid embedded reset claim")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return EmbeddedBinding{}, errors.New("store is closed")
	}
	// Never resolve through the writer pool: a failed rollback can leave its
	// single connection inside the transaction whose outcome we are checking.
	dsn := (&url.URL{Scheme: "file", Path: s.path, RawQuery: "mode=ro"}).String()
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		return EmbeddedBinding{}, err
	}
	defer reader.Close()
	var count int
	err = reader.QueryRowContext(ctx, `SELECT count(*) FROM embedded_sessions old JOIN embedded_sessions next ON next.agent_id=old.agent_id JOIN managed_agents m ON m.agent_id=next.agent_id
WHERE old.agent_id=? AND old.session_id=? AND old.epoch=? AND old.generation=? AND old.role=? AND old.current=0 AND old.owner_token=?
AND next.session_id=? AND next.epoch=? AND next.generation=? AND next.role=? AND next.current=1 AND next.owner_id=? AND next.owner_token=1
AND m.pi_session_id=next.session_id AND m.session_epoch=next.epoch AND m.runtime_generation=next.generation`, old.AgentID, old.SessionID, old.Epoch, old.Generation, role, old.OwnerToken, next.SessionID, next.Epoch, next.Generation, role, identity).Scan(&count)
	if err != nil {
		return EmbeddedBinding{}, err
	}
	if count != 1 {
		return EmbeddedBinding{}, fmt.Errorf("%w: reset claim not authoritative", ErrStaleEmbeddedFence)
	}
	next.OwnerToken = 1
	return next, nil
}

func (s *Store) resetEmbeddedSessionEpoch(ctx context.Context, old, next EmbeddedBinding, role, identity string, seed func(EmbeddedBinding) error) (bool, bool, error) {
	if err := validateEmbedded(old); err != nil {
		return false, true, err
	}
	if err := validateEmbedded(next); err != nil {
		return false, true, err
	}
	if old.AgentID != next.AgentID || next.Epoch <= old.Epoch || !validEmbeddedRole(role) {
		return false, true, errors.New("invalid embedded reset")
	}
	return s.runImmediateWithCommitOutcome(ctx, func() error {
		var session, generation, savedRole string
		var epoch int64
		err := s.db.QueryRowContext(ctx, `SELECT session_id,epoch,generation,role FROM embedded_sessions WHERE agent_id=? AND current=1`, old.AgentID).Scan(&session, &epoch, &generation, &savedRole)
		if err != nil {
			return err
		}
		if session == next.SessionID && epoch == next.Epoch && generation == next.Generation && savedRole == role {
			// A committed retry must still identify the retired old owner.
			// next commonly has token zero: ownership is acquired after reset.
			var prior int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_sessions
WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND role=? AND current=0
AND (?<=0 OR owner_token=?)`, old.AgentID, old.SessionID, old.Epoch, old.Generation, role, old.OwnerToken, old.OwnerToken).Scan(&prior); err != nil {
				return err
			}
			if prior != 1 {
				return errors.New("stale embedded reset owner")
			}
			if identity != "" {
				var owner sql.NullString
				var token int64
				if err := s.db.QueryRowContext(ctx, `SELECT owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, next.AgentID, next.SessionID, next.Epoch, next.Generation).Scan(&owner, &token); err != nil {
					return err
				}
				if !owner.Valid || owner.String != identity || token != 1 {
					return fmt.Errorf("%w: reset claim replaced", ErrStaleEmbeddedFence)
				}
			}
			if err := s.embeddedFence(ctx, next); err != nil {
				return err
			}
			if seed != nil {
				next.OwnerToken = 1
				return seed(next)
			}
			return nil
		}
		if err := s.embeddedFence(ctx, old); err != nil {
			return err
		}
		if session != old.SessionID || epoch != old.Epoch || generation != old.Generation || savedRole != role {
			return errors.New("stale embedded reset")
		}

		// Acceptance is not consumption. Keep ACKed attempts and checked user
		// appends even if their ACK is absent. Correlate by the reserved append
		// key, never by equal message text. No accepted message fields change.
		if _, err := s.db.ExecContext(ctx, `DELETE FROM embedded_attempts
WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?
AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.attempt_id=embedded_attempts.attempt_id)
AND NOT EXISTS (
 SELECT 1 FROM embedded_append_keys k JOIN embedded_entries e
 ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch
 AND e.generation=k.generation AND e.seq=k.entry_seq
 WHERE k.append_key=embedded_attempts.append_key AND e.kind='user'
 AND k.agent_id=embedded_attempts.agent_id AND k.session_id=embedded_attempts.session_id
 AND k.epoch=embedded_attempts.epoch AND k.generation=embedded_attempts.generation
)`, old.AgentID, old.SessionID, old.Epoch, old.Generation); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?, session_epoch=?, updated_at=? WHERE agent_id=? AND pi_session_id=? AND session_epoch=? AND runtime_generation=?`, next.SessionID, next.Epoch, now, old.AgentID, old.SessionID, old.Epoch, old.Generation); err != nil {
			return err
		}
		if err := s.clearEmbeddedLifecycleRecoveryTx(ctx, old.AgentID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_sessions SET current=0 WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, old.AgentID, old.SessionID, old.Epoch, old.Generation); err != nil {
			return err
		}
		if identity != "" {
			expires := formatTimestamp(s.clock.Now().Add(100 * 365 * 24 * time.Hour))
			_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current,owner_id,owner_token,owner_expires_at) VALUES(?,?,?,?,?,1,?,1,?)`, next.AgentID, next.SessionID, next.Epoch, next.Generation, role, identity, expires)
		} else {
			_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, next.AgentID, next.SessionID, next.Epoch, next.Generation, role)
		}
		if err != nil {
			return err
		}
		if seed != nil {
			next.OwnerToken = 1
			return seed(next)
		}
		return nil
	})
}

// clearEmbeddedLifecycleRecoveryTx is called only inside an immediate transaction
// after the managed identity fence. Older schemas may lack either table.
func (s *Store) clearEmbeddedLifecycleRecoveryTx(ctx context.Context, agentID string) error {
	for _, table := range []string{"embedded_restart_wake_slots", "embedded_interrupted_notices"} {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			return err
		}
		if exists == 1 {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE agent_id=?`, agentID); err != nil {
				return err
			}
		}
	}
	return s.pruneEmbeddedProcessGenerationsTx(ctx)
}

func embeddedHash(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:", len(p))
		_, _ = h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func embeddedEntryDigest(b EmbeddedBinding, e EmbeddedEntry) string {
	return embeddedHash([]byte(b.AgentID), []byte(b.SessionID), []byte(fmt.Sprint(b.Epoch)), []byte(fmt.Sprint(e.Sequence)), []byte(e.ParentDigest), []byte(e.Kind), e.Body)
}
func embeddedCheckpointDigest(b EmbeddedBinding, c EmbeddedCheckpoint) string {
	return embeddedHash([]byte(b.AgentID), []byte(b.SessionID), []byte(fmt.Sprint(b.Epoch)), []byte(fmt.Sprint(c.Sequence)), []byte(c.AncestorDigest), c.Body)
}
func embeddedBody(body []byte) error {
	if len(body) == 0 || len(body) > embeddedMaxBody {
		return errors.New("embedded body size invalid")
	}
	return nil
}

// appendEmbeddedTx runs only inside runImmediate. The caller checks its binding
// and expected head in the same transaction as the insert.
func (s *Store) appendEmbeddedTx(ctx context.Context, b EmbeddedBinding, kind string, body []byte, expectedSeq int64, expectedDigest string) (EmbeddedEntry, error) {
	return s.appendEmbeddedAdmittedTx(ctx, b, kind, body, expectedSeq, expectedDigest, s.clock.Now().UnixMilli())
}

func (s *Store) appendEmbeddedAdmittedTx(ctx context.Context, b EmbeddedBinding, kind string, body []byte, expectedSeq int64, expectedDigest string, admittedAt int64) (EmbeddedEntry, error) {
	if admittedAt <= 0 {
		return EmbeddedEntry{}, errors.New("invalid retention admission time")
	}
	if kind == "" || len(kind) > 64 {
		return EmbeddedEntry{}, errors.New("invalid embedded entry kind")
	}
	if err := embeddedBody(body); err != nil {
		return EmbeddedEntry{}, err
	}
	var seq int64
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch).Scan(&seq, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		seq = 0
		digest = embeddedGenesis
	} else if err != nil {
		return EmbeddedEntry{}, err
	}
	if seq != expectedSeq {
		return EmbeddedEntry{}, errors.New("embedded head mismatch")
	}
	e := EmbeddedEntry{Sequence: seq + 1, ParentDigest: digest, Kind: kind, Body: bytes.Clone(body)}
	e.Digest = embeddedEntryDigest(b, e)
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest,retention_admitted_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, e.Sequence, e.ParentDigest, e.Kind, e.Body, e.Digest, admittedAt)
	return e, err
}
func (s *Store) embeddedHead(ctx context.Context, b EmbeddedBinding) (int64, string, error) {
	var seq int64
	var digest string
	err := s.db.QueryRowContext(ctx, `SELECT seq,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq DESC LIMIT 1`, b.AgentID, b.SessionID, b.Epoch).Scan(&seq, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, embeddedGenesis, nil
	}
	return seq, digest, err
}

// AppendEmbeddedEntry is a convenience for serialized callers. For an explicit
// retry identity and compare-and-append, use AppendEmbeddedEntryAt.
func (s *Store) AppendEmbeddedEntry(ctx context.Context, b EmbeddedBinding, kind string, body []byte) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		entry, err = s.appendEmbeddedTx(ctx, b, kind, body, seq, digest)
		return err
	})
	return entry, err
}

// AppendEmbeddedEntryAt requires the caller's observed head and a stable key.
// The key and entry are committed together. A retry succeeds only if the
// binding, expected head, kind and payload match the original append.
func (s *Store) AppendEmbeddedEntryAt(ctx context.Context, b EmbeddedBinding, expectedSeq int64, expectedDigest, key, kind string, body []byte) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	if key == "" || len(key) > 1024 || expectedSeq < 0 {
		return entry, errors.New("invalid append expectation or key")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var err error
		entry, err = s.appendEmbeddedKeyedTx(ctx, b, expectedSeq, expectedDigest, key, kind, body, nil)
		return err
	})
	return entry, err
}

// A nil origin is explicitly untagged. No retry may attach an origin later.
func (s *Store) appendEmbeddedKeyedTx(ctx context.Context, b EmbeddedBinding, expectedSeq int64, expectedDigest, key, kind string, body []byte, origin *embeddedWakeOrigin) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	if key == "" || len(key) > 1024 || expectedSeq < 0 {
		return entry, errors.New("invalid append expectation or key")
	}
	var seq int64
	var savedKind, savedDigest string
	var savedBody []byte
	err := s.db.QueryRowContext(ctx, `SELECT e.seq,e.kind,e.digest,e.body FROM embedded_append_keys k JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE k.append_key=? AND k.agent_id=? AND k.session_id=? AND k.epoch=? AND k.generation=?`, key, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&seq, &savedKind, &savedDigest, &savedBody)
	if err == nil {
		if seq != expectedSeq+1 || savedKind != kind || !bytes.Equal(savedBody, body) {
			return entry, errors.New("conflicting embedded append key")
		}
		var parent string
		if err := s.db.QueryRowContext(ctx, `SELECT parent_digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, seq).Scan(&parent); err != nil {
			return entry, err
		}
		entry = EmbeddedEntry{Sequence: seq, ParentDigest: parent, Kind: kind, Body: bytes.Clone(savedBody), Digest: savedDigest}
		if err := s.matchEmbeddedWakeOrigin(ctx, b, entry.Sequence, origin); err != nil {
			return EmbeddedEntry{}, err
		}
		return entry, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return entry, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_append_keys WHERE append_key=?`, key).Scan(&exists); err != nil {
		return entry, err
	}
	if exists != 0 {
		return entry, errors.New("embedded append key belongs to another binding")
	}
	entry, err = s.appendEmbeddedTx(ctx, b, kind, body, expectedSeq, expectedDigest)
	if err != nil {
		return entry, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_append_keys(append_key,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?)`, key, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence)
	if err != nil {
		return EmbeddedEntry{}, err
	}
	if origin != nil {
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_restart_wake_checked_origins(agent_id,session_id,epoch,runtime_generation,seq,slot,wake_id,process_generation,claim_owner_token) VALUES(?,?,?,?,?,'current',?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence, origin.id, origin.g, b.OwnerToken)
	}
	return entry, err
}
func (s *Store) CommitEmbeddedCheckpoint(ctx context.Context, b EmbeddedBinding, seq int64, ancestor string, body []byte) error {
	if err := embeddedBody(body); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var digest string
		if err := s.db.QueryRowContext(ctx, `SELECT digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, seq).Scan(&digest); err != nil {
			return err
		}
		c := EmbeddedCheckpoint{Sequence: seq, AncestorDigest: digest, Body: bytes.Clone(body)}
		c.Digest = embeddedCheckpointDigest(b, c)
		_, err := s.db.ExecContext(ctx, `INSERT INTO embedded_checkpoints(agent_id,session_id,epoch,generation,at_seq,ancestor_digest,body,digest) VALUES(?,?,?,?,?,?,?,?)`, b.AgentID, b.SessionID, b.Epoch, b.Generation, c.Sequence, c.AncestorDigest, c.Body, c.Digest)
		return err
	})
}
func (s *Store) ReopenLatestAgentEmbedded(ctx context.Context, agentID string) (EmbeddedSnapshot, error) {
	out := EmbeddedSnapshot{Entries: []EmbeddedEntry{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return out, errors.New("store is closed")
	}
	var sessionID string
	var epoch int64
	err := s.db.QueryRowContext(ctx, `SELECT session_id, epoch FROM embedded_entries WHERE agent_id=? ORDER BY rowid DESC LIMIT 1`, agentID).Scan(&sessionID, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	b := EmbeddedBinding{AgentID: agentID, SessionID: sessionID, Epoch: epoch}
	return s.readEmbeddedSnapshotTx(ctx, b)
}

func (s *Store) ReopenEmbedded(ctx context.Context, b EmbeddedBinding) (EmbeddedSnapshot, error) {
	out := EmbeddedSnapshot{Entries: []EmbeddedEntry{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return out, errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return out, err
	}
	return s.readEmbeddedSnapshotTx(ctx, b)
}

// Call only with s.mu held, after the caller has checked its own authority.
// The same chain and checkpoint checks protect runtime reopen and parent reads.
func (s *Store) readEmbeddedSnapshotTx(ctx context.Context, b EmbeddedBinding) (EmbeddedSnapshot, error) {
	out := EmbeddedSnapshot{Entries: []EmbeddedEntry{}}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY seq`, b.AgentID, b.SessionID, b.Epoch)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e EmbeddedEntry
		if err = rows.Scan(&e.Sequence, &e.ParentDigest, &e.Kind, &e.Body, &e.Digest); err != nil {
			break
		}
		if e.Sequence != int64(len(out.Entries)+1) || e.Kind == "" || embeddedBody(e.Body) != nil {
			err = errors.New("corrupt embedded entry chain")
			break
		}
		out.Entries = append(out.Entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return EmbeddedSnapshot{}, err
	}
	if closeErr != nil {
		return EmbeddedSnapshot{}, closeErr
	}
	checkpoints, err := s.db.QueryContext(ctx, `SELECT at_seq,ancestor_digest,body,digest FROM embedded_checkpoints WHERE agent_id=? AND session_id=? AND epoch=? ORDER BY at_seq`, b.AgentID, b.SessionID, b.Epoch)
	if err != nil {
		return EmbeddedSnapshot{}, err
	}
	for checkpoints.Next() {
		var c EmbeddedCheckpoint
		if err = checkpoints.Scan(&c.Sequence, &c.AncestorDigest, &c.Body, &c.Digest); err != nil {
			break
		}
		if c.Sequence <= 0 || c.Sequence > int64(len(out.Entries)) || out.Entries[c.Sequence-1].Digest != c.AncestorDigest || embeddedBody(c.Body) != nil || embeddedCheckpointDigest(b, c) != c.Digest {
			err = errors.New("corrupt embedded checkpoint ancestry")
			break
		}
		out.Checkpoint = &c
	}
	if err == nil {
		err = checkpoints.Err()
	}
	closeErr = checkpoints.Close()
	if err != nil {
		return EmbeddedSnapshot{}, err
	}
	if closeErr != nil {
		return EmbeddedSnapshot{}, closeErr
	}
	return out, nil
}

// An intent is committed before Execute; even a crash immediately after this
// call leaves outcome unknown. Reusing an ID is forbidden, including identical
// input: a caller must reconcile it, not run the effect again.
func (s *Store) RecordEmbeddedEffectIntent(ctx context.Context, b EmbeddedBinding, id string, intent []byte) error {
	if id == "" || len(id) > 256 {
		return errors.New("invalid effect ID")
	}
	if err := embeddedBody(intent); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		// Only the own-reminder effect needs its registering lease incarnation.
		// Other tools retain their existing unknown-effect semantics.
		var call struct {
			Name string `json:"name"`
		}
		var reminderOwner any
		if json.Unmarshal(intent, &call) == nil && (call.Name == "mecha_reminder" || call.Name == "mecha_context") {
			reminderOwner = b.OwnerToken
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO embedded_effects(effect_id,agent_id,session_id,epoch,generation,intent,state,reminder_owner_token) VALUES(?,?,?,?,?,?,'unknown',?)`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, bytes.Clone(intent), reminderOwner)
		return err
	})
}
func (s *Store) ObserveEmbeddedEffect(ctx context.Context, b EmbeddedBinding, id string, result []byte) error {
	if err := embeddedBody(result); err != nil {
		return err
	}
	return s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_effects SET state='observed',result=? WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown'`, bytes.Clone(result), id, b.AgentID, b.SessionID, b.Epoch, b.Generation)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("effect already observed or not bound")
		}
		return nil
	})
}
func (s *Store) RecoverEmbeddedEffect(ctx context.Context, b EmbeddedBinding, id string) (EmbeddedEffect, error) {
	var e EmbeddedEffect
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return e, errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return e, err
	}
	err := s.db.QueryRowContext(ctx, `SELECT state,intent,result FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&e.State, &e.Intent, &e.Result)
	if err != nil {
		return EmbeddedEffect{}, err
	}
	if e.State != "unknown" && e.State != "observed" {
		return EmbeddedEffect{}, errors.New("invalid effect state")
	}
	return e, nil
}

// EmbeddedGoalInvocation is process-local, one-use admission. It is never
// reconstructed from saved intent, domain receipt, or SDK history.
type EmbeddedGoalInvocation struct {
	mu                                   sync.Mutex
	store                                *Store
	binding                              EmbeddedBinding
	process, userSeq                     int64
	userDigest, attempt, id, name        string
	intent, content                      []byte
	executing, ready, appended, consumed bool
	entry                                EmbeddedEntry
}

func (s *Store) embeddedGoalInvocationFenceTx(ctx context.Context, w *EmbeddedGoalInvocation) error {
	if w == nil || w.store != s || w.process <= 0 {
		return errors.New("missing current goal invocation")
	}
	if err := s.embeddedFence(ctx, w.binding); err != nil {
		return err
	}
	var revision int64
	if err := s.db.QueryRowContext(ctx, `SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?`, w.binding.AgentID).Scan(&revision); err != nil {
		return err
	}
	if err := s.embeddedEvidenceReplayTx(ctx, w.binding, EmbeddedGoalEvaluationKey{UserSeq: w.userSeq, UserDigest: w.userDigest, GoalRevision: revision}, w.id); err != nil {
		return err
	}
	var process int64
	if err := s.db.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&process); err != nil {
		return err
	}
	if process != w.process {
		return errors.New("stale goal invocation process")
	}
	var entry EmbeddedEntry
	entry.Sequence = w.userSeq
	if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation, w.userSeq).Scan(&entry.Kind, &entry.Body, &entry.ParentDigest, &entry.Digest); err != nil {
		return err
	}
	if entry.Kind != "user" || entry.Digest != w.userDigest {
		return errors.New("goal invocation user mismatch")
	}
	var later, entered int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq>? AND kind='user'`, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.userSeq).Scan(&later); err != nil {
		return err
	}
	if later != 0 {
		return errors.New("goal invocation turn superseded")
	}
	if w.attempt == "" {
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_nonmail_provider_entries p WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND user_seq=? AND user_digest=? AND NOT EXISTS (SELECT 1 FROM embedded_acks a WHERE a.agent_id=p.agent_id AND a.session_id=p.session_id AND a.epoch=p.epoch AND a.entry_seq=p.user_seq)`, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation, w.userSeq, w.userDigest).Scan(&entered); err != nil {
			return err
		}
	} else {
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND entry_seq=? AND attempt_id=?`, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation, w.userSeq, w.attempt).Scan(&entered); err != nil {
			return err
		}
	}
	if entered != 1 {
		return errors.New("goal invocation has no checked provider turn or mail ACK")
	}
	return nil
}

// RecordEmbeddedGoalInvocation inserts the original intent and returns its
// admission only to this invocation. Existing IDs never receive a witness.
func (s *Store) RecordEmbeddedGoalInvocation(ctx context.Context, b EmbeddedBinding, process, userSeq int64, userDigest, attempt, id string, intent []byte) (*EmbeddedGoalInvocation, error) {
	var call struct {
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if id == "" || len(id) > 256 || embeddedBody(intent) != nil || json.Unmarshal(intent, &call) != nil || call.ID != id || (call.Name != "goal" && call.Name != "goal_snooze" && call.Name != "workflow_snooze") {
		return nil, errors.New("invalid goal invocation intent")
	}
	if call.Name == "goal" && call.Arguments["action"] != "set" && call.Arguments["action"] != "off" {
		return nil, errors.New("goal invocation requires a mutation")
	}
	w := &EmbeddedGoalInvocation{store: s, binding: b, process: process, userSeq: userSeq, userDigest: userDigest, attempt: attempt, id: id, name: call.Name, intent: bytes.Clone(intent)}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedGoalInvocationFenceTx(ctx, w); err != nil {
			return err
		}
		var prior int
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM embedded_goal_calls WHERE call_id=?) + (SELECT count(*) FROM embedded_goal_snooze_calls WHERE call_id=?)`, id, id).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 {
			return errors.New("historical goal domain call cannot receive current admission")
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO embedded_effects(effect_id,agent_id,session_id,epoch,generation,intent,state) VALUES(?,?,?,?,?,?,'unknown')`, id, b.AgentID, b.SessionID, b.Epoch, b.Generation, w.intent)
		return err
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Store) checkEmbeddedGoalInvocationIntentTx(ctx context.Context, w *EmbeddedGoalInvocation) error {
	if err := s.embeddedGoalInvocationFenceTx(ctx, w); err != nil {
		return err
	}
	var state string
	var intent []byte
	if err := s.db.QueryRowContext(ctx, `SELECT state,intent FROM embedded_effects WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, w.id, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation).Scan(&state, &intent); err != nil {
		return err
	}
	if state != "unknown" || !bytes.Equal(intent, w.intent) {
		return errors.New("goal invocation intent is not fresh unknown")
	}
	return nil
}

// CheckEmbeddedGoalInvocation is the additional current-process Execute fence.
func (s *Store) CheckEmbeddedGoalInvocation(ctx context.Context, w *EmbeddedGoalInvocation) error {
	if w == nil {
		return errors.New("missing current goal invocation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.executing || w.ready || w.appended || w.consumed {
		return errors.New("goal invocation already executed")
	}
	w.executing = true // Consume Execute admission even if its fence fails.
	err := s.runImmediate(ctx, func() error { return s.checkEmbeddedGoalInvocationIntentTx(ctx, w) })
	if err != nil {
		w.consumed = true
	}
	return err
}

func (s *Store) checkEmbeddedGoalInvocationDomainTx(ctx context.Context, w *EmbeddedGoalInvocation, result ai.ToolResultMessage) error {
	if result.ToolCallID != w.id || result.ToolName != w.name || result.IsError || len(result.Content) != 1 {
		return errors.New("goal invocation SDK result mismatch")
	}
	text, ok := result.Content[0].(ai.TextContent)
	if !ok {
		return errors.New("goal invocation result is not text")
	}
	var payload string
	var err error
	if w.name == "goal" {
		err = s.db.QueryRowContext(ctx, `SELECT c.result_json FROM embedded_goal_calls c JOIN embedded_sessions e ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.generation=c.generation AND e.role=c.role AND e.current=1 WHERE c.call_id=? AND c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=? AND c.state='complete'`, w.id, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation).Scan(&payload)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT c.result_json FROM embedded_goal_snooze_calls c JOIN embedded_sessions e ON e.agent_id=c.agent_id AND e.session_id=c.session_id AND e.epoch=c.epoch AND e.generation=c.generation AND e.role=c.role AND e.current=1 WHERE c.call_id=? AND c.agent_id=? AND c.session_id=? AND c.epoch=? AND c.generation=? AND c.user_seq=? AND c.user_digest=?`, w.id, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation, w.userSeq, w.userDigest).Scan(&payload)
	}
	if err != nil {
		return err
	}
	if w.name == "workflow_snooze" || w.name == "goal_snooze" {
		var p EmbeddedGoalSnoozePreference
		if err = json.Unmarshal([]byte(payload), &p); err != nil {
			return err
		}
		var expected []byte
		if w.name == "workflow_snooze" {
			expected, err = p.WorkflowResultJSON()
		} else {
			expected, err = p.GoalResultJSON()
		}
		if err != nil {
			return err
		}
		payload = string(expected)
	}
	if !bytes.Equal([]byte(text.Text), []byte(payload)) {
		return errors.New("goal invocation result differs from successful domain transaction")
	}
	return nil
}

// CompleteEmbeddedGoalInvocation is called only after this Host Execute's
// successful non-replay domain transaction. Saved domain rows cannot mint it.
func (s *Store) CompleteEmbeddedGoalInvocation(ctx context.Context, w *EmbeddedGoalInvocation, result ai.ToolResultMessage) error {
	if w == nil {
		return errors.New("missing current goal invocation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.executing || w.ready || w.appended || w.consumed {
		return errors.New("goal invocation completion already supplied or Execute absent")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.checkEmbeddedGoalInvocationIntentTx(ctx, w); err != nil {
			return err
		}
		return s.checkEmbeddedGoalInvocationDomainTx(ctx, w, result)
	})
	if err != nil {
		w.consumed = true
		return err
	}
	content, err := json.Marshal(result.Content)
	if err != nil {
		w.consumed = true
		return err
	}
	w.content, w.ready = content, true
	return nil
}

func (s *Store) AppendEmbeddedGoalInvocationResult(ctx context.Context, w *EmbeddedGoalInvocation, expectedSeq int64, expectedDigest, key string, result ai.ToolResultMessage, body []byte) (EmbeddedEntry, error) {
	if w == nil {
		return EmbeddedEntry{}, errors.New("missing current goal invocation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready || w.appended || w.consumed {
		return EmbeddedEntry{}, errors.New("goal invocation has no one-use completion")
	}
	w.appended = true // A failed append is not retry authority.
	content, err := json.Marshal(result.Content)
	if err != nil {
		return EmbeddedEntry{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return EmbeddedEntry{}, err
	}
	if result.Timestamp <= 0 || !bytes.Equal(content, w.content) || !bytes.Equal(encoded, body) {
		return EmbeddedEntry{}, errors.New("goal invocation checked SDK bytes mismatch")
	}
	var entry EmbeddedEntry
	err = s.runImmediate(ctx, func() error {
		if err := s.checkEmbeddedGoalInvocationIntentTx(ctx, w); err != nil {
			return err
		}
		if err := s.checkEmbeddedGoalInvocationDomainTx(ctx, w, result); err != nil {
			return err
		}
		var prior int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_append_keys WHERE append_key=?`, key).Scan(&prior); err != nil {
			return err
		}
		if prior != 0 || expectedSeq < w.userSeq {
			return errors.New("goal invocation cannot replay a checked append")
		}
		var err error
		entry, err = s.appendEmbeddedKeyedTx(ctx, w.binding, expectedSeq, expectedDigest, key, string(ai.RoleToolResult), body, nil)
		return err
	})
	if err == nil {
		w.entry = entry
		w.entry.Body = bytes.Clone(entry.Body)
	}
	return entry, err
}

// Observation is deliberately separate from checked append. Losing the live
// witness between these transactions leaves UNKNOWN, even with saved SDK bytes.
func (s *Store) ObserveEmbeddedGoalInvocation(ctx context.Context, w *EmbeddedGoalInvocation, entry EmbeddedEntry) error {
	if w == nil {
		return errors.New("missing current goal invocation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.ready || !w.appended || w.consumed || w.entry.Sequence <= 0 {
		return errors.New("goal invocation has no checked completion")
	}
	w.consumed = true
	if entry.Sequence != w.entry.Sequence || entry.Digest != w.entry.Digest || entry.ParentDigest != w.entry.ParentDigest || entry.Kind != w.entry.Kind || !bytes.Equal(entry.Body, w.entry.Body) {
		return errors.New("goal invocation checked entry mismatch")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.checkEmbeddedGoalInvocationIntentTx(ctx, w); err != nil {
			return err
		}
		var saved EmbeddedEntry
		saved.Sequence = entry.Sequence
		if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation, entry.Sequence).Scan(&saved.Kind, &saved.Body, &saved.ParentDigest, &saved.Digest); err != nil {
			return err
		}
		if saved.Kind != entry.Kind || saved.Digest != entry.Digest || saved.ParentDigest != entry.ParentDigest || !bytes.Equal(saved.Body, entry.Body) {
			return errors.New("goal invocation SDK entry is not persisted")
		}
		message, err := ai.UnmarshalMessage(saved.Body)
		if err != nil {
			return err
		}
		result, ok := message.(ai.ToolResultMessage)
		if !ok {
			return errors.New("goal invocation saved SDK entry is not a tool result")
		}
		if err := s.checkEmbeddedGoalInvocationDomainTx(ctx, w, result); err != nil {
			return err
		}
		r, err := s.db.ExecContext(ctx, `UPDATE embedded_effects SET state='observed',result=? WHERE effect_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=? AND state='unknown'`, bytes.Clone(saved.Body), w.id, w.binding.AgentID, w.binding.SessionID, w.binding.Epoch, w.binding.Generation)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("goal invocation observation lost")
		}
		return nil
	})
}

// EmbeddedMailAttempt is a frozen accepted envelope. Only a safely unstarted
// legacy reservation surviving retirement by an older binary can move to the
// current session; normal reset deletes unconsumed attempts for fresh allocation.
type EmbeddedMailAttempt struct {
	AttemptID, MessageID, MessageIncarnationID, SnapshotID string
	Sender, Body, AcceptedDigest                           string
	Binding                                                EmbeddedBinding
	Attachments                                            []MailAttachment
}

func (s *Store) embeddedMailAttemptTx(ctx context.Context, id string) (*EmbeddedMailAttempt, error) {
	a := &EmbeddedMailAttempt{}
	err := s.db.QueryRowContext(ctx, `SELECT attempt_id,message_id,message_incarnation_id,snapshot_id,sender,accepted_body,accepted_digest,agent_id,session_id,epoch,generation FROM embedded_attempts WHERE message_id=?`, id).Scan(&a.AttemptID, &a.MessageID, &a.MessageIncarnationID, &a.SnapshotID, &a.Sender, &a.Body, &a.AcceptedDigest, &a.Binding.AgentID, &a.Binding.SessionID, &a.Binding.Epoch, &a.Binding.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Store) embeddedMailAcceptedTx(ctx context.Context, a *EmbeddedMailAttempt, requireQueued bool) (string, error) {
	var incarnation, sender, recipient, body, state, expiry string
	err := s.db.QueryRowContext(ctx, `SELECT message_incarnation_id,sender,recipient,body,state,expires_at FROM messages WHERE id=?`, a.MessageID).Scan(&incarnation, &sender, &recipient, &body, &state, &expiry)
	if err != nil {
		return "", err
	}
	if incarnation != a.MessageIncarnationID || sender != a.Sender || recipient != a.Binding.AgentID || body != a.Body || embeddedHash([]byte(a.MessageID), []byte(incarnation), []byte(sender), []byte(recipient), []byte(body)) != a.AcceptedDigest {
		return "", fmt.Errorf("%w: embedded accepted mail changed", ErrStaleEmbeddedFence)
	}
	var snapshot, kind, owner, sourceKind, sourceOwner, sourceState string
	err = s.db.QueryRowContext(ctx, `SELECT t.snapshot_id,t.source_kind,t.owner_identity,r.source_kind,r.owner_identity,r.state FROM message_target_snapshots t JOIN recipient_sources r ON r.recipient_name=t.recipient_name WHERE t.message_id=? AND t.message_incarnation_id=? AND t.recipient_name=?`, a.MessageID, incarnation, recipient).Scan(&snapshot, &kind, &owner, &sourceKind, &sourceOwner, &sourceState)
	if err != nil {
		return "", err
	}
	if snapshot != a.SnapshotID || kind != "managed" || owner != recipient || sourceKind != "managed" || sourceOwner != recipient || sourceState != "active" {
		return "", fmt.Errorf("%w: embedded mail target changed", ErrStaleEmbeddedFence)
	}
	if requireQueued && (state != "queued" || expiry <= formatTimestamp(s.clock.Now())) {
		return "", fmt.Errorf("%w: embedded mail is no longer queued or live", ErrStaleEmbeddedFence)
	}
	return state, nil
}

// ReserveEmbeddedMail is the sole attempt allocator. Reservations survive Store
// reopen. Legacy unstarted reservations left by an older binary can be reconciled
// only from an exact retired epoch; normal reset allocates a new attempt instead.
func (s *Store) ReserveEmbeddedMail(ctx context.Context, b EmbeddedBinding, messageID string) (*EmbeddedMailAttempt, error) {
	if messageID == "" {
		return nil, errors.New("missing message ID")
	}
	var out *EmbeddedMailAttempt
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, messageID)
		if err != nil {
			return err
		}
		if a != nil {
			var held bool
			if err := s.db.QueryRowContext(ctx, `SELECT claude_stdin_started FROM messages WHERE id=? AND message_incarnation_id=?`, a.MessageID, a.MessageIncarnationID).Scan(&held); err != nil {
				return err
			}
			if held {
				return errors.New("Claude stdin outcome uncertain; mail held")
			}
			if a.Binding.AgentID != b.AgentID || a.Binding.SessionID != b.SessionID || a.Binding.Epoch != b.Epoch || a.Binding.Generation != b.Generation {
				if a.Binding.AgentID != b.AgentID || a.Binding.Generation != b.Generation || a.Binding.Epoch >= b.Epoch {
					return errors.New("embedded mail reserved by another binding")
				}
				if _, err := s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
					return err
				}
				// Compatibility only: older binaries could retire a session without
				// releasing its unstarted reservations. Normal reset deletes them.
				// The current requester is fenced above. Retired identity and absence
				// of admission evidence must also hold in this same transaction;
				// neither an expired lease nor a missing session grants takeover.
				r, err := s.db.ExecContext(ctx, `UPDATE embedded_attempts SET session_id=?,epoch=?
					WHERE attempt_id=? AND message_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?
					AND EXISTS (SELECT 1 FROM embedded_sessions s WHERE s.agent_id=embedded_attempts.agent_id
						AND s.session_id=embedded_attempts.session_id AND s.epoch=embedded_attempts.epoch
						AND s.generation=embedded_attempts.generation AND s.current=0)
					AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.attempt_id=embedded_attempts.attempt_id)
					AND NOT EXISTS (SELECT 1 FROM embedded_append_keys k WHERE k.append_key=embedded_attempts.append_key)
					AND NOT EXISTS (SELECT 1 FROM embedded_reply_outbox r WHERE r.attempt_id=embedded_attempts.attempt_id)`,
					b.SessionID, b.Epoch, a.AttemptID, a.MessageID, a.Binding.AgentID, a.Binding.SessionID, a.Binding.Epoch, a.Binding.Generation)
				if err != nil {
					return err
				}
				n, err := r.RowsAffected()
				if err != nil {
					return err
				}
				if n != 1 {
					return errors.New("embedded mail reserved by another binding")
				}
				a.Binding.SessionID, a.Binding.Epoch = b.SessionID, b.Epoch
			}
			state, err := s.embeddedMailAcceptedTx(ctx, a, false)
			if err != nil {
				return err
			}
			if state == "queued" {
				if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
					return err
				}
			} else if state != "acknowledged" {
				return errors.New("embedded mail no longer reservable")
			}
			a.Binding.OwnerToken = b.OwnerToken
			a.Attachments = s.loadMessageAttachmentsTx(ctx, messageID)
			out = a
			return nil
		}
		var incarnation, sender, recipient, body, state, expiry string
		var held bool
		err = s.db.QueryRowContext(ctx, `SELECT message_incarnation_id,sender,recipient,body,state,expires_at,claude_stdin_started FROM messages WHERE id=?`, messageID).Scan(&incarnation, &sender, &recipient, &body, &state, &expiry, &held)
		if err != nil {
			return err
		}
		if held || incarnation == "" || recipient != b.AgentID || state != "queued" || expiry <= formatTimestamp(s.clock.Now()) {
			return errors.New("embedded message is not a live fixed target")
		}
		target, err := snapshotForMessageTx(ctx, s.db, messageID)
		if err != nil {
			return err
		}
		if target.MessageIncarnationID != incarnation || target.RecipientName != recipient || target.SourceKind != "managed" || target.OwnerIdentity != b.AgentID {
			return errors.New("embedded target mismatch")
		}
		id, err := s.ids.Next()
		if err != nil {
			return err
		}
		a = &EmbeddedMailAttempt{AttemptID: id, MessageID: messageID, MessageIncarnationID: incarnation, SnapshotID: target.SnapshotID, Sender: sender, Body: body, AcceptedDigest: embeddedHash([]byte(messageID), []byte(incarnation), []byte(sender), []byte(recipient), []byte(body)), Binding: b, Attachments: s.loadMessageAttachmentsTx(ctx, messageID)}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_attempts(attempt_id,message_id,message_incarnation_id,snapshot_id,sender,accepted_body,accepted_digest,agent_id,session_id,epoch,generation,append_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, messageID, incarnation, target.SnapshotID, sender, body, a.AcceptedDigest, b.AgentID, b.SessionID, b.Epoch, b.Generation, "embedded-mail:"+id)
		if err == nil {
			out = a
		}
		return err
	})
	return out, err
}

// The attributed payload is a role-valid SDK user message. Its sole text
// content is a canonical JSON envelope; decoding cannot invent a sender.
func embeddedMailUser(a *EmbeddedMailAttempt, timestamp int64) ([]byte, error) {
	content, err := json.Marshal(struct{ Version, AttemptID, MessageID, IncarnationID, Sender, Recipient, Body string }{"embedded-mail/v1", a.AttemptID, a.MessageID, a.MessageIncarnationID, a.Sender, a.Binding.AgentID, a.Body})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: string(content)}}, Timestamp: timestamp})
}

// EmbeddedMailUserMessage constructs the exact SDK prompt that the checked
// append callback must commit. Attribution is taken only from the reservation.
func EmbeddedMailUserMessage(a *EmbeddedMailAttempt, timestamp int64) (ai.UserMessage, error) {
	body, err := embeddedMailUser(a, timestamp)
	if err != nil {
		return ai.UserMessage{}, err
	}
	message, err := ai.UnmarshalMessage(body)
	if err != nil {
		return ai.UserMessage{}, err
	}
	return message.(ai.UserMessage), nil
}

type MailAttachment struct {
	ID        string `json:"id,omitempty"`
	URL       string `json:"url,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	SizeBytes int    `json:"size_bytes,omitempty"`
	Name      string `json:"name,omitempty"`
	Path      string `json:"path,omitempty"`
	Data      string `json:"data,omitempty"`
}

func (s *Store) loadMessageAttachmentsTx(ctx context.Context, messageID string) []MailAttachment {
	var attachmentsJSON sql.NullString
	_ = s.db.QueryRowContext(ctx, `SELECT attachments_json FROM conversation_messages WHERE message_id=? ORDER BY conversation_sequence DESC LIMIT 1`, messageID).Scan(&attachmentsJSON)

	var attachments []MailAttachment
	if attachmentsJSON.Valid && attachmentsJSON.String != "" && attachmentsJSON.String != "[]" {
		if err := json.Unmarshal([]byte(attachmentsJSON.String), &attachments); err != nil {
			var stringPaths []string
			if err2 := json.Unmarshal([]byte(attachmentsJSON.String), &stringPaths); err2 == nil {
				for _, p := range stringPaths {
					attachments = append(attachments, MailAttachment{Path: p})
				}
			}
		}
	}

	if s.path != "" {
		attDir := s.path + ".attachments"
		for i := range attachments {
			if attachments[i].Path == "" && attachments[i].ID != "" {
				cand := filepath.Join(attDir, attachments[i].ID)
				if _, err := os.Stat(cand); err == nil {
					attachments[i].Path = cand
				}
			}
		}
	}
	return attachments
}

// NextEmbeddedMail selects the oldest queued, fixed managed target. Reservation
// still checks every field atomically, so this read grants no authority.
func (s *Store) NextEmbeddedMail(ctx context.Context, agentID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("store is closed")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT m.id FROM messages m JOIN message_target_snapshots t ON t.message_id=m.id AND t.message_incarnation_id=m.message_incarnation_id JOIN recipient_sources r ON r.recipient_name=t.recipient_name WHERE m.state='queued' AND m.claude_stdin_started=0 AND m.expires_at>? AND m.recipient=? AND t.source_kind='managed' AND t.owner_identity=? AND r.source_kind='managed' AND r.owner_identity=? AND r.state='active' ORDER BY m.created_at ASC, m.id ASC LIMIT 1`, formatTimestamp(s.clock.Now()), agentID, agentID, agentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// NextEmbeddedSteerMail selects the oldest queued managed target not currently
// in-flight in an active turn reservation.
func (s *Store) NextEmbeddedSteerMail(ctx context.Context, agentID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("store is closed")
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT m.id FROM messages m JOIN message_target_snapshots t ON t.message_id=m.id AND t.message_incarnation_id=m.message_incarnation_id JOIN recipient_sources r ON r.recipient_name=t.recipient_name WHERE m.state='queued' AND m.claude_stdin_started=0 AND m.expires_at>? AND m.recipient=? AND t.source_kind='managed' AND t.owner_identity=? AND r.source_kind='managed' AND r.owner_identity=? AND r.state='active' AND NOT EXISTS (SELECT 1 FROM embedded_attempts ea WHERE ea.message_id=m.id) ORDER BY m.created_at ASC, m.id ASC LIMIT 1`, formatTimestamp(s.clock.Now()), agentID, agentID, agentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// AppendEmbeddedMailAndACK atomically appends, records evidence, and changes
// ordinary mail state. A failed transaction leaves an unstarted reservation;
// no provider/tool execution is implied. Only the exact ACK retries as a no-op.
func (s *Store) AppendEmbeddedMailAndACK(ctx context.Context, b EmbeddedBinding, attempt string, body []byte) error {
	_, err := s.appendEmbeddedMailAndACK(ctx, b, attempt, body, nil)
	return err
}

// AppendEmbeddedMailSDKAndACK commits the exact user message passed to
// RunMessages, before the SDK can enter a provider or tool callback.
func (s *Store) AppendEmbeddedMailSDKAndACK(ctx context.Context, b EmbeddedBinding, attempt string, body []byte, sdkMessage []byte) error {
	_, err := s.AppendEmbeddedMailSDKAndACKWithHistory(ctx, b, attempt, body, sdkMessage)
	return err
}

func (s *Store) AppendEmbeddedMailSDKAndACKWithHistory(ctx context.Context, b EmbeddedBinding, attempt string, body []byte, sdkMessage []byte) (ManagedHistoryEntry, error) {
	if len(sdkMessage) == 0 {
		return ManagedHistoryEntry{}, errors.New("missing SDK mail message")
	}
	return s.appendEmbeddedMailAndACK(ctx, b, attempt, body, sdkMessage)
}

// EmbeddedMailAppendRolledBack proves this append/ACK transaction did not
// commit. It grants no provider authority; a later offer must recheck all fences.
type EmbeddedMailAppendRolledBack struct{ Cause error }

func (e *EmbeddedMailAppendRolledBack) Error() string { return e.Cause.Error() }
func (e *EmbeddedMailAppendRolledBack) Unwrap() error { return e.Cause }

func (s *Store) appendEmbeddedMailAndACK(ctx context.Context, b EmbeddedBinding, attempt string, body, sdkMessage []byte) (ManagedHistoryEntry, error) {
	if attempt == "" {
		return ManagedHistoryEntry{}, errors.New("missing attempt")
	}
	var historyEntry ManagedHistoryEntry
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var message, key string
		err := s.db.QueryRowContext(ctx, `SELECT message_id,append_key FROM embedded_attempts WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attempt, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&message, &key)
		if err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, message)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != attempt || !bytes.Equal(body, []byte(a.Body)) {
			return errors.New("embedded mail body or attempt mismatch")
		}
		state, err := s.embeddedMailAcceptedTx(ctx, a, false)
		if err != nil {
			return err
		}
		var ackSeq int64
		err = s.db.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_acks WHERE attempt_id=? AND message_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attempt, message, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&ackSeq)
		if err == nil {
			if state != "acknowledged" {
				return errors.New("embedded ACK missing ordinary acknowledgement")
			}
			var e EmbeddedEntry
			if err = s.db.QueryRowContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, ackSeq).Scan(&e.Sequence, &e.ParentDigest, &e.Kind, &e.Body, &e.Digest); err != nil {
				return err
			}
			if e.Kind != "user" {
				return errors.New("corrupt embedded ACK entry")
			}
			if sdkMessage != nil && !bytes.Equal(e.Body, sdkMessage) {
				return errors.New("embedded SDK mail differs from committed entry")
			}
			var sourceKind string
			_ = s.db.QueryRowContext(ctx, `SELECT source_kind FROM messages WHERE id=?`, message).Scan(&sourceKind)
			category := "managed_message"
			messageText := "Message received"
			presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success", From: a.Sender, To: b.AgentID}
			if sourceKind == "reminder" {
				category = "reminder"
				messageText = "Reminder received"
				presentation = &ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"}
			}
			presentationJSON, presErr := managedHistoryPresentationJSON(presentation)
			if presErr != nil {
				return presErr
			}
			eventKey := fmt.Sprintf("embedded-mail:%s:%s", b.AgentID, attempt)
			details := managedHistoryDetails{MessageContent: &a.Body, SenderIdentity: &a.Sender}
			historyEntry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, category, "info", messageText, eventKey, presentationJSON, presentation, details)
			if err != nil {
				return err
			}
			return checkEmbeddedMailUser(e.Body, a)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if state != "queued" {
			return errors.New("embedded mail not queued")
		}
		if _, err = s.embeddedMailAcceptedTx(ctx, a, true); err != nil {
			return err
		}
		var occupied int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_append_keys WHERE append_key=?`, key).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			return errors.New("unacknowledged embedded append key")
		}
		payload := sdkMessage
		if payload == nil {
			payload, err = embeddedMailUser(a, s.clock.Now().UnixMilli())
			if err != nil {
				return err
			}
		}
		if err = checkEmbeddedMailUser(payload, a); err != nil {
			return err
		}
		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		e, err := s.appendEmbeddedTx(ctx, b, "user", payload, seq, digest)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_append_keys(append_key,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?)`, key, b.AgentID, b.SessionID, b.Epoch, b.Generation, e.Sequence)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_acks(attempt_id,message_id,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?,?)`, attempt, message, b.AgentID, b.SessionID, b.Epoch, b.Generation, e.Sequence)
		if err != nil {
			return err
		}
		var sourceKind string
		_ = s.db.QueryRowContext(ctx, `SELECT source_kind FROM messages WHERE id=?`, message).Scan(&sourceKind)
		r, err := s.db.ExecContext(ctx, `UPDATE messages SET state='acknowledged' WHERE id=? AND message_incarnation_id=? AND state='queued' AND expires_at>?`, message, a.MessageIncarnationID, formatTimestamp(s.clock.Now()))
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("embedded ordinary ACK lost race")
		}
		category := "managed_message"
		messageText := "Message received"
		presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success", From: a.Sender, To: b.AgentID}
		if sourceKind == "reminder" {
			category = "reminder"
			messageText = "Reminder received"
			presentation = &ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"}
		}
		presentationJSON, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		eventKey := fmt.Sprintf("embedded-mail:%s:%s", b.AgentID, attempt)
		details := managedHistoryDetails{MessageContent: &a.Body, SenderIdentity: &a.Sender}
		historyEntry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, category, "info", messageText, eventKey, presentationJSON, presentation, details)
		if err != nil {
			return err
		}
		return s.completeLinkedReminder(ctx, message)
	})
	if err != nil && !committed && conclusive {
		return ManagedHistoryEntry{}, &EmbeddedMailAppendRolledBack{Cause: err}
	}
	return historyEntry, err
}

// AppendEmbeddedSteerMailAndACK commits a mid-turn steered user message, records
// its ACK in embedded_acks, and transitions the message to acknowledged state.
func (s *Store) AppendEmbeddedSteerMailAndACK(ctx context.Context, b EmbeddedBinding, attempt string, sdkMessage []byte) (EmbeddedEntry, ManagedHistoryEntry, error) {
	if attempt == "" {
		return EmbeddedEntry{}, ManagedHistoryEntry{}, errors.New("missing attempt")
	}
	if len(sdkMessage) == 0 {
		return EmbeddedEntry{}, ManagedHistoryEntry{}, errors.New("missing SDK mail message")
	}
	var entry EmbeddedEntry
	var historyEntry ManagedHistoryEntry
	committed, conclusive, err := s.runImmediateWithCommitOutcome(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var message, key string
		err := s.db.QueryRowContext(ctx, `SELECT message_id,append_key FROM embedded_attempts WHERE attempt_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attempt, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&message, &key)
		if err != nil {
			return err
		}
		a, err := s.embeddedMailAttemptTx(ctx, message)
		if err != nil {
			return err
		}
		if a == nil || a.AttemptID != attempt {
			return errors.New("embedded steer mail attempt mismatch")
		}
		state, err := s.embeddedMailAcceptedTx(ctx, a, false)
		if err != nil {
			return err
		}
		var ackSeq int64
		err = s.db.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_acks WHERE attempt_id=? AND message_id=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, attempt, message, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&ackSeq)
		if err == nil {
			if state != "acknowledged" {
				return errors.New("embedded ACK missing ordinary acknowledgement")
			}
			if err = s.db.QueryRowContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, ackSeq).Scan(&entry.Sequence, &entry.ParentDigest, &entry.Kind, &entry.Body, &entry.Digest); err != nil {
				return err
			}
			return nil
		}

		var occupied int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_append_keys WHERE append_key=?`, key).Scan(&occupied); err != nil {
			return err
		}
		if occupied != 0 {
			return errors.New("unacknowledged embedded append key")
		}

		msg, err := ai.UnmarshalMessage(sdkMessage)
		if err != nil {
			return err
		}
		if msg.MessageRole() != ai.RoleUser {
			return errors.New("steered message must have role user")
		}

		seq, digest, err := s.embeddedHead(ctx, b)
		if err != nil {
			return err
		}
		entry, err = s.appendEmbeddedTx(ctx, b, "user", sdkMessage, seq, digest)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_append_keys(append_key,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?)`, key, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_acks(attempt_id,message_id,agent_id,session_id,epoch,generation,entry_seq) VALUES(?,?,?,?,?,?,?)`, attempt, message, b.AgentID, b.SessionID, b.Epoch, b.Generation, entry.Sequence)
		if err != nil {
			return err
		}
		var sourceKind string
		_ = s.db.QueryRowContext(ctx, `SELECT source_kind FROM messages WHERE id=?`, message).Scan(&sourceKind)
		r, err := s.db.ExecContext(ctx, `UPDATE messages SET state='acknowledged' WHERE id=? AND message_incarnation_id=? AND state='queued' AND expires_at>?`, message, a.MessageIncarnationID, formatTimestamp(s.clock.Now()))
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("embedded ordinary ACK lost race")
		}
		category := "managed_message"
		messageText := "Message received"
		presentation := &ManagedHistoryPresentation{Kind: "message", Outcome: "success", From: a.Sender, To: b.AgentID}
		if sourceKind == "reminder" {
			category = "reminder"
			messageText = "Reminder received"
			presentation = &ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"}
		}
		presentationJSON, err := managedHistoryPresentationJSON(presentation)
		if err != nil {
			return err
		}
		eventKey := fmt.Sprintf("embedded-mail:%s:%s", b.AgentID, attempt)
		details := managedHistoryDetails{MessageContent: &a.Body, SenderIdentity: &a.Sender}
		historyEntry, err = s.recordManagedCompleteHistoryStrictTx(ctx, b.AgentID, category, "info", messageText, eventKey, presentationJSON, presentation, details)
		if err != nil {
			return err
		}
		return s.completeLinkedReminder(ctx, message)
	})
	if err != nil && !committed && conclusive {
		return EmbeddedEntry{}, ManagedHistoryEntry{}, &EmbeddedMailAppendRolledBack{Cause: err}
	}
	return entry, historyEntry, err
}

func checkEmbeddedMailUser(body []byte, a *EmbeddedMailAttempt) error {
	msg, err := ai.UnmarshalMessage(body)
	if err != nil {
		return err
	}
	user, ok := msg.(ai.UserMessage)
	if !ok || len(user.Content) != 1 {
		return errors.New("invalid embedded mail user role/content")
	}
	text, ok := user.Content[0].(ai.TextContent)
	if !ok {
		return errors.New("invalid embedded mail text")
	}
	expected, err := embeddedMailUser(a, user.Timestamp)
	if err != nil {
		return err
	}
	if !bytes.Equal(body, expected) || len(text.Text) == 0 {
		return errors.New("embedded mail attribution differs from acceptance")
	}
	return nil
}

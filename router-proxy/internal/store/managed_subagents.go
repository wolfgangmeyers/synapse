package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ManagedSubagentRecord contains private, parent-bound child state. It is not
// a top-level managed worker projection.
type ManagedSubagentRecord struct {
	AgentID, ParentAgentID, ParentSessionID, ParentGeneration string
	ParentEpoch                                               int64
	Description, SubagentType, Status                         string
	MaxTurns, TurnCount, ToolUseCount, TotalTokens, Attempt   int
	DurationSeconds                                           float64
	ResultText, Error, ResumePrompt                           string
	CreatedAt, UpdatedAt, TerminalAt                          string
}

type ManagedSubagentResult struct {
	Status                               string
	TurnCount, ToolUseCount, TotalTokens int
	DurationSeconds                      float64
	ResultText, Error                    string
}

func (s *Store) managedSubagentSchemaPresent(ctx context.Context) (bool, error) {
	var present bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='managed_subagents')`).Scan(&present)
	return present, err
}

const managedSubagentSelect = `SELECT agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,turn_count,tool_use_count,total_tokens,result_text,error,created_at,updated_at,terminal_at,attempt,duration_seconds,resume_prompt FROM managed_subagents`

func scanManagedSubagent(row interface{ Scan(...any) error }) (*ManagedSubagentRecord, error) {
	var r ManagedSubagentRecord
	var terminal sql.NullString
	err := row.Scan(&r.AgentID, &r.ParentAgentID, &r.ParentSessionID, &r.ParentEpoch, &r.ParentGeneration, &r.Description, &r.SubagentType, &r.Status, &r.MaxTurns, &r.TurnCount, &r.ToolUseCount, &r.TotalTokens, &r.ResultText, &r.Error, &r.CreatedAt, &r.UpdatedAt, &terminal, &r.Attempt, &r.DurationSeconds, &r.ResumePrompt)
	if err != nil {
		return nil, err
	}
	r.TerminalAt = terminal.String
	return &r, nil
}

func validSubagentDuration(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func subagentTerminal(status string) bool {
	switch status {
	case "completed", "failed", "stopped", "steered", "aborted", "error", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}

func subagentTransition(old, next string) bool {
	switch old {
	case "queued":
		return next == "running" || next == "stopped" || next == "failed" || next == "error" || next == "interrupted" || next == "cancelled" || next == "aborted"
	case "running":
		return subagentTerminal(next)
	default:
		return false
	}
}

// The read fence checks durable parent identity, not a live owner lease. A
// completed or interrupted result stays available after process restart while
// its parent session/epoch/generation is still the current one.
func (s *Store) subagentReadFenceTx(ctx context.Context, parent EmbeddedBinding) error {
	if err := validateEmbedded(parent); err != nil {
		return err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id=? AND runtime='embedded' AND pi_session_id=? AND session_epoch=? AND runtime_generation=?`, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("stale subagent parent binding")
	}
	return nil
}

func (s *Store) subagentWriteFenceTx(ctx context.Context, parent EmbeddedBinding) error {
	if parent.OwnerToken <= 0 {
		var token int64
		_ = s.db.QueryRowContext(ctx, `SELECT owner_token FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1 AND owner_expires_at > ?`, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation, formatTimestamp(s.clock.Now())).Scan(&token)
		if token > 0 {
			parent.OwnerToken = token
		}
	}
	if err := s.embeddedFence(ctx, parent); err != nil {
		return err
	}
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation).Scan(&role)
	if err != nil {
		return err
	}
	if role != "root" && role != "worker" {
		return errors.New("subagent parent requires root or worker role")
	}
	return nil
}

// CreateManagedSubagent reserves a slot-free embedded managed identity and its
// metadata atomically under the exact parent runtime and owner fence. Child
// session binding and execution belong to the later runtime slice.
func (s *Store) CreateManagedSubagent(ctx context.Context, parent EmbeddedBinding, child ManagedSubagentRecord) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if !managedIdentifierRE.MatchString(child.AgentID) || child.Status != "queued" && child.Status != "running" || child.MaxTurns < 0 || child.ParentAgentID != parent.AgentID {
		return errors.New("invalid subagent admission")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.subagentWriteFenceTx(ctx, parent); err != nil {
			return err
		}
		var cwd, provider, model, manifest string
		var revision int
		err := s.db.QueryRowContext(ctx, `SELECT cwd,selected_model_provider,selected_model_id,manifest_revision,manifest_hash FROM managed_agents WHERE agent_id=? AND NOT EXISTS (SELECT 1 FROM managed_subagents WHERE agent_id=managed_agents.agent_id)`, parent.AgentID).Scan(&cwd, &provider, &model, &revision, &manifest)
		if err != nil {
			return fmt.Errorf("subagent parent must be a top-level managed agent: %w", err)
		}
		if provider == "" {
			provider = "antigravity"
		}
		if model == "" {
			model = "gemini-3.8-flash"
		}
		now := formatTimestamp(s.clock.Now())
		hasParent, colErr := managedAgentColumnTx(ctx, s.db, "parent_agent_id")
		if colErr != nil {
			return colErr
		}
		if hasParent {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,runtime,parent_agent_id) VALUES(?,?,0,?,?,?,'running','running','idle',0,?,1,?,?,?,?,?,'embedded',?)`, child.AgentID, child.AgentID, cwd, provider, model, child.AgentID+"-session", parent.Generation, revision, manifest, now, now, parent.AgentID); err != nil {
				return err
			}
		} else {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,runtime) VALUES(?,?,0,?,?,?,'running','running','idle',0,?,1,?,?,?,?,?,'embedded')`, child.AgentID, child.AgentID, cwd, provider, model, child.AgentID+"-session", parent.Generation, revision, manifest, now, now); err != nil {
				return err
			}
		}
		kind := child.SubagentType
		if kind == "" {
			kind = "general-purpose"
		}
		hasResumePrompt, colErr := managedSubagentColumnTx(ctx, s.db, "resume_prompt")
		if colErr != nil {
			return colErr
		}
		if hasResumePrompt {
			_, err = s.db.ExecContext(ctx, `INSERT INTO managed_subagents(agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,created_at,updated_at,resume_prompt) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, child.AgentID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation, child.Description, kind, child.Status, child.MaxTurns, now, now, child.ResumePrompt)
		} else {
			_, err = s.db.ExecContext(ctx, `INSERT INTO managed_subagents(agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, child.AgentID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation, child.Description, kind, child.Status, child.MaxTurns, now, now)
		}
		if err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ManagedSubagent permits only the same current parent session to read its
// child. No live owner token is needed to retrieve a committed terminal result.
func (s *Store) ManagedSubagent(ctx context.Context, parent EmbeddedBinding, childID string) (*ManagedSubagentRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if err := s.subagentReadFenceTx(ctx, parent); err != nil {
		return nil, err
	}
	return scanManagedSubagent(s.db.QueryRowContext(ctx, managedSubagentSelect+` WHERE agent_id=? AND parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=?`, childID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation))
}

func (s *Store) ManagedSubagents(ctx context.Context, parent EmbeddedBinding) ([]*ManagedSubagentRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if err := s.subagentReadFenceTx(ctx, parent); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, managedSubagentSelect+` WHERE parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=? ORDER BY agent_id`, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*ManagedSubagentRecord, 0)
	for rows.Next() {
		child, err := scanManagedSubagent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, child)
	}
	return result, rows.Err()
}

// ManagedSubagentTranscript reads the child's checked SDK history for its
// current embedded session. Parent binding and child lineage are checked
// together without borrowing or mutating the child's runtime owner lease.
func (s *Store) ManagedSubagentTranscript(ctx context.Context, parent EmbeddedBinding, childID string) (EmbeddedSnapshot, error) {
	var empty EmbeddedSnapshot
	if ctx == nil {
		return empty, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return empty, errors.New("store is closed")
	}
	if err := s.subagentReadFenceTx(ctx, parent); err != nil {
		return empty, err
	}
	var binding EmbeddedBinding
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT e.agent_id,e.session_id,e.epoch,e.generation,e.role
 FROM managed_subagents c JOIN managed_agents a ON a.agent_id=c.agent_id
 JOIN embedded_sessions e ON e.agent_id=a.agent_id AND e.session_id=a.pi_session_id AND e.epoch=a.session_epoch AND e.generation=a.runtime_generation AND e.current=1
 WHERE c.agent_id=? AND c.parent_agent_id=? AND c.parent_session_id=? AND c.parent_epoch=? AND c.parent_generation=?
 AND c.status IN ('completed','failed','stopped','steered','aborted','error','interrupted') AND a.runtime='embedded'`,
		childID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation).Scan(&binding.AgentID, &binding.SessionID, &binding.Epoch, &binding.Generation, &role)
	if err != nil {
		return empty, err
	}
	if role != "subagent" {
		return empty, errors.New("invalid child embedded role")
	}
	return s.readEmbeddedSnapshotTx(ctx, binding)
}

// UpdateManagedSubagent is a single fenced compare-and-swap. A terminal row
// cannot be overwritten by a late runner or another terminal outcome.
func (s *Store) UpdateManagedSubagent(ctx context.Context, parent EmbeddedBinding, childID, expected string, next ManagedSubagentResult) error {
	return s.UpdateManagedSubagentAttempt(ctx, parent, childID, 1, expected, next)
}

// UpdateManagedSubagentAttempt rejects late writes from an older execution of
// the same child ID. Counters and duration are cumulative across resumes.
func (s *Store) UpdateManagedSubagentAttempt(ctx context.Context, parent EmbeddedBinding, childID string, attempt int, expected string, next ManagedSubagentResult) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if attempt < 1 || !subagentTransition(expected, next.Status) || next.TurnCount < 0 || next.ToolUseCount < 0 || next.TotalTokens < 0 || !validSubagentDuration(next.DurationSeconds) {
		return errors.New("invalid subagent status transition")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.subagentWriteFenceTx(ctx, parent); err != nil {
			return err
		}
		now := formatTimestamp(s.clock.Now())
		var terminal any
		if subagentTerminal(next.Status) {
			terminal = now
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status=?,turn_count=?,tool_use_count=?,total_tokens=?,duration_seconds=?,result_text=?,error=?,updated_at=?,terminal_at=? WHERE agent_id=? AND parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=? AND status=? AND attempt=? AND turn_count<=? AND tool_use_count<=? AND total_tokens<=? AND duration_seconds<=?`, next.Status, next.TurnCount, next.ToolUseCount, next.TotalTokens, next.DurationSeconds, next.ResultText, next.Error, now, terminal, childID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation, expected, attempt, next.TurnCount, next.ToolUseCount, next.TotalTokens, next.DurationSeconds)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("stale or foreign subagent transition")
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ManagedSubagentProgress is a durable cumulative snapshot. Every metric must
// be at least its previous persisted value, including elapsed active duration.
type ManagedSubagentProgress struct {
	TurnCount, ToolUseCount, TotalTokens int
	DurationSeconds                      float64
}

func (s *Store) UpdateManagedSubagentProgress(ctx context.Context, parent EmbeddedBinding, childID string, attempt int, progress ManagedSubagentProgress) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if attempt < 1 || progress.TurnCount < 0 || progress.ToolUseCount < 0 || progress.TotalTokens < 0 || !validSubagentDuration(progress.DurationSeconds) {
		return errors.New("invalid subagent progress")
	}
	return s.runImmediate(ctx, func() error {
		if err := s.subagentWriteFenceTx(ctx, parent); err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET turn_count=?,tool_use_count=?,total_tokens=?,duration_seconds=?,updated_at=? WHERE agent_id=? AND parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=? AND attempt=? AND status='running' AND turn_count<=? AND tool_use_count<=? AND total_tokens<=? AND duration_seconds<=?`, progress.TurnCount, progress.ToolUseCount, progress.TotalTokens, progress.DurationSeconds, formatTimestamp(s.clock.Now()), childID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation, attempt, progress.TurnCount, progress.ToolUseCount, progress.TotalTokens, progress.DurationSeconds)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale subagent progress")
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
}

// ResumeManagedSubagent requeues one settled child without creating another
// managed identity. The old SDK conversation stays bound to that same agent;
// the runtime appends ResumePrompt to the checked SDK history on execution.
func (s *Store) ResumeManagedSubagent(ctx context.Context, parent EmbeddedBinding, childID, prompt string, maxTurns ...int) (*ManagedSubagentRecord, error) {
	if len(maxTurns) > 1 {
		return nil, errors.New("at most one max turns override is allowed")
	}
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if prompt == "" {
		return nil, errors.New("resume prompt is required")
	}
	var resumed *ManagedSubagentRecord
	err := s.runImmediate(ctx, func() error {
		if err := s.subagentWriteFenceTx(ctx, parent); err != nil {
			return err
		}
		old, err := scanManagedSubagent(s.db.QueryRowContext(ctx, managedSubagentSelect+` WHERE agent_id=? AND parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=?`, childID, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation))
		if err != nil {
			return err
		}
		if !subagentTerminal(old.Status) {
			return errors.New("only terminal subagents may resume")
		}
		effectiveMaxTurns := old.MaxTurns
		if len(maxTurns) == 1 && maxTurns[0] > 0 {
			effectiveMaxTurns = maxTurns[0]
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status='queued',attempt=attempt+1,max_turns=?,resume_prompt=?,result_text='',error='',terminal_at=NULL,updated_at=? WHERE agent_id=? AND status=? AND attempt=? AND parent_agent_id=? AND parent_session_id=? AND parent_epoch=? AND parent_generation=?`, effectiveMaxTurns, prompt, now, childID, old.Status, old.Attempt, parent.AgentID, parent.SessionID, parent.Epoch, parent.Generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("stale subagent resume")
		}
		resumed, err = scanManagedSubagent(s.db.QueryRowContext(ctx, managedSubagentSelect+` WHERE agent_id=?`, childID))
		if err != nil {
			return err
		}
		_, err = s.bumpManagedProjectionVersion(ctx)
		return err
	})
	return resumed, err
}

// Reconcile only orphaned active child work on a freshly opened store. Subagents
// whose parents are active/running are preserved for restart resumption.
func (s *Store) reconcileManagedSubagents(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		var active int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_subagents WHERE status IN ('running','queued') AND parent_agent_id IN (SELECT agent_id FROM managed_agents WHERE desired_state='terminated') LIMIT 1`).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status='failed',error='parent agent terminated',updated_at=?,terminal_at=? WHERE status IN ('running','queued') AND parent_agent_id IN (SELECT agent_id FROM managed_agents WHERE desired_state='terminated')`, now, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			_, err = s.bumpManagedProjectionVersion(ctx)
		}
		return err
	})
}

// MarkInFlightSubagentsFailed marks in-flight subagents as failed / interrupted in a single query on router startup.
func (s *Store) MarkInFlightSubagentsFailed(ctx context.Context) error {
	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		result, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status='failed', error='interrupted', updated_at=?, terminal_at=? WHERE status IN ('running','queued')`, now, now)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			_, _ = s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated', lifecycle_state='failed', activity_state='stopped', accepting_prompts=0, last_error_code='SUBAGENT_INTERRUPTED', last_error_message='subagent interrupted by process restart', last_error_at=?, updated_at=? WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE status='failed' AND error='interrupted') AND desired_state='running'`, now, now)
			_, err = s.bumpManagedProjectionVersion(ctx)
		}
		return err
	})
}

// ActiveManagedSubagents returns all managed subagents that are currently running or queued.
func (s *Store) ActiveManagedSubagents(ctx context.Context) ([]*ManagedSubagentRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedSubagentSelect+` WHERE status IN ('running','queued') ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*ManagedSubagentRecord
	for rows.Next() {
		rec, err := scanManagedSubagent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// CompletedSubagentsPendingNotification returns all completed or terminal subagents that
// have not yet had a completion notification message enqueued to their parent mailbox.
func (s *Store) CompletedSubagentsPendingNotification(ctx context.Context) ([]*ManagedSubagentRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedSubagentSelect+` WHERE status IN ('completed','failed','stopped','steered','aborted','error')
		AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.id LIKE 'subagent-completed-' || agent_id || '%' OR m.id LIKE 'subagent-terminated-' || agent_id || '%')
		AND NOT EXISTS (SELECT 1 FROM conversation_messages c WHERE c.message_id LIKE 'subagent-completed-' || agent_id || '%' OR c.message_id LIKE 'subagent-terminated-' || agent_id || '%')
		ORDER BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []*ManagedSubagentRecord
	for rows.Next() {
		rec, err := scanManagedSubagent(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// InterruptedSubagentsPendingNotification returns all interrupted subagents that
// have not yet had an interrupted notification message enqueued to their parent.
func (s *Store) InterruptedSubagentsPendingNotification(ctx context.Context) ([]*ManagedSubagentRecord, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	rows, err := s.db.QueryContext(ctx, managedSubagentSelect+` WHERE ((status='failed' AND (error='interrupted' OR error LIKE '%interrupted%')) OR status='interrupted')
		AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.id LIKE 'subagent-interrupted-' || agent_id || '%')
		AND NOT EXISTS (SELECT 1 FROM conversation_messages c WHERE c.message_id LIKE 'subagent-interrupted-' || agent_id || '%')
		ORDER BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*ManagedSubagentRecord, 0)
	for rows.Next() {
		r, err := scanManagedSubagent(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// The existing lifecycle sweep owns this 24-hour default bound. Deleting the
// managed identity cascades child state and any later embedded transcript.
func (s *Store) pruneManagedSubagentsTx(ctx context.Context, now time.Time, age time.Duration) (int, error) {
	cutoff := formatTimestamp(now.Add(-age))
	result, err := s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE terminal_at < ?)`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		_, err = s.bumpManagedProjectionVersion(ctx)
	}
	return int(n), err
}

// PruneExpiredSubagents deletes terminal subagents whose terminal_at is older than ttl.
func (s *Store) PruneExpiredSubagents(ctx context.Context, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	return s.runImmediate(ctx, func() error {
		cutoff := formatTimestamp(s.clock.Now().Add(-ttl))
		res, err := s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE terminal_at IS NOT NULL AND terminal_at < ?)`, cutoff)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM managed_subagents WHERE terminal_at IS NOT NULL AND terminal_at < ?`, cutoff)
		if err != nil {
			return err
		}
		hasParent, _ := managedAgentColumnTx(ctx, s.db, "parent_agent_id")
		if hasParent {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE (agent_id LIKE 'subagent-%' OR (parent_agent_id IS NOT NULL AND parent_agent_id != '')) AND desired_state='terminated' AND updated_at < ?`, cutoff)
		} else {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id LIKE 'subagent-%' AND desired_state='terminated' AND updated_at < ?`, cutoff)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			_, _ = s.bumpManagedProjectionVersion(ctx)
		}
		return nil
	})
}

// TimeoutRunningSubagents marks subagents running or queued longer than timeout as failed.
func (s *Store) TimeoutRunningSubagents(ctx context.Context, timeout time.Duration) (int, error) {
	if timeout <= 0 {
		timeout = 1 * time.Hour
	}
	var count int
	err := s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		cutoff := formatTimestamp(s.clock.Now().Add(-timeout))
		res, err := s.db.ExecContext(ctx, `UPDATE managed_subagents SET status='failed', error='subagent execution timed out (exceeded 1 hour TTL)', updated_at=?, terminal_at=? WHERE status IN ('queued', 'running') AND created_at < ?`, now, now, cutoff)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		count = int(n)
		if count > 0 {
			_, _ = s.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated', lifecycle_state='failed', activity_state='stopped', accepting_prompts=0, last_error_code='SUBAGENT_TIMEOUT', last_error_message='subagent execution timed out', last_error_at=?, updated_at=? WHERE agent_id IN (SELECT agent_id FROM managed_subagents WHERE error='subagent execution timed out (exceeded 1 hour TTL)') AND desired_state='running'`, now, now)
			_, _ = s.bumpManagedProjectionVersion(ctx)
		}
		return nil
	})
	return count, err
}

func managedSubagentColumnTx(ctx context.Context, db *sql.DB, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(managed_subagents)`)
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
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

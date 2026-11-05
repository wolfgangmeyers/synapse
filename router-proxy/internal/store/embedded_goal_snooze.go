package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const EmbeddedGoalSnoozeMaxDurationMS int64 = 24 * 60 * 60 * 1000

// EmbeddedGoalSnoozeDenial is a pure pre-intent argument denial. Callers must
// validate before recording any SDK effect intent.
type EmbeddedGoalSnoozeDenial struct{ Reason string }

func (e *EmbeddedGoalSnoozeDenial) Error() string { return "invalid goal snooze: " + e.Reason }

type EmbeddedGoalSnoozeArguments struct {
	DurationMS int64 `json:"duration_ms"`
}

// ParseEmbeddedGoalSnoozeArguments accepts only the closed JSON shape and an
// integer number token. It does not consult the clock or mutate the Store.
func ParseEmbeddedGoalSnoozeArguments(raw []byte) (EmbeddedGoalSnoozeArguments, error) {
	var args EmbeddedGoalSnoozeArguments
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, &EmbeddedGoalSnoozeDenial{Reason: "duration_ms must be a positive integer at most 24h"}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return EmbeddedGoalSnoozeArguments{}, &EmbeddedGoalSnoozeDenial{Reason: "unexpected trailing input"}
	}
	if args.DurationMS <= 0 || args.DurationMS > EmbeddedGoalSnoozeMaxDurationMS {
		return EmbeddedGoalSnoozeArguments{}, &EmbeddedGoalSnoozeDenial{Reason: "duration_ms must be a positive integer at most 24h"}
	}
	// Reject null and missing rather than accepting the Go zero value by accident.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 1 || bytes.Equal(bytes.TrimSpace(fields["duration_ms"]), []byte("null")) {
		return EmbeddedGoalSnoozeArguments{}, &EmbeddedGoalSnoozeDenial{Reason: "duration_ms required"}
	}
	// json.Unmarshal accepts duplicate keys. A closed argument object must not.
	keys := json.NewDecoder(bytes.NewReader(raw))
	_, _ = keys.Token() // already validated as an object above
	seen := 0
	for keys.More() {
		key, err := keys.Token()
		if err != nil || key != "duration_ms" || seen != 0 {
			return EmbeddedGoalSnoozeArguments{}, &EmbeddedGoalSnoozeDenial{Reason: "duplicate or unknown argument"}
		}
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return EmbeddedGoalSnoozeArguments{}, &EmbeddedGoalSnoozeDenial{Reason: "invalid argument"}
		}
		seen++
	}
	return args, nil
}

type EmbeddedGoalSnoozeTurn struct {
	UserSeq      int64
	UserDigest   string
	GoalRevision int64
}

type EmbeddedGoalSnoozePreference struct {
	EmbeddedGoalSnoozeTurn
	AgentID, SessionID, Generation, Role string
	Epoch, PolicyVersion                 int64
	CallID, DueAt                        string
	Workflow                             string `json:",omitempty"`
	SnoozeUntil                          string
	DurationMS                           int64
	Active                               bool
}

// CurrentEmbeddedGoalSnoozePreference returns the currently active snooze preference for an agent if any.
func (s *Store) CurrentEmbeddedGoalSnoozePreference(ctx context.Context, agentID string) (*EmbeddedGoalSnoozePreference, error) {
	if ctx == nil || agentID == "" {
		return nil, errors.New("invalid arguments")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	var p EmbeddedGoalSnoozePreference
	var result string
	err := s.db.QueryRowContext(ctx, `SELECT p.agent_id,p.session_id,p.epoch,p.generation,p.role,p.user_seq,p.user_digest,p.goal_revision,p.policy_version,p.call_id,p.due_at,c.result_json
 FROM embedded_goal_snooze_preferences p JOIN embedded_goal_snooze_calls c ON c.call_id=p.call_id
 JOIN embedded_sessions e ON e.agent_id=p.agent_id AND e.session_id=p.session_id AND e.epoch=p.epoch AND e.generation=p.generation
 JOIN managed_agent_policies m ON m.agent_id=p.agent_id
 WHERE p.agent_id=? AND p.active=1 AND e.current=1 AND e.role=p.role AND m.goal_enabled=1 AND m.desired_revision=p.goal_revision
 ORDER BY p.due_at DESC LIMIT 1`, agentID).Scan(&p.AgentID, &p.SessionID, &p.Epoch, &p.Generation, &p.Role, &p.UserSeq, &p.UserDigest, &p.GoalRevision, &p.PolicyVersion, &p.CallID, &p.DueAt, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.embeddedEvidenceReadGate(ctx, EmbeddedBinding{AgentID: p.AgentID, SessionID: p.SessionID, Epoch: p.Epoch, Generation: p.Generation}, EmbeddedGoalEvaluationKey{UserSeq: p.UserSeq, UserDigest: p.UserDigest, GoalRevision: p.GoalRevision}, p.CallID); err != nil {
		return nil, err
	}
	var snapshot EmbeddedGoalSnoozePreference
	if err := json.Unmarshal([]byte(result), &snapshot); err != nil {
		return nil, err
	}
	p.DurationMS = snapshot.DurationMS
	p.SnoozeUntil = p.DueAt
	p.Active = true
	return &p, nil
}

// EmbeddedGoalSnoozePreferenceFor returns only the current, active preference.
// It does not interpret due time as permission to wake a turn.
func (s *Store) EmbeddedGoalSnoozePreferenceFor(ctx context.Context, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn) (*EmbeddedGoalSnoozePreference, error) {
	if ctx == nil || turn.UserSeq <= 0 || turn.UserDigest == "" || turn.GoalRevision <= 0 {
		return nil, &EmbeddedGoalSnoozeDenial{Reason: "invalid turn"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return nil, err
	}
	var p EmbeddedGoalSnoozePreference
	var result string
	err := s.db.QueryRowContext(ctx, `SELECT p.agent_id,p.session_id,p.epoch,p.generation,p.role,p.user_seq,p.user_digest,p.goal_revision,p.policy_version,p.call_id,p.due_at,c.result_json
 FROM embedded_goal_snooze_preferences p JOIN embedded_goal_snooze_calls c ON c.call_id=p.call_id
 JOIN embedded_sessions e ON e.agent_id=p.agent_id AND e.session_id=p.session_id AND e.epoch=p.epoch AND e.generation=p.generation
 JOIN managed_agent_policies m ON m.agent_id=p.agent_id
 WHERE p.agent_id=? AND p.session_id=? AND p.epoch=? AND p.generation=? AND p.user_seq=? AND p.user_digest=? AND p.goal_revision=?
 AND p.active=1 AND e.current=1 AND e.role=p.role AND m.goal_enabled=1 AND m.desired_revision=p.goal_revision`, b.AgentID, b.SessionID, b.Epoch, b.Generation, turn.UserSeq, turn.UserDigest, turn.GoalRevision).Scan(&p.AgentID, &p.SessionID, &p.Epoch, &p.Generation, &p.Role, &p.UserSeq, &p.UserDigest, &p.GoalRevision, &p.PolicyVersion, &p.CallID, &p.DueAt, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.embeddedEvidenceReadGate(ctx, EmbeddedBinding{AgentID: p.AgentID, SessionID: p.SessionID, Epoch: p.Epoch, Generation: p.Generation}, EmbeddedGoalEvaluationKey{UserSeq: p.UserSeq, UserDigest: p.UserDigest, GoalRevision: p.GoalRevision}, p.CallID); err != nil {
		return nil, err
	}
	var snapshot EmbeddedGoalSnoozePreference
	if err := json.Unmarshal([]byte(result), &snapshot); err != nil {
		return nil, err
	}
	if snapshot.CallID != p.CallID || snapshot.PolicyVersion != p.PolicyVersion || snapshot.EmbeddedGoalSnoozeTurn != turn || snapshot.DueAt != p.DueAt || snapshot.Role != p.Role {
		return nil, errors.New("conflicting goal snooze preference evidence")
	}
	p.DurationMS = snapshot.DurationMS
	p.Active = true
	return &p, nil
}

// SaveEmbeddedGoalSnoozePreference commits a preference, not a wake. The SDK
// turn may not have an applied receipt yet. A later call in the same turn and
// revision replaces the row and increments its ordered policy version.
func (s *Store) SaveEmbeddedGoalSnoozePreference(ctx context.Context, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, rawArgs []byte) (EmbeddedGoalSnoozePreference, bool, error) {
	return s.saveEmbeddedGoalSnoozePreference(ctx, b, turn, callID, rawArgs, nil)
}

// SaveCurrentEmbeddedGoalSnoozePreference fences the original process in the
// same immediate transaction as the preference/result writes. It never replays.
func (s *Store) SaveCurrentEmbeddedGoalSnoozePreference(ctx context.Context, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, rawArgs []byte, invocation *EmbeddedGoalInvocation) (EmbeddedGoalSnoozePreference, bool, error) {
	if invocation == nil {
		return EmbeddedGoalSnoozePreference{}, false, errors.New("missing current snooze admission")
	}
	invocation.mu.Lock()
	defer invocation.mu.Unlock()
	preference, replay, err := s.saveEmbeddedGoalSnoozePreference(ctx, b, turn, callID, rawArgs, invocation)
	if err != nil {
		invocation.consumed = true
	}
	return preference, replay, err
}

func (s *Store) saveEmbeddedGoalSnoozePreference(ctx context.Context, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, rawArgs []byte, invocation *EmbeddedGoalInvocation) (EmbeddedGoalSnoozePreference, bool, error) {
	workflow := invocation != nil && invocation.name == "workflow_snooze"
	var args EmbeddedGoalSnoozeArguments
	var err error
	if !workflow {
		args, err = ParseEmbeddedGoalSnoozeArguments(rawArgs)
		if err != nil {
			return EmbeddedGoalSnoozePreference{}, false, err
		}
	}
	if ctx == nil || turn.UserSeq <= 0 || turn.UserDigest == "" || turn.GoalRevision <= 0 || callID == "" || len(callID) > 256 || !utf8.ValidString(callID) || strings.ContainsAny(callID, "\x00\r\n") {
		return EmbeddedGoalSnoozePreference{}, false, &EmbeddedGoalSnoozeDenial{Reason: "invalid call or turn"}
	}
	canonical, _ := json.Marshal(args)
	var preference EmbeddedGoalSnoozePreference
	replay := false
	err = s.runImmediate(ctx, func() error {
		if err := s.embeddedEvidenceReplayTx(ctx, b, EmbeddedGoalEvaluationKey{UserSeq: turn.UserSeq, UserDigest: turn.UserDigest, GoalRevision: turn.GoalRevision}, callID); err != nil {
			return err
		}
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		workflowName := ""
		workflowDefaultMS := int64(600000)
		if workflow {
			var defaultMS int64
			if err := s.db.QueryRowContext(ctx, `SELECT w.name,w.snooze_duration_ms FROM managed_agents a JOIN workflows w ON w.id=a.active_workflow_id WHERE a.agent_id=? AND a.active_workflow_state_id IS NOT NULL`, b.AgentID).Scan(&workflowName, &defaultMS); err != nil {
				return err
			}
			var duration int64
			workflowDefaultMS = defaultMS
			canonical, duration, err = NormalizeEmbeddedWorkflowSnoozeArguments(rawArgs, defaultMS)
			if err != nil {
				return err
			}
			args.DurationMS = duration
		}
		if invocation != nil {
			if err := s.embeddedGoalDomainInvocationTx(ctx, b, callID, invocation.name, invocation); err != nil {
				return err
			}
			if turn.UserSeq != invocation.userSeq || turn.UserDigest != invocation.userDigest {
				return errors.New("current snooze invocation turn mismatch")
			}
			var original struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(invocation.intent, &original); err != nil {
				return err
			}
			if workflow {
				originalCanonical, _, err := NormalizeEmbeddedWorkflowSnoozeArguments(original.Arguments, workflowDefaultMS)
				if err != nil {
					return err
				}
				if !bytes.Equal(originalCanonical, canonical) {
					return errors.New("current workflow snooze invocation arguments mismatch")
				}
			} else if invocation.name == "goal_snooze" {
				originalArgs, err := ParseEmbeddedGoalSnoozeArguments(original.Arguments)
				if err != nil {
					return err
				}
				originalCanonical, err := json.Marshal(originalArgs)
				if err != nil {
					return err
				}
				if !bytes.Equal(originalCanonical, canonical) {
					return errors.New("current snooze invocation arguments mismatch")
				}
			}
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return errors.New("invalid saved embedded role")
		}
		var desired int64
		var enabled int
		if err := s.db.QueryRowContext(ctx, `SELECT desired_revision,goal_enabled FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&desired, &enabled); err != nil {
			return err
		}
		if desired != turn.GoalRevision || enabled != 1 {
			return errors.New("goal snooze revision is off or stale")
		}
		var kind, digest, parentDigest string
		var body []byte
		entry := EmbeddedEntry{Sequence: turn.UserSeq}
		if err := s.db.QueryRowContext(ctx, `SELECT kind,body,parent_digest,digest FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, turn.UserSeq).Scan(&kind, &body, &parentDigest, &digest); err != nil {
			return err
		}
		entry.Kind, entry.Body, entry.ParentDigest, entry.Digest = kind, body, parentDigest, digest
		if kind != "user" || digest != turn.UserDigest || embeddedBody(body) != nil || embeddedEntryDigest(b, entry) != digest {
			return errors.New("goal snooze turn mismatch")
		}
		var later int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_entries WHERE agent_id=? AND session_id=? AND epoch=? AND seq>? AND kind='user'`, b.AgentID, b.SessionID, b.Epoch, turn.UserSeq).Scan(&later); err != nil {
			return err
		}
		if later != 0 {
			return errors.New("goal snooze turn superseded")
		}
		var saved EmbeddedGoalSnoozePreference
		var savedArgs, savedResult string
		err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version FROM embedded_goal_snooze_calls WHERE call_id=?`, callID).Scan(&saved.AgentID, &saved.SessionID, &saved.Epoch, &saved.Generation, &saved.Role, &saved.UserSeq, &saved.UserDigest, &saved.GoalRevision, &savedArgs, &savedResult, &saved.PolicyVersion)
		if err == nil {
			if invocation != nil {
				return errors.New("current snooze domain call cannot replay")
			}
			if saved.AgentID != b.AgentID || saved.SessionID != b.SessionID || saved.Epoch != b.Epoch || saved.Generation != b.Generation || saved.Role != role || saved.EmbeddedGoalSnoozeTurn != turn || savedArgs != string(canonical) {
				return errors.New("conflicting embedded goal snooze call")
			}
			if err := json.Unmarshal([]byte(savedResult), &saved); err != nil {
				return err
			}
			preference = saved
			replay = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var version int64
		err = s.db.QueryRowContext(ctx, `SELECT policy_version FROM embedded_goal_snooze_preferences WHERE agent_id=? AND session_id=? AND epoch=? AND user_seq=? AND user_digest=? AND goal_revision=?`, b.AgentID, b.SessionID, b.Epoch, turn.UserSeq, turn.UserDigest, turn.GoalRevision).Scan(&version)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		version++
		due := s.clock.Now().UTC().Add(time.Duration(args.DurationMS) * time.Millisecond)
		preference = EmbeddedGoalSnoozePreference{EmbeddedGoalSnoozeTurn: turn, AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, Role: role, PolicyVersion: version, CallID: callID, DueAt: formatTimestamp(due), DurationMS: args.DurationMS, Active: true, Workflow: workflowName}
		result, err := json.Marshal(preference)
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_snooze_calls(call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, turn.UserSeq, turn.UserDigest, turn.GoalRevision, string(canonical), string(result), version); err != nil {
			return err
		}
		written, err := s.db.ExecContext(ctx, `INSERT INTO embedded_goal_snooze_preferences(agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,policy_version,call_id,due_at,active) VALUES(?,?,?,?,?,?,?,?,?,?,?,1) ON CONFLICT(agent_id,session_id,epoch,user_seq,user_digest,goal_revision) DO UPDATE SET active=1,policy_version=excluded.policy_version,call_id=excluded.call_id,due_at=excluded.due_at WHERE embedded_goal_snooze_preferences.policy_version+1=excluded.policy_version`, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, turn.UserSeq, turn.UserDigest, turn.GoalRevision, version, callID, preference.DueAt)
		if err != nil {
			return err
		}
		affected, err := written.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return errors.New("goal snooze preference was not saved")
		}
		// Only a new accepted call supersedes older active turn preferences.
		// Replay returned above, and an ordinary unsnoozed turn never enters here.
		if _, err := s.db.ExecContext(ctx, `UPDATE embedded_goal_snooze_preferences SET active=0 WHERE agent_id=? AND active=1 AND call_id<>?`, b.AgentID, callID); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE managed_agent_policies SET snoozed_until=?,updated_at=? WHERE agent_id=? AND desired_revision=? AND goal_enabled=1`, preference.DueAt, formatTimestamp(s.clock.Now()), b.AgentID, turn.GoalRevision)
		return err
	})
	if err != nil {
		return EmbeddedGoalSnoozePreference{}, false, err
	}
	return preference, replay, nil
}

// SaveCurrentEmbeddedWorkflowSnoozePreference uses the original workflow intent,
// not a synthetic goal_snooze payload. Policy and call evidence commit together.
func (s *Store) SaveCurrentEmbeddedWorkflowSnoozePreference(ctx context.Context, b EmbeddedBinding, turn EmbeddedGoalSnoozeTurn, callID string, rawArgs []byte, invocation *EmbeddedGoalInvocation) (EmbeddedGoalSnoozePreference, bool, error) {
	if invocation == nil || invocation.name != "workflow_snooze" {
		return EmbeddedGoalSnoozePreference{}, false, errors.New("missing workflow snooze admission")
	}
	return s.SaveCurrentEmbeddedGoalSnoozePreference(ctx, b, turn, callID, rawArgs, invocation)
}

package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

// EmbeddedGoalCommand has no target: its subject is always the saved binding.
// Only set takes a goal and settings. Status and off have no extra arguments.
type EmbeddedGoalCommand struct {
	Action   string              `json:"action"`
	Goal     string              `json:"goal,omitempty"`
	Settings ManagedGoalSettings `json:"settings,omitempty"`
}

func validEmbeddedGoalCommand(c EmbeddedGoalCommand) bool {
	switch c.Action {
	case "set":
		return validManagedGoalConfig(ManagedGoalConfig{Enabled: true, Goal: c.Goal, Settings: c.Settings})
	case "status", "off":
		return c.Goal == "" && c.Settings == (ManagedGoalSettings{})
	default:
		return false
	}
}

// CheckEmbeddedGoalResult requires the successful SDK result to match the
// immutable policy snapshot bound to this call before ordered persistence.
func (s *Store) CheckEmbeddedGoalResult(ctx context.Context, b EmbeddedBinding, callID string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("store is closed")
	}
	if err := s.embeddedFence(ctx, b); err != nil {
		return err
	}
	var agent, session, generation, role, state string
	var epoch int64
	var result sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,role,state,result_json FROM embedded_goal_calls WHERE call_id=?`, callID).Scan(&agent, &session, &epoch, &generation, &role, &state, &result); err != nil {
		return err
	}
	var savedRole string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&savedRole); err != nil {
		return err
	}
	if agent != b.AgentID || session != b.SessionID || epoch != b.Epoch || generation != b.Generation || role != savedRole || !validEmbeddedRole(role) || state != "complete" || !result.Valid || !json.Valid(payload) || !bytes.Equal(payload, []byte(result.String)) {
		return errors.New("goal result does not match bound policy evidence")
	}
	return nil
}

// ApplyEmbeddedGoalCall fences the live owner, saved role, and managed runtime
// before binding a globally unique SDK call ID to canonical arguments. A
// complete call replays its immutable snapshot. An unknown pre-result intent
// never executes again. This commits desired policy, not applied Pi state or a
// checked SDK tool-result append; the caller must append that evidence next.
func (s *Store) ApplyEmbeddedGoalCall(ctx context.Context, b EmbeddedBinding, callID string, c EmbeddedGoalCommand) (*ManagedGoalPolicy, bool, error) {
	return s.applyEmbeddedGoalCall(ctx, b, callID, c, false, nil)
}

// ApplyFreshEmbeddedGoalCall retains the legacy fresh-intent contract. Its
// caller must prove new intent admission; current SDK Execute additionally uses
// ApplyCurrentEmbeddedGoalCall to fence the original router process atomically.
func (s *Store) ApplyFreshEmbeddedGoalCall(ctx context.Context, b EmbeddedBinding, callID string, c EmbeddedGoalCommand) (*ManagedGoalPolicy, bool, error) {
	return s.applyEmbeddedGoalCall(ctx, b, callID, c, true, nil)
}

// EmbeddedGoalInvocationDomainDenied identifies a current invocation rejected
// inside the DOMAIN transaction, rather than at the earlier Execute check.
type EmbeddedGoalInvocationDomainDenied struct{ Cause error }

func (e *EmbeddedGoalInvocationDomainDenied) Error() string {
	return "current goal domain denied: " + e.Cause.Error()
}
func (e *EmbeddedGoalInvocationDomainDenied) Unwrap() error { return e.Cause }

// ApplyCurrentEmbeddedGoalCall uses only the original process-local admission.
// Its process check and mutation share the same SQLite immediate transaction.
// Legacy/UI callers keep the existing Apply[Fresh]EmbeddedGoalCall contract.
func (s *Store) ApplyCurrentEmbeddedGoalCall(ctx context.Context, b EmbeddedBinding, callID string, c EmbeddedGoalCommand, invocation *EmbeddedGoalInvocation) (*ManagedGoalPolicy, bool, error) {
	if invocation == nil || (c.Action != "set" && c.Action != "off") {
		return nil, false, errors.New("missing current goal mutation admission")
	}
	invocation.mu.Lock()
	defer invocation.mu.Unlock()
	out, replay, err := s.applyEmbeddedGoalCall(ctx, b, callID, c, true, invocation)
	if err != nil {
		invocation.consumed = true
	}
	return out, replay, err
}

// Called only under the invocation lock and the DOMAIN immediate transaction.
func (s *Store) embeddedGoalDomainInvocationTx(ctx context.Context, b EmbeddedBinding, callID, name string, invocation *EmbeddedGoalInvocation) error {
	if invocation.store != s || invocation.binding != b || invocation.id != callID || invocation.name != name || !invocation.executing || invocation.ready || invocation.appended || invocation.consumed {
		return &EmbeddedGoalInvocationDomainDenied{Cause: errors.New("goal domain has no current Execute admission")}
	}
	if err := s.checkEmbeddedGoalInvocationIntentTx(ctx, invocation); err != nil {
		return &EmbeddedGoalInvocationDomainDenied{Cause: err}
	}
	return nil
}

func (s *Store) applyEmbeddedGoalCall(ctx context.Context, b EmbeddedBinding, callID string, c EmbeddedGoalCommand, fresh bool, invocation *EmbeddedGoalInvocation) (*ManagedGoalPolicy, bool, error) {
	if ctx == nil || !validEmbeddedGoalCommand(c) || callID == "" || len(callID) > 256 || !utf8.ValidString(callID) || strings.ContainsAny(callID, "\x00\r\n") {
		return nil, false, errors.New("invalid embedded goal call")
	}
	canonical, err := json.Marshal(c)
	if err != nil {
		return nil, false, err
	}
	var out *ManagedGoalPolicy
	replay := false
	err = s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		if invocation != nil {
			if err := s.embeddedGoalDomainInvocationTx(ctx, b, callID, "goal", invocation); err != nil {
				return err
			}
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return errors.New("invalid saved embedded role")
		}
		var agent, session, generation, savedRole, action, args, state string
		var epoch int64
		var result sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,role,action,args_json,state,result_json FROM embedded_goal_calls WHERE call_id=?`, callID).Scan(&agent, &session, &epoch, &generation, &savedRole, &action, &args, &state, &result)
		if err == nil {
			if invocation != nil {
				return errors.New("current goal domain call cannot replay")
			}
			if agent != b.AgentID || session != b.SessionID || epoch != b.Epoch || generation != b.Generation || savedRole != role || action != c.Action || args != string(canonical) {
				return errors.New("conflicting embedded goal call")
			}
			if state != "complete" || !result.Valid {
				return errors.New("embedded goal outcome unknown")
			}
			var snapshot ManagedGoalPolicy
			if err := json.Unmarshal([]byte(result.String), &snapshot); err != nil {
				return err
			}
			out = &snapshot
			replay = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// SDK intent is committed before Execute. If it survived but the
		// policy/result transaction did not, absence of a goal call is not
		// permission to apply it on recovery. Check its original arguments
		// before reporting the unresolved outcome.
		var effectAgent, effectSession, effectGeneration string
		var effectEpoch int64
		var intent []byte
		err = s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,intent FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectAgent, &effectSession, &effectEpoch, &effectGeneration, &intent)
		if err == nil {
			var original struct {
				ID        string         `json:"id"`
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(intent, &original); err != nil {
				return errors.New("conflicting embedded goal call")
			}
			if original.ID != callID || original.Name != "goal" {
				return errors.New("conflicting embedded goal call")
			}
			if effectAgent != b.AgentID || effectSession != b.SessionID || effectEpoch != b.Epoch || effectGeneration != b.Generation {
				return errors.New("conflicting embedded goal call")
			}
			origAction, _ := original.Arguments["action"].(string)
			if origAction != c.Action {
				return errors.New("conflicting embedded goal call")
			}
			if c.Action == "set" {
				if c.Settings != DefaultManagedGoalSettings() || len(original.Arguments) != 2 {
					return errors.New("conflicting embedded goal call")
				}
				origGoal, _ := original.Arguments["goal"].(string)
				if origGoal != c.Goal {
					return errors.New("conflicting embedded goal call")
				}
			} else {
				if c.Goal != "" || c.Settings != (ManagedGoalSettings{}) {
					return errors.New("conflicting embedded goal call")
				}
				for k, v := range original.Arguments {
					if k != "action" && k != "goal" {
						return errors.New("conflicting embedded goal call")
					}
					if k == "goal" && v != nil {
						if _, ok := v.(string); !ok {
							return errors.New("conflicting embedded goal call")
						}
					}
				}
			}
			if !fresh {
				return errors.New("embedded goal outcome unknown")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		} else if fresh {
			return errors.New("fresh goal intent not recorded")
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO embedded_goal_calls(call_id,agent_id,session_id,epoch,generation,role,action,args_json,state) VALUES(?,?,?,?,?,?,?,?,'unknown')`, callID, b.AgentID, b.SessionID, b.Epoch, b.Generation, role, c.Action, string(canonical)); err != nil {
			return err
		}
		if c.Action == "status" {
			out, err = scanManagedGoalPolicy(s.db.QueryRowContext(ctx, `SELECT `+managedGoalPolicyColumns+` FROM managed_agent_policies WHERE agent_id=?`, b.AgentID))
			if errors.Is(err, sql.ErrNoRows) {
				out = &ManagedGoalPolicy{AgentID: b.AgentID, Config: ManagedGoalConfig{Settings: DefaultManagedGoalSettings()}, ProjectionState: "applied"}
			} else if err != nil {
				return err
			}
		} else {
			config := ManagedGoalConfig{Settings: DefaultManagedGoalSettings()}
			if c.Action == "set" {
				config = ManagedGoalConfig{Enabled: true, Goal: c.Goal, Settings: c.Settings}
			}
			settings, err := managedGoalSettingsJSON(config.Settings)
			if err != nil {
				return err
			}
			out, err = s.updateManagedGoalPolicyTx(ctx, b.AgentID, config, settings)
			if err != nil {
				return err
			}
			// The public embedded projection changes with this desired revision.
			// Bump in this same transaction, never for status or replay.
			if _, err := s.bumpManagedProjectionVersion(ctx); err != nil {
				return err
			}
		}
		evidence, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE embedded_goal_calls SET state='complete',result_json=? WHERE call_id=? AND state='unknown'`, string(evidence), callID)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return out, replay, nil
}

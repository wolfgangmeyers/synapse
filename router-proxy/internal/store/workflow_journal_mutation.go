package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type WorkflowJournalMutationResult struct {
	RunID    string         `json:"run_id"`
	EventID  string         `json:"event_id"`
	Target   *WorkflowState `json:"target,omitempty"`
	Response string         `json:"response,omitempty"`
}
type WorkflowJournalNoteInput struct {
	Source        string `json:"source"`
	Text          string `json:"text"`
	RunID         string `json:"run_id,omitempty"`
	TargetEventID string `json:"target_event_id,omitempty"`
}

func journalText(text string, required bool) (string, error) {
	if !utf8.ValidString(text) || len(text) > 4096 {
		return "", errors.New("journal text exceeds limit or is invalid UTF-8")
	}
	text = strings.TrimSpace(text)
	if required && text == "" {
		return "", errors.New("journal text is required")
	}
	return text, nil
}
func (s *Store) journalResultTx(ctx context.Context, agent string) (WorkflowJournalMutationResult, error) {
	var r WorkflowJournalMutationResult
	err := s.db.QueryRowContext(ctx, `SELECT r.id,e.id FROM workflow_journal_runs r JOIN workflow_journal_events e ON e.run_id=r.id AND e.sequence=r.next_sequence-1 WHERE r.agent_id=? AND r.status='active'`, agent).Scan(&r.RunID, &r.EventID)
	return r, err
}
func (s *Store) journalMutation(ctx context.Context, agent, kind, op string, payload any, apply func() (WorkflowJournalMutationResult, error)) (WorkflowJournalMutationResult, bool, error) {
	fingerprint, _ := json.Marshal(payload)
	encoded, replay, err := s.WorkflowJournalOperation(ctx, agent, kind, op, fmt.Sprintf("%x", sha256.Sum256(fingerprint)), func() (string, error) {
		r, e := apply()
		if e != nil {
			return "", e
		}
		b, e := json.Marshal(r)
		return string(b), e
	})
	var result WorkflowJournalMutationResult
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &result)
	}
	return result, replay, err
}

func (s *Store) TransitionWorkflowJournal(ctx context.Context, agent, op string, generation int64, from, target, reason string, render ...func(WorkflowJournalMutationResult) (string, error)) (WorkflowJournalMutationResult, bool, error) {
	raw := reason
	reason, err := journalText(reason, false)
	if err != nil {
		return WorkflowJournalMutationResult{}, false, err
	}
	payload := struct{ Target, Reason string }{target, raw}
	return s.journalMutation(ctx, agent, "transition", op, payload, func() (WorkflowJournalMutationResult, error) {
		state, e := s.authorizeWorkflowTransitionTx(ctx, agent, generation, from, target)
		if e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		r, e := s.journalResultTx(ctx, agent)
		if e != nil {
			return r, e
		}
		r.Target = state
		_, e = s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET reason=? WHERE id=?`, reason, r.EventID)
		if e == nil && len(render) > 0 {
			r.Response, e = render[0](r)
		}
		return r, e
	})
}

func (s *Store) ExitWorkflowJournal(ctx context.Context, agent, op string, generation int64, from, reason string, render ...func(WorkflowJournalMutationResult) (string, error)) (WorkflowJournalMutationResult, bool, error) {
	raw := reason
	reason, err := journalText(reason, false)
	if err != nil {
		return WorkflowJournalMutationResult{}, false, err
	}
	payload := struct{ Reason string }{raw}
	return s.journalMutation(ctx, agent, "exit", op, payload, func() (WorkflowJournalMutationResult, error) {
		var current sql.NullString
		var actual int64
		if e := s.db.QueryRowContext(ctx, `SELECT active_workflow_state_id,workflow_binding_generation FROM managed_agents WHERE agent_id=?`, agent).Scan(&current, &actual); e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		if !current.Valid || current.String != from || actual != generation {
			return WorkflowJournalMutationResult{}, errors.New("workflow binding changed")
		}
		// Read permission through this transaction's connection. The writer lock
		// keeps the state definition stable until the binding clear and terminal
		// event commit; a separate GetWorkflowState read would race that mutation.
		var allowExit int
		if e := s.db.QueryRowContext(ctx, `SELECT allow_exit FROM workflow_states WHERE id=?`, from).Scan(&allowExit); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return WorkflowJournalMutationResult{}, &WorkflowTransitionRejection{Message: fmt.Sprintf("workflow exit is not permitted from state %q", from)}
			}
			return WorkflowJournalMutationResult{}, e
		}
		if allowExit != 1 {
			return WorkflowJournalMutationResult{}, &WorkflowTransitionRejection{Message: fmt.Sprintf("workflow exit is not permitted from state %q", from)}
		}
		state, e := s.GetWorkflowState(ctx, from)
		if e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		run, e := s.journalActiveTx(ctx, agent)
		if e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		if run == "" {
			if e = s.journalStartTx(ctx, agent, from, true); e != nil {
				return WorkflowJournalMutationResult{}, e
			}
			run, e = s.journalActiveTx(ctx, agent)
			if e != nil {
				return WorkflowJournalMutationResult{}, e
			}
		}
		if e = s.setManagedAgentActiveWorkflowWithVariablesTx(ctx, agent, nil, nil, nil); e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		if e = s.journalAppendTx(ctx, run, "end", ""); e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		var r WorkflowJournalMutationResult
		r.RunID = run
		r.Target = state
		e = s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&r.EventID)
		if e != nil {
			return r, e
		}
		if _, e = s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET reason=? WHERE id=?`, reason, r.EventID); e != nil {
			return r, e
		}
		if _, e = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET status='terminal',terminal_at=?,outcome='exited' WHERE id=?`, formatTimestamp(s.clock.Now()), run); e != nil {
			return r, e
		}
		if e = s.journalSweepTx(ctx); e != nil {
			return r, e
		}

		e = s.retireWorkflowSnoozeTx(ctx, agent, true)
		if e == nil && len(render) > 0 {
			r.Response, e = render[0](r)
		}
		return r, e
	})
}

// Caller enforces tool admission and ownership of agent.
func (s *Store) AppendWorkflowJournalNote(ctx context.Context, agent, op string, in WorkflowJournalNoteInput) (WorkflowJournalMutationResult, bool, error) {
	text, err := journalText(in.Text, true)
	if err != nil {
		return WorkflowJournalMutationResult{}, false, err
	}
	if in.Source != "proactive" && in.Source != "human-requested" && in.Source != "prompted" {
		return WorkflowJournalMutationResult{}, false, errors.New("invalid journal note source")
	}
	if in.Source == "prompted" {
		if in.RunID == "" || in.TargetEventID == "" {
			return WorkflowJournalMutationResult{}, false, ErrWorkflowJournalNotFound
		}
	} else if in.RunID != "" || in.TargetEventID != "" {
		return WorkflowJournalMutationResult{}, false, errors.New("ordinary note cannot specify target")
	}
	return s.journalMutation(ctx, agent, "note", op, in, func() (WorkflowJournalMutationResult, error) {
		run, target := in.RunID, in.TargetEventID
		if in.Source == "prompted" {
			cutoff, e := s.journalReadExpiry(ctx)
			if e != nil {
				return WorkflowJournalMutationResult{}, e
			}
			var exists int
			e = s.db.QueryRowContext(ctx, `SELECT 1 FROM workflow_journal_runs r JOIN workflow_journal_events e ON e.run_id=r.id WHERE r.agent_id=? AND r.id=? AND e.id=? AND (r.status='active' OR r.terminal_at>?)`, agent, run, target, cutoff).Scan(&exists)
			if errors.Is(e, sql.ErrNoRows) {
				return WorkflowJournalMutationResult{}, ErrWorkflowJournalNotFound
			}
			if e != nil {
				return WorkflowJournalMutationResult{}, e
			}
		} else {
			e := s.db.QueryRowContext(ctx, `SELECT id,current_visit_id FROM workflow_journal_runs WHERE agent_id=? AND status='active' AND current_visit_id<>''`, agent).Scan(&run, &target)
			if errors.Is(e, sql.ErrNoRows) {
				return WorkflowJournalMutationResult{}, ErrWorkflowJournalNotFound
			}
			if e != nil {
				return WorkflowJournalMutationResult{}, e
			}
		}
		if e := s.journalAppendTx(ctx, run, "note", ""); e != nil {
			return WorkflowJournalMutationResult{}, e
		}
		var r WorkflowJournalMutationResult
		r.RunID = run
		if e := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, run).Scan(&r.EventID); e != nil {
			return r, e
		}
		_, e := s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET source=?,text=?,target_event_id=? WHERE id=?`, in.Source, text, target, r.EventID)
		return r, e
	})
}

// ReplayWorkflowJournalTransition resolves a receipt before reading mutable agent state.
func (s *Store) ReplayWorkflowJournalTransition(ctx context.Context, agent, op, action, target, reason string) (WorkflowJournalMutationResult, bool, error) {
	var payload any
	if action == "transition" {
		payload = struct{ Target, Reason string }{target, reason}
	} else {
		payload = struct{ Reason string }{reason}
	}
	miss := errors.New("workflow journal receipt not found")
	r, replay, err := s.journalMutation(ctx, agent, action, op, payload, func() (WorkflowJournalMutationResult, error) { return WorkflowJournalMutationResult{}, miss })
	if errors.Is(err, miss) {
		return WorkflowJournalMutationResult{}, false, nil
	}
	return r, replay, err
}

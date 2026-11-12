package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// WorkflowJournalActivationInput is the resolved binding intent. Callers must
// supply stable request fields, not mutable lookup results, for replay safety.
type WorkflowJournalActivationInput struct {
	WorkflowID *string           `json:"workflow_id"`
	StateID    *string           `json:"state_id"`
	Variables  map[string]string `json:"variables"`
}

// SetWorkflowJournalActivation commits the binding, journal, and receipt together.
func (s *Store) SetWorkflowJournalActivation(ctx context.Context, agentID, operationID string, input WorkflowJournalActivationInput) (WorkflowJournalMutationResult, bool, error) {
	return s.journalMutation(ctx, agentID, "activation", operationID, input, func() (WorkflowJournalMutationResult, error) {

		var oldRun string
		err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agentID).Scan(&oldRun)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WorkflowJournalMutationResult{}, err
		}
		state := input.StateID
		if state != nil && *state == "" {
			state = nil
		}
		if err := s.setManagedAgentActiveWorkflowJournalTx(ctx, agentID, input.WorkflowID, state, input.Variables); err != nil {
			return WorkflowJournalMutationResult{}, err
		}
		if state != nil {
			result, err := s.journalResultTx(ctx, agentID)
			if err != nil {
				return result, err
			}
			return result, nil
		}
		if oldRun == "" {
			return WorkflowJournalMutationResult{}, nil
		}
		result := WorkflowJournalMutationResult{RunID: oldRun}
		err = s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_events WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, oldRun).Scan(&result.EventID)
		return result, err
	})
}

// WorkflowActivationRequest contains caller intent, before mutable definition lookup.
type WorkflowActivationRequest struct {
	Action      string            `json:"action"`
	WorkflowID  string            `json:"workflow_id"`
	StateID     string            `json:"workflow_state_id"`
	Variables   map[string]string `json:"variables"`
	ResolveName bool              `json:"resolve_name"`
}

type WorkflowActivationResult struct {
	WorkflowID *string           `json:"workflow_id"`
	State      *WorkflowState    `json:"state"`
	Variables  map[string]string `json:"variables"`
}

var ErrActivationWorkflowNotFound = errors.New("workflow not found")
var ErrActivationStateNotFound = errors.New("workflow state not found")
var ErrActivationMissingState = errors.New("workflow has no entry state")

// ActivateWorkflow resolves definitions only after receipt lookup, within the
// same transaction as the binding, journal and immutable response snapshot.
func (s *Store) ActivateWorkflow(ctx context.Context, agentID, operationID string, fingerprint any, in WorkflowActivationRequest) (result WorkflowActivationResult, replay bool, err error) {
	payload, err := json.Marshal(fingerprint)
	if err != nil {
		return result, false, err
	}
	encoded, replay, err := s.WorkflowJournalOperation(ctx, agentID, "activation", operationID, fmt.Sprintf("%x", sha256.Sum256(payload)), func() (string, error) {
		var exists int
		if e := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id=?`, agentID).Scan(&exists); e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				return "", &ManagedAgentNotFoundError{AgentID: agentID}
			}
			return "", e
		}
		var wf *Workflow
		var state *WorkflowState
		var workflowID *string
		if in.Action == "activate" {
			if in.WorkflowID != "" {
				var e error
				if in.ResolveName {
					wf, e = s.FindWorkflowByIDOrName(ctx, in.WorkflowID)
				} else {
					wf, e = s.GetWorkflow(ctx, in.WorkflowID)
				}
				if e != nil {
					return "", e
				}
				if wf == nil {
					return "", ErrActivationWorkflowNotFound
				}
				workflowID = &wf.ID
			}
			stateID := in.StateID
			if stateID == "" && wf != nil && wf.EntryStateID != nil {
				stateID = strings.TrimSpace(*wf.EntryStateID)
			}
			if stateID == "" {
				return "", ErrActivationMissingState
			}
			var e error
			state, e = s.GetWorkflowState(ctx, stateID)
			if e != nil {
				return "", e
			}
			if state == nil {
				return "", ErrActivationStateNotFound
			}
			if workflowID == nil {
				workflowID = state.WorkflowID
			}
		} else if in.Action != "deactivate" {
			return "", errors.New("invalid activation action")
		}
		var stateID *string
		if state != nil {
			stateID = &state.ID
		}
		if e := s.setManagedAgentActiveWorkflowJournalTx(ctx, agentID, workflowID, stateID, in.Variables); e != nil {
			return "", e
		}
		r := WorkflowActivationResult{WorkflowID: workflowID, State: state}
		if state != nil {
			r.Variables = in.Variables
		}
		b, e := json.Marshal(r)
		return string(b), e
	})
	if err == nil {
		err = json.Unmarshal([]byte(encoded), &result)
	}
	return
}

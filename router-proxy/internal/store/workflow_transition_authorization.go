package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// WorkflowTransitionRejection is a user-facing authorization error.
type WorkflowTransitionRejection struct{ Message string }

func (e *WorkflowTransitionRejection) Error() string { return e.Message }

// eligibleWorkflowPredecessor is shared by authorization and guidance. Callers
// pass binding values read together; authorization also holds a write transaction.
func (s *Store) eligibleWorkflowPredecessor(ctx context.Context, state *WorkflowState, current, workflow, previous sql.NullString) (*WorkflowState, error) {
	if !state.AllowImplicitReturn || !current.Valid || current.String != state.ID || !workflow.Valid || !previous.Valid || previous.String == state.ID {
		return nil, nil
	}
	target, err := s.GetWorkflowState(ctx, previous.String)
	if err != nil {
		return nil, err
	}
	if target == nil || target.WorkflowID == nil || *target.WorkflowID != workflow.String {
		return nil, nil
	}
	return target, nil
}

// AuthorizeWorkflowTransition resolves and commits a transition under one write
// transaction. The caller captures generation before starting an attempt. Definition
// edits made before this transaction are evaluated, not trusted from a snapshot.
func (s *Store) AuthorizeWorkflowTransition(ctx context.Context, agentID string, generation int64, from, input string) (*WorkflowState, error) {
	var target *WorkflowState
	err := s.runImmediate(ctx, func() error {
		var err error
		target, err = s.authorizeWorkflowTransitionTx(ctx, agentID, generation, from, input)
		return err
	})
	return target, err
}

func (s *Store) authorizeWorkflowTransitionTx(ctx context.Context, agentID string, generation int64, from, input string) (*WorkflowState, error) {
	var target *WorkflowState
	err := func() error {
		var current, workflow, predecessor sql.NullString
		var actual int64
		if err := s.db.QueryRowContext(ctx, `SELECT active_workflow_state_id, active_workflow_id, workflow_predecessor_state_id, workflow_binding_generation FROM managed_agents WHERE agent_id=?`, agentID).Scan(&current, &workflow, &predecessor, &actual); err != nil {
			return err
		}
		if !current.Valid || current.String != from || actual != generation {
			return errors.New("workflow binding changed")
		}
		state, err := s.GetWorkflowState(ctx, from)
		if err != nil {
			return err
		}
		if state == nil {
			return errors.New("current workflow state not found")
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return errors.New("target_state is required")
		}
		// Explicit edges take priority, including cross-workflow and legacy self edges.
		var allowed []string
		authorization := "explicit"
		seen := make(map[string]bool)
		for _, edge := range state.Transitions {
			candidate, readErr := s.GetWorkflowState(ctx, edge.TargetStateID)
			name := edge.TargetStateID
			if readErr == nil && candidate != nil {
				name = candidate.Name
			}
			if !seen[edge.TargetStateID] {
				allowed = append(allowed, name)
				seen[edge.TargetStateID] = true
			}
			if edge.TargetStateID == input || strings.EqualFold(name, input) {
				// As before, a matching ID is resolved again and must exist.
				target, readErr = s.GetWorkflowState(ctx, edge.TargetStateID)
				if readErr != nil || target == nil {
					return &WorkflowTransitionRejection{Message: fmt.Sprintf("target workflow state %q not found", edge.TargetStateID)}
				}
				break
			}
		}
		if target == nil {
			previous, err := s.eligibleWorkflowPredecessor(ctx, state, current, workflow, predecessor)
			if err != nil {
				return err
			}
			if previous != nil {
				if !seen[previous.ID] {
					allowed = append(allowed, previous.Name)
				}
				if previous.ID == input || strings.EqualFold(previous.Name, input) {
					target = previous
					authorization = "implicit_return"
				}
			}
		}
		if target == nil {
			if len(allowed) == 0 {
				return &WorkflowTransitionRejection{Message: fmt.Sprintf("workflow state %q has no outgoing transitions", state.Name)}
			}
			return &WorkflowTransitionRejection{Message: fmt.Sprintf("transition to %q is not permitted from state %q (allowed targets: %s)", input, state.Name, strings.Join(allowed, ", "))}
		}
		if err := s.setManagedAgentActiveWorkflowStateTx(ctx, agentID, &target.ID); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET workflow_predecessor_state_id=? WHERE agent_id=?`, from, agentID); err != nil {
			return err
		}
		if err := s.journalVisitTx(ctx, agentID, target.ID, "visit"); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET source=? WHERE id=(SELECT current_visit_id FROM workflow_journal_runs WHERE agent_id=? AND status='active')`, authorization, agentID); err != nil {
			return err
		}
		if err := s.retireWorkflowSnoozeTx(ctx, agentID, false); err != nil {
			return err
		}
		return nil
	}()
	return target, err
}

// WorkflowStateForGuidance adds the currently eligible predecessor without changing the definition.
func (s *Store) WorkflowStateForGuidance(ctx context.Context, agentID string, state *WorkflowState) (*WorkflowState, error) {
	if state == nil {
		return nil, nil
	}
	copyState := *state
	copyState.Transitions = append([]WorkflowTransitionConfig(nil), state.Transitions...)
	if !state.AllowImplicitReturn {
		return &copyState, nil
	}
	var current, workflow, previous sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT active_workflow_state_id, active_workflow_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?`, agentID).Scan(&current, &workflow, &previous); err != nil {
		return nil, err
	}
	target, err := s.eligibleWorkflowPredecessor(ctx, state, current, workflow, previous)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return &copyState, nil
	}
	for _, edge := range copyState.Transitions {
		if edge.TargetStateID == target.ID {
			return &copyState, nil
		}
	}
	copyState.Transitions = append(copyState.Transitions, WorkflowTransitionConfig{TargetStateID: target.ID, Description: "Return to the immediately previous state"})
	return &copyState, nil
}

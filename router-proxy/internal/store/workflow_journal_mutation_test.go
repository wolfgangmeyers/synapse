package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowJournalFencedExitAndNotes(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Exit"})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	binding, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	note, replay, err := s.AppendWorkflowJournalNote(ctx, agent.AgentID, "note-1", WorkflowJournalNoteInput{Source: "proactive", Text: "Useful note"})
	require.NoError(t, err)
	require.False(t, replay)
	again, replay, err := s.AppendWorkflowJournalNote(ctx, agent.AgentID, "note-1", WorkflowJournalNoteInput{Source: "proactive", Text: "Useful note"})
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, note, again)
	_, _, err = s.AppendWorkflowJournalNote(ctx, agent.AgentID, "note-1", WorkflowJournalNoteInput{Source: "proactive", Text: "changed"})
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	_, _, err = s.ExitWorkflowJournal(ctx, agent.AgentID, "stale", binding.Generation-1, a.ID, "")
	require.Error(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET allow_exit=0 WHERE id=?`, a.ID)
	require.NoError(t, err)
	_, _, err = s.ExitWorkflowJournal(ctx, agent.AgentID, "denied", binding.Generation, a.ID, "")
	require.Error(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET allow_exit=1 WHERE id=?`, a.ID)
	require.NoError(t, err)
	result, replay, err := s.ExitWorkflowJournal(ctx, agent.AgentID, "exit", binding.Generation, a.ID, "done")
	require.NoError(t, err)
	require.False(t, replay)
	require.NotEmpty(t, result.EventID)
	same, replay, err := s.ExitWorkflowJournal(ctx, agent.AgentID, "exit", binding.Generation, a.ID, "done")
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, result, same)
	_, _, err = s.AppendWorkflowJournalNote(ctx, agent.AgentID, "late", WorkflowJournalNoteInput{Source: "prompted", Text: "Outcome", RunID: result.RunID, TargetEventID: result.EventID})
	require.NoError(t, err)
	_, _, err = s.AppendWorkflowJournalNote(ctx, agent.AgentID, "ordinary", WorkflowJournalNoteInput{Source: "proactive", Text: "late"})
	require.Error(t, err)
	_, _, err = s.AppendWorkflowJournalNote(ctx, agent.AgentID, "long", WorkflowJournalNoteInput{Source: "prompted", Text: strings.Repeat("x", 4097), RunID: result.RunID, TargetEventID: result.EventID})
	require.Error(t, err)
	var reason, status string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT reason FROM workflow_journal_events WHERE id=?`, result.EventID).Scan(&reason))
	require.Equal(t, "done", reason)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT status FROM workflow_journal_runs WHERE id=?`, result.RunID).Scan(&status))
	require.Equal(t, "terminal", status)
}

func TestWorkflowJournalNoteSameOperationConcurrency(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "Concurrent note"})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &state.ID, nil))
	input := WorkflowJournalNoteInput{Source: "proactive", Text: "one operation"}
	type outcome struct {
		result WorkflowJournalMutationResult
		replay bool
		err error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			r, replay, err := s.AppendWorkflowJournalNote(ctx, agent.AgentID, "concurrent-note", input)
			outcomes <- outcome{r, replay, err}
		}()
	}
	close(start)
	first, second := <-outcomes, <-outcomes
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, first.result, second.result)
	require.NotEqual(t, first.replay, second.replay)
	var events, receipts int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=? AND kind='note'`, first.result.RunID).Scan(&events))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_receipts WHERE agent_id=? AND kind='note' AND operation_id='concurrent-note'`, agent.AgentID).Scan(&receipts))
	require.Equal(t, 1, events)
	require.Equal(t, 1, receipts)
}

func TestWorkflowJournalTransitionReceiptAndRollback(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A"})
	require.NoError(t, err)
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B"})
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+b.ID+`"}]`, a.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	binding, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `CREATE TRIGGER journal_mutation_fail BEFORE INSERT ON workflow_journal_events BEGIN SELECT RAISE(ABORT,'injected'); END`)
	require.NoError(t, err)
	_, _, err = s.TransitionWorkflowJournal(ctx, agent.AgentID, "transition", binding.Generation, a.ID, b.ID, "why")
	require.Error(t, err)
	after, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, binding.Generation, after.Generation)
	require.Equal(t, a.ID, after.StateID)
	_, err = s.db.ExecContext(ctx, `DROP TRIGGER journal_mutation_fail`)
	require.NoError(t, err)
	_, _, err = s.TransitionWorkflowJournal(ctx, agent.AgentID, "transition", binding.Generation, a.ID, b.ID, "why", func(WorkflowJournalMutationResult) (string, error) { return "", errors.New("render failed") })
	require.ErrorContains(t, err, "render failed")
	after, err = s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, binding, after)
	var receipts int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_receipts WHERE operation_id='transition'`).Scan(&receipts))
	require.Zero(t, receipts)
	first, replay, err := s.TransitionWorkflowJournal(ctx, agent.AgentID, "transition", binding.Generation, a.ID, b.ID, "why", func(WorkflowJournalMutationResult) (string, error) { return "original expanded response", nil })
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, b.ID, first.Target.ID)
	require.Equal(t, "original expanded response", first.Response)
	again, replay, err := s.TransitionWorkflowJournal(ctx, agent.AgentID, "transition", binding.Generation+1, b.ID, b.ID, "why")
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	_, _, err = s.TransitionWorkflowJournal(ctx, agent.AgentID, "transition", binding.Generation, a.ID, b.ID, "changed")
	require.ErrorIs(t, err, ErrWorkflowJournalOperationConflict)
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, first.RunID).Scan(&count))
	require.Equal(t, 2, count)
}

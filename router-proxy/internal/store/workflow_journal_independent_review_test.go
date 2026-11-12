package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJournalNoteRejectsExpiredUnsweptRun(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A"})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, nil, &a.ID, nil))
	var run, event string
	require.NoError(t, s.db.QueryRow(`SELECT id,current_visit_id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent.AgentID).Scan(&run, &event))
	cutoff := s.clock.Now().UTC().Add(200 * 24 * time.Hour)
	_, err = s.db.Exec(`UPDATE workflow_journal_runs SET status='terminal',terminal_at=? WHERE id=?`, formatTimestamp(cutoff.Add(-90*24*time.Hour)), run)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO workflow_journal_retention(id,cutoff) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET cutoff=excluded.cutoff`, formatTimestamp(cutoff))
	require.NoError(t, err)
	_, _, err = s.AppendWorkflowJournalNote(ctx, agent.AgentID, "expired", WorkflowJournalNoteInput{Source: "prompted", Text: "late", RunID: run, TargetEventID: event})
	require.ErrorIs(t, err, ErrWorkflowJournalNotFound)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM workflow_journal_events WHERE run_id=?`, run).Scan(&count))
	require.Equal(t, 1, count)
}

func TestJournalTransitionRecordsAuthorizationType(t *testing.T) {
	ctx := context.Background()
	s, agent := readinessSourceFixture(t)
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{Name: "flow"})
	require.NoError(t, err)
	yes := true
	b, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "B", WorkflowID: &wf.ID, AllowImplicitReturn: &yes})
	require.NoError(t, err)
	a, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{Name: "A", WorkflowID: &wf.ID, Transitions: []WorkflowTransitionConfig{{TargetStateID: b.ID}}})
	require.NoError(t, err)
	require.NoError(t, s.SetManagedAgentActiveWorkflowWithVariables(ctx, agent.AgentID, &wf.ID, &a.ID, nil))
	binding, err := s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	first, _, err := s.TransitionWorkflowJournal(ctx, agent.AgentID, "first", binding.Generation, a.ID, b.ID, "")
	require.NoError(t, err)
	binding, err = s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	second, _, err := s.TransitionWorkflowJournal(ctx, agent.AgentID, "second", binding.Generation, b.ID, a.ID, "")
	require.NoError(t, err)
	var one, two string
	require.NoError(t, s.db.QueryRow(`SELECT source FROM workflow_journal_events WHERE id=?`, first.EventID).Scan(&one))
	require.NoError(t, s.db.QueryRow(`SELECT source FROM workflow_journal_events WHERE id=?`, second.EventID).Scan(&two))
	require.Equal(t, "explicit", one)
	require.Equal(t, "implicit_return", two)
	detail, err := s.GetWorkflowJournalRun(ctx, agent.AgentID, first.RunID, WorkflowJournalTimelineFilter{})
	require.NoError(t, err)
	require.Equal(t, "explicit", detail.Events[1].Source)
	require.Equal(t, "implicit_return", detail.Events[2].Source)
	require.NotNil(t, detail.Events[1].DurationMS)
	// A configured edge wins even when the same target is the predecessor.
	_, err = s.db.Exec(`UPDATE workflow_states SET transitions_json=? WHERE id=?`, `[{"target_state_id":"`+a.ID+`"}]`, b.ID)
	require.NoError(t, err)
	binding, err = s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	_, _, err = s.TransitionWorkflowJournal(ctx, agent.AgentID, "third", binding.Generation, a.ID, b.ID, "")
	require.NoError(t, err)
	binding, err = s.WorkflowBindingVersion(ctx, agent.AgentID)
	require.NoError(t, err)
	fourth, _, err := s.TransitionWorkflowJournal(ctx, agent.AgentID, "fourth", binding.Generation, b.ID, a.ID, "")
	require.NoError(t, err)
	var precedence string
	require.NoError(t, s.db.QueryRow(`SELECT source FROM workflow_journal_events WHERE id=?`, fourth.EventID).Scan(&precedence))
	require.Equal(t, "explicit", precedence)
}

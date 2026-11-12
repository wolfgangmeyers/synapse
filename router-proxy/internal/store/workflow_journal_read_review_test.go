package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestJournalReadExpiryAndSweep(t *testing.T) {
	ctx := context.Background()
	s, a := readinessSourceFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.clock = deterministic.NewFixedClock(now)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,terminal_at,status) VALUES('expired',?,'a','','',?,?,'terminal')`, a.AgentID, formatTimestamp(now.Add(-100*24*time.Hour)), formatTimestamp(now.Add(-90*24*time.Hour)))
	require.NoError(t, err)
	page, err := s.ListWorkflowJournalRuns(ctx, a.AgentID, WorkflowJournalFilter{})
	require.NoError(t, err)
	require.Empty(t, page.Runs)
	_, err = s.GetWorkflowJournalRun(ctx, a.AgentID, "expired", WorkflowJournalTimelineFilter{})
	require.ErrorIs(t, err, ErrWorkflowJournalNotFound)
	require.NoError(t, s.SweepWorkflowJournal(ctx))
	var count int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE id='expired'`).Scan(&count))
	require.Zero(t, count)
}

func TestJournalWorkflowFilterRetainedAndEvictedBoundaries(t *testing.T) {
	ctx := context.Background()
	s, retainedAgent := readinessSourceFixture(t)
	// Mirror readinessSourceFixture's managed-launch setup in the shared store.
	evicted := ManagedAgentRecord{AgentID: "evicted-agent", Name: "evicted-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "evicted-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	evictedOp := ManagedOperation{OperationID: "evicted-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "evicted-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, s.ReserveManagedLaunch(ctx, evicted, evictedOp))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, evicted.AgentID, evicted.PiSessionID, ProcessIdentityRecord{PID: 100, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	now := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	s.clock = deterministic.NewFixedClock(now)
	for _, row := range []struct {
		id, agent string
		started, visit time.Time
		workflow string
	}{
		{"retained", retainedAgent.AgentID, now.Add(-20 * time.Second), now.Add(-10 * time.Second), "wf-known"},
		{"evicted", evicted.AgentID, now.Add(-10 * time.Second), now.Add(-5 * time.Second), "wf-current"},
	} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status,incomplete,cutoff_sequence,current_visit_id,current_visit_sequence,current_visit_at,current_workflow_id,current_state_id,current_state_name) VALUES(?,?,'a','state','State',?,'active',1,1,'gone',1,?,?, 'state','State')`, row.id, row.agent, formatTimestamp(row.started), formatTimestamp(row.visit), row.workflow)
		require.NoError(t, err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,state_id) VALUES('boundary','retained',1,'visit',?,'wf-known','state'),('old','evicted',1,'visit',?,'wf-old','state')`, formatTimestamp(now.Add(-20*time.Second)), formatTimestamp(now.Add(-10*time.Second)))
	require.NoError(t, err)
	// The old workflow is represented only by an evicted historical event;
	// current run metadata belongs to wf-current and must drive filtering.
	_, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_events WHERE id='old'`)
	require.NoError(t, err)
	for _, tc := range []struct{ agent, workflow, want string }{
		{retainedAgent.AgentID, "wf-known", "retained"},
		{evicted.AgentID, "wf-current", "evicted"},
		{evicted.AgentID, "wf-old", ""},
	} {
		page, e := s.ListWorkflowJournalRuns(ctx, tc.agent, WorkflowJournalFilter{WorkflowID: tc.workflow})
		require.NoError(t, e)
		if tc.want == "" {
			require.Empty(t, page.Runs)
		} else {
			require.Len(t, page.Runs, 1)
			require.Equal(t, tc.want, page.Runs[0].ID)
		}
	}
}

func TestJournalReadVisitDurationsAndEvictedCurrent(t *testing.T) {
	ctx := context.Background()
	s, a := readinessSourceFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	s.clock = deterministic.NewFixedClock(now)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status,incomplete,cutoff_sequence,current_visit_id,current_visit_sequence,current_visit_at,current_workflow_id,current_state_id,current_state_name) VALUES('run',?,'a','s','state',?,'active',1,1,'old',1,?,'wf','s','state')`, a.AgentID, formatTimestamp(now.Add(-10*time.Second)), formatTimestamp(now.Add(-10*time.Second)))
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,state_id,text) VALUES('note','run',2,'note',?,'s','hello')`, formatTimestamp(now.Add(-time.Second)))
	require.NoError(t, err)
	page, err := s.ListWorkflowJournalRuns(ctx, a.AgentID, WorkflowJournalFilter{WorkflowID: "wf"})
	require.NoError(t, err)
	require.Len(t, page.Runs, 1)
	d, err := s.GetWorkflowJournalRun(ctx, a.AgentID, "run", WorkflowJournalTimelineFilter{})
	require.NoError(t, err)
	require.Len(t, d.Events, 1)
	require.Nil(t, d.Events[0].DurationMS)
	require.NotNil(t, d.CurrentVisit)
	require.Equal(t, "old", d.CurrentVisit.ID)
	require.EqualValues(t, 10000, *d.CurrentVisit.DurationMS)
}

func TestJournalSweepRollbackAndActiveProtection(t *testing.T) {
	ctx := context.Background()
	s, a := readinessSourceFixture(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	s.clock = deterministic.NewFixedClock(now)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status) VALUES('active',?,'a','','',?,'active')`, a.AgentID, formatTimestamp(now.Add(-200*24*time.Hour)))
	require.NoError(t, err)
	require.NoError(t, s.SweepWorkflowJournal(ctx))
	s.clock = deterministic.NewFixedClock(now.Add(-100 * 24 * time.Hour))
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,terminal_at,status) VALUES('late',?,'a','','',?,?,'terminal')`, a.AgentID, formatTimestamp(now.Add(-200*24*time.Hour)), formatTimestamp(now.Add(-90*24*time.Hour)))
	require.NoError(t, err)
	p, err := s.ListWorkflowJournalRuns(ctx, a.AgentID, WorkflowJournalFilter{})
	require.NoError(t, err)
	require.Len(t, p.Runs, 1)
	require.Equal(t, "active", p.Runs[0].ID)
	require.NoError(t, s.SweepWorkflowJournal(ctx))
	var n int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs`).Scan(&n))
	require.Equal(t, 1, n)
}

type journalSnapshotWriterClock struct {
	now   time.Time
	calls int
	write func()
}

func (c *journalSnapshotWriterClock) Now() time.Time {
	c.calls++
	// The second call happens after the run row has been read and the read
	// transaction's SQLite snapshot has been established.
	if c.calls == 2 {
		c.write()
	}
	return c.now
}

func TestJournalDetailUsesOneSnapshotAcrossQueries(t *testing.T) {
	ctx := context.Background()
	s, a := readinessSourceFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status,incomplete,cutoff_sequence) VALUES('snapshot',?,'a','s','S',?,'active',1,1)`, a.AgentID, formatTimestamp(now.Add(-10*time.Second)))
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name) VALUES('start','snapshot',1,'start',?,'w','W','s','S')`, formatTimestamp(now.Add(-10*time.Second)))
	require.NoError(t, err)
	writer, err := sql.Open("sqlite", s.path)
	require.NoError(t, err)
	defer writer.Close()
	s.clock = &journalSnapshotWriterClock{now: now, write: func() {
		_, writeErr := writer.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name) VALUES('concurrent','snapshot',2,'visit',?,'w','W','s','S')`, formatTimestamp(now.Add(-5*time.Second)))
		require.NoError(t, writeErr)
	}}

	d, err := s.GetWorkflowJournalRun(ctx, a.AgentID, "snapshot", WorkflowJournalTimelineFilter{})
	require.NoError(t, err)
	require.Len(t, d.Events, 1)
	// Neither the timeline nor duration aggregation may observe a write that
	// committed after the run metadata query established the read snapshot.
	require.Len(t, d.StateTotals, 1)
	require.EqualValues(t, 10000, d.StateTotals[0].DurationMS)
}

func TestJournalTotalsCoverAllPages(t *testing.T) {
	ctx := context.Background()
	s, a := readinessSourceFixture(t)
	now := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	s.clock = deterministic.NewFixedClock(now)
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,initial_state_id,initial_state_name,started_at,status,incomplete,cutoff_sequence) VALUES('totals',?,'a','s','S',?,'active',1,2)`, a.AgentID, formatTimestamp(now.Add(-10*time.Second)))
	require.NoError(t, err)
	for i, kind := range []string{"start", "visit", "state_move"} {
		_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name) VALUES(?,'totals',?,?,?,?,?,?,?)`, kind, i+2, kind, formatTimestamp(now.Add(time.Duration(i*3-9)*time.Second)), "w", "Workflow", "s", "State")
		require.NoError(t, err)
	}
	d, err := s.GetWorkflowJournalRun(ctx, a.AgentID, "totals", WorkflowJournalTimelineFilter{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, d.NextCursor)
	require.True(t, d.Partial)
	require.EqualValues(t, 2, d.CutoffSequence)
	require.Len(t, d.StateTotals, 1)
	require.EqualValues(t, 9000, d.StateTotals[0].DurationMS)
	d2, err := s.GetWorkflowJournalRun(ctx, a.AgentID, "totals", WorkflowJournalTimelineFilter{Cursor: d.NextCursor, Limit: 1})
	require.NoError(t, err)
	require.Equal(t, d.StateTotals, d2.StateTotals)
}

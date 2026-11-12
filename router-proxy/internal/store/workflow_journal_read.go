package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

var ErrWorkflowJournalNotFound = errors.New("workflow journal not found")
var ErrWorkflowJournalActive = errors.New("workflow journal run is active")
var ErrWorkflowJournalCursor = errors.New("invalid workflow journal cursor")

type WorkflowJournalFilter struct {
	WorkflowID, Status, Cursor string
	Limit                      int
}
type WorkflowJournalTimelineFilter struct {
	Cursor string
	Limit  int
}
type WorkflowJournalRun struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	AgentName        string  `json:"agent_name"`
	WorkflowID       string  `json:"workflow_id"`
	WorkflowName     string  `json:"workflow_name"`
	InitialStateID   string  `json:"initial_state_id"`
	InitialStateName string  `json:"initial_state_name"`
	StartedAt        string  `json:"started_at"`
	TerminalAt       *string `json:"terminal_at,omitempty"`
	Status           string  `json:"status"`
	Outcome          string  `json:"outcome,omitempty"`
	Synthetic        bool    `json:"synthetic"`
	Incomplete       bool    `json:"incomplete"`
	CutoffSequence   int64   `json:"cutoff_sequence"`
}
type WorkflowJournalEvent struct {
	ID            string `json:"id"`
	Sequence      int64  `json:"sequence"`
	Kind          string `json:"kind"`
	OccurredAt    string `json:"occurred_at"`
	WorkflowID    string `json:"workflow_id"`
	WorkflowName  string `json:"workflow_name"`
	StateID       string `json:"state_id"`
	StateName     string `json:"state_name"`
	Reason        string `json:"reason,omitempty"`
	Source        string `json:"source,omitempty"`
	Text          string `json:"text,omitempty"`
	TargetEventID string `json:"target_event_id,omitempty"`
	DurationMS    *int64 `json:"duration_ms"`
}
type WorkflowJournalPage struct {
	Runs         []WorkflowJournalRun `json:"runs"`
	NextCursor   string               `json:"next_cursor,omitempty"`
	RetainedOnly bool                 `json:"retained_only"`
}
type WorkflowJournalStateTotal struct {
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	StateID      string `json:"state_id"`
	StateName    string `json:"state_name"`
	DurationMS   int64  `json:"duration_ms"`
}
type WorkflowJournalDetail struct {
	StateTotals    []WorkflowJournalStateTotal `json:"state_totals"`
	Run            WorkflowJournalRun          `json:"run"`
	Events         []WorkflowJournalEvent      `json:"events"`
	NextCursor     string                      `json:"next_cursor,omitempty"`
	Partial        bool                        `json:"partial"`
	CutoffSequence int64                       `json:"cutoff_sequence"`
	ObservedAt     string                      `json:"observed_at"`
	CurrentVisit   *WorkflowJournalEvent       `json:"current_visit,omitempty"`
}
type journalCursor struct {
	Agent, Run, Workflow, Status, Time, ID string
	Sequence                               int64
}

func encodeJournalCursor(c journalCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeJournalCursor(s string, c *journalCursor) error {
	b, e := base64.RawURLEncoding.DecodeString(s)
	if e != nil || json.Unmarshal(b, c) != nil {
		return ErrWorkflowJournalCursor
	}
	return nil
}
func journalLimit(n int) int {
	if n <= 0 {
		return 100
	}
	if n > 1000 {
		return 1000
	}
	return n
}

const journalRunColumns = `id,agent_id,agent_name,workflow_id,workflow_name,initial_state_id,initial_state_name,started_at,terminal_at,status,outcome,synthetic,incomplete,cutoff_sequence`

type journalScanner interface{ Scan(...any) error }

func scanJournalRun(s journalScanner) (r WorkflowJournalRun, err error) {
	err = s.Scan(&r.ID, &r.AgentID, &r.AgentName, &r.WorkflowID, &r.WorkflowName, &r.InitialStateID, &r.InitialStateName, &r.StartedAt, &r.TerminalAt, &r.Status, &r.Outcome, &r.Synthetic, &r.Incomplete, &r.CutoffSequence)
	return
}
func (s *Store) listWorkflowJournalRuns(ctx context.Context, agent string, f WorkflowJournalFilter) (WorkflowJournalPage, error) {
	p := WorkflowJournalPage{Runs: []WorkflowJournalRun{}, RetainedOnly: true}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM managed_agents WHERE agent_id=?`, agent).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, ErrWorkflowJournalNotFound
		}
		return p, err
	}
	c := journalCursor{Agent: agent, Workflow: f.WorkflowID, Status: f.Status}
	if f.Cursor != "" {
		if err := decodeJournalCursor(f.Cursor, &c); err != nil {
			return p, err
		}
		if c.Agent != agent || c.Workflow != f.WorkflowID || c.Status != f.Status || c.Time == "" || c.ID == "" || c.Run != "" || c.Sequence != 0 {
			return p, ErrWorkflowJournalCursor
		}
	}
	n := journalLimit(f.Limit)
	cutoff, err := s.journalReadExpiry(ctx)
	if err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+journalRunColumns+` FROM workflow_journal_runs r WHERE (status='active' OR terminal_at>?) AND agent_id=? AND (?='' OR status=?) AND (?='' OR current_workflow_id=? OR EXISTS(SELECT 1 FROM workflow_journal_events e WHERE e.run_id=r.id AND e.workflow_id=? AND e.state_id<>'' AND e.kind IN ('start','visit','admin_visit','state_move'))) AND (?='' OR started_at<? OR (started_at=? AND id<?)) ORDER BY started_at DESC,id DESC LIMIT ?`, cutoff, agent, f.Status, f.Status, f.WorkflowID, f.WorkflowID, f.WorkflowID, c.Time, c.Time, c.Time, c.ID, n+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		r, e := scanJournalRun(rows)
		if e != nil {
			return p, e
		}
		p.Runs = append(p.Runs, r)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	if len(p.Runs) > n {
		p.Runs = p.Runs[:n]
		last := p.Runs[len(p.Runs)-1]
		c.Time = last.StartedAt
		c.ID = last.ID
		p.NextCursor = encodeJournalCursor(c)
	}
	return p, nil
}
func (s *Store) getWorkflowJournalRun(ctx context.Context, agent, run string, f WorkflowJournalTimelineFilter) (*WorkflowJournalDetail, error) {
	cutoff, err := s.journalReadExpiry(ctx)
	if err != nil {
		return nil, err
	}
	r, err := scanJournalRun(s.db.QueryRowContext(ctx, `SELECT `+journalRunColumns+` FROM workflow_journal_runs WHERE agent_id=? AND id=? AND (status='active' OR terminal_at>?)`, agent, run, cutoff))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrWorkflowJournalNotFound
	}
	if err != nil {
		return nil, err
	}
	c := journalCursor{Agent: agent, Run: run}
	if f.Cursor != "" {
		if err = decodeJournalCursor(f.Cursor, &c); err != nil {
			return nil, err
		}
		if c.Agent != agent || c.Run != run || c.Sequence <= 0 || c.Workflow != "" || c.Status != "" || c.Time != "" || c.ID != "" {
			return nil, ErrWorkflowJournalCursor
		}
	}
	d := &WorkflowJournalDetail{Run: r, Events: []WorkflowJournalEvent{}, Partial: r.Incomplete, CutoffSequence: r.CutoffSequence, ObservedAt: formatTimestamp(s.clock.Now())}
	n := journalLimit(f.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name,reason,source,text,target_event_id FROM workflow_journal_events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?`, run, c.Sequence, n+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e WorkflowJournalEvent
		if err = rows.Scan(&e.ID, &e.Sequence, &e.Kind, &e.OccurredAt, &e.WorkflowID, &e.WorkflowName, &e.StateID, &e.StateName, &e.Reason, &e.Source, &e.Text, &e.TargetEventID); err != nil {
			rows.Close()
			return nil, err
		}
		d.Events = append(d.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(d.Events) > n {
		d.Events = d.Events[:n]
		c.Sequence = d.Events[len(d.Events)-1].Sequence
		d.NextCursor = encodeJournalCursor(c)
	}
	for i := range d.Events {
		e := &d.Events[i]
		if e.StateID == "" || (e.Kind != "start" && e.Kind != "visit" && e.Kind != "admin_visit" && e.Kind != "state_move") {
			continue
		}
		var end string
		err = s.db.QueryRowContext(ctx, `SELECT occurred_at FROM workflow_journal_events WHERE run_id=? AND sequence>? AND kind IN ('start','visit','admin_visit','end','state_lost','state_move') ORDER BY sequence LIMIT 1`, run, e.Sequence).Scan(&end)
		if errors.Is(err, sql.ErrNoRows) {
			// Missing retained boundaries make a duration unknowable. Only the
			// retained current visit can be closed by the observation time below.
			continue
		} else if err != nil {
			return nil, err
		}
		a, e1 := time.Parse(time.RFC3339Nano, e.OccurredAt)
		b, e2 := time.Parse(time.RFC3339Nano, end)
		if e1 == nil && e2 == nil {
			ms := b.Sub(a).Milliseconds()
			if ms < 0 {
				ms = 0
			}
			e.DurationMS = &ms
		}
	}
	var current WorkflowJournalEvent
	var exists int
	err = s.db.QueryRowContext(ctx, `SELECT current_visit_id,current_visit_sequence,current_visit_at,current_workflow_id,current_workflow_name,current_state_id,current_state_name, EXISTS(SELECT 1 FROM workflow_journal_events e WHERE e.run_id=r.id AND e.id=r.current_visit_id) FROM workflow_journal_runs r WHERE id=?`, run).Scan(&current.ID, &current.Sequence, &current.OccurredAt, &current.WorkflowID, &current.WorkflowName, &current.StateID, &current.StateName, &exists)
	if err != nil {
		return nil, err
	}
	if r.Status == "active" && current.ID != "" && current.StateID != "" && exists == 0 {
		current.Kind = "visit"
		a, e1 := time.Parse(time.RFC3339Nano, current.OccurredAt)
		b, e2 := time.Parse(time.RFC3339Nano, d.ObservedAt)
		if e1 == nil && e2 == nil {
			ms := b.Sub(a).Milliseconds()
			if ms < 0 {
				ms = 0
			}
			current.DurationMS = &ms
		}
		d.CurrentVisit = &current
	}
	// Stream only boundary events, not notes, and aggregate against the same
	// observation time and database snapshot as the timeline page.
	d.StateTotals = []WorkflowJournalStateTotal{}
	boundaries, err := s.db.QueryContext(ctx, `SELECT kind,occurred_at,workflow_id,workflow_name,state_id,state_name FROM workflow_journal_events WHERE run_id=? AND kind IN ('start','visit','admin_visit','end','state_lost','state_move') ORDER BY sequence`, run)
	if err != nil {
		return nil, err
	}
	type boundary struct{ kind, at, workflow, workflowName, state, stateName string }
	var previous *boundary
	indices := map[[2]string]int{}
	add := func(b *boundary, end string) {
		if b == nil || b.state == "" || (b.kind != "start" && b.kind != "visit" && b.kind != "admin_visit" && b.kind != "state_move") {
			return
		}
		a, e1 := time.Parse(time.RFC3339Nano, b.at)
		z, e2 := time.Parse(time.RFC3339Nano, end)
		if e1 != nil || e2 != nil {
			return
		}
		ms := z.Sub(a).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		key := [2]string{b.workflow, b.state}
		i, ok := indices[key]
		if !ok {
			i = len(d.StateTotals)
			indices[key] = i
			d.StateTotals = append(d.StateTotals, WorkflowJournalStateTotal{WorkflowID: b.workflow, WorkflowName: b.workflowName, StateID: b.state, StateName: b.stateName})
		}
		d.StateTotals[i].DurationMS += ms
	}
	for boundaries.Next() {
		var b boundary
		if err = boundaries.Scan(&b.kind, &b.at, &b.workflow, &b.workflowName, &b.state, &b.stateName); err != nil {
			boundaries.Close()
			return nil, err
		}
		if previous == nil || previous.kind == "start" || previous.kind == "visit" || previous.kind == "admin_visit" || previous.kind == "state_move" {
			// If rolling eviction removed the predecessor, the first retained
			// boundary is not a known duration endpoint for any visit.
			if previous != nil || r.CutoffSequence == 0 {
				add(previous, b.at)
			}
		}
		previous = &b
	}
	err = boundaries.Err()
	boundaries.Close()
	if err != nil {
		return nil, err
	}
	if r.Status == "active" {
		if previous != nil && (previous.kind == "start" || previous.kind == "visit" || previous.kind == "admin_visit" || previous.kind == "state_move") && (r.CutoffSequence == 0 || previous == nil) {
			add(previous, d.ObservedAt)
		}
	} else if r.TerminalAt != nil {
		if previous != nil && (r.CutoffSequence == 0 || previous.kind == "end") {
			add(previous, *r.TerminalAt)
		}
	}
	if d.CurrentVisit != nil {
		v := d.CurrentVisit
		add(&boundary{kind: "visit", at: v.OccurredAt, workflow: v.WorkflowID, workflowName: v.WorkflowName, state: v.StateID, stateName: v.StateName}, d.ObservedAt)
	}
	return d, nil
}
func (s *Store) DeleteWorkflowJournalRun(ctx context.Context, agent, run string) error {
	return s.runImmediate(ctx, func() error {
		var status string
		cutoff, err := s.journalReadExpiry(ctx)
		if err != nil {
			return err
		}
		err = s.db.QueryRowContext(ctx, `SELECT status FROM workflow_journal_runs WHERE agent_id=? AND id=? AND (status='active' OR terminal_at>?)`, agent, run, cutoff).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowJournalNotFound
		}
		if err != nil {
			return err
		}
		if status != "terminal" {
			return ErrWorkflowJournalActive
		}
		_, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_runs WHERE id=? AND agent_id=?`, run, agent)
		return err
	})
}

// Journal reads take a single SQLite snapshot for metadata, pages and durations.
func (s *Store) ListWorkflowJournalRuns(ctx context.Context, agent string, f WorkflowJournalFilter) (p WorkflowJournalPage, err error) {
	err = s.runImmediate(ctx, func() error { var e error; p, e = s.listWorkflowJournalRuns(ctx, agent, f); return e })
	return
}
func (s *Store) GetWorkflowJournalRun(ctx context.Context, agent, run string, f WorkflowJournalTimelineFilter) (d *WorkflowJournalDetail, err error) {
	if err = s.lockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("store is closed")
	}
	if _, err = s.db.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_, _ = s.db.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if d, err = s.getWorkflowJournalRun(ctx, agent, run, f); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, "COMMIT")
	if err != nil {
		return nil, err
	}
	return d, nil
}
func (s *Store) journalReadExpiry(ctx context.Context) (string, error) {
	now := s.clock.Now().UTC()
	var cutoff string
	err := s.db.QueryRowContext(ctx, `SELECT cutoff FROM workflow_journal_retention WHERE id=1`).Scan(&cutoff)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if cutoff != "" {
		t, e := time.Parse(time.RFC3339Nano, cutoff)
		if e != nil {
			return "", e
		}
		if t.After(now) {
			now = t
		}
	}
	return formatTimestamp(now.Add(-90 * 24 * time.Hour)), nil
}

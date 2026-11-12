package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const workflowJournalEventLimit = 10000

// journalAppendTx runs inside the binding mutation transaction.
func (s *Store) journalAppendTx(ctx context.Context, run, kind, state string) error {
	id, err := s.ids.Next()
	if err != nil {
		return err
	}
	var seq int64
	if err = s.db.QueryRowContext(ctx, `UPDATE workflow_journal_runs SET next_sequence=next_sequence+1 WHERE id=? RETURNING next_sequence-1`, run).Scan(&seq); err != nil {
		return err
	}
	var wfID, wfName, stateName string
	if state != "" {
		if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(st.workflow_id,''),COALESCE(w.name,''),st.name FROM workflow_states st LEFT JOIN workflows w ON w.id=st.workflow_id WHERE st.id=?`, state).Scan(&wfID, &wfName, &stateName); err != nil {
			return err
		}
	}
	now := formatTimestamp(s.clock.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_events(id,run_id,sequence,kind,occurred_at,workflow_id,workflow_name,state_id,state_name) VALUES(?,?,?,?,?,?,?,?,?)`, id, run, seq, kind, now, wfID, wfName, state, stateName)
	if err != nil {
		return err
	}
	if kind == "start" || kind == "visit" || kind == "admin_visit" {
		_, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET current_visit_id=?,current_visit_sequence=?,current_visit_at=?,current_workflow_id=?,current_workflow_name=?,current_state_id=?,current_state_name=? WHERE id=?`, id, seq, now, wfID, wfName, state, stateName, run)
		if err != nil {
			return err
		}
	}
	if seq > workflowJournalEventLimit {
		cutoff := seq - workflowJournalEventLimit
		if _, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_events WHERE run_id=? AND sequence<=?`, run, cutoff); err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET incomplete=1,cutoff_sequence=? WHERE id=?`, cutoff, run)
	}
	return err
}
func (s *Store) journalActiveTx(ctx context.Context, agent string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM workflow_journal_runs WHERE agent_id=? AND status='active'`, agent).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
func (s *Store) journalStartTx(ctx context.Context, agent, state string, synthetic bool) error {
	id, err := s.ids.Next()
	if err != nil {
		return err
	}
	var name, wfID, wfName, stateName string
	if state != "" {
		err = s.db.QueryRowContext(ctx, `SELECT a.name,COALESCE(st.workflow_id,''),COALESCE(w.name,''),st.name FROM managed_agents a JOIN workflow_states st ON st.id=? LEFT JOIN workflows w ON w.id=st.workflow_id WHERE a.agent_id=?`, state, agent).Scan(&name, &wfID, &wfName, &stateName)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT a.name,COALESCE(w.id,''),COALESCE(w.name,'') FROM managed_agents a LEFT JOIN workflows w ON w.id=a.active_workflow_id WHERE a.agent_id=?`, agent).Scan(&name, &wfID, &wfName)
	}
	if err != nil {
		return err
	}
	flag := 0
	if synthetic {
		flag = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_journal_runs(id,agent_id,agent_name,workflow_id,workflow_name,initial_state_id,initial_state_name,started_at,status,synthetic,incomplete,current_workflow_id,current_workflow_name) VALUES(?,?,?,?,?,?,?,?,'active',?,?,?,?)`, id, agent, name, wfID, wfName, state, stateName, formatTimestamp(s.clock.Now()), flag, flag, wfID, wfName)
	if err != nil {
		return err
	}
	if state != "" {
		return s.journalAppendTx(ctx, id, "start", state)
	}
	return nil
}
func (s *Store) journalEndTx(ctx context.Context, agent, outcome string) error {
	id, err := s.journalActiveTx(ctx, agent)
	if err != nil || id == "" {
		return err
	}
	if err = s.journalAppendTx(ctx, id, "end", ""); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET status='terminal',terminal_at=?,outcome=? WHERE id=?`, formatTimestamp(s.clock.Now()), outcome, id)
	if err != nil {
		return err
	}
	return s.journalSweepTx(ctx)
}
func (s *Store) journalVisitTx(ctx context.Context, agent, state, kind string) error {
	id, err := s.journalActiveTx(ctx, agent)
	if err != nil {
		return err
	}
	if id == "" {
		return s.journalStartTx(ctx, agent, state, true)
	}
	return s.journalAppendTx(ctx, id, kind, state)
}
func (s *Store) journalSweepTx(ctx context.Context) error {
	now := formatTimestamp(s.clock.Now())
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workflow_journal_retention(id,cutoff) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET cutoff=MAX(cutoff,excluded.cutoff)`, now); err != nil {
		return err
	}
	var cutoff string
	if err := s.db.QueryRowContext(ctx, `SELECT cutoff FROM workflow_journal_retention WHERE id=1`).Scan(&cutoff); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339Nano, cutoff)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_runs WHERE status='terminal' AND terminal_at<=?`, formatTimestamp(t.Add(-90*24*time.Hour))); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_runs WHERE id IN (SELECT id FROM workflow_journal_runs WHERE status='terminal' ORDER BY terminal_at DESC,id DESC LIMIT -1 OFFSET 100000)`); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM workflow_journal_receipts WHERE expires_at<=?`, cutoff)
	return err
}
func (s *Store) migrateWorkflowJournalTx(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,COALESCE(active_workflow_state_id,'') FROM managed_agents WHERE active_workflow_state_id IS NOT NULL OR active_workflow_id IS NOT NULL`)
	if err != nil {
		return err
	}
	type pair struct{ agent, state string }
	var all []pair
	for rows.Next() {
		var p pair
		if err = rows.Scan(&p.agent, &p.state); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		if err = s.journalStartTx(ctx, p.agent, p.state, true); err != nil {
			return err
		}
	}
	return s.journalSweepTx(ctx)
}

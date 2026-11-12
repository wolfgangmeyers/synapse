package store

import "context"

// adminBindingsTx captures binding columns before FK actions erase their values.
func (s *Store) adminBindingsTx(ctx context.Context) (map[string][2]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,COALESCE(active_workflow_id,''),COALESCE(active_workflow_state_id,'') FROM managed_agents WHERE active_workflow_id IS NOT NULL OR active_workflow_state_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][2]string{}
	for rows.Next() {
		var id, w, st string
		if err = rows.Scan(&id, &w, &st); err != nil {
			return nil, err
		}
		result[id] = [2]string{w, st}
	}
	return result, rows.Err()
}
func (s *Store) journalStateLostTx(ctx context.Context, agent string) error {
	run, err := s.journalActiveTx(ctx, agent)
	if err != nil || run == "" {
		return err
	}
	if err = s.journalAppendTx(ctx, run, "state_lost", ""); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET current_visit_id='',current_visit_sequence=0,current_visit_at='',current_workflow_id='',current_workflow_name='',current_state_id='',current_state_name='' WHERE id=?`, run)
	return err
}
func (s *Store) journalAdminBindingsChangedTx(ctx context.Context, before map[string][2]string) error {
	for agent, old := range before {
		var w, st string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(active_workflow_id,''),COALESCE(active_workflow_state_id,'') FROM managed_agents WHERE agent_id=?`, agent).Scan(&w, &st); err != nil {
			return err
		}
		if old == [2]string{w, st} {
			continue
		}
		if w == "" && st == "" {
			if err := s.journalEndTx(ctx, agent, "deactivated"); err != nil {
				return err
			}
		} else if old[1] != st {
			if err := s.journalStateLostTx(ctx, agent); err != nil {
				return err
			}
		} else {
			run, err := s.journalActiveTx(ctx, agent)
			if err != nil {
				return err
			}
			if run != "" {
				if err = s.journalAppendTx(ctx, run, "binding_changed", ""); err != nil {
					return err
				}
				// A workflow-only FK clear leaves the current state visit intact.
				// Its identity comes from the state definition, not the cleared binding.
				if old[0] != "" && w == "" && st != "" {
					var stateWorkflow, stateName string
					err = s.db.QueryRowContext(ctx, `SELECT COALESCE(workflow_id,''),name FROM workflow_states WHERE id=?`, st).Scan(&stateWorkflow, &stateName)
					if err != nil {
						return err
					}
					workflowName := ""
					if stateWorkflow != "" {
						if err = s.db.QueryRowContext(ctx, `SELECT name FROM workflows WHERE id=?`, stateWorkflow).Scan(&workflowName); err != nil {
							return err
						}
					}
					if _, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET workflow_id=?,workflow_name=?,state_id=?,state_name=? WHERE run_id=? AND sequence=(SELECT current_visit_sequence FROM workflow_journal_runs WHERE id=?)`, stateWorkflow, workflowName, st, stateName, run, run); err != nil {
						return err
					}
					if _, err = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET current_workflow_id=?,current_workflow_name=?,current_state_id=?,current_state_name=? WHERE id=?`, stateWorkflow, workflowName, st, stateName, run); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (s *Store) journalSnapshotWorkflowOnlyTx(ctx context.Context, agent string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET workflow_id=COALESCE((SELECT active_workflow_id FROM managed_agents WHERE agent_id=?),''),workflow_name=COALESCE((SELECT w.name FROM managed_agents a JOIN workflows w ON w.id=a.active_workflow_id WHERE a.agent_id=?),'') WHERE agent_id=? AND status='active'`, agent, agent, agent)
	return err
}
func (s *Store) journalStateMovedToTx(ctx context.Context, state, workflowID, stateName string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM workflow_journal_runs WHERE status='active' AND current_state_id=?`, state)
	if err != nil {
		return err
	}
	var agents []string
	for rows.Next() {
		var agent string
		if err = rows.Scan(&agent); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, agent)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	workflowName := ""
	if workflowID != "" {
		if err = s.db.QueryRowContext(ctx, `SELECT name FROM workflows WHERE id=?`, workflowID).Scan(&workflowName); err != nil {
			return err
		}
	}
	for _, agent := range agents {
		run, e := s.journalActiveTx(ctx, agent)
		if e != nil {
			return e
		}
		if run == "" {
			continue
		}
		if e = s.journalAppendTx(ctx, run, "admin_visit", state); e != nil {
			return e
		}
		_, e = s.db.ExecContext(ctx, `UPDATE workflow_journal_events SET workflow_id=?,workflow_name=?,state_name=? WHERE run_id=? AND sequence=(SELECT current_visit_sequence FROM workflow_journal_runs WHERE id=?)`, workflowID, workflowName, stateName, run, run)
		if e != nil {
			return e
		}
		_, e = s.db.ExecContext(ctx, `UPDATE workflow_journal_runs SET current_workflow_id=?,current_workflow_name=?,current_state_id=?,current_state_name=? WHERE id=?`, workflowID, workflowName, state, stateName, run)
		if e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) journalStateMovedTx(ctx context.Context, state string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM workflow_journal_runs WHERE status='active' AND current_state_id=?`, state)
	if err != nil {
		return err
	}
	var agents []string
	for rows.Next() {
		var agent string
		if err = rows.Scan(&agent); err != nil {
			rows.Close()
			return err
		}
		agents = append(agents, agent)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, agent := range agents {
		run, e := s.journalActiveTx(ctx, agent)
		if e != nil {
			return e
		}
		if run == "" {
			continue
		}
		if e = s.journalAppendTx(ctx, run, "admin_visit", state); e != nil {
			return e
		}
	}
	return nil
}

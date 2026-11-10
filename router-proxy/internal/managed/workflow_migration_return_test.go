package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// IR-001: invoke the production managed tool against an upgraded, populated
// pre-129 binding. Historical transitions must not authorize a return.
func TestWorkflowMigrationRejectsHistoricalReturn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	migrations := store.DefaultMigrations()
	cutoff := -1
	for i, m := range migrations {
		if m.ID == "129_workflow_allow_implicit_return" {
			cutoff = i
			break
		}
	}
	require.Greater(t, cutoff, 0)
	legacy, err := store.Open(ctx, path, store.Options{Migrations: migrations[:cutoff]})
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	statements := []string{
		`INSERT INTO workflows(id,name,created_at,updated_at) VALUES('wf','Legacy','2024-01-01','2024-01-01')`,
		`INSERT INTO workflow_states(id,name,workflow_id,created_at,updated_at) VALUES('a','A','wf','2024-01-01','2024-01-01'),('b','B','wf','2024-01-01','2024-01-01')`,
		`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id) VALUES('legacy','legacy','/tmp','google','fixture','terminated','terminated','idle','embedded','session',1,'generation',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','2024-01-01','2024-01-01','wf','b')`,
		`INSERT INTO managed_agent_workflow_reminders(agent_id,workflow_state_id,reminder_id,created_at) VALUES('legacy','b','keep','2024-01-01')`,
		`INSERT INTO managed_agent_monitors(id,agent_id,workflow_state_id,name,next_eval_at,question_instructions,nudge_template,created_at,updated_at) VALUES('monitor','legacy','b','keep','2025-01-01','check','nudge','2024-01-01','2024-01-01')`,
		`INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json,tool_input_json) VALUES('legacy','managed_message','info','Workflow state transitioned','2024-01-01','old-transition','{"kind":"state","outcome":"success","from":"A","to":"B"}','{"action":"transition","target_state":"B"}')`,
	}
	for _, statement := range statements {
		_, err = raw.ExecContext(ctx, statement)
		require.NoError(t, err)
	}
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	before, err := db.WorkflowBindingVersion(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, "b", before.StateID)
	require.Empty(t, before.PredecessorID)
	require.Zero(t, before.Generation)
	state, err := db.GetWorkflowState(ctx, "b")
	require.NoError(t, err)
	require.False(t, state.AllowImplicitReturn)
	remindersBefore, err := db.Reminders(ctx)
	require.NoError(t, err)
	var historyBefore int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id='legacy'`).Scan(&historyBefore))
	require.Equal(t, 1, historyBefore)
	tool := (&Manager{store: db}).embeddedWorkflowTransitionTool(embeddedOwner{binding: store.EmbeddedBinding{AgentID: "legacy"}}, "mecha_workflow_transition")
	_, err = tool.Execute(ctx, "attempt", map[string]any{"action": "transition", "target_state": "A"}, nil)
	require.ErrorContains(t, err, "no outgoing transitions")
	after, err := db.WorkflowBindingVersion(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, before, after)
	remindersAfter, err := db.Reminders(ctx)
	require.NoError(t, err)
	require.Equal(t, remindersBefore, remindersAfter)
	var historyAfter int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id='legacy'`).Scan(&historyAfter))
	require.Equal(t, historyBefore, historyAfter)
	var reminderState, monitorState string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id FROM managed_agent_workflow_reminders WHERE agent_id='legacy' AND reminder_id='keep'`).Scan(&reminderState))
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id FROM managed_agent_monitors WHERE agent_id='legacy' AND id='monitor'`).Scan(&monitorState))
	require.Equal(t, "b", reminderState)
	require.Equal(t, "b", monitorState)
	// Establish a trusted predecessor through a real explicit transition, without
	// changing either migrated flag. A has no explicit edge back to B.
	_, err = raw.ExecContext(ctx, `UPDATE workflow_states SET transitions_json='[{"target_state_id":"a","description":"back"}]' WHERE id='b'`)
	require.NoError(t, err)
	_, err = tool.Execute(ctx, "forward", map[string]any{"action": "transition", "target_state": "A"}, nil)
	require.NoError(t, err)
	before, err = db.WorkflowBindingVersion(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, "a", before.StateID)
	require.Equal(t, "b", before.PredecessorID)
	state, err = db.GetWorkflowState(ctx, "a")
	require.NoError(t, err)
	require.False(t, state.AllowImplicitReturn)
	require.Empty(t, state.Transitions)

	// The successful transition cleans up the old effects; seed real records
	// in the new state so a rejected return cannot pass on empty collections.
	_, err = raw.ExecContext(ctx, `INSERT INTO managed_agent_workflow_reminders(agent_id,workflow_state_id,reminder_id,created_at) VALUES('legacy','a','new-reminder','2024-01-01')`)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO managed_agent_monitors(id,agent_id,workflow_state_id,name,next_eval_at,question_instructions,nudge_template,created_at,updated_at) VALUES('new-monitor','legacy','a','watch','2025-01-01','check again','nudge again','2024-01-01','2024-01-01')`)
	require.NoError(t, err)
	var reminderBefore, monitorBefore, historyContentBefore string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id || ':' || reminder_id || ':' || created_at FROM managed_agent_workflow_reminders WHERE agent_id='legacy' AND reminder_id='new-reminder'`).Scan(&reminderBefore))
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id || ':' || name || ':' || question_instructions || ':' || nudge_template FROM managed_agent_monitors WHERE agent_id='legacy' AND id='new-monitor'`).Scan(&monitorBefore))
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT group_concat(event_key || ':' || message || ':' || presentation_json || ':' || tool_input_json, '|') FROM (SELECT * FROM managed_complete_history WHERE agent_id='legacy' ORDER BY event_key)`).Scan(&historyContentBefore))
	require.Contains(t, historyContentBefore, "old-transition")
	require.Contains(t, historyContentBefore, "workflow-transition:legacy:b:a:forward")

	_, err = tool.Execute(ctx, "disabled-return", map[string]any{"action": "transition", "target_state": "B"}, nil)
	require.ErrorContains(t, err, "no outgoing transitions")
	after, err = db.WorkflowBindingVersion(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, before, after)
	var reminderAfter, monitorAfter, historyContentAfter string
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id || ':' || reminder_id || ':' || created_at FROM managed_agent_workflow_reminders WHERE agent_id='legacy' AND reminder_id='new-reminder'`).Scan(&reminderAfter))
	require.Equal(t, reminderBefore, reminderAfter)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT workflow_state_id || ':' || name || ':' || question_instructions || ':' || nudge_template FROM managed_agent_monitors WHERE agent_id='legacy' AND id='new-monitor'`).Scan(&monitorAfter))
	require.Equal(t, monitorBefore, monitorAfter)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT group_concat(event_key || ':' || message || ':' || presentation_json || ':' || tool_input_json, '|') FROM (SELECT * FROM managed_complete_history WHERE agent_id='legacy' ORDER BY event_key)`).Scan(&historyContentAfter))
	require.Equal(t, historyContentBefore, historyContentAfter)

}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// WJ-07: a real 131→132 upgrade snapshots the current binding once, not its history.
func TestWorkflowJournalMigration132ActiveBindingRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "legacy.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	migrations := DefaultMigrations()
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "132_workflow_journal")], Clock: clock})
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO workflows (id,name,description,created_at,updated_at) VALUES ('wf','Legacy workflow','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
 INSERT INTO workflow_states (id,workflow_id,name,description,entry_text,created_at,updated_at) VALUES ('state','wf','Legacy state','','','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
 INSERT INTO managed_agents (agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id,workflow_variables_json) VALUES ('legacy','Legacy agent',?,'p','m','running','running','idle',1,1,'hash','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','wf','state','{"project":"old"}')`, root)
	require.NoError(t, err)
	schedule, err := legacy.CreateWorkflowSchedule(ctx, WorkflowScheduleRequest{WorkflowID: "wf", Name: "Legacy schedule", TargetCwd: root, Schedule: StructuredSchedule{DaysMode: "every_day", TimeMode: "repeating_interval", Interval: &IntervalSpec{Value: 1, Unit: "hours"}, Timezone: "UTC"}})
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	var firstRun WorkflowJournalRun
	for attempt := 0; attempt < 3; attempt++ {
		upgraded, err := Open(ctx, path, Options{Clock: clock})
		require.NoError(t, err)
		preserved, err := upgraded.GetWorkflowSchedule(ctx, schedule.ID)
		require.NoError(t, err)
		require.Equal(t, schedule, preserved)
		var workflow, state, vars string
		require.NoError(t, upgraded.db.QueryRowContext(ctx, `SELECT active_workflow_id,active_workflow_state_id,workflow_variables_json FROM managed_agents WHERE agent_id='legacy'`).Scan(&workflow, &state, &vars))
		require.Equal(t, "wf", workflow)
		require.Equal(t, "state", state)
		require.JSONEq(t, `{"project":"old"}`, vars)
		page, err := upgraded.ListWorkflowJournalRuns(ctx, "legacy", WorkflowJournalFilter{})
		require.NoError(t, err)
		require.Len(t, page.Runs, 1)
		run := page.Runs[0]
		require.Equal(t, "active", run.Status)
		require.True(t, run.Synthetic)
		require.True(t, run.Incomplete)
		require.Equal(t, "Legacy agent", run.AgentName)
		require.Equal(t, "wf", run.WorkflowID)
		require.Equal(t, "Legacy workflow", run.WorkflowName)
		require.Equal(t, "state", run.InitialStateID)
		require.Equal(t, "Legacy state", run.InitialStateName)
		require.Equal(t, "2026-11-02T10:00:00.000Z", run.StartedAt)
		detail, err := upgraded.GetWorkflowJournalRun(ctx, "legacy", run.ID, WorkflowJournalTimelineFilter{})
		require.NoError(t, err)
		require.Len(t, detail.Events, 1)
		require.Equal(t, "start", detail.Events[0].Kind)
		require.Equal(t, "state", detail.Events[0].StateID)
		require.Equal(t, run.StartedAt, detail.Events[0].OccurredAt)
		if attempt == 0 {
			firstRun = run
		} else {
			require.Equal(t, firstRun, run)
		}
		require.NoError(t, upgraded.Close())
		require.NoError(t, clock.Advance(time.Minute))
	}
}

func TestWorkflowJournalMigration132NameCollision(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"legacy", "legacy2", "compatible", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "router.db")
			migrations := DefaultMigrations()
			before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "132_workflow_journal")]})
			require.NoError(t, err)
			var create string
			switch kind {
			case "legacy":
				create = `CREATE TABLE workflow_journal_events (run_id TEXT NOT NULL, sequence INTEGER NOT NULL, type TEXT NOT NULL, state_id TEXT, entry_id TEXT, exit TEXT, note TEXT, at TEXT NOT NULL); INSERT INTO workflow_journal_events(run_id,sequence,type,note,at) VALUES('old',1,'run_launched','saved','2026-01-01');`
			case "legacy2":
				create = `CREATE TABLE workflow_journal_events (run_id TEXT NOT NULL, sequence INTEGER NOT NULL, semantic_key TEXT, event_type TEXT, occurred_at TEXT NOT NULL, state_id TEXT, entry_id TEXT, exit_name TEXT, note TEXT, safe_outcome_json TEXT); INSERT INTO workflow_journal_events(run_id,sequence,semantic_key,event_type,occurred_at,note) VALUES('old2',1,'sem_key','run_launched','2026-01-01','saved2');`
			case "compatible":
				create = `CREATE TABLE workflow_journal_events (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,sequence INTEGER NOT NULL,kind TEXT NOT NULL,occurred_at TEXT NOT NULL,workflow_id TEXT NOT NULL DEFAULT '',workflow_name TEXT NOT NULL DEFAULT '',state_id TEXT NOT NULL DEFAULT '',state_name TEXT NOT NULL DEFAULT '',reason TEXT NOT NULL DEFAULT '',source TEXT NOT NULL DEFAULT '',text TEXT NOT NULL DEFAULT '',target_event_id TEXT NOT NULL DEFAULT '',UNIQUE(run_id,sequence));`
			case "unknown":
				create = `CREATE TABLE workflow_journal_events (mystery TEXT); INSERT INTO workflow_journal_events VALUES('saved');`
			}
			_, err = before.db.ExecContext(ctx, create)
			require.NoError(t, err)
			require.NoError(t, before.Close())
			after, err := Open(ctx, path, Options{})
			if kind == "unknown" {
				require.ErrorContains(t, err, "unknown schema")
				// Failed migration leaves both the original table and ledger unchanged.
				raw, e := sql.Open("sqlite", path)
				require.NoError(t, e)
				defer raw.Close()
				var saved string
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT mystery FROM workflow_journal_events`).Scan(&saved))
				require.Equal(t, "saved", saved)
				var recorded int
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='132_workflow_journal'`).Scan(&recorded))
				require.Zero(t, recorded)
				return
			}
			require.NoError(t, err)
			defer after.Close()
			var recorded int
			require.NoError(t, after.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='132_workflow_journal'`).Scan(&recorded))
			require.Equal(t, 1, recorded)
			if kind == "legacy" {
				var note string
				require.NoError(t, after.db.QueryRowContext(ctx, `SELECT note FROM workflow_journal_events_pre132 WHERE run_id='old'`).Scan(&note))
				require.Equal(t, "saved", note)
			}
			if kind == "legacy2" {
				var note string
				require.NoError(t, after.db.QueryRowContext(ctx, `SELECT note FROM workflow_journal_events_pre132 WHERE run_id='old2'`).Scan(&note))
				require.Equal(t, "saved2", note)
			}
			var column string
			require.NoError(t, after.db.QueryRowContext(ctx, `SELECT name FROM pragma_table_info('workflow_journal_events') WHERE name='kind'`).Scan(&column))
		})
	}
}

// Existing databases may have applied 132 with the legacy receipt deletion trigger.
func TestWorkflowJournalReceiptDeleteMigration133(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "receipts.db")
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "132_workflow_journal")
	legacy, err := Open(ctx, path, Options{Migrations: migrations[:idx+1]})
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `INSERT INTO managed_agents (agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at) VALUES ('receipt-agent','Agent','/tmp','p','m','running','running','idle',1,1,'hash','now','now'); INSERT INTO workflow_journal_receipts(agent_id,kind,operation_id,fingerprint,result,expires_at,bytes) VALUES ('receipt-agent','test','op','fp','{}','2999-01-01T00:00:00Z',2);`)
	require.NoError(t, err)
	var triggerCount int
	require.NoError(t, legacy.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='workflow_journal_receipts_agent_delete'`).Scan(&triggerCount))
	require.Zero(t, triggerCount, "migration 132 must not create the legacy receipt deletion trigger")
	_, err = legacy.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS workflow_journal_receipts_agent_delete`)
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `CREATE TRIGGER workflow_journal_receipts_agent_delete AFTER DELETE ON managed_agents BEGIN DELETE FROM workflow_journal_receipts WHERE agent_id=OLD.agent_id; END;`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	upgraded, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer upgraded.Close()
	_, err = upgraded.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id='receipt-agent'`)
	require.NoError(t, err)
	var count int
	require.NoError(t, upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_journal_receipts WHERE agent_id='receipt-agent'`).Scan(&count))
	require.Equal(t, 1, count)
	var upgradedTriggerCount int
	require.NoError(t, upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='workflow_journal_receipts_agent_delete'`).Scan(&upgradedTriggerCount))
	require.Zero(t, upgradedTriggerCount)
}

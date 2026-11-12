package transport

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Upgrade a real pre-132 database, then fire its existing schedule over HTTP.
func TestMigration132PreservesScheduleAndActiveBinding(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	migrations := store.DefaultMigrations()
	index := -1
	for i, m := range migrations {
		if m.ID == "132_workflow_journal" {
			index = i
			break
		}
	}
	require.Greater(t, index, 0)
	legacy, err := store.Open(ctx, path, store.Options{Migrations: migrations[:index], Clock: clock})
	require.NoError(t, err)
	state, err := legacy.CreateWorkflowState(ctx, store.WorkflowStateRequest{Name: "entry", EntryText: "Do scheduled work"})
	require.NoError(t, err)
	wf, err := legacy.CreateWorkflow(ctx, store.WorkflowRequest{Name: "scheduled", EntryStateID: &state.ID})
	require.NoError(t, err)
	schedule, err := legacy.CreateWorkflowSchedule(ctx, store.WorkflowScheduleRequest{WorkflowID: wf.ID, Name: "hourly", TargetCwd: root, ModelProvider: "google", ModelID: "gemini-2.5-flash", Schedule: store.StructuredSchedule{DaysMode: "every_day", TimeMode: "repeating_interval", Interval: &store.IntervalSpec{Value: 1, Unit: "hours"}, Timezone: "UTC"}})
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, `INSERT INTO managed_agents (agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id,workflow_variables_json) VALUES ('legacy','legacy',?,'google','gemini-2.5-flash','running','running','idle',1,1,'hash','2026-11-02T10:00:00Z','2026-11-02T10:00:00Z',?,?, '{"project":"legacy"}')`, root, wf.ID, state.ID)
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	durable, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	preserved, err := durable.GetWorkflowSchedule(ctx, schedule.ID)
	require.NoError(t, err)
	require.Equal(t, schedule, preserved)
	raw, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	defer raw.Close()
	var workflow, activeState, variables, status string
	var synthetic, incomplete, runs int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT active_workflow_id,active_workflow_state_id,workflow_variables_json FROM managed_agents WHERE agent_id='legacy'`).Scan(&workflow, &activeState, &variables))
	require.Equal(t, wf.ID, workflow)
	require.Equal(t, state.ID, activeState)
	require.JSONEq(t, `{"project":"legacy"}`, variables)
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT status,synthetic,incomplete FROM workflow_journal_runs WHERE agent_id='legacy'`).Scan(&status, &synthetic, &incomplete))
	require.Equal(t, "active", status)
	require.Equal(t, 1, synthetic)
	require.Equal(t, 1, incomplete)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	sessionID := ""
	process := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1234, StartMarker: "start", Incarnation: "inc"}, stopped: make(chan struct{})}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, Clock: clock, ProcessFactory: managedTestProcessFactory{process: process, session: &sessionID}, RPCFactory: managedTestRPCFactory{rpc: managedTestRPC{session: &sessionID, active: expectedManagedActiveToolsForTransportTest(manifest)}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "google", ID: "gemini-2.5-flash", Available: true}), ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager, Clock: clock, EnableTestClock: true})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	defer router.Close(ctx)
	_, e := durable.ManagedAgents(ctx)
	t.Logf("managed agents error: %v", e)
	res, err := server.Client().Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewBufferString(`{"delta_ms":3900000}`))
	require.NoError(t, err)
	res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	// The minimal legacy agent lacks managed launch records required by the general sweep.
	// Invoke the real scheduler sweep after advancing the HTTP test clock.
	router.sweepWorkflowSchedules(ctx)
	fired, err := durable.GetWorkflowSchedule(ctx, schedule.ID)
	require.NoError(t, err)
	require.NotNil(t, fired.LastRunAt)
	require.NotNil(t, fired.LastRunStatus)
	require.Equal(t, "success", *fired.LastRunStatus)
	require.NotEqual(t, schedule.NextRunAt, fired.NextRunAt)
	var background int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE is_background=1 AND active_workflow_id=?`, wf.ID).Scan(&background))
	require.Equal(t, 1, background, "scheduled workflow must launch a background agent")
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT count(*) FROM workflow_journal_runs WHERE agent_id='legacy'`).Scan(&runs))
	require.Equal(t, 1, runs)
}

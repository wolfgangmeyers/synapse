package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

func TestWorkflowSchedulesSweepBehavioral(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	startTime := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(startTime)

	durable, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	names := expectedManagedActiveToolsForTransportTest(manifest)
	process := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1234, StartMarker: "start", Incarnation: "inc"}, stopped: make(chan struct{})}
	sessionID := ""
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:                      durable,
		Config:                     managed.Config{AllowedWorkingDirectoryRoots: []string{root}},
		Manifest:                   manifest,
		Clock:                      clock,
		ProcessFactory:             managedTestProcessFactory{process: process, session: &sessionID},
		RPCFactory:                 managedTestRPCFactory{rpc: managedTestRPC{session: &sessionID, active: names}},
		CatalogSource:              managed.CatalogFromModels(managed.Model{Provider: "google", ID: "gemini-2.5-flash", Available: true}),
		ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)

	router, err := New(Options{
		Store:           durable,
		Managed:         manager,
		Clock:           clock,
		EnableTestClock: true,
	})
	require.NoError(t, err)

	server := httptest.NewServer(router)
	defer func() {
		server.Close()
		_ = router.Close(ctx)
		_ = durable.Close()
	}()

	client := server.Client()

	// 1. Verify that a router with test clock disabled returns 404 for /v1/test/clock/advance
	noTestClockRouter, err := New(Options{Store: durable, EnableTestClock: false})
	require.NoError(t, err)
	noTestClockServer := httptest.NewServer(noTestClockRouter)
	defer func() {
		noTestClockServer.Close()
		_ = noTestClockRouter.Close(ctx)
	}()

	advancePayload, _ := json.Marshal(map[string]any{"delta_ms": 1000})
	res, err := client.Post(noTestClockServer.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advancePayload))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, res.StatusCode)
	res.Body.Close()

	// 2. Verify invalid requests to /v1/test/clock/advance (non-positive delta) -> 400
	badAdvance, _ := json.Marshal(map[string]any{"delta_ms": 0})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(badAdvance))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	negativeAdvance, _ := json.Marshal(map[string]any{"delta_ms": -500})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(negativeAdvance))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	res.Body.Close()

	// 3. Create entry workflow state and workflow
	state, err := durable.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:        "entry-state",
		Description: "Entry state for scheduled execution",
		EntryText:   "Execute task for {{PROJECT}}",
	})
	require.NoError(t, err)

	wfVars := []store.WorkflowVariableDefinition{
		{Name: "PROJECT", Required: true},
	}
	wf, err := durable.CreateWorkflow(ctx, store.WorkflowRequest{
		Name:         "Scheduled Project Task",
		EntryStateID: &state.ID,
		Variables:    &wfVars,
	})
	require.NoError(t, err)

	// 4. Create structured schedule: repeating interval every 1 hour, starting from startTime
	// NextRunAt will be startTime + 1 hour = 2026-11-02T11:00:00Z
	schedRec, err := durable.CreateWorkflowSchedule(ctx, store.WorkflowScheduleRequest{
		WorkflowID:    wf.ID,
		Name:          "Hourly Task",
		TargetCwd:     root,
		ModelProvider: "google",
		ModelID:       "gemini-2.5-flash",
		Variables:     map[string]string{"PROJECT": "Alpha"},
		Schedule: store.StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "repeating_interval",
			Interval: &store.IntervalSpec{
				Value: 1,
				Unit:  "hours",
			},
			Timezone: "UTC",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "2026-11-02T11:00:00.000Z", schedRec.NextRunAt)
	require.Nil(t, schedRec.LastRunAt)

	// 5. Advancing 30 minutes (to 10:30) does NOT reach 11:00, so schedule does not fire
	advance30Min, _ := json.Marshal(map[string]any{"delta_ms": 30 * 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance30Min))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	var advRes map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&advRes))
	res.Body.Close()
	require.Equal(t, "2026-11-02T10:30:00Z", advRes["now"])

	// Check schedule not fired yet
	unfired, err := durable.GetWorkflowSchedule(ctx, schedRec.ID)
	require.NoError(t, err)
	require.Nil(t, unfired.LastRunAt)
	require.Equal(t, "2026-11-02T11:00:00.000Z", unfired.NextRunAt)

	// 6. Advance another 35 minutes (to 11:05), crossing the 11:00 due threshold
	// The advance handler triggers sweep synchronously!
	advance35Min, _ := json.Marshal(map[string]any{"delta_ms": 35 * 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance35Min))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify schedule was executed and advanced!
	fired, err := durable.GetWorkflowSchedule(ctx, schedRec.ID)
	require.NoError(t, err)
	require.NotNil(t, fired.LastRunAt, "schedule should have recorded last_run_at")
	require.NotNil(t, fired.LastRunStatus)
	require.Equal(t, "success", *fired.LastRunStatus)
	require.Nil(t, fired.LastRunError)
	// Next run should now be advanced by 1 hour from 11:05 -> 12:05
	require.Equal(t, "2026-11-02T12:05:00.000Z", fired.NextRunAt)

	// 7. Verify background agent was launched in the store
	agents, err := durable.ManagedAgents(ctx)
	require.NoError(t, err)
	var bgAgent *store.ManagedAgentRecord
	for _, a := range agents {
		if a.IsBackground && a.ActiveWorkflowID == wf.ID {
			bgAgent = a
			break
		}
	}
	require.NotNil(t, bgAgent, "expected launched background workflow agent")
	require.Contains(t, bgAgent.AgentID, fmt.Sprintf("bg-wf-%s", wf.ID))

	// 8. Verify failing schedule behavior (e.g., missing required workflow variable at runtime)
	// Add a new required variable to the workflow definition that the schedule does not provide
	updatedVars := []store.WorkflowVariableDefinition{
		{Name: "PROJECT", Required: true},
		{Name: "EXTRA_REQUIRED", Required: true},
	}
	_, err = durable.UpdateWorkflow(ctx, wf.ID, store.WorkflowRequest{
		Name:         wf.Name,
		EntryStateID: &state.ID,
		Variables:    &updatedVars,
	})
	require.NoError(t, err)

	// Advance clock by 1 hour (to 12:05) so the schedule is due again
	advance1Hr, _ := json.Marshal(map[string]any{"delta_ms": 60 * 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance1Hr))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify that the schedule ran, recorded "failed" with last_run_error, and still advanced next_run_at!
	failedRun, err := durable.GetWorkflowSchedule(ctx, schedRec.ID)
	require.NoError(t, err)
	require.NotNil(t, failedRun.LastRunStatus)
	require.Equal(t, "failed", *failedRun.LastRunStatus)
	require.NotNil(t, failedRun.LastRunError)
	require.Contains(t, *failedRun.LastRunError, "EXTRA_REQUIRED")
	// Next run should have advanced another hour to 13:05
	require.Equal(t, "2026-11-02T13:05:00.000Z", failedRun.NextRunAt)

	// 9. Non-UTC timezone schedule evaluation during sweep (e.g. "America/New_York")
	// At current test time (2026-11-02 12:05:00 UTC), EST is UTC-5, so local time is 07:05:00.
	// A schedule set for 09:00 America/New_York corresponds to 14:00:00 UTC.
	nySchedule, err := durable.CreateWorkflowSchedule(ctx, store.WorkflowScheduleRequest{
		WorkflowID:    wf.ID,
		Name:          "NY Morning Task",
		TargetCwd:     root,
		ModelProvider: "google",
		ModelID:       "gemini-2.5-flash",
		Variables:     map[string]string{"PROJECT": "Empire", "EXTRA_REQUIRED": "yes"},
		Schedule: store.StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "specific_times",
			Times:    []string{"09:00"},
			Timezone: "America/New_York",
		},
	})
	require.NoError(t, err)
	// 09:00 EST on Nov 2, 2026 is 14:00 UTC
	require.Equal(t, "2026-11-02T14:00:00.000Z", nySchedule.NextRunAt)
	require.Nil(t, nySchedule.LastRunAt)

	// Advance clock by 110 minutes (from 12:05 to 13:55 UTC) -> NY schedule not yet due (14:00)
	advance110Min, _ := json.Marshal(map[string]any{"delta_ms": 110 * 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance110Min))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	unfiredNY, err := durable.GetWorkflowSchedule(ctx, nySchedule.ID)
	require.NoError(t, err)
	require.Nil(t, unfiredNY.LastRunAt)
	require.Equal(t, "2026-11-02T14:00:00.000Z", unfiredNY.NextRunAt)

	// Advance clock by 10 minutes (from 13:55 to 14:05 UTC) -> crosses 14:00 UTC (09:00 EST)
	advance10Min, _ := json.Marshal(map[string]any{"delta_ms": 10 * 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance10Min))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	firedNY, err := durable.GetWorkflowSchedule(ctx, nySchedule.ID)
	require.NoError(t, err)
	require.NotNil(t, firedNY.LastRunAt)
	require.Equal(t, "success", *firedNY.LastRunStatus)
	// Next run for 09:00 America/New_York advances to the following day at 09:00 EST = 2026-11-03 14:00:00 UTC
	require.Equal(t, "2026-11-03T14:00:00.000Z", firedNY.NextRunAt)

	// 10. Fallback when NextRunAt fails on a corrupted schedule record during sweep
	// Create a valid schedule, then directly corrupt its schedule_json in SQLite so NextRunAt fails
	corruptSched, err := durable.CreateWorkflowSchedule(ctx, store.WorkflowScheduleRequest{
		WorkflowID:    wf.ID,
		Name:          "Corrupt Schedule Task",
		TargetCwd:     root,
		ModelProvider: "google",
		ModelID:       "gemini-2.5-flash",
		Variables:     map[string]string{"PROJECT": "Corrupt", "EXTRA_REQUIRED": "present"},
		Schedule: store.StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "repeating_interval",
			Interval: &store.IntervalSpec{Value: 1, Unit: "hours"},
			Timezone: "UTC",
		},
	})
	require.NoError(t, err)

	// Corrupt schedule_json with an invalid timezone and invalid mode so NextRunAt fails
	rawDB, err := sql.Open("sqlite", filepath.Join(root, "router.db"))
	require.NoError(t, err)
	defer rawDB.Close()
	corruptScheduleJSON := `{"days_mode":"invalid_days","time_mode":"invalid_time","timezone":"NonExistent/Zone_Nowhere"}`
	// Also set next_run_at to past so it is immediately due on sweep
	duePast := "2026-11-02T10:00:00.000Z"
	_, err = rawDB.ExecContext(ctx, `
		UPDATE workflow_schedules
		SET schedule_json = ?, next_run_at = ?
		WHERE id = ?
	`, corruptScheduleJSON, duePast, corruptSched.ID)
	require.NoError(t, err)

	// Advance clock by 1 minute to trigger sweep
	advance1Min, _ := json.Marshal(map[string]any{"delta_ms": 60 * 1000})
	res, err = client.Post(server.URL+"/v1/test/clock/advance", "application/json", bytes.NewReader(advance1Min))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.StatusCode)
	res.Body.Close()

	// Verify fallback: NextRunAt calculation failure resulted in fallback advancement (now + 1 hour)
	sweptCorrupt, err := durable.GetWorkflowSchedule(ctx, corruptSched.ID)
	require.NoError(t, err)
	require.NotNil(t, sweptCorrupt.LastRunAt)
	// Even though background launch succeeded, NextRunAt failed, which logs the error in last_run_error
	require.NotNil(t, sweptCorrupt.LastRunError)
	require.NotEmpty(t, *sweptCorrupt.LastRunError)
	// Current time is 14:06:00 UTC (14:05 + 1 min), fallback next_run_at is now + 1h = 15:06:00 UTC
	require.Equal(t, "2026-11-02T15:06:00.000Z", sweptCorrupt.NextRunAt)
}

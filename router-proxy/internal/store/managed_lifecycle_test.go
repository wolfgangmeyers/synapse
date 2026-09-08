package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is the first lifecycle-control contract test. Detailed transition and
// recovery tests belong beside the store APIs as they are introduced.
func TestMigration027CreatesLifecycleControlLedger(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()

	for _, table := range []string{
		"managed_session_artifacts",
		"managed_lifecycle_actions",
		"managed_lifecycle_batch_items",
		"managed_control_requests",
	} {
		var count int
		require.NoError(t, durable.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&count))
		require.Equalf(t, 1, count, "missing 027 table %s", table)
	}

	var launchLink, terminalCode, terminalMessage int
	require.NoError(t, durable.db.QueryRowContext(ctx,
		`SELECT
			(SELECT COUNT(*) FROM pragma_table_info('managed_agents') WHERE name='launch_operation_id'),
			(SELECT COUNT(*) FROM pragma_table_info('managed_http_operations') WHERE name='terminal_code'),
			(SELECT COUNT(*) FROM pragma_table_info('managed_http_operations') WHERE name='terminal_message')`,
	).Scan(&launchLink, &terminalCode, &terminalMessage))
	require.Equal(t, 1, launchLink)
	require.Equal(t, 1, terminalCode)
	require.Equal(t, 1, terminalMessage)
}

func TestMigration027BackfillsLegacyRestartAndRetainsForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:len(migrations)-1]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "legacy", Name: "legacy", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 42, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, launch.OperationID))
	stored, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	_, _, err = before.ReserveManagedRestart(ctx, restartReservation(*stored, launch))
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	action, err := after.ManagedLifecycleAction(ctx, "restart-operation")
	require.NoError(t, err)
	require.Equal(t, "legacy025", action.ExecutionOwner)
	require.Equal(t, "admitted", action.State)
	require.Equal(t, "admission", action.Stage)
	require.NotNil(t, action.SourceFence)
	var foreignKeys, foreignProblems int
	require.NoError(t, after.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, 1, foreignKeys)
	require.Zero(t, foreignProblems)
}

func TestReserveManagedLifecycleResetIsReplaySafe(t *testing.T) {
	durable, ctx, agent, _ := restartFixture(t)
	request := ManagedLifecycleReservation{
		AgentID:                        agent.AgentID,
		Kind:                           "reset",
		Method:                         "POST",
		CanonicalPath:                  "/v1/managed-agents/restartable/reset",
		RequestID:                      "123e4567-e89b-42d3-a456-426614174000",
		CanonicalBodyHash:              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Fence:                          restartFenceFromAgent(&agent),
		PlannedTargetRuntimeGeneration: "123e4567-e89b-42d3-a456-426614174001",
		Operation: ManagedOperation{
			OperationID: "123e4567-e89b-42d3-a456-426614174000",
			Method:      "POST", CanonicalPath: "/v1/managed-agents/restartable/reset",
			RequestID:         "123e4567-e89b-42d3-a456-426614174000",
			CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			State:             "pending", ResponseStatus: 202,
			ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"},
			ResponseBody:    []byte(`{"operation_id":"123e4567-e89b-42d3-a456-426614174000"}`),
		},
	}
	admission, err := durable.ReserveManagedLifecycle(ctx, request)
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, "reset", admission.Action.Kind)
	require.Equal(t, "admitted", admission.Action.State)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "resetting", stored.LifecycleState)
	require.False(t, stored.AcceptingPrompts)

	replay, err := durable.ReserveManagedLifecycle(ctx, request)
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	changed := request
	changed.CanonicalBodyHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err = durable.ReserveManagedLifecycle(ctx, changed)
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
}

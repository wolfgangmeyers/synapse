package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration058AddsConstrainedUnconfirmedOrderlyStopMarker(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration058 := migrationIndex(t, migrations, "058_managed_orderly_stop_confirmation")
	before, err := Open(ctx, path, Options{Migrations: migrations[:migration058]})
	require.NoError(t, err)
	fence := ManagedRuntimeFence{AgentID: "migration-orderly", PiSessionID: "migration-session", SessionEpoch: 1, ProcessPID: 811, ProcessStartMarker: "migration-start", ProcessIncarnation: "migration-incarnation", RuntimeGeneration: "migration-generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, before, fence.AgentID, "migration-orderly-launch", fence)
	_, err = before.db.ExecContext(ctx, `INSERT INTO managed_orderly_stop_markers(agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,marked_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, fence.AgentID, fence.PiSessionID, fence.SessionEpoch, fence.ProcessPID, fence.ProcessStartMarker, fence.ProcessIncarnation, fence.RuntimeGeneration, fence.ManifestRevision, fence.ManifestHash, "2026-09-13T12:00:00.000Z")
	require.NoError(t, err)
	var confirmedColumns int
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('managed_orderly_stop_markers') WHERE name='confirmed'`).Scan(&confirmedColumns))
	require.Zero(t, confirmedColumns)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	marker, err := after.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.False(t, marker.Confirmed)
	var tableSchema string
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_orderly_stop_markers'`).Scan(&tableSchema))
	require.Contains(t, tableSchema, `confirmed INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0,1))`)
	_, err = after.db.ExecContext(ctx, `UPDATE managed_orderly_stop_markers SET confirmed=2 WHERE agent_id=?`, fence.AgentID)
	require.Error(t, err)
}

func TestMigration057WidensRestartRetryConstraintForExistingDatabases(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration057 := migrationIndex(t, migrations, "057_managed_restart_retry_constraint")

	before, err := Open(ctx, path, Options{Migrations: migrations[:migration057]})
	require.NoError(t, err)

	agent := ManagedAgentRecord{
		AgentID: "migration-restart", Name: "migration-restart", SlotHeld: true, Cwd: "/tmp",
		SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:  "running", LifecycleState: "starting", ActivityState: "starting",
		PiSessionID: "migration-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	launch := ManagedOperation{
		OperationID: "migration-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: "migration-launch", CanonicalBodyHash: testManifestHash, State: "pending",
		ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
	}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 811, StartMarker: "migration-start", Incarnation: "migration-incarnation"}, "migration-generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, launch.OperationID))
	ready, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	source := restartFenceFromAgent(ready)

	artifact := ManagedSessionArtifact{
		ArtifactID: lifecycleUUID(700), AgentID: agent.AgentID, SessionEpoch: 2,
		PiSessionID: "replacement-session", RootRelativePath: "sessions/replacement.json",
		PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained",
		DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z",
	}
	require.NoError(t, before.RecordManagedSessionArtifact(ctx, artifact))

	predecessor := lifecycleActionInput(lifecycleUUID(701), agent.AgentID, "restart", "direct", source)
	predecessor.TargetArtifactID = artifact.ArtifactID
	predecessor.State, predecessor.Stage = "failed", "target_start"
	predecessor.TerminalCode, predecessor.TerminalMessage, predecessor.Retryable = "RESTART_TARGET_REAP_FAILED", "replacement process cleanup did not complete", true
	predecessor.Operation.State = "terminal"
	predecessor.Operation.TerminalAt = "2026-09-08T00:00:00.000Z"
	predecessor.Operation.ExpiresAt = "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, predecessor))
	_, err = before.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',activity_state='stopped',accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)

	var predecessorArtifact string
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT target_artifact_id FROM managed_lifecycle_actions WHERE action_id=?`, predecessor.ActionID).Scan(&predecessorArtifact))
	require.Equal(t, artifact.ArtifactID, predecessorArtifact)
	var actionIndexCount int
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='managed_lifecycle_actions' AND sql IS NOT NULL`).Scan(&actionIndexCount))
	var batchSchema, diagnosticSchema string
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_batch_items'`).Scan(&batchSchema))
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_termination_diagnostics'`).Scan(&diagnosticSchema))
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()

	var applied int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='057_managed_restart_retry_constraint'`).Scan(&applied))
	require.Equal(t, 1, applied)
	var tableSchema string
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_actions'`).Scan(&tableSchema))
	require.Contains(t, tableSchema, `CHECK (retry_of IS NULL OR kind IN ('restart','reset','terminate'))`)
	require.NotContains(t, tableSchema, `CHECK (retry_of IS NULL OR kind IN ('reset','terminate'))`)
	var afterActionIndexCount int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='managed_lifecycle_actions' AND sql IS NOT NULL`).Scan(&afterActionIndexCount))
	require.Equal(t, actionIndexCount, afterActionIndexCount)
	var afterBatchSchema, afterDiagnosticSchema string
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_lifecycle_batch_items'`).Scan(&afterBatchSchema))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_termination_diagnostics'`).Scan(&afterDiagnosticSchema))
	require.Equal(t, batchSchema, afterBatchSchema)
	require.Equal(t, diagnosticSchema, afterDiagnosticSchema)

	var preservedArtifact string
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT target_artifact_id FROM managed_lifecycle_actions WHERE action_id=?`, predecessor.ActionID).Scan(&preservedArtifact))
	require.Equal(t, artifact.ArtifactID, preservedArtifact)
	var foreignProblems, foreignKeys int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.NoError(t, after.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys))
	require.Zero(t, foreignProblems)
	require.Equal(t, 1, foreignKeys)

	retryOperationID := lifecycleUUID(702)
	admission, err := after.ReserveManagedLifecycleRestart(ctx, ManagedLifecycleRestartReservation{
		AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/migration-restart/restart",
		RequestID: retryOperationID, CanonicalBodyHash: testManifestHash, Source: "direct",
		RetryOf: predecessor.ActionID, CurrentOperationID: launch.OperationID, SourceFence: source,
		PlannedTargetRuntimeGeneration: lifecycleUUID(703), TargetArtifactID: artifact.ArtifactID,
		Operation: ManagedOperation{OperationID: retryOperationID, Method: "POST", CanonicalPath: "/v1/managed-agents/migration-restart/restart", RequestID: retryOperationID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)},
	})
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, predecessor.ActionID, admission.Action.RetryOf)
	var retryRows int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE retry_of=? AND kind='restart'`, predecessor.ActionID).Scan(&retryRows))
	require.Equal(t, 1, retryRows)
}

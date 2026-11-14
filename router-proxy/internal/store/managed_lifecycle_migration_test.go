package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateManagedForeignKeyDelta057RejectsNewUnrelatedViolation(t *testing.T) {
	baseline := []managedForeignKeyViolation{{table: "managed_orderly_stop_markers", parent: "managed_agents", rowID: 11, hasRowID: true, foreignKeyID: 0}}
	after := append(append([]managedForeignKeyViolation(nil), baseline...), managedForeignKeyViolation{table: "messages", parent: "agents", rowID: 12, hasRowID: true, foreignKeyID: 0})
	require.ErrorContains(t, validateManagedForeignKeyDelta057(baseline, after), "introduced foreign-key violation")
	removed := after[:1]
	require.NoError(t, validateManagedForeignKeyDelta057(baseline, removed))
	require.ErrorContains(t, validateManagedForeignKeyDelta057(baseline, nil), "removed a pre-existing unrelated")
}

func TestValidateManagedForeignKeyDelta057RejectsUnresolvedLifecycleViolations(t *testing.T) {
	for _, table := range []string{"managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_session_artifacts", "managed_termination_diagnostics"} {
		t.Run(table, func(t *testing.T) {
			violation := managedForeignKeyViolation{table: table, parent: "managed_lifecycle_parent", rowID: 21, hasRowID: true, foreignKeyID: 0}
			require.ErrorContains(t, validateManagedForeignKeyDelta057(nil, []managedForeignKeyViolation{violation}), "left lifecycle foreign-key violation")
		})
	}
}

func TestMigration057PreflightRepairsDebtBeforeLifecycleRebuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration057 := migrationIndex(t, migrations, "057_managed_restart_retry_constraint")
	before, err := Open(ctx, path, Options{Migrations: migrations[:migration057]})
	require.NoError(t, err)

	agent := ManagedAgentRecord{AgentID: "migration-preflight", Name: "migration-preflight", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "preflight-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "preflight-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "preflight-launch", CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 813, StartMarker: "preflight-start", Incarnation: "preflight-incarnation"}, "preflight-generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, launch.OperationID))
	ready, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	source := restartFenceFromAgent(ready)

	missingOperation := lifecycleActionInput(lifecycleUUID(720), agent.AgentID, "restart", "direct", source)
	missingOperation.State, missingOperation.Stage = "failed", "target_start"
	missingOperation.TerminalCode, missingOperation.TerminalMessage, missingOperation.Retryable = "RESTART_TARGET_START_FAILED", "pre-057 operation parent was lost", true
	missingOperation.Operation.State, missingOperation.Operation.TerminalAt, missingOperation.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingOperation))
	artifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(721), AgentID: agent.AgentID, SessionEpoch: 2, PiSessionID: "preflight-target", RootRelativePath: "sessions/preflight.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, before.RecordManagedSessionArtifact(ctx, artifact))
	missingArtifact := lifecycleActionInput(lifecycleUUID(722), agent.AgentID, "reset", "direct", source)
	missingArtifact.TargetArtifactID = artifact.ArtifactID
	missingArtifact.State, missingArtifact.Stage = "failed", "target_start"
	missingArtifact.TerminalCode, missingArtifact.TerminalMessage, missingArtifact.Retryable = "RESET_TARGET_START_FAILED", "pre-057 artifact parent was lost", true
	missingArtifact.Operation.State, missingArtifact.Operation.TerminalAt, missingArtifact.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingArtifact))
	missingDiagnosticParent := lifecycleActionInput(lifecycleUUID(723), agent.AgentID, "terminate", "direct", source)
	missingDiagnosticParent.SourceFence = nil
	missingDiagnosticParent.State, missingDiagnosticParent.Stage = "failed", "complete"
	missingDiagnosticParent.TerminalCode, missingDiagnosticParent.TerminalMessage, missingDiagnosticParent.Retryable = "TERMINATION_CLEANUP_FAILED", "pre-057 diagnostic parent was lost", true
	missingDiagnosticParent.Operation.State, missingDiagnosticParent.Operation.TerminalAt, missingDiagnosticParent.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingDiagnosticParent))
	require.NoError(t, before.RecordManagedTerminationDiagnostic(ctx, missingDiagnosticParent.ActionID, "ABORT_TIMEOUT"))

	validArtifact := ManagedSessionArtifact{ArtifactID: lifecycleUUID(724), AgentID: agent.AgentID, SessionEpoch: 3, PiSessionID: "valid-target", RootRelativePath: "sessions/valid.json", PathSHA256: strings.Repeat("1", 64), DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, before.RecordManagedSessionArtifact(ctx, validArtifact))
	valid := lifecycleActionInput(lifecycleUUID(727), agent.AgentID, "restart", "direct", source)
	valid.TargetArtifactID = validArtifact.ArtifactID
	valid.State, valid.Stage = "failed", "target_start"
	valid.TerminalCode, valid.TerminalMessage, valid.Retryable = "RESTART_TARGET_START_FAILED", "valid retry predecessor", true
	valid.Operation.State, valid.Operation.TerminalAt, valid.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, valid))

	_, err = before.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, missingOperation.Operation.OperationID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_session_artifacts WHERE artifact_id=?`, artifact.ArtifactID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_lifecycle_actions WHERE action_id=?`, missingDiagnosticParent.ActionID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, missingDiagnosticParent.Operation.OperationID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `INSERT INTO managed_orderly_stop_markers(agent_id,pi_session_id,session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,manifest_revision,manifest_hash,marked_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, "unrelated-missing-agent", "unrelated-session", 1, 814, "unrelated-start", "unrelated-incarnation", "unrelated-generation", 1, testManifestHash, "2026-09-08T00:00:00.000Z")
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	var beforeForeignProblems int
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&beforeForeignProblems))
	require.Equal(t, 4, beforeForeignProblems)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	var foreignProblems int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, 1, foreignProblems)
	var unrelatedMarker int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_orderly_stop_markers WHERE agent_id=?`, "unrelated-missing-agent").Scan(&unrelatedMarker))
	require.Equal(t, 1, unrelatedMarker)
	var integrity string
	require.NoError(t, after.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity))
	require.Equal(t, "ok", integrity)
	var migration057Recorded, migration059Recorded int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='057_managed_restart_retry_constraint'`).Scan(&migration057Recorded))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='059_managed_lifecycle_orphan_repair'`).Scan(&migration059Recorded))
	require.Equal(t, 1, migration057Recorded)
	require.Equal(t, 1, migration059Recorded)
	var artifactLink, diagnosticParent, diagnosticCount int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=? AND target_artifact_id IS NULL`, missingArtifact.ActionID).Scan(&artifactLink))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, missingDiagnosticParent.ActionID).Scan(&diagnosticParent))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_termination_diagnostics WHERE action_id=?`, missingDiagnosticParent.ActionID).Scan(&diagnosticCount))
	require.Equal(t, 1, artifactLink)
	require.Equal(t, 1, diagnosticParent)
	require.Equal(t, 1, diagnosticCount)

	_, err = after.db.ExecContext(ctx, `UPDATE managed_agents SET lifecycle_state='restarting',activity_state='stopped',accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	retryID := lifecycleUUID(728)
	admission, err := after.ReserveManagedLifecycleRestart(ctx, ManagedLifecycleRestartReservation{
		AgentID: agent.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/migration-preflight/restart", RequestID: retryID, CanonicalBodyHash: testManifestHash, Source: "direct", RetryOf: valid.ActionID, CurrentOperationID: launch.OperationID, SourceFence: source,
		PlannedTargetRuntimeGeneration: lifecycleUUID(729), TargetArtifactID: validArtifact.ArtifactID, Operation: ManagedOperation{OperationID: retryID, Method: "POST", CanonicalPath: "/v1/managed-agents/migration-preflight/restart", RequestID: retryID, CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)},
	})
	require.NoError(t, err)
	require.False(t, admission.Replayed)
	require.Equal(t, valid.ActionID, admission.Action.RetryOf)
}

func TestMigration059RepairsPreExistingLifecycleForeignKeyOrphansAfter057(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	migration059 := migrationIndex(t, migrations, "059_managed_lifecycle_orphan_repair")
	before, err := Open(ctx, path, Options{Migrations: migrations[:migration059]})
	require.NoError(t, err)
	var migration057Recorded int
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='057_managed_restart_retry_constraint'`).Scan(&migration057Recorded))
	require.Equal(t, 1, migration057Recorded)

	agent := ManagedAgentRecord{AgentID: "migration-orphans", Name: "migration-orphans", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "migration-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "migration-orphans-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-orphans-launch", CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 812, StartMarker: "migration-start", Incarnation: "migration-incarnation"}, "migration-generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, launch.OperationID))
	ready, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	source := restartFenceFromAgent(ready)

	missingOperation := lifecycleActionInput(lifecycleUUID(710), agent.AgentID, "restart", "direct", source)
	missingOperation.State, missingOperation.Stage = "failed", "target_start"
	missingOperation.TerminalCode, missingOperation.TerminalMessage, missingOperation.Retryable = "RESTART_TARGET_START_FAILED", "old operation was lost", true
	missingOperation.Operation.State, missingOperation.Operation.TerminalAt, missingOperation.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingOperation))
	missingArtifactRecord := ManagedSessionArtifact{ArtifactID: lifecycleUUID(711), AgentID: agent.AgentID, SessionEpoch: 2, PiSessionID: "replacement", RootRelativePath: "sessions/replacement.json", PathSHA256: testManifestHash, DiscoveryState: "not_materialized", CleanupState: "retained", DiscoveredAt: "2026-09-08T00:00:00.000Z", CleanupUpdatedAt: "2026-09-08T00:00:00.000Z"}
	require.NoError(t, before.RecordManagedSessionArtifact(ctx, missingArtifactRecord))
	missingArtifact := lifecycleActionInput(lifecycleUUID(712), agent.AgentID, "reset", "direct", source)
	missingArtifact.TargetArtifactID = missingArtifactRecord.ArtifactID
	missingArtifact.State, missingArtifact.Stage = "failed", "target_start"
	missingArtifact.TerminalCode, missingArtifact.TerminalMessage, missingArtifact.Retryable = "RESET_TARGET_START_FAILED", "session artifact was lost", true
	missingArtifact.Operation.State, missingArtifact.Operation.TerminalAt, missingArtifact.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingArtifact))
	missingDiagnosticParent := lifecycleActionInput(lifecycleUUID(713), agent.AgentID, "terminate", "direct", source)
	missingDiagnosticParent.SourceFence = nil
	missingDiagnosticParent.State, missingDiagnosticParent.Stage = "failed", "complete"
	missingDiagnosticParent.TerminalCode, missingDiagnosticParent.TerminalMessage, missingDiagnosticParent.Retryable = "TERMINATION_CLEANUP_FAILED", "termination diagnostic parent was lost", true
	missingDiagnosticParent.Operation.State, missingDiagnosticParent.Operation.TerminalAt, missingDiagnosticParent.Operation.ExpiresAt = "terminal", "2026-09-08T00:00:00.000Z", "2026-09-09T00:00:00.000Z"
	require.NoError(t, before.CreateManagedLifecycleAction(ctx, missingDiagnosticParent))
	require.NoError(t, before.RecordManagedTerminationDiagnostic(ctx, missingDiagnosticParent.ActionID, "ABORT_TIMEOUT"))

	var actionCount, operationCount, artifactCount, agentCount int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM managed_lifecycle_actions`: &actionCount,
		`SELECT COUNT(*) FROM managed_http_operations`:   &operationCount,
		`SELECT COUNT(*) FROM managed_session_artifacts`: &artifactCount,
		`SELECT COUNT(*) FROM managed_agents`:            &agentCount,
	} {
		require.NoError(t, before.db.QueryRowContext(ctx, query).Scan(destination))
	}
	_, err = before.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, missingOperation.Operation.OperationID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_session_artifacts WHERE artifact_id=?`, missingArtifactRecord.ArtifactID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_lifecycle_actions WHERE action_id=?`, missingDiagnosticParent.ActionID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `DELETE FROM managed_http_operations WHERE operation_id=?`, missingDiagnosticParent.Operation.OperationID)
	require.NoError(t, err)
	_, err = before.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)
	var preflightForeignProblems int
	require.NoError(t, before.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&preflightForeignProblems))
	require.Equal(t, 3, preflightForeignProblems)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	var migration059Recorded int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='059_managed_lifecycle_orphan_repair'`).Scan(&migration059Recorded))
	require.Equal(t, 1, migration059Recorded)
	var afterActionCount, afterOperationCount, afterArtifactCount, afterAgentCount, foreignProblems int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions`).Scan(&afterActionCount))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_http_operations`).Scan(&afterOperationCount))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_session_artifacts`).Scan(&afterArtifactCount))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents`).Scan(&afterAgentCount))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignProblems))
	require.Equal(t, actionCount, afterActionCount)
	require.Equal(t, operationCount, afterOperationCount)
	require.Equal(t, artifactCount-1, afterArtifactCount)
	require.Equal(t, agentCount, afterAgentCount)
	require.Zero(t, foreignProblems)
	var integrity string
	require.NoError(t, after.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity))
	require.Equal(t, "ok", integrity)

	var repairedOperation, repairedArtifact string
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT operation_id FROM managed_lifecycle_actions WHERE action_id=?`, missingOperation.ActionID).Scan(&repairedOperation))
	require.Equal(t, missingOperation.Operation.OperationID, repairedOperation)
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COALESCE(target_artifact_id,'') FROM managed_lifecycle_actions WHERE action_id=?`, missingArtifact.ActionID).Scan(&repairedArtifact))
	require.Empty(t, repairedArtifact)
	var diagnosticParent, diagnosticCount int
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=?`, missingDiagnosticParent.ActionID).Scan(&diagnosticParent))
	require.NoError(t, after.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_termination_diagnostics WHERE action_id=?`, missingDiagnosticParent.ActionID).Scan(&diagnosticCount))
	require.Equal(t, 1, diagnosticParent)
	require.Equal(t, 1, diagnosticCount)
}

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

func TestMigration028ExistingDatabaseUpgradeAndRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "028_managed_restart_all_batch_references")]})
	require.NoError(t, err)
	parent, child := lifecycleUUID(980), lifecycleUUID(981)
	seedLegacyRestartAllBatch(t, before, parent, "2999-01-01T00:00:00.000Z", []string{"0", "missing-agent", "rejected", "NOT_READY"})
	require.NoError(t, insertManagedOperation(ctx, before.db, lifecycleOperation(child, "2999-01-01T00:00:00.000Z")))
	_, err = before.db.Exec(`INSERT INTO managed_lifecycle_actions (action_id,operation_id,agent_id,kind,source,parent_action_id,execution_owner,state,stage,planned_target_runtime_generation,terminal_code,terminal_message,retryable,completed_at,created_at,updated_at) VALUES (?,?,?,'restart','restart_all_child',?,'lifecycle','succeeded','complete','generation','RESTARTED','restarted',0,'2026-09-08','2026-09-08','2026-09-08')`, child, child, "legacy-agent", parent)
	require.NoError(t, err)
	_, err = before.db.Exec(`INSERT INTO managed_lifecycle_batch_items (parent_action_id,snapshot_position,agent_id,status,child_action_id,child_operation_id,created_at) VALUES (?,1,'legacy-agent','accepted',?,?,'2026-09-08')`, parent, child, child)
	require.NoError(t, err)
	rows := captureNamedTableRows(t, before.db, []string{"managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_http_operations"})
	var schema string
	require.NoError(t, before.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='managed_lifecycle_batch_items'`).Scan(&schema))
	before.managedRestartAllFault = func(stage string) error {
		if stage == "migration_028_before_record" {
			return fmt.Errorf("injected 028 failure")
		}
		return nil
	}
	require.ErrorContains(t, before.applyManagedRestartAllBatchReferencesMigration028(ctx, migrationByID(t, migrations, "028_managed_restart_all_batch_references")), "injected 028 failure")
	require.Equal(t, rows, captureNamedTableRows(t, before.db, []string{"managed_lifecycle_actions", "managed_lifecycle_batch_items", "managed_http_operations"}))
	var restored string
	var count int
	require.NoError(t, before.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='managed_lifecycle_batch_items'`).Scan(&restored))
	require.Equal(t, schema, restored)
	require.NoError(t, before.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='028_managed_restart_all_batch_references'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, before.Close())
	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	upgraded := captureNamedTableRows(t, after.db, []string{"managed_lifecycle_batch_items", "managed_http_operations"})
	for _, table := range []string{"managed_lifecycle_batch_items", "managed_http_operations"} {
		require.Equal(t, rows[table].Rows, upgraded[table].Rows)
	}
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM managed_lifecycle_actions WHERE action_id=? AND source='restart_all_child' AND parent_action_id=?`, child, parent).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='028_managed_restart_all_batch_references'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count))
	require.Zero(t, count)
}

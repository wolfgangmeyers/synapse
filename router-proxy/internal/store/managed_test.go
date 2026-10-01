package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testManifestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestReconcileManagedRecipientSourcesLeavesRetiredManagedRowsUntouched(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, func() error {
		_, err := durable.db.Exec(`UPDATE managed_agents SET name='system-1-research' WHERE agent_id=?`, fence.AgentID)
		return err
	}())
	require.NoError(t, func() error {
		_, err := durable.db.Exec(`UPDATE recipient_sources SET recipient_name='system-1-research',owner_identity='retired:system-1-research',state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name=?`, fence.AgentID)
		return err
	}())

	require.NoError(t, durable.ReconcileManagedRecipientSources(ctx))
	source, err := durable.RecipientSource(ctx, "system-1-research")
	require.NoError(t, err)
	require.Equal(t, "managed", source.SourceKind)
	require.Equal(t, "retired:system-1-research", source.OwnerIdentity)
	require.Equal(t, "retired", source.State)
	require.Empty(t, source.ConflictKind)
	require.Empty(t, source.ConflictOwner)
}

func TestManagedRecipientConflictResolution(t *testing.T) {
	ctx := context.Background()
	durable, fence := readyManagedDirectFixture(t)
	enqueue := func(id, body string) (EnqueueResult, error) {
		envelope := testEnvelope(id, body)
		envelope.To = fence.AgentID
		return durable.Enqueue(ctx, envelope)
	}
	first, err := enqueue("conflict-first", "first")
	require.NoError(t, err)
	firstTarget, err := durable.ResolveMessageTarget(ctx, first.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "managed", firstTarget.SourceKind)

	// Startup reconciliation keeps both concrete contenders. No new snapshot is
	// selected while the recipient is conflicted, so lookup order cannot fall
	// back to tmux or managed delivery.
	_, err = durable.db.Exec(`UPDATE recipient_sources SET state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-source',updated_at=? WHERE recipient_name=?`, formatTimestamp(time.Now()), fence.AgentID)
	require.NoError(t, err)
	blocked, err := enqueue("conflict-blocked", "blocked")
	require.NoError(t, err)
	blockedTarget, err := durable.ResolveMessageTarget(ctx, blocked.Message.ID)
	require.NoError(t, err)
	require.Nil(t, blockedTarget)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.Nil(t, attempt, "a conflicted source must not fall back to managed delivery")

	// Retiring the secondary resumes the primary without rewriting its snapshot.
	require.NoError(t, durable.RetireTmuxRecipientSource(ctx, fence.AgentID, "tmux-source"))
	source, err := durable.RecipientSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "managed", source.SourceKind)
	require.Equal(t, "active", source.State)
	blockedTarget, err = durable.ResolveMessageTarget(ctx, blocked.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "managed", blockedTarget.SourceKind)

	// A second conflict makes the managed owner the retired primary. Its old
	// snapshots become terminal and the recorded tmux survivor is promoted.
	_, err = durable.db.Exec(`UPDATE recipient_sources SET state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-source',updated_at=? WHERE recipient_name=?`, formatTimestamp(time.Now()), fence.AgentID)
	require.NoError(t, err)
	survivor, err := enqueue("conflict-survivor", "survivor")
	require.NoError(t, err)
	require.NoError(t, durable.RetireManagedRecipientSource(ctx, fence.AgentID, fence.AgentID))
	source, err = durable.RecipientSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "tmux", source.SourceKind)
	require.Equal(t, "tmux-source", source.OwnerIdentity)
	require.Equal(t, "active", source.State)
	for _, id := range []string{first.Message.ID, blocked.Message.ID} {
		message, err := durable.MessageFor(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "dead_letter", message.State, "retired snapshots are terminalized")
	}
	survivorTarget, err := durable.ResolveMessageTarget(ctx, survivor.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "tmux", survivorTarget.SourceKind)
	require.Equal(t, "tmux-source", survivorTarget.OwnerIdentity)
	attempt, err = durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.Nil(t, attempt, "the promoted tmux survivor must not fall back to managed direct mail")
}

func TestManagedReadyReconcilesReservedQueuedMessages(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	envelope := testEnvelope("reserved-startup", "queued before ready")
	envelope.To = fence.AgentID
	_, err := durable.Enqueue(ctx, envelope)
	require.NoError(t, err)
	target, err := durable.ResolveMessageTarget(ctx, "reserved-startup")
	require.NoError(t, err)
	require.Nil(t, target, "a reserved managed source must not select a transport")

	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	target, err = durable.MessageTargetSnapshot(ctx, "reserved-startup")
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)
	require.Equal(t, fence.AgentID, target.OwnerIdentity)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.Equal(t, "reserved-startup", attempt.MessageID)
}

func TestDefaultModelSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.db")
	ctx := context.Background()
	first, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	require.NoError(t, first.SetDefaultModel(ctx, &ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}))
	require.NoError(t, first.Close())
	second, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer second.Close()
	model, err := second.GetDefaultModel(ctx)
	require.NoError(t, err)
	require.Equal(t, &ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, model)
}

func TestFailedManagedRowsRemainDurableWhileTheirSlotIsReleased(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "failed-agent", Name: "reserved-name", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "failed-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "failed-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.MarkManagedFailed(ctx, agent.AgentID, "READINESS_FAILED", "failed startup"))
	result, err := durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedAgents)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.LifecycleState)
	require.False(t, stored.SlotHeld)
	conflict := agent
	conflict.AgentID = "replacement"
	conflictOp := op
	conflictOp.OperationID, conflictOp.RequestID = "replacement-operation", "replacement-request"
	var nameConflict *ManagedNameConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, conflict, conflictOp), &nameConflict)
}

func TestRedactedManagedDiagnosticRedactsLocalPathsAcrossDiagnosticEncodings(t *testing.T) {
	for _, test := range []struct {
		name, detail, forbidden string
	}{
		{"quoted Unix path", `exit status 17: cwd="/Users/wolfgang/project/output.log" token=secret-value permission denied`, "/Users/wolfgang/project/output.log"},
		{"HTML-escaped quote path", `exit status 17: cwd=&quot;/Users/wolfgang/project/output.log&quot; token=secret-value permission denied`, "/Users/wolfgang/project/output.log"},
		{"quoted path with spaces", `exit status 17: cwd="/Users/wolfgang/project/output log" token=secret-value permission denied`, "/Users/wolfgang/project/output log"},
		{"file URL", `exit status 17: file:///Users/wolfgang/project/output.log token=secret-value permission denied`, "file:///Users/wolfgang/project/output.log"},
		{"Windows-like path", `exit status 17: cwd="C:\Users\wolfgang\project\output log" token=secret-value permission denied`, `C:\Users\wolfgang\project\output log`},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostic := RedactedManagedDiagnostic(test.detail)
			require.NotContains(t, diagnostic, test.forbidden)
			require.NotContains(t, diagnostic, "secret-value")
			require.Contains(t, diagnostic, "exit status 17")
			require.Contains(t, diagnostic, "permission denied")
			require.Contains(t, diagnostic, "[REDACTED]")
			require.LessOrEqual(t, len(diagnostic), 512)
		})
	}
}

func TestRecordManagedPreReadinessProcessExitUsesExactFenceAndKeepsLaunchReceipt(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	launch, err := durable.ManagedOperation(ctx, "source-launch")
	require.NoError(t, err)
	immutableReceipt := append([]byte(nil), launch.ResponseBody...)

	stale := fence
	stale.ProcessIncarnation = "replaced"
	require.ErrorAs(t, durable.RecordManagedPreReadinessProcessExit(ctx, stale, launch.OperationID, "managed Pi exited with code 17: token=do-not-retain /private/router"), new(*ManagedRestartStaleError))
	afterStale, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, afterStale)

	unsafeDiagnostic := `managed Pi exited with code 17: cwd="/Users/wolfgang/project/output.log" cwd=&quot;/Users/wolfgang/project/output.log&quot; cwd="/Users/wolfgang/project/output log" file:///Users/wolfgang/project/output.log cwd="C:\Users\wolfgang\project\output log" token=do-not-retain permission denied`
	require.NoError(t, durable.RecordManagedPreReadinessProcessExit(ctx, fence, launch.OperationID, unsafeDiagnostic))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", agent.LifecycleState)
	require.Equal(t, "failed", agent.ActivityState)
	require.False(t, agent.AcceptingPrompts)
	require.False(t, agent.SlotHeld)
	require.Equal(t, "PROCESS_EXITED", agent.LastErrorCode)
	require.Contains(t, agent.LastErrorMessage, "code 17")
	for _, forbidden := range []string{"do-not-retain", "/Users/wolfgang/project/output.log", "/Users/wolfgang/project/output log", "file:///Users/wolfgang/project/output.log", `C:\Users\wolfgang\project\output log`} {
		require.NotContains(t, agent.LastErrorMessage, forbidden)
	}
	require.Contains(t, agent.LastErrorMessage, "permission denied")
	require.LessOrEqual(t, len(agent.LastErrorMessage), 512)
	operation, err := durable.ManagedOperation(ctx, launch.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", operation.State)
	require.Equal(t, immutableReceipt, operation.ResponseBody)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
}

func TestMarkManagedFailedForFenceRetainsIdentityAndRejectsStaleFence(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.MarkManagedFailedForFence(ctx, fence, "PROCESS_STOP_FAILED", "stop failed"))
	stored, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, fence.ProcessPID, stored.ProcessPID)
	require.Equal(t, "PROCESS_STOP_FAILED", stored.LastErrorCode)
	stale := fence
	stale.ProcessIncarnation = "replacement"
	require.ErrorAs(t, durable.MarkManagedFailedForFence(ctx, stale, "PROCESS_STOP_FAILED", "stale"), new(*ManagedRestartStaleError))
	require.NoError(t, durable.PrepareManagedFailedProcessStopRecovery(ctx, fence))
	stored, err = durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.Equal(t, "stopped", stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.True(t, stored.SlotHeld)
}

func TestMarkManagedFailedForFenceAndOperationIsAtomic(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.Error(t, durable.MarkManagedFailedForFenceAndOperation(ctx, fence, "missing-operation", "PROCESS_STOP_FAILED", "stop failed"))
	stored, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", stored.LifecycleState)
	require.Equal(t, "starting", stored.ActivityState)
	require.True(t, stored.SlotHeld)
}

func TestMarkManagedFailedForFenceAndOperationAcceptsPendingAndTerminalOwners(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "terminal"}[terminal], func(t *testing.T) {
			durable, fence := readinessSourceFixture(t)
			if terminal {
				require.NoError(t, durable.MarkManagedOperationTerminal(ctx, "source-launch"))
			}
			before, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.NoError(t, durable.MarkManagedFailedForFenceAndOperation(ctx, fence, "source-launch", "PROCESS_STOP_FAILED", "stop failed"))
			after, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, before+1, after)
			agent, err := durable.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "failed", agent.LifecycleState)
			op, err := durable.ManagedOperation(ctx, "source-launch")
			require.NoError(t, err)
			require.Equal(t, "terminal", op.State)
		})
	}
}

func TestMarkManagedFailedForFenceAndOperationRejectsWrongOwnerAtomically(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	other := ManagedAgentRecord{AgentID: "other-owner", Name: "other-owner", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "other-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	otherOperation := ManagedOperation{OperationID: "wrong-owner", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "wrong-owner", CanonicalBodyHash: "other-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, other, otherOperation))
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_agents SET launch_operation_id=? WHERE agent_id=?`, "wrong-owner", fence.AgentID)
	require.NoError(t, err)
	err = durable.MarkManagedFailedForFenceAndOperation(ctx, fence, "source-launch", "PROCESS_STOP_FAILED", "stop failed")
	require.ErrorAs(t, err, new(*ManagedRestartStaleError))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", agent.LifecycleState)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestMarkManagedFailedForFenceAndOperationRejectsStaleFenceAtomically(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	stale := fence
	stale.ProcessIncarnation = "replacement-incarnation"
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	err = durable.MarkManagedFailedForFenceAndOperation(ctx, stale, "source-launch", "PROCESS_STOP_FAILED", "stop failed")
	require.ErrorAs(t, err, new(*ManagedRestartStaleError))
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", agent.LifecycleState)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestMarkManagedStoppedClearsLiveProjectionAndSlot(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "stoppable", Name: "stoppable", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "stoppable-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "stoppable-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedStopped(ctx, agent.AgentID))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.LifecycleState)
	require.Equal(t, "stopped", stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.False(t, stored.SlotHeld)
}

func restartFixture(t *testing.T) (*Store, context.Context, ManagedAgentRecord, ManagedOperation) {
	t.Helper()
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	agent := ManagedAgentRecord{AgentID: "restartable", Name: "restartable", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "launch-operation", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-request", CanonicalBodyHash: "launch-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"}, ResponseBody: []byte(`{"operation_id":"launch-operation"}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, launch))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 42, StartMarker: "start", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, durable.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, durable.MarkManagedOperationTerminal(ctx, launch.OperationID))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	return durable, ctx, *stored, launch
}

func restartReservation(agent ManagedAgentRecord, current ManagedOperation) ManagedRestartReservation {
	operation := ManagedOperation{OperationID: "restart-operation", Method: "POST", CanonicalPath: "/v1/managed-agents/restartable/restart", RequestID: "restart-operation", CanonicalBodyHash: "empty-object", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": "restart-operation"}, ResponseBody: []byte(`{"operation_id":"restart-operation","agent":{"agent_id":"restartable"}}`)}
	return ManagedRestartReservation{Fence: ManagedRestartFence{AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch, ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation, RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash}, CurrentOperationID: current.OperationID, Operation: operation, Transition: ManagedRestartTransition{TargetRuntimeGeneration: "replacement-generation"}}
}

func TestForwardMigration023To025PreservesManagedRowsAndRestartTransition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	require.Greater(t, len(migrations), 22)
	migrationIDs := make(map[string]bool, len(migrations))
	for _, migration := range migrations {
		migrationIDs[migration.ID] = true
	}
	require.True(t, migrationIDs["049_managed_bridge_store"])
	// Preserve this 023-to-025 regression as a historical slice. The full 027
	// fixture below is covered separately and deliberately requires no starting
	// launch row.
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "025_managed_restart_execution")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "migration-agent", Name: "migration-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 2, ManifestRevision: 3, ManifestHash: testManifestHash, PendingInitialInstruction: "hello"}
	op := ManagedOperation{OperationID: "migration-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "migration-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 77, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	require.NoError(t, before.MarkManagedReady(ctx, agent.AgentID))
	require.NoError(t, before.MarkManagedOperationTerminal(ctx, op.OperationID))
	beforeRestart, err := before.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	// The pre-025 store API now writes the widened transition columns. Seed one
	// real 023-shaped retained row directly so this migration proves its copy
	// preserves an in-flight restart as well as ordinary managed rows.
	restartOperation := ManagedOperation{OperationID: "migration-restart", Method: "POST", CanonicalPath: "/v1/managed-agents/migration-agent/restart", RequestID: "migration-restart", CanonicalBodyHash: "restart-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.PutManagedOperation(ctx, restartOperation))
	_, err = before.db.Exec(`INSERT INTO managed_restart_transitions (
		agent_id,operation_id,old_pi_session_id,old_session_epoch,old_process_pid,
		old_process_start_marker,old_process_incarnation,old_runtime_generation,target_runtime_generation,
		old_desired_state,old_lifecycle_state,old_activity_state,old_accepting_prompts,old_slot_held,
		reservation_activity_state,planned_stop,stage,created_at,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		beforeRestart.AgentID, restartOperation.OperationID, beforeRestart.PiSessionID, beforeRestart.SessionEpoch, beforeRestart.ProcessPID,
		beforeRestart.ProcessStartMarker, beforeRestart.ProcessIncarnation, beforeRestart.RuntimeGeneration, "target-generation",
		"running", "running", beforeRestart.ActivityState, 1, 1, beforeRestart.ActivityState, 0, "reserved", beforeRestart.UpdatedAt, beforeRestart.UpdatedAt)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting', accepting_prompts=0 WHERE agent_id=?`, beforeRestart.AgentID)
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	stored, err := after.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "session", stored.PiSessionID)
	require.Equal(t, int64(2), stored.SessionEpoch)
	require.Equal(t, 77, stored.ProcessPID)
	require.Equal(t, "marker", stored.ProcessStartMarker)
	require.Equal(t, "incarnation", stored.ProcessIncarnation)
	require.Equal(t, "generation", stored.RuntimeGeneration)
	transition, err := after.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "migration-restart", transition.OperationID)
	require.Equal(t, "reserved", transition.Stage)
	require.Equal(t, 3, transition.Fence.ManifestRevision)
	require.Equal(t, testManifestHash, transition.Fence.ManifestHash)
	require.Nil(t, transition.TargetFence)
	var indexSQL string
	require.NoError(t, after.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='managed_agents_slot_idx'`).Scan(&indexSQL))
	require.Contains(t, indexSQL, "slot_held")
	_, err = after.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting', activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	_, err = after.db.Exec(`UPDATE managed_agents SET desired_state='stopped' WHERE agent_id=?`, agent.AgentID)
	require.Error(t, err)
	var applied int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='023_managed_restart'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='025_managed_restart_execution'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='026_managed_direct_delivery'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, after.Close())
	reopened, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	defer reopened.Close()
	require.NoError(t, reopened.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='023_managed_restart'`).Scan(&applied))
	require.Equal(t, 1, applied)
}

func TestReserveManagedRestartAtomicallyStoresTransitionAndOperation(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	reservation := restartReservation(agent, launch)
	operation, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, reservation.Operation.OperationID, operation.OperationID)
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "restarting", stored.LifecycleState)
	require.Equal(t, agent.ActivityState, stored.ActivityState)
	require.False(t, stored.AcceptingPrompts)
	require.True(t, stored.SlotHeld)
	transition, err := durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, reservation.Operation.OperationID, transition.OperationID)
	require.Equal(t, "reserved", transition.Stage)
	require.Equal(t, agent.RuntimeGeneration, transition.Fence.RuntimeGeneration)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	require.Equal(t, 202, operation.ResponseStatus)
	require.Equal(t, reservation.Operation.RequestID, operation.OperationID)
	require.Equal(t, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": reservation.Operation.RequestID}, operation.ResponseHeaders)
}

func TestRestartIdempotencyUsesWorkerScopeThenPathBodyComparison(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	stored, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, reservation.Operation.ResponseBody, stored.ResponseBody)
	changedPath := reservation
	changedPath.Operation.CanonicalPath += "/other"
	var conflict *ManagedIdempotencyConflictError
	_, _, err = durable.ReserveManagedRestart(ctx, changedPath)
	require.ErrorAs(t, err, &conflict)
	changedBody := reservation
	changedBody.Operation.CanonicalBodyHash = "different"
	_, _, err = durable.ReserveManagedRestart(ctx, changedBody)
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestManagedOperationStorageUsesTheInternalWorkerScope(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	operation, err := durable.FindManagedOperation(ctx, reservation.Operation.Method, reservation.Operation.RequestID)
	require.NoError(t, err)
	require.Equal(t, reservation.Operation.OperationID, operation.OperationID)
	var scope string
	require.NoError(t, durable.db.QueryRow(`SELECT session_identity_hash FROM managed_http_operations WHERE operation_id = ?`, reservation.Operation.OperationID).Scan(&scope))
	require.Equal(t, managedWorkerOperationScope, scope)
}

func TestReserveManagedRestartRejectsIneligibleRowsWithoutEffects(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(t *testing.T, durable *Store, agent ManagedAgentRecord, reservation *ManagedRestartReservation)
		conflict bool
	}{
		{name: "non-running lifecycle", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='starting' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "admission closed", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET accepting_prompts=0 WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "slot released", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET slot_held=0 WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "waiting activity", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='waiting' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "failed activity", mutate: func(_ *testing.T, durable *Store, agent ManagedAgentRecord, _ *ManagedRestartReservation) {
			_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='failed' WHERE agent_id=?`, agent.AgentID)
			require.NoError(t, err)
		}},
		{name: "mismatched fence", mutate: func(_ *testing.T, _ *Store, _ ManagedAgentRecord, reservation *ManagedRestartReservation) {
			reservation.Fence.ProcessIncarnation = "other-incarnation"
		}},
		{name: "existing transition", conflict: true, mutate: func(t *testing.T, durable *Store, agent ManagedAgentRecord, reservation *ManagedRestartReservation) {
			first := *reservation
			first.Operation = reservation.Operation
			first.Operation.OperationID, first.Operation.RequestID = "existing-restart", "existing-restart"
			first.Operation.ResponseBody = []byte(`{}`)
			_, _, err := durable.ReserveManagedRestart(context.Background(), first)
			require.NoError(t, err)
			agentAfter, err := durable.ManagedAgent(context.Background(), agent.AgentID)
			require.NoError(t, err)
			reservation.Fence = ManagedRestartFence{AgentID: agentAfter.AgentID, PiSessionID: agentAfter.PiSessionID, SessionEpoch: agentAfter.SessionEpoch, ProcessPID: agentAfter.ProcessPID, ProcessStartMarker: agentAfter.ProcessStartMarker, ProcessIncarnation: agentAfter.ProcessIncarnation, RuntimeGeneration: agentAfter.RuntimeGeneration, ManifestRevision: agentAfter.ManifestRevision, ManifestHash: agentAfter.ManifestHash}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			durable, ctx, agent, launch := restartFixture(t)
			reservation := restartReservation(agent, launch)
			test.mutate(t, durable, agent, &reservation)
			beforeVersion, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			var operations, transitions int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations`).Scan(&operations))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_restart_transitions`).Scan(&transitions))
			_, _, err = durable.ReserveManagedRestart(ctx, reservation)
			if test.conflict {
				var conflict *ManagedRestartConflictError
				require.ErrorAs(t, err, &conflict)
			} else {
				var notReady *ManagedAgentNotReadyError
				require.ErrorAs(t, err, &notReady)
			}
			afterVersion, err := durable.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, beforeVersion, afterVersion)
			var afterOperations, afterTransitions int
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations`).Scan(&afterOperations))
			require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_restart_transitions`).Scan(&afterTransitions))
			require.Equal(t, operations, afterOperations)
			require.Equal(t, transitions, afterTransitions)
		})
	}
}

func TestReserveManagedRestartRejectsCurrentPendingOperationWithoutEffects(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, err := durable.db.Exec(`UPDATE managed_http_operations SET state='pending', terminal_at=NULL, expires_at=NULL WHERE operation_id=?`, launch.OperationID)
	require.NoError(t, err)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	var conflict *ManagedRestartConflictError
	require.ErrorAs(t, err, &conflict)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = durable.db.Exec(`UPDATE managed_http_operations SET state='terminal', terminal_at='2026-01-01T00:00:00.000Z', expires_at='2026-01-02T00:00:00.000Z' WHERE operation_id=?`, launch.OperationID)
	require.NoError(t, err)
	reservation.CurrentLifecycleAction = "terminating"
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.ErrorAs(t, err, &conflict)
	reservation.CurrentLifecycleAction = ""
	_, replayed, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.False(t, replayed)
	_ = agent
}

func TestRollbackManagedRestartPreparationRestoresReservationAndTerminalizesOperation(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.RollbackManagedRestartPreparation(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_PREPARATION_FAILED", "restart preparation failed"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.LifecycleState, stored.LifecycleState)
	require.Equal(t, agent.ActivityState, stored.ActivityState)
	require.Equal(t, agent.AcceptingPrompts, stored.AcceptingPrompts)
	require.Equal(t, "RESTART_PREPARATION_FAILED", stored.LastErrorCode)
	require.Equal(t, "restart preparation failed", stored.LastErrorMessage)
	op, err := durable.ManagedOperation(ctx, reservation.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
	require.NotEmpty(t, op.ExpiresAt)
	_, err = time.Parse("2006-01-02T15:04:05.000Z", op.ExpiresAt)
	require.NoError(t, err)
	_, err = durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.Error(t, err)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	var stale *ManagedRestartStaleError
	require.ErrorAs(t, durable.RollbackManagedRestartPreparation(ctx, reservation.Fence, reservation.Operation.OperationID, "different", "different"), &stale)
	afterStale, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, after, afterStale)
}

func TestMarkManagedRestartTurnSettledRequiresExactOldFence(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	reservation := restartReservation(agent, launch)
	_, _, err := durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	activeFence := reservation.Fence
	// The fixture starts idle. The active-only settlement writer must reject it
	// without changing the transition or public projection.
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.ErrorAs(t, durable.MarkManagedRestartTurnSettled(ctx, activeFence, reservation.Operation.OperationID), new(*ManagedRestartStaleError))
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_ = agent
}

func TestManagedRestartExecutionTransactions(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	reservation := restartReservation(agent, launch)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartSettling(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartTurnSettled(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartStopping(ctx, reservation.Fence, reservation.Operation.OperationID))
	transition, err := durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "stopping", transition.Stage)
	require.True(t, transition.PlannedStop)
	require.NotEmpty(t, transition.TurnSettledAt)
	require.NoError(t, durable.MarkManagedRestartOldStopped(ctx, reservation.Fence, reservation.Operation.OperationID))
	target := reservation.Fence
	target.ProcessPID = 88
	target.ProcessStartMarker = "target-marker"
	target.ProcessIncarnation = "target-incarnation"
	target.RuntimeGeneration = "replacement-generation"
	require.NoError(t, durable.InstallManagedRestartTarget(ctx, reservation.Fence, target, reservation.Operation.OperationID))
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, target, policy))
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, target, ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: target.PiSessionID, Sequence: 0, Canonical: []byte(`{}`), SHA256: testManifestHash}))
	require.NoError(t, durable.CompleteManagedRestartReady(ctx, target, reservation.Operation.OperationID))
	ready, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", ready.LifecycleState)
	require.True(t, ready.AcceptingPrompts)
	require.Equal(t, target.RuntimeGeneration, ready.RuntimeGeneration)
	_, err = durable.ManagedRestartTransition(ctx, agent.AgentID)
	require.Error(t, err)
	op, err := durable.ManagedOperation(ctx, reservation.Operation.OperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)
}

func TestRestartAdmissionAndFailureStayDurableAndRedacted(t *testing.T) {
	durable, ctx, agent, launch := restartFixture(t)
	_, err := durable.db.Exec(`UPDATE managed_agents SET activity_state='active' WHERE agent_id=?`, agent.AgentID)
	require.NoError(t, err)
	updated, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	agent = *updated
	reservation := restartReservation(agent, launch)
	admission, err := durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, reservation.Operation.RequestID, reservation.Operation.CanonicalPath, reservation.Operation.CanonicalBodyHash, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, agent.AgentID, admission.Agent.AgentID)
	_, _, err = durable.ReserveManagedRestart(ctx, reservation)
	require.NoError(t, err)
	_, err = durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, "second", reservation.Operation.CanonicalPath, reservation.Operation.CanonicalBodyHash, agent.AgentID)
	var conflict *ManagedRestartConflictError
	require.ErrorAs(t, err, &conflict)
	require.NoError(t, durable.MarkManagedRestartPrepared(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.MarkManagedRestartSettling(ctx, reservation.Fence, reservation.Operation.OperationID))
	require.NoError(t, durable.RecordManagedRestartFailure(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_SETTLE_FAILED", "restart turn did not settle"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "RESTART_SETTLE_FAILED", stored.LastErrorCode)
	require.Equal(t, "restart turn did not settle", stored.LastErrorMessage)
	require.Error(t, durable.RecordManagedRestartFailure(ctx, reservation.Fence, reservation.Operation.OperationID, "RESTART_SETTLE_FAILED", "raw stderr token=secret"))
	_, err = durable.LookupManagedRestartAdmission(ctx, reservation.Operation.Method, reservation.Operation.RequestID, reservation.Operation.CanonicalPath+"/other", reservation.Operation.CanonicalBodyHash, agent.AgentID)
	var idempotency *ManagedIdempotencyConflictError
	require.ErrorAs(t, err, &idempotency)
}

func TestManagedLaunchReservesNameSlotAndOperationAtomically(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "123", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, agent, op), &conflict)
	stored, err := durable.ManagedOperation(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, "hash", stored.CanonicalBodyHash)

	sameName := agent
	sameName.AgentID = "different-alpha"
	sameNameOp := op
	sameNameOp.OperationID, sameNameOp.RequestID = "124", "124"
	var nameConflict *ManagedNameConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, sameName, sameNameOp), &nameConflict)

	second := agent
	second.AgentID, second.Name = "beta", "beta"
	secondOp := op
	secondOp.OperationID, secondOp.RequestID = "456", "456"
	var slot *ManagedSlotLimitError
	require.ErrorAs(t, durable.ReserveManagedLaunchWithLimit(ctx, second, secondOp, 1), &slot)
}

func TestManagedProviderSnapshotIsBoundedTransactionalAndNonSecret(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	providers := []ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Authenticated: true, Status: "authenticated"},
		{Provider: "google", Label: "Google", Authenticated: false, Status: "signed_out"},
		{Provider: "cursor", Label: "Cursor", Authenticated: true, Status: "authenticated"},
	}
	models := []ManagedProviderModel{{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true}, {Provider: "cursor", ID: "cursor-fast", Name: "Cursor Fast", Available: true}}
	revision, err := durable.CommitManagedProviderSnapshot(ctx, providers, models)
	require.NoError(t, err)
	require.Equal(t, int64(1), revision)
	snapshot, err := durable.ManagedProviderSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, revision, snapshot.AuthRevision)
	require.Len(t, snapshot.Providers, 3)
	require.ElementsMatch(t, models, snapshot.Models)

	// Invalid input fails before the transaction, retaining the old catalog and
	// revision rather than creating auth history or a partial provider state.
	_, err = durable.CommitManagedProviderSnapshot(ctx, providers[:2], nil)
	require.Error(t, err)
	after, err := durable.ManagedProviderSnapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot, after)

	var secretRows int
	require.NoError(t, durable.db.QueryRow(`SELECT count(*) FROM pragma_table_info('managed_provider_status') WHERE lower(name) LIKE '%token%' OR lower(name) LIKE '%secret%' OR lower(name) LIKE '%credential%'`).Scan(&secretRows))
	require.Zero(t, secretRows)
}

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func readinessSourceFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	agent := ManagedAgentRecord{AgentID: "source-agent", Name: "source-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "source-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "source-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 99, StartMarker: "marker", Incarnation: "incarnation"}, "generation"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	return durable, restartFenceFromAgent(stored)
}

func TestForwardMigration023To024BackfillsManagedReadinessSources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "legacy-source", Name: "legacy-source", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "legacy-op", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "legacy-op", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, before.Close())
	// This historical migration proof stops before 027: its deliberately
	// unmatched starting row is now a migration-027 rejection fixture.
	after, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "027_managed_control_requests")]})
	require.NoError(t, err)
	defer after.Close()
	source, err := after.ManagedReadinessSource(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, DefaultManagedReadinessPolicy(), source.DesiredPolicy)
	require.Nil(t, source.Fence)
	require.Nil(t, source.AppliedPolicy)
	require.Nil(t, source.Snapshot)
	var foreignKeySQL string
	require.NoError(t, after.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='managed_readiness_sources'`).Scan(&foreignKeySQL))
	require.NotContains(t, foreignKeySQL, "ON DELETE CASCADE")
}

func TestManagedReadinessSourceTransactions(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, DefaultManagedReadinessPolicy(), source.DesiredPolicy)
	require.Equal(t, &fence, source.Fence)
	require.Nil(t, source.AppliedPolicy)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	afterPolicy, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, afterPolicy, "policy source confirmation does not bump projection")
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	afterSnapshot, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before, afterSnapshot, "snapshot source confirmation does not bump projection")
	stale := fence
	stale.ManifestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var staleErr *ManagedReadinessSourceStaleError
	require.ErrorAs(t, durable.ConfirmManagedPolicyApplied(ctx, stale, policy), &staleErr)
	result, err := durable.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	require.Zero(t, result.ManagedOperations)
	source, err = durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, source.Snapshot, "generic operation retention never deletes current source rows")
}

func TestManagedChildDeltaRequiresCurrentFenceAndSequence(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	initial := ManagedChildSnapshot{SnapshotID: "initial", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"initial"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, initial))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)

	first := ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 1, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"description": "Child", "model": nil, "lifecycle_state": "running", "started_at": "2026-01-01T00:00:00Z", "finished_at": nil, "duration_ms": nil, "compaction": map[string]any{"state": "idle", "count": 0, "started_at": nil, "last_outcome": nil}}}
	updated, err := durable.ApplyManagedChildDelta(ctx, fence, first)
	require.NoError(t, err)
	require.True(t, updated)
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)

	updated, err = durable.ApplyManagedChildDelta(ctx, fence, first)
	require.NoError(t, err)
	require.False(t, updated, "a duplicate or regressed lifecycle delta is ignored")
	updated, err = durable.ApplyManagedChildDelta(ctx, fence, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 2, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"compaction": map[string]any{"state": "compacting", "count": 0, "started_at": "2026-01-01T00:00:01Z", "last_outcome": nil}}})
	require.NoError(t, err)
	require.True(t, updated, "a newer delta merges into the stored current run")
	complete, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, complete.Entries)
	require.Equal(t, "Child compacting", complete.Entries[0].Message)
	require.Equal(t, &ManagedHistoryPresentation{Kind: "compaction", ChildLabel: "Child", Outcome: "compacting"}, complete.Entries[0].Presentation)

	// The RPC reader intentionally keeps live JSON integers as json.Number.
	// Terminal history must retain that duration instead of only working after a
	// stored snapshot has been decoded back into float64.
	terminal := ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 3, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "steered", "finished_at": "2026-01-01T00:00:20Z", "duration_ms": json.Number("20000")}}
	updated, err = durable.ApplyManagedChildDelta(ctx, fence, terminal)
	require.NoError(t, err)
	require.True(t, updated)
	complete, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Equal(t, "Subagent wrapped up", complete.Entries[0].Message)
	require.Equal(t, &ManagedHistoryPresentation{Kind: "child", ChildState: "steered", Outcome: "success", ChildLabel: "Child", Summary: "Wrapped up in 20 seconds"}, complete.Entries[0].Presentation)
	published, err := durable.ManagedChildHistoryEntryForDelta(ctx, fence, terminal)
	require.NoError(t, err)
	require.Equal(t, complete.Entries[0], published)

	updated, err = durable.ApplyManagedChildDelta(ctx, fence, ManagedChildDelta{OwnerSessionID: "other-session", Sequence: 4, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "completed"}})
	require.NoError(t, err)
	require.False(t, updated, "a different owner session cannot update the current snapshot")
	stale := fence
	stale.RuntimeGeneration = "stale-generation"
	updated, err = durable.ApplyManagedChildDelta(ctx, stale, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 4, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "completed"}})
	require.NoError(t, err)
	require.False(t, updated, "a replaced process cannot overwrite the current child view")
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 3, source.Snapshot.Sequence)
	var stored map[string]any
	require.NoError(t, json.Unmarshal(source.Snapshot.Canonical, &stored))
	runs := stored["runs"].([]any)
	require.Len(t, runs, 1)
	require.Equal(t, "steered", runs[0].(map[string]any)["lifecycle_state"])
	require.Equal(t, "compacting", runs[0].(map[string]any)["compaction"].(map[string]any)["state"])
	// An exact retry is idempotent. Owner rejection remains a single complete
	// history fact, while a replaced fence cannot mutate current state.
	var staleHistory int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=? AND details_json LIKE '%stale%'`, fence.AgentID).Scan(&staleHistory))
	require.Equal(t, 1, staleHistory)
	require.NoError(t, durable.RecordStaleManagedChildDelta(ctx, stale, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 4, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "completed"}}, "parent_fence_replaced"))
	require.NoError(t, durable.RecordStaleManagedChildDelta(ctx, stale, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 4, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "completed"}}, "parent_fence_replaced"))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=? AND details_json LIKE '%parent_fence_replaced%'`, fence.AgentID).Scan(&staleHistory))
	require.Equal(t, 1, staleHistory)

	// The fenced transaction is the one complete-history writer for child
	// changes. Every transition has one visible row, including rejected stale
	// deliveries, and no generic UI projection row is needed to describe it.
	var transitionCount, completeCount int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=?`, fence.AgentID).Scan(&transitionCount))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_child'`, fence.AgentID).Scan(&completeCount))
	require.Equal(t, 5, transitionCount)
	require.Equal(t, transitionCount, completeCount)
}

func TestManagedChildDeltaGapRecoversFromCurrentSnapshotWithIdempotencyAndConflictFences(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	initial := ManagedChildSnapshot{SnapshotID: "initial", OwnerSessionID: fence.PiSessionID, Sequence: 1, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":"2026-01-01T00:00:00Z","finished_at":null,"duration_ms":null,"compaction":{"state":"idle","count":0,"started_at":null,"last_outcome":null},"sequence":1}],"sequence":1,"snapshot_id":"initial"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, initial))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	updated, err := durable.ApplyManagedChildDelta(ctx, fence, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 3, TaskID: "task", RunID: "run", ParentEntryID: "entry", Changes: map[string]any{"lifecycle_state": "completed"}})
	require.ErrorIs(t, err, ErrManagedChildDeltaGap)
	require.False(t, updated)

	recovered := ManagedChildSnapshot{SnapshotID: "recovered", OwnerSessionID: fence.PiSessionID, Sequence: 3, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"completed","started_at":"2026-01-01T00:00:00Z","finished_at":"2026-01-01T00:00:02Z","duration_ms":2000,"compaction":{"state":"idle","count":0,"started_at":null,"last_outcome":null},"sequence":3}],"sequence":3,"snapshot_id":"recovered"}`), SHA256: testManifestHash}
	updated, err = durable.RecoverManagedChildSnapshot(ctx, fence, recovered)
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = durable.RecoverManagedChildSnapshot(ctx, fence, recovered)
	require.NoError(t, err)
	require.False(t, updated, "the same authoritative snapshot is idempotent")
	conflicting := recovered
	conflicting.Canonical = []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":3,"snapshot_id":"conflict"}`)
	updated, err = durable.RecoverManagedChildSnapshot(ctx, fence, conflicting)
	require.ErrorIs(t, err, ErrManagedChildSnapshotConflict)
	require.False(t, updated)

	// A newer snapshot may omit an active run only by retaining its last
	// structured state as interrupted_unknown, never by manufacturing terminal.
	active := initial
	active.SnapshotID, active.Sequence = "active", 4
	active.Canonical = []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry-2","task_id":"task-2","run_id":"run-2","description":"Active Child","model":null,"lifecycle_state":"running","started_at":"2026-01-01T00:00:03Z","finished_at":null,"duration_ms":null,"compaction":{"state":"idle","count":0,"started_at":null,"last_outcome":null},"sequence":4}],"sequence":4,"snapshot_id":"active"}`)
	updated, err = durable.RecoverManagedChildSnapshot(ctx, fence, active)
	require.NoError(t, err)
	require.True(t, updated)
	omitting := ManagedChildSnapshot{SnapshotID: "omitting", OwnerSessionID: fence.PiSessionID, Sequence: 5, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":5,"snapshot_id":"omitting"}`), SHA256: testManifestHash}
	updated, err = durable.RecoverManagedChildSnapshot(ctx, fence, omitting)
	require.NoError(t, err)
	require.True(t, updated)
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Len(t, source.Recoveries, 1)
	require.Equal(t, "snapshot_omitted", source.Recoveries[0].Cause)
}

func TestManagedChildSnapshotOmissionKeepsRecoveryLedgerBounded(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	snapshot := func(index int, sequence int64, active bool) ManagedChildSnapshot {
		runs := []any{}
		if active {
			runs = append(runs, map[string]any{
				"parent_entry_id": fmt.Sprintf("entry-%03d", index), "task_id": fmt.Sprintf("task-%03d", index), "run_id": fmt.Sprintf("run-%03d", index),
				"description": "Active child", "model": nil, "lifecycle_state": "running", "started_at": "2026-01-01T00:00:00Z", "finished_at": nil, "duration_ms": nil,
				"compaction": map[string]any{"state": "idle", "count": 0, "started_at": nil, "last_outcome": nil}, "sequence": sequence,
			})
		}
		canonical, err := json.Marshal(map[string]any{"owner_session_id": fence.PiSessionID, "protocol": "mecha.children/v1", "runs": runs, "sequence": sequence, "snapshot_id": fmt.Sprintf("snapshot-%03d-%t", index, active)})
		require.NoError(t, err)
		return ManagedChildSnapshot{SnapshotID: fmt.Sprintf("snapshot-%03d-%t", index, active), OwnerSessionID: fence.PiSessionID, Sequence: sequence, Canonical: canonical, SHA256: testManifestHash}
	}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot(0, 1, true)))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	for index := 0; index <= maxManagedHistoryEntries; index++ {
		if index > 0 {
			updated, err := durable.RecoverManagedChildSnapshot(ctx, fence, snapshot(index, int64(index*2+1), true))
			require.NoError(t, err)
			require.True(t, updated)
		}
		updated, err := durable.RecoverManagedChildSnapshot(ctx, fence, snapshot(index, int64(index*2+2), false))
		require.NoError(t, err)
		require.True(t, updated)
	}

	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Len(t, source.Recoveries, maxManagedHistoryEntries)
	var recoveries int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_recoveries`).Scan(&recoveries))
	require.Equal(t, maxManagedHistoryEntries, recoveries)
}

func TestManagedChildSnapshotReconcilesLostCompactionEndWithoutTerminalInference(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	compacting := ManagedChildSnapshot{SnapshotID: "compacting", OwnerSessionID: fence.PiSessionID, Sequence: 1, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":null,"finished_at":null,"duration_ms":null,"compaction":{"state":"compacting","count":1,"started_at":"2026-01-01T00:00:01Z","last_outcome":null},"sequence":1}],"sequence":1,"snapshot_id":"compacting"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, compacting))
	idleWithoutEnd := ManagedChildSnapshot{SnapshotID: "idle", OwnerSessionID: fence.PiSessionID, Sequence: 2, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":null,"finished_at":null,"duration_ms":null,"compaction":{"state":"idle","count":1,"started_at":null,"last_outcome":null},"sequence":2}],"sequence":2,"snapshot_id":"idle"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, idleWithoutEnd))
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(source.Snapshot.Canonical, &snapshot))
	run := snapshot["runs"].([]any)[0].(map[string]any)
	require.Equal(t, "running", run["lifecycle_state"])
	require.Equal(t, "reconciled_unknown", run["compaction"].(map[string]any)["last_outcome"])
	var historyCount int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=? AND details_json LIKE '%reconciled_unknown%'`, fence.AgentID).Scan(&historyCount))
	require.Equal(t, 1, historyCount)
}

func TestManagedChildRecoveryArchivesActiveRunBeforeFenceRollAndMarksOmission(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	active := ManagedChildSnapshot{SnapshotID: "active", OwnerSessionID: fence.PiSessionID, Sequence: 7, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":"2026-01-01T00:00:00Z","finished_at":null,"duration_ms":null,"compaction":{"state":"compacting","count":1,"started_at":"2026-01-01T00:00:01Z","last_outcome":null},"sequence":7}],"sequence":7,"snapshot_id":"active"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, active))

	next := fence
	next.ProcessPID = 100
	next.ProcessStartMarker = "replacement-marker"
	next.ProcessIncarnation = "replacement-incarnation"
	next.RuntimeGeneration = "replacement-generation"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, next.ProcessPID, next.ProcessStartMarker, next.ProcessIncarnation, next.RuntimeGeneration, next.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.RollManagedReadinessSourceFence(ctx, next))
	source, err := durable.ManagedReadinessSource(ctx, next.AgentID)
	require.NoError(t, err)
	require.Len(t, source.Recoveries, 1)
	require.Equal(t, "parent_process_lost", source.Recoveries[0].Cause)
	require.Equal(t, "run", source.Recoveries[0].RunID)

	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, next, DefaultManagedReadinessPolicy()))
	omitting := ManagedChildSnapshot{SnapshotID: "replacement", OwnerSessionID: next.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"replacement"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, next, omitting))
	source, err = durable.ManagedReadinessSource(ctx, next.AgentID)
	require.NoError(t, err)
	require.Len(t, source.Recoveries, 1)
	require.Equal(t, "snapshot_omitted", source.Recoveries[0].Cause)

	var historyCount int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=? AND kind='recovery'`, next.AgentID).Scan(&historyCount))
	require.Equal(t, 2, historyCount, "the fenced archive and later omission are each recorded once")
}

func TestManagedParentProcessLossArchivesLiveChildBeforeFailure(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	active := ManagedChildSnapshot{SnapshotID: "active", OwnerSessionID: fence.PiSessionID, Sequence: 7, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":"2026-01-01T00:00:00Z","finished_at":null,"duration_ms":null,"compaction":{"state":"compacting","count":1,"started_at":"2026-01-01T00:00:01Z","last_outcome":null},"sequence":7}],"sequence":7,"snapshot_id":"active"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, active))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	updated, err := durable.RecordManagedParentProcessLost(ctx, fence)
	require.NoError(t, err)
	require.True(t, updated)
	source, err := durable.ManagedReadinessSource(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Nil(t, source.Snapshot, "a dead parent cannot keep publishing its old child snapshot")
	require.Len(t, source.Recoveries, 1)
	require.Equal(t, "parent_process_lost", source.Recoveries[0].Cause)
	var transitions, complete int
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_child_transitions WHERE agent_id=? AND recovery_cause='parent_process_lost'`, fence.AgentID).Scan(&transitions))
	require.NoError(t, durable.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_child'`, fence.AgentID).Scan(&complete))
	require.Equal(t, 1, transitions)
	require.Equal(t, transitions, complete)

	updated, err = durable.RecordManagedParentProcessLost(ctx, fence)
	require.NoError(t, err)
	require.False(t, updated, "the hard-exit boundary is idempotent")
}

func TestManagedChildSessionRecoveryNeverAttachesToNewEpoch(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, DefaultManagedReadinessPolicy()))
	active := ManagedChildSnapshot{SnapshotID: "active", OwnerSessionID: fence.PiSessionID, Sequence: 1, Canonical: []byte(`{"owner_session_id":"session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"Child","model":null,"lifecycle_state":"running","started_at":null,"finished_at":null,"duration_ms":null,"compaction":{"state":"idle","count":0,"started_at":null,"last_outcome":null},"sequence":1}],"sequence":1,"snapshot_id":"active"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, active))
	next := fence
	next.PiSessionID = "new-session"
	next.SessionEpoch++
	next.ProcessPID = 100
	next.ProcessStartMarker = "replacement-marker"
	next.ProcessIncarnation = "replacement-incarnation"
	next.RuntimeGeneration = "replacement-generation"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, next.PiSessionID, next.SessionEpoch, next.ProcessPID, next.ProcessStartMarker, next.ProcessIncarnation, next.RuntimeGeneration, next.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.RollManagedReadinessSourceFence(ctx, next))
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, next, DefaultManagedReadinessPolicy()))
	matchingNewRun := ManagedChildSnapshot{SnapshotID: "replacement", OwnerSessionID: next.PiSessionID, Sequence: 1, Canonical: []byte(`{"owner_session_id":"new-session","protocol":"mecha.children/v1","runs":[{"parent_entry_id":"entry","task_id":"task","run_id":"run","description":"New Child","model":null,"lifecycle_state":"running","started_at":null,"finished_at":null,"duration_ms":null,"compaction":{"state":"idle","count":0,"started_at":null,"last_outcome":null},"sequence":1}],"sequence":1,"snapshot_id":"replacement"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, next, matchingNewRun))
	source, err := durable.ManagedReadinessSource(ctx, next.AgentID)
	require.NoError(t, err)
	require.Len(t, source.Recoveries, 1)
	require.Equal(t, "session_changed", source.Recoveries[0].Cause)
}

func TestManagedReadyRequiresSourceConfirmation(t *testing.T) {
	ctx := context.Background()
	durable, fence := readinessSourceFixture(t)
	var stale *ManagedReadinessSourceStaleError
	require.ErrorAs(t, durable.MarkManagedReadyFromSources(ctx, fence), &stale)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, policy))
	require.ErrorAs(t, durable.MarkManagedReadyFromSources(ctx, fence), &stale)
	snapshot := ManagedChildSnapshot{SnapshotID: "snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	agent, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", agent.LifecycleState)
	require.True(t, agent.AcceptingPrompts)
}

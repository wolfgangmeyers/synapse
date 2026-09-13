package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func readyManagedGoalEvaluatorFixture(t *testing.T) (*Store, ManagedRuntimeFence, *deterministic.FixedClock) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC))
	durable, err := Open(context.Background(), t.TempDir()+"/router.db", Options{Clock: clock})
	require.NoError(t, err)
	fence := ManagedRuntimeFence{AgentID: "goal-evaluator", PiSessionID: "goal-session", SessionEpoch: 1, ProcessPID: 101, ProcessStartMarker: "goal-marker", ProcessIncarnation: "goal-incarnation", RuntimeGeneration: "goal-generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	agent := ManagedAgentRecord{AgentID: fence.AgentID, Name: fence.AgentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash}
	op := ManagedOperation{OperationID: "goal-evaluator-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "goal-evaluator-launch", CanonicalBodyHash: "goal-evaluator-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(context.Background(), agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(context.Background(), fence.AgentID, fence.PiSessionID, ProcessIdentityRecord{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, fence.RuntimeGeneration))
	require.NoError(t, durable.ConfirmManagedPolicyApplied(context.Background(), fence, DefaultManagedReadinessPolicy()))
	snapshot := ManagedChildSnapshot{SnapshotID: "goal-evaluator-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"goal-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"goal-evaluator-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(context.Background(), fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(context.Background(), fence))
	policy, err := durable.UpdateManagedGoalPolicy(context.Background(), fence, ManagedGoalConfig{Enabled: true, Goal: "finish the router", Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	applied, err := durable.ConfirmManagedGoalPolicyApplied(context.Background(), fence, policy.DesiredRevision)
	require.NoError(t, err)
	require.True(t, applied)
	t.Cleanup(func() { _ = durable.Close() })
	return durable, fence, clock
}

func TestMigration039AddsEvaluatorStateToAReleased038Store(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "039_managed_goal_evaluator_state")]})
	require.NoError(t, err)
	legacy := ManagedAgentRecord{AgentID: "legacy-goal", Name: "legacy-goal", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "legacy-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	launch := ManagedOperation{OperationID: "legacy-goal-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "legacy-goal-launch", CanonicalBodyHash: testManifestHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, before.ReserveManagedLaunch(ctx, legacy, launch))
	_, err = before.db.Exec(`UPDATE managed_agent_policies SET desired_revision=4,applied_revision=4,goal_enabled=1,goal_text='finish legacy work',projection_state='applied',evaluator_state='evaluating',evaluator_attempts=7,not_done_wakeups=19,last_settled_sequence=42 WHERE agent_id=?`, legacy.AgentID)
	require.NoError(t, err)
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer after.Close()
	var columns, applied int
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('managed_agent_policies') WHERE name IN ('last_outcome','rearm_activity_count','evaluator_lease_until')`).Scan(&columns))
	require.Equal(t, 3, columns)
	require.NoError(t, after.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE id='039_managed_goal_evaluator_state'`).Scan(&applied))
	require.Equal(t, 1, applied)
	upgraded, err := after.ManagedGoalPolicyFor(ctx, legacy.AgentID)
	require.NoError(t, err)
	require.Equal(t, "finish legacy work", upgraded.Config.Goal)
	require.Equal(t, "evaluating", upgraded.EvaluatorState)
	require.EqualValues(t, 19, upgraded.NotDoneWakeups)
	require.EqualValues(t, 42, upgraded.LastSettledSequence)
	require.Empty(t, upgraded.LastOutcome)
	require.Zero(t, upgraded.RearmActivityCount)
	require.Empty(t, upgraded.EvaluatorLeaseUntil)
}

func TestManagedGoalEvaluationOnlyClaimsASettledBoundaryOnce(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)

	policy, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "evaluating", policy.EvaluatorState)

	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.False(t, claimed)

	policy, changed, err := durable.CompleteManagedGoalEvaluation(ctx, fence, 10, ManagedGoalEvaluationNotDone)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "idle", policy.EvaluatorState)
	require.EqualValues(t, 1, policy.NotDoneWakeups)
	require.NotEmpty(t, policy.WakeupMessageID)
	wakeup, err := durable.MessageFor(ctx, policy.WakeupMessageID)
	require.NoError(t, err)
	require.Equal(t, "system", wakeup.Source.Kind)
	target, err := durable.MessageTargetSnapshot(ctx, policy.WakeupMessageID)
	require.NoError(t, err)
	require.Equal(t, fence.AgentID, target.OwnerIdentity)
	var reserved int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_goal_wakeups WHERE message_id=? AND evaluation_id=?`, policy.WakeupMessageID, policy.EvaluatorID).Scan(&reserved))
	require.Equal(t, 1, reserved)

	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.False(t, claimed, "the same settled event cannot start a busy loop")
}

func TestManagedGoalCompletionAndDisableStopFurtherEvaluation(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)

	_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.True(t, claimed)
	policy, changed, err := durable.CompleteManagedGoalEvaluation(ctx, fence, 10, ManagedGoalEvaluationCompleted)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "done", policy.EvaluatorState)
	require.Equal(t, ManagedGoalEvaluationCompleted, policy.LastOutcome)

	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 11)
	require.NoError(t, err)
	require.False(t, claimed)

	disabled, err := durable.UpdateManagedGoalPolicy(ctx, fence, ManagedGoalConfig{Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	_, err = durable.ConfirmManagedGoalPolicyApplied(ctx, fence, disabled.DesiredRevision)
	require.NoError(t, err)
	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.False(t, policy.Config.Enabled)
	require.Equal(t, "done", policy.EvaluatorState)
	require.Empty(t, policy.LastOutcome)
}

func TestManagedGoalEvaluationWaitsForChildrenAndCompaction(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)

	_, err := durable.ApplyManagedChildDelta(ctx, fence, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 1, ParentEntryID: "parent", TaskID: "child", RunID: "run", Changes: map[string]any{"description": "child", "model": nil, "lifecycle_state": "running", "started_at": "2026-09-14T12:00:00.000Z", "finished_at": nil, "duration_ms": nil, "compaction": map[string]any{"state": "idle", "count": 0, "started_at": nil, "last_outcome": nil}}})
	require.NoError(t, err)
	_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.False(t, claimed)

	_, err = durable.ApplyManagedChildDelta(ctx, fence, ManagedChildDelta{OwnerSessionID: fence.PiSessionID, Sequence: 2, ParentEntryID: "parent", TaskID: "child", RunID: "run", Changes: map[string]any{"lifecycle_state": "completed", "finished_at": "2026-09-14T12:00:01.000Z", "duration_ms": int64(1000)}})
	require.NoError(t, err)
	_, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 3, State: "compacting"})
	require.NoError(t, err)
	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 11)
	require.NoError(t, err)
	require.False(t, claimed)

	_, err = durable.ApplyManagedParentCompaction(ctx, fence, ManagedParentCompactionEvent{Sequence: 4, State: "idle", Outcome: "completed"})
	require.NoError(t, err)
	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 12)
	require.NoError(t, err)
	require.True(t, claimed)
}

func TestManagedGoalSnoozeRejectsAStaleFenceAndPreservesItsFirstDeadline(t *testing.T) {
	ctx := context.Background()
	durable, fence, clock := readyManagedGoalEvaluatorFixture(t)
	stale := fence
	stale.ProcessIncarnation = "old-incarnation"

	policy, snoozed, err := durable.SnoozeManagedGoalPolicy(ctx, stale)
	require.NoError(t, err)
	require.False(t, snoozed)
	require.Nil(t, policy)

	policy, snoozed, err = durable.SnoozeManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.True(t, snoozed)
	firstDeadline := policy.SnoozedUntil
	require.NotEmpty(t, firstDeadline)
	require.NoError(t, clock.Advance(time.Second))

	policy, snoozed, err = durable.SnoozeManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.False(t, snoozed)
	require.Nil(t, policy)
	stored, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, firstDeadline, stored.SnoozedUntil)
}

func TestManagedGoalClaimRecoversAnExpiredDurableLease(t *testing.T) {
	ctx := context.Background()
	durable, fence, clock := readyManagedGoalEvaluatorFixture(t)

	_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, durable.Close())

	reopened, err := Open(ctx, durable.path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	require.NoError(t, clock.Advance(managedGoalEvaluationLease+time.Millisecond))
	policy, claimed, err := reopened.ClaimManagedGoalEvaluation(ctx, fence, 11)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, "evaluating", policy.EvaluatorState)
}

func TestManagedGoalRecoveryDropsInterruptedEvaluatorAfterReplacement(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)
	_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.True(t, claimed)

	// A replacement is the only recovery point: a lost evaluator cannot be
	// completed by its former process after the new runtime is ready.
	require.NoError(t, durable.db.QueryRow(`UPDATE managed_agent_policies SET evaluator_pi_session_id='prior-session', evaluator_process_incarnation='prior-incarnation' WHERE agent_id=? RETURNING 1`, fence.AgentID).Scan(new(int)))
	changed, err := durable.RecoverManagedGoalEvaluationForFence(ctx, fence)
	require.NoError(t, err)
	require.True(t, changed)
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", policy.EvaluatorState)
	require.Equal(t, "evaluation interrupted", policy.EvaluatorReason)
}

func TestManagedGoalClaimAndRearmAreAtomic(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)

	var claims int
	var resultsMu sync.Mutex
	var claimWG sync.WaitGroup
	startClaims := make(chan struct{})
	for worker := 0; worker < 16; worker++ {
		claimWG.Add(1)
		go func() {
			defer claimWG.Done()
			<-startClaims
			_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
			require.NoError(t, err)
			if claimed {
				resultsMu.Lock()
				claims++
				resultsMu.Unlock()
			}
		}()
	}
	close(startClaims)
	claimWG.Wait()
	require.Equal(t, 1, claims)

	_, err := durable.db.Exec(`UPDATE managed_agent_policies SET evaluator_state='scheduled',not_done_wakeups=1000,rearm_activity_count=0 WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	var rearms int
	var rearmWG sync.WaitGroup
	startRearms := make(chan struct{})
	for worker := 0; worker < 16; worker++ {
		rearmWG.Add(1)
		go func() {
			defer rearmWG.Done()
			<-startRearms
			_, rearmed, err := durable.RecordManagedGoalMeaningfulActivity(ctx, fence, ManagedGoalActivity{Kind: "tool", ID: fmt.Sprintf("concurrent-%d", worker)})
			require.NoError(t, err)
			if rearmed {
				resultsMu.Lock()
				rearms++
				resultsMu.Unlock()
			}
		}()
	}
	close(startRearms)
	rearmWG.Wait()
	require.Equal(t, 1, rearms)
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", policy.EvaluatorState)
	require.Zero(t, policy.NotDoneWakeups)
	require.Zero(t, policy.RearmActivityCount)
}

func TestManagedGoalSnoozeAndRearmAreDurable(t *testing.T) {
	ctx := context.Background()
	durable, fence, clock := readyManagedGoalEvaluatorFixture(t)

	policy, snoozed, err := durable.SnoozeManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.True(t, snoozed)
	require.Equal(t, "snoozed", policy.EvaluatorState)
	_, claimed, err := durable.ClaimManagedGoalEvaluation(ctx, fence, 10)
	require.NoError(t, err)
	require.False(t, claimed)

	require.NoError(t, clock.Advance(time.Minute))
	policy, due, err := durable.WakeSnoozedManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, "idle", policy.EvaluatorState)

	for sequence := int64(11); sequence < 1_011; sequence++ {
		_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, sequence)
		require.NoError(t, err)
		require.True(t, claimed)
		_, changed, completeErr := durable.CompleteManagedGoalEvaluation(ctx, fence, sequence, ManagedGoalEvaluationNotDone)
		require.NoError(t, completeErr)
		require.True(t, changed)
	}
	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1000, policy.NotDoneWakeups)
	require.Equal(t, "scheduled", policy.EvaluatorState)
	policy, due, err = durable.WakeSnoozedManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.False(t, due)
	require.Nil(t, policy)
	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 1_011)
	require.NoError(t, err)
	require.False(t, claimed, "the NOT_DONE bound must survive a scheduled wake attempt")
	for index := 0; index < 2; index++ {
		policy, rearmed, activityErr := durable.RecordManagedGoalMeaningfulActivity(ctx, fence, ManagedGoalActivity{Kind: "tool", ID: fmt.Sprintf("durable-%d", index)})
		require.NoError(t, activityErr)
		require.False(t, rearmed)
		require.Equal(t, "scheduled", policy.EvaluatorState)
	}
	policy, rearmed, err := durable.RecordManagedGoalMeaningfulActivity(ctx, fence, ManagedGoalActivity{Kind: "tool", ID: "durable-final"})
	require.NoError(t, err)
	require.True(t, rearmed)
	require.EqualValues(t, 0, policy.NotDoneWakeups)
	require.Equal(t, "idle", policy.EvaluatorState)

	_, claimed, err = durable.ClaimManagedGoalEvaluation(ctx, fence, 1_011)
	require.NoError(t, err)
	require.True(t, claimed, "a meaningful-activity rearm must also reopen the bounded evaluator budget")
}

func TestManagedGoalActivityDedupesPersistedIDsUnderTheFullFence(t *testing.T) {
	ctx := context.Background()
	durable, fence, _ := readyManagedGoalEvaluatorFixture(t)
	require.NoError(t, durable.db.QueryRow(`UPDATE managed_agent_policies SET evaluator_state='scheduled',not_done_wakeups=1000,rearm_activity_count=0 WHERE agent_id=? RETURNING 1`, fence.AgentID).Scan(new(int)))

	activity := ManagedGoalActivity{Kind: "child", ID: "task:run:entry"}
	for attempt := 0; attempt < 3; attempt++ {
		policy, rearmed, err := durable.RecordManagedGoalMeaningfulActivity(ctx, fence, activity)
		require.NoError(t, err)
		require.False(t, rearmed)
		if attempt == 0 {
			require.NotNil(t, policy)
			require.EqualValues(t, 1, policy.RearmActivityCount)
		} else {
			require.Nil(t, policy)
		}
	}
	stale := fence
	stale.ProcessIncarnation = "stale-incarnation"
	policy, rearmed, err := durable.RecordManagedGoalMeaningfulActivity(ctx, stale, ManagedGoalActivity{Kind: "tool", ID: "stale-tool"})
	require.NoError(t, err)
	require.False(t, rearmed)
	require.Nil(t, policy)

	policy, rearmed, err = durable.RecordManagedGoalMeaningfulActivity(ctx, fence, ManagedGoalActivity{Kind: "tool", ID: "request-a"})
	require.NoError(t, err)
	require.False(t, rearmed)
	require.EqualValues(t, 2, policy.RearmActivityCount)
	policy, rearmed, err = durable.RecordManagedGoalMeaningfulActivity(ctx, fence, ManagedGoalActivity{Kind: "builtin", ID: "entry-a"})
	require.NoError(t, err)
	require.True(t, rearmed)
	require.Equal(t, "idle", policy.EvaluatorState)

	var rows int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_goal_activity WHERE agent_id=?`, fence.AgentID).Scan(&rows))
	require.Equal(t, 3, rows)
}

func TestManagedGoalSnoozeFenceRecoveryClearsStaleTimerState(t *testing.T) {
	ctx := context.Background()
	durable, fence, clock := readyManagedGoalEvaluatorFixture(t)
	policy, snoozed, err := durable.SnoozeManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.True(t, snoozed)
	require.True(t, ManagedGoalSnoozeFenceMatches(policy, fence))

	replacement := fence
	replacement.ProcessPID++
	replacement.ProcessIncarnation = "replacement-incarnation"
	replacement.RuntimeGeneration = "replacement-generation"
	require.NoError(t, durable.db.QueryRow(`UPDATE managed_agents SET process_pid=?,process_incarnation=?,runtime_generation=? WHERE agent_id=? RETURNING 1`, replacement.ProcessPID, replacement.ProcessIncarnation, replacement.RuntimeGeneration, fence.AgentID).Scan(new(int)))
	changed, err := durable.RecoverManagedGoalEvaluationForFence(ctx, replacement)
	require.NoError(t, err)
	require.True(t, changed)
	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", policy.EvaluatorState)
	require.Empty(t, policy.SnoozedUntil)
	require.Empty(t, policy.WakeupMessageID)

	require.NoError(t, clock.Advance(time.Hour))
	policy, woke, err := durable.WakeSnoozedManagedGoalPolicy(ctx, fence)
	require.NoError(t, err)
	require.False(t, woke)
	require.Nil(t, policy)
}

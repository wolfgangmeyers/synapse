package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func crashPolicyFixture(t *testing.T) (*Store, *deterministic.FixedClock, ManagedRuntimeFence) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	durable, err := Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	agent := ManagedAgentRecord{AgentID: "crash-policy", Name: "crash-policy", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "crash-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "crash-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(context.Background(), agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(context.Background(), agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 100, StartMarker: "marker-100", Incarnation: "inc-100"}, "generation-100"))
	require.NoError(t, durable.MarkManagedReady(context.Background(), agent.AgentID))
	stored, err := durable.ManagedAgent(context.Background(), agent.AgentID)
	require.NoError(t, err)
	return durable, clock, restartFenceFromAgent(stored)
}

func readyCrashPolicyFence(t *testing.T, durable *Store, fence ManagedRuntimeFence, pid int) ManagedRuntimeFence {
	t.Helper()
	_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,slot_held=1,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, pid, "marker-"+time.Now().Format("150405.000"), "inc-"+time.Now().Format("150405.000"), "generation-next", fence.AgentID)
	require.NoError(t, err)
	stored, err := durable.ManagedAgent(context.Background(), fence.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.runImmediate(context.Background(), func() error {
		return markManagedCrashReadyTx(context.Background(), durable.db, fence.AgentID, formatTimestamp(durable.clock.Now()), durable.clock.Now())
	}))
	return restartFenceFromAgent(stored)
}

func TestManagedCrashPolicyBehavioralMatrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		secondAt    time.Duration
		thirdAt     time.Duration
		thirdLoop   bool
		thirdCount  int
		secondDelay time.Duration
	}{
		{name: "119.999 keeps oldest", secondAt: 119999 * time.Millisecond, thirdAt: 0, thirdLoop: true, thirdCount: 3, secondDelay: 5 * time.Second},
		{name: "120.000 excludes oldest", secondAt: 120 * time.Second, thirdAt: 0, thirdLoop: false, thirdCount: 3, secondDelay: time.Second},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			durable, clock, fence := crashPolicyFixture(t)
			first, err := durable.RecordManagedUnexpectedExit(ctx, fence, "crash-launch", "first")
			require.NoError(t, err)
			require.Equal(t, 1, first.CrashCount)
			require.Equal(t, formatTimestamp(clock.Now().Add(time.Second)), first.RetryAt)
			fence = readyCrashPolicyFence(t, durable, fence, 101)
			require.NoError(t, clock.Advance(test.secondAt))
			second, err := durable.RecordManagedUnexpectedExit(ctx, fence, "", "second")
			require.NoError(t, err)
			require.Equal(t, 2, second.CrashCount)
			require.Equal(t, formatTimestamp(clock.Now().Add(test.secondDelay)), second.RetryAt)
			fence = readyCrashPolicyFence(t, durable, fence, 102)
			require.NoError(t, clock.Advance(test.thirdAt))
			third, err := durable.RecordManagedUnexpectedExit(ctx, fence, "", "third")
			require.NoError(t, err)
			require.Equal(t, test.thirdLoop, third.CrashLoop)
			require.Equal(t, test.thirdCount, third.CrashCount)
			stored, err := durable.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			if test.thirdLoop {
				require.Equal(t, "failed", stored.LifecycleState)
				require.Equal(t, "CRASH_LOOP", stored.LastErrorCode)
			} else {
				require.Equal(t, "running", stored.LifecycleState)
				require.NotEmpty(t, third.RetryAt)
			}
		})
	}
}

func TestManagedCrashPolicyMigration047ConvertsOnlyPending046Crash(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	migrations := DefaultMigrations()
	before, err := Open(ctx, path, Options{Migrations: migrations[:migrationIndex(t, migrations, "047_managed_crash_policy")]})
	require.NoError(t, err)

	seed := func(agentID string) {
		agent := ManagedAgentRecord{AgentID: agentID, Name: agentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: agentID + "-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
		op := ManagedOperation{OperationID: agentID + "-op", Method: "POST", CanonicalPath: "/v1/managed-agents/" + agentID, RequestID: agentID + "-op", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
		require.NoError(t, before.ReserveManagedLaunch(ctx, agent, op))
	}
	seed("legacy-pending")
	seed("legacy-planned")
	seed("legacy-idle")
	retryAt := "2026-09-13T12:00:01.000Z"
	_, err = before.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='stopped',accepting_prompts=0,slot_held=1,last_error_code='PROCESS_EXITED',last_error_message='unexpected',last_error_at=?,updated_at=? WHERE agent_id='legacy-pending'`, "2026-09-13T12:00:00.000Z", retryAt)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_agents SET lifecycle_state='restarting',activity_state='stopped',accepting_prompts=0,slot_held=1,last_error_code='PROCESS_STOP_FAILED',last_error_message='planned',last_error_at=?,updated_at=? WHERE agent_id='legacy-planned'`, "2026-09-13T12:00:00.000Z", retryAt)
	require.NoError(t, err)
	_, err = before.db.Exec(`UPDATE managed_agents SET lifecycle_state='running',activity_state='idle',accepting_prompts=1,slot_held=1,last_error_code=NULL,last_error_message=NULL,last_error_at=NULL,updated_at=? WHERE agent_id='legacy-idle'`, retryAt)
	require.NoError(t, err)
	for _, agentID := range []string{"legacy-pending", "legacy-planned", "legacy-idle"} {
		_, err = before.db.Exec(`UPDATE managed_crash_recovery SET retry_at=? WHERE agent_id=?`, retryAt, agentID)
		require.NoError(t, err)
	}
	require.NoError(t, before.Close())

	after, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })
	for _, test := range []struct {
		agentID string
		history string
	}{
		{agentID: "legacy-pending", history: `["2026-09-13T12:00:00.000Z"]`},
		{agentID: "legacy-planned", history: "[]"},
		{agentID: "legacy-idle", history: "[]"},
	} {
		var history string
		require.NoError(t, after.db.QueryRow(`SELECT crash_history FROM managed_crash_recovery WHERE agent_id=?`, test.agentID).Scan(&history))
		require.Equal(t, test.history, history, test.agentID)
	}
}

func TestManagedStartupCrashPolicyCountsFirstSecondAndThirdExit(t *testing.T) {
	ctx := context.Background()
	durable, clock, fence := crashPolicyFixture(t)
	setStarting := func(pid int) {
		_, err := durable.db.Exec(`UPDATE managed_agents SET lifecycle_state='starting',activity_state='starting',accepting_prompts=0,slot_held=1,process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=? WHERE agent_id=?`, pid, "startup-marker", "startup-incarnation", "startup-generation", fence.AgentID)
		require.NoError(t, err)
		stored, err := durable.ManagedAgent(ctx, fence.AgentID)
		require.NoError(t, err)
		fence = restartFenceFromAgent(stored)
	}
	setStarting(201)
	first, err := durable.RecordManagedStartupExit(ctx, fence, "", "startup first")
	require.NoError(t, err)
	require.Equal(t, 1, first.CrashCount)
	require.Equal(t, formatTimestamp(clock.Now().Add(time.Second)), first.RetryAt)
	setStarting(202)
	require.NoError(t, clock.Advance(time.Second))
	second, err := durable.RecordManagedStartupExit(ctx, fence, "", "startup second")
	require.NoError(t, err)
	require.Equal(t, 2, second.CrashCount)
	require.Equal(t, formatTimestamp(clock.Now().Add(5*time.Second)), second.RetryAt)
	setStarting(203)
	third, err := durable.RecordManagedStartupExit(ctx, fence, "", "startup third")
	require.NoError(t, err)
	require.Equal(t, 3, third.CrashCount)
	require.True(t, third.CrashLoop)
	stored, err := durable.ManagedAgent(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", stored.LifecycleState)
	require.Equal(t, "CRASH_LOOP", stored.LastErrorCode)
}

func TestManagedCrashPolicyHealthyResetBoundary(t *testing.T) {
	for _, delta := range []struct {
		name string
		age  time.Duration
		want int
	}{
		{name: "599.999 retains history", age: 599999 * time.Millisecond, want: 2},
		{name: "600.000 clears history", age: 600 * time.Second, want: 1},
	} {
		t.Run(delta.name, func(t *testing.T) {
			durable, clock, fence := crashPolicyFixture(t)
			_, err := durable.RecordManagedUnexpectedExit(context.Background(), fence, "crash-launch", "first")
			require.NoError(t, err)
			fence = readyCrashPolicyFence(t, durable, fence, 111)
			require.NoError(t, clock.Advance(delta.age))
			decision, err := durable.RecordManagedUnexpectedExit(context.Background(), fence, "", "second")
			require.NoError(t, err)
			require.Equal(t, delta.want, decision.CrashCount)
			require.False(t, decision.CrashLoop)
			require.NotEmpty(t, decision.RetryAt)
		})
	}
}

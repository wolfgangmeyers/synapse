package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func orderlyStopMarkerTestAgent(t *testing.T, durable *Store, agentID, operationID string, fence ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	agent := ManagedAgentRecord{
		AgentID: agentID, Name: agentID, SlotHeld: true, Cwd: "/tmp",
		SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:  "running", LifecycleState: "starting", ActivityState: "starting",
		PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash,
	}
	op := ManagedOperation{OperationID: operationID, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: operationID, CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agentID, fence.PiSessionID, ProcessIdentityRecord{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, fence.RuntimeGeneration))
}

func TestRetryManagedOrderlyStopUsesExplicitFailureAllowlist(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	for index, testCase := range []struct {
		code    string
		retryOK bool
	}{
		{code: "RECOVERY_TARGET_START_FAILED", retryOK: true},
		{code: "RECOVERY_READINESS_FAILED", retryOK: true},
		{code: "RECOVERY_LAUNCH_OWNER_MISSING", retryOK: true},
		{code: "RECOVERY_TARGET_UNKNOWN"},
		{code: "ORPHAN_IDENTITY_UNSAFE"},
	} {
		t.Run(testCase.code, func(t *testing.T) {
			fence := ManagedRuntimeFence{AgentID: "retry-allowlist-" + string(rune('a'+index)), PiSessionID: "session", SessionEpoch: 1, ProcessPID: 910 + index, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: testManifestHash}
			orderlyStopMarkerTestAgent(t, durable, fence.AgentID, "retry-allowlist-launch-"+string(rune('a'+index)), fence)
			require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))
			action, _, err := durable.ClaimManagedRootRecovery(ctx, fence.AgentID, fence, testManifestHash, "target-generation")
			require.NoError(t, err)
			require.NoError(t, durable.FailManagedRootRecovery(ctx, action.ActionID, testCase.code, "retained failure"))
			retried, err := durable.RetryManagedOrderlyStop(ctx, fence, "retry-generation")
			if testCase.retryOK {
				require.NoError(t, err)
				require.NotNil(t, retried)
				require.Equal(t, "claimed", retried.Stage)
				require.Equal(t, "retry-generation", retried.TargetRuntimeGeneration)
			} else {
				require.Error(t, err)
				require.Nil(t, retried)
				retained, readErr := durable.ManagedRootRecoveryAction(ctx, fence.AgentID)
				require.NoError(t, readErr)
				require.Equal(t, "failed", retained.Stage)
				require.Equal(t, testCase.code, retained.FailureCode)
			}
		})
	}
}

func TestManagedOrderlyStopMarkerPersistsAcrossReopenUntilConsumed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	agent := ManagedAgentRecord{
		AgentID: "orderly-stop-agent", Name: "orderly-stop-agent", SlotHeld: true,
		Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState: "running", LifecycleState: "starting", ActivityState: "starting",
		PiSessionID: "session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash,
	}
	op := ManagedOperation{OperationID: "orderly-stop-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "orderly-stop-launch", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, ProcessIdentityRecord{PID: 901, StartMarker: "start", Incarnation: "incarnation"}, "runtime-generation"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	fence := restartFenceFromAgent(stored)

	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, fence.RuntimeGeneration, marker.RuntimeGeneration)
	require.False(t, marker.Confirmed)
	require.NoError(t, durable.Close())

	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	marker, err = reopened.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, fence.RuntimeGeneration, marker.RuntimeGeneration)
	require.NoError(t, reopened.ConsumeManagedOrderlyStopMarker(ctx, fence))
	marker, err = reopened.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
}

func TestManagedOrderlyStopMarkerRejectsStaleAndStoppedFences(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	fence := ManagedRuntimeFence{AgentID: "orderly-stale-agent", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 902, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, durable, fence.AgentID, "orderly-stale-launch", fence)

	stale := fence
	stale.RuntimeGeneration = "stale-generation"
	require.Error(t, durable.MarkManagedOrderlyStopped(ctx, stale))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)

	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))
	require.NoError(t, durable.ConfirmManagedOrderlyStopped(ctx, fence))
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence), "repeating the exact fence is idempotent")
	marker, err = durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, fence, marker.ManagedRuntimeFence)
	require.True(t, marker.Confirmed, "a duplicate marker write must not downgrade confirmation")
	require.NoError(t, durable.ConfirmManagedOrderlyStopped(ctx, fence))
	_, err = durable.db.ExecContext(ctx, `UPDATE managed_agents SET desired_state='terminated' WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	marker, err = durable.ManagedOrderlyStopMarker(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker, "non-running desired rows are not recoverable")
}

func TestPrepareManagedOrderlyStopRetryBumpsProjectionOnlyAfterTransition(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	fence := ManagedRuntimeFence{AgentID: "orderly-projection-agent", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 907, ProcessStartMarker: "start", ProcessIncarnation: "incarnation", RuntimeGeneration: "generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, durable, fence.AgentID, "orderly-projection-launch", fence)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, fence))
	require.NoError(t, durable.MarkManagedFailedForFence(ctx, fence, "PROCESS_STOP_FAILED", "stop failed"))
	before, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, durable.PrepareManagedOrderlyStopRetry(ctx, fence))
	after, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, before+1, after)
	require.NoError(t, durable.PrepareManagedOrderlyStopRetry(ctx, fence))
	unchanged, err := durable.GetProjectionVersion(ctx)
	require.NoError(t, err)
	require.Equal(t, after, unchanged)
}

func TestManagedOrderlyStopMarkerNewFenceStartsUnconfirmed(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	oldFence := ManagedRuntimeFence{AgentID: "orderly-confirmed-replace", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 905, ProcessStartMarker: "start-old", ProcessIncarnation: "incarnation-old", RuntimeGeneration: "generation-old", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, durable, oldFence.AgentID, "orderly-confirmed-replace-launch", oldFence)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, oldFence))
	require.NoError(t, durable.ConfirmManagedOrderlyStopped(ctx, oldFence))
	newFence := oldFence
	newFence.ProcessPID = 906
	newFence.ProcessStartMarker = "start-new"
	newFence.ProcessIncarnation = "incarnation-new"
	newFence.RuntimeGeneration = "generation-new"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,activity_state='idle',accepting_prompts=1,slot_held=1,lifecycle_state='running' WHERE agent_id=?`, newFence.ProcessPID, newFence.ProcessStartMarker, newFence.ProcessIncarnation, newFence.RuntimeGeneration, newFence.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, newFence))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, newFence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, newFence, marker.ManagedRuntimeFence)
	require.False(t, marker.Confirmed, "a new fence is a new unconfirmed stop intent")
}

func TestManagedOrderlyStopMarkerConsumesAfterFreshFenceCommit(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	oldFence := ManagedRuntimeFence{AgentID: "orderly-fresh-agent", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 905, ProcessStartMarker: "start-old", ProcessIncarnation: "incarnation-old", RuntimeGeneration: "generation-old", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, durable, oldFence.AgentID, "orderly-fresh-launch", oldFence)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, oldFence))

	freshFence := oldFence
	freshFence.ProcessPID = 906
	freshFence.ProcessStartMarker = "start-fresh"
	freshFence.ProcessIncarnation = "incarnation-fresh"
	freshFence.RuntimeGeneration = "generation-fresh"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,activity_state='idle',accepting_prompts=1,slot_held=1,lifecycle_state='running' WHERE agent_id=?`, freshFence.ProcessPID, freshFence.ProcessStartMarker, freshFence.ProcessIncarnation, freshFence.RuntimeGeneration, freshFence.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.ConsumeManagedOrderlyStopMarker(ctx, oldFence))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, oldFence.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
}

func TestManagedOrderlyStopMarkerConsumeKeepsNewerMarker(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	oldFence := ManagedRuntimeFence{AgentID: "orderly-newer-agent", PiSessionID: "session", SessionEpoch: 1, ProcessPID: 903, ProcessStartMarker: "start-old", ProcessIncarnation: "incarnation-old", RuntimeGeneration: "generation-old", ManifestRevision: 1, ManifestHash: testManifestHash}
	orderlyStopMarkerTestAgent(t, durable, oldFence.AgentID, "orderly-newer-launch", oldFence)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, oldFence))

	newFence := oldFence
	newFence.ProcessPID = 904
	newFence.ProcessStartMarker = "start-new"
	newFence.ProcessIncarnation = "incarnation-new"
	newFence.RuntimeGeneration = "generation-new"
	_, err := durable.db.ExecContext(ctx, `UPDATE managed_agents SET process_pid=?,process_start_marker=?,process_incarnation=?,runtime_generation=?,activity_state='idle',accepting_prompts=1,slot_held=1 WHERE agent_id=?`, newFence.ProcessPID, newFence.ProcessStartMarker, newFence.ProcessIncarnation, newFence.RuntimeGeneration, newFence.AgentID)
	require.NoError(t, err)
	require.NoError(t, durable.MarkManagedOrderlyStopped(ctx, newFence))

	require.NoError(t, durable.ConsumeManagedOrderlyStopMarker(ctx, oldFence))
	marker, err := durable.ManagedOrderlyStopMarker(ctx, oldFence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, newFence.RuntimeGeneration, marker.RuntimeGeneration, "a stale consume keeps the newer marker")
	require.NoError(t, durable.ConsumeManagedOrderlyStopMarker(ctx, newFence))
	marker, err = durable.ManagedOrderlyStopMarker(ctx, newFence.AgentID)
	require.NoError(t, err)
	require.Nil(t, marker)
}

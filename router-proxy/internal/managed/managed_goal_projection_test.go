package managed

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const managedGoalProjectionTestManifestHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type goalProjectionRPC struct {
	*launchRPC
	revision int64
	policy   map[string]any
	stale    bool
	fail     bool
}

func (r *goalProjectionRPC) ApplyPolicy(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	if r.fail {
		return nil, errors.New("projection unavailable")
	}
	policy, _ := envelope["policy"].(map[string]any)
	r.policy = policy
	goal, _ := policy["goal"].(map[string]any)
	revision, _ := goal["revision"].(int64)
	if !r.stale {
		r.revision = revision
	}
	_, err := r.launchRPC.ApplyPolicy(ctx, envelope)
	if err != nil {
		return nil, err
	}
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *goalProjectionRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	if r.fail {
		return nil, errors.New("projection unavailable")
	}
	receipt, err := r.launchRPC.WaitForPolicyApplied(ctx, generation, epoch)
	if err != nil {
		return nil, err
	}
	if r.policy != nil {
		inline, _ := r.policy["inline"].(map[string]any)
		recall, _ := r.policy["recall"].(map[string]any)
		settled, _ := r.policy["settled"].(map[string]any)
		goalLock, _ := r.policy["goal_lock"].(map[string]any)
		compaction, _ := r.policy["compaction"].(map[string]any)
		receipt["applied"] = map[string]any{
			"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"],
			"settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"],
		}
	}
	applied := receipt["applied"].(map[string]any)
	if r.revision > 0 {
		applied["goal_revision"] = r.revision
	}
	return receipt, nil
}

func readyGoalProjectionFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, *goalProjectionRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	launch := store.ManagedAgentRecord{AgentID: "goal-agent", Name: "goal-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "goal-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedGoalProjectionTestManifestHash}
	op := store.ManagedOperation{OperationID: "goal-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "goal-launch", CanonicalBodyHash: "goal-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, launch, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, launch.AgentID, launch.PiSessionID, store.ProcessIdentityRecord{PID: 99, StartMarker: "goal-marker", Incarnation: "goal-incarnation"}, "goal-generation"))
	stored, err := durable.ManagedAgent(ctx, launch.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "goal-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"goal-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"goal-snapshot"}`), SHA256: managedGoalProjectionTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	rpc := &goalProjectionRPC{launchRPC: &launchRPC{session: fence.PiSessionID, active: expectedManagedActiveTools(manifest)}}
	process := &launchProcess{identity: ProcessIdentity{PID: int(fence.ProcessPID), StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: directControlProbeOps{state: ProcessOwned}})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, durable, binding, fence, rpc
}

func TestManagedGoalDirectControlPersistsSharedSettingsBeforeProjection(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	settings := map[string]any{
		"goal_lock_enabled": true, "snooze_enabled": true, "snooze_duration_ms": int64(120_000),
		"snooze_turn_limit": int64(3), "snooze_budget_cap_ms": int64(300_000),
		"snooze_refill_yield_ms": int64(30_000), "snooze_refill_elapsed_ms": int64(60_000),
	}
	result, err := manager.DirectRootControl(ctx, fence.AgentID, "123e4567-e89b-42d3-a456-426614174003", "goal.control", map[string]any{"action": "set", "goal": "finish the migration", "settings": settings})
	require.NoError(t, err)
	state, stateOK := result.Details["goal_state"].(map[string]any)
	require.True(t, stateOK)
	require.Equal(t, true, state["goal_lock_enabled"])
	require.Equal(t, int64(120_000), state["snooze_duration_ms"])
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, true, policy.Config.Settings.GoalLockEnabled)
	require.NotNil(t, policy.Config.Settings.SnoozeTurnLimit)
	require.EqualValues(t, 3, *policy.Config.Settings.SnoozeTurnLimit)
	projected := rpc.policy["goal"].(map[string]any)["settings"].(map[string]any)
	require.Equal(t, true, projected["goalLockEnabled"])
	require.Equal(t, int64(120_000), projected["snoozeDurationMs"])

	_, err = manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174004", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "invalid settings", "settings": map[string]any{}}, Fence: fence})
	require.Error(t, err)
	stored, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, stored.DesiredRevision)
}

func TestManagedGoalProjectionAppliesCurrentRevisionAndKeepsFailurePending(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, rpc := readyGoalProjectionFixture(t)
	result, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174001", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the migration"}, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	state, stateOK := result.Details["goal_state"].(map[string]any)
	require.True(t, stateOK)
	require.Equal(t, true, state["enabled"])
	policy, err := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 1, policy.DesiredRevision)
	require.EqualValues(t, 1, policy.AppliedRevision)

	rpc.revision = 1 // A delayed receipt from revision one cannot settle revision two.
	rpc.stale = true
	pending, err := manager.executeManagedGoalControl(ctx, binding, RootControlInvocation{RequestID: "123e4567-e89b-42d3-a456-426614174002", OperationID: "goal.control", Arguments: map[string]any{"action": "set", "goal": "finish the validation"}, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "ok", pending.Status)
	pendingState, pendingStateOK := pending.Details["goal_state"].(map[string]any)
	require.True(t, pendingStateOK)
	require.Equal(t, "pending", pendingState["projection_state"])
	policy, err = durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
	require.NoError(t, err)
	require.EqualValues(t, 2, policy.DesiredRevision)
	require.EqualValues(t, 1, policy.AppliedRevision)
	require.Equal(t, "pending", policy.ProjectionState)
	require.Equal(t, "GOAL_PROJECTION_FAILED", policy.LastErrorCode)

	// The failed post-commit projection is retried from the durable revision;
	// it does not create a third desired revision or rerun a mutable control.
	rpc.stale = false
	require.Eventually(t, func() bool {
		current, lookupErr := durable.ManagedGoalPolicyFor(ctx, fence.AgentID)
		return lookupErr == nil && current.DesiredRevision == 2 && current.AppliedRevision == 2 && current.ProjectionState == "applied"
	}, 3*time.Second, 10*time.Millisecond)
}

package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const managedInlineAllowanceTestManifestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type inlineAllowanceRPC struct {
	*launchRPC
	policy map[string]any
}

func (r *inlineAllowanceRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.policy, _ = envelope["policy"].(map[string]any)
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *inlineAllowanceRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	inline, _ := r.policy["inline"].(map[string]any)
	recall, _ := r.policy["recall"].(map[string]any)
	settled, _ := r.policy["settled"].(map[string]any)
	goalLock, _ := r.policy["goal_lock"].(map[string]any)
	compaction, _ := r.policy["compaction"].(map[string]any)
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{
		"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"],
		"settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"],
	}}, nil
}

func readyInlineAllowanceFixture(t *testing.T) (*Manager, *managedBinding, store.ManagedRuntimeFence, *inlineAllowanceRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "inline-agent", Name: "inline-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "inline-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedInlineAllowanceTestManifestHash}
	op := store.ManagedOperation{OperationID: "inline-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "inline-launch", CanonicalBodyHash: "inline-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 100, StartMarker: "inline-marker", Incarnation: "inline-incarnation"}, "inline-generation"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "inline-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"inline-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"inline-snapshot"}`), SHA256: managedInlineAllowanceTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	rpc := &inlineAllowanceRPC{launchRPC: &launchRPC{session: fence.PiSessionID, active: expectedManagedActiveTools(manifest)}}
	process := &launchProcess{identity: ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, binding, fence, rpc
}

func TestManagedInlineAllowanceProjectsRequestedCreditBeforeSuccess(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, rpc := readyInlineAllowanceFixture(t)
	// Default deny remains in force until the managed inline setting is enabled.
	_, err := manager.executeRootControlHandler(ctx, binding, nil, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174004", OperationID: "policy.inline_allowance",
		Arguments: map[string]any{"toolName": "bash", "usageCount": int64(1)}, Fence: fence,
	}, false)
	require.Error(t, err)
	require.NoError(t, manager.SetManagedInlineTools(ctx, fence.AgentID, true))

	result, err := manager.executeRootControlHandler(ctx, binding, nil, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174003", OperationID: "policy.inline_allowance",
		Arguments: map[string]any{"toolName": "bash", "usageCount": int64(2)}, Fence: fence,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	inline := rpc.policy["inline"].(map[string]any)
	require.Equal(t, true, inline["enabled"])
	require.Equal(t, int64(2), inline["credits"].(map[string]any)["bash"])
}

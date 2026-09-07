package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	agent := ManagedAgentRecord{AgentID: "failed-agent", Name: "reserved-name", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: "hash"}
	op := ManagedOperation{OperationID: "failed-operation", SessionIdentityHash: "session", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "failed-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
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

func TestMarkManagedStoppedClearsLiveProjectionAndSlot(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "stoppable", Name: "stoppable", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: "hash"}
	op := ManagedOperation{OperationID: "stoppable-operation", SessionIdentityHash: "session", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "stoppable-request", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
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

func TestManagedLaunchReservesNameSlotAndOperationAtomically(t *testing.T) {
	ctx := context.Background()
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{})
	require.NoError(t, err)
	defer durable.Close()
	agent := ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, ManifestRevision: 1, ManifestHash: "hash"}
	op := ManagedOperation{OperationID: "123", SessionIdentityHash: "session", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	var conflict *ManagedIdempotencyConflictError
	require.ErrorAs(t, durable.ReserveManagedLaunch(ctx, agent, op), &conflict)
	stored, err := durable.ManagedOperation(ctx, "123")
	require.NoError(t, err)
	require.Equal(t, "hash", stored.CanonicalBodyHash)

	second := agent
	second.AgentID, second.Name = "beta", "beta"
	secondOp := op
	secondOp.OperationID, secondOp.RequestID = "456", "456"
	var slot *ManagedSlotLimitError
	require.ErrorAs(t, durable.ReserveManagedLaunchWithLimit(ctx, second, secondOp, 1), &slot)
}

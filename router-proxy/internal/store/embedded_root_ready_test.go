package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedRootStartingBoundReadyCASAndCrashReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	agent := ManagedAgentRecord{AgentID: "ready-root", Name: "ready-root", Cwd: t.TempDir(), SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, Runtime: "embedded", PiSessionID: "saved-session", SessionEpoch: 1, RuntimeGeneration: "saved-generation", ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "ready-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "ready-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	b := EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: 1, Generation: agent.RuntimeGeneration}
	require.Error(t, s.ReadyEmbeddedRoot(ctx, b, op.OperationID), "ready before bound is forbidden")
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "root"))
	b = claimEmbeddedFixture(t, s, b)
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "root"), "owned bound retry is idempotent")
	saved, err := s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", saved.LifecycleState)
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "root"), "cold reopen of bound reservation")
	wrong := b
	wrong.Generation = "stale-generation"
	require.Error(t, s.ReadyEmbeddedRoot(ctx, wrong, op.OperationID))
	_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_embedded_ready BEFORE UPDATE OF lifecycle_state ON main.managed_agents WHEN NEW.agent_id='ready-root' BEGIN SELECT RAISE(ABORT,'injected embedded ready fault'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, s.ReadyEmbeddedRoot(ctx, b, op.OperationID), "injected embedded ready fault")
	saved, err = s.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", saved.LifecycleState)
	// TEMP fault disappears with the writer connection. Retry on a distinct
	// Store after close, rather than fixing and retrying within one process.
	require.Equal(t, agent.PiSessionID, saved.PiSessionID)
	require.Equal(t, agent.SessionEpoch, saved.SessionEpoch)
	require.Equal(t, agent.RuntimeGeneration, saved.RuntimeGeneration)
	var operationState string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM managed_http_operations WHERE operation_id=?`, op.OperationID).Scan(&operationState))
	require.Equal(t, "pending", operationState)
	require.NoError(t, s.Close())
	fresh, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer fresh.Close()
	saved, err = fresh.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "starting", saved.LifecycleState)
	require.Equal(t, agent.PiSessionID, saved.PiSessionID)
	require.Equal(t, agent.SessionEpoch, saved.SessionEpoch)
	require.Equal(t, agent.RuntimeGeneration, saved.RuntimeGeneration)
	require.NoError(t, fresh.db.QueryRow(`SELECT state FROM managed_http_operations WHERE operation_id=?`, op.OperationID).Scan(&operationState))
	require.Equal(t, "pending", operationState)
	staleOwner := b
	staleOwner.OwnerToken--
	require.Error(t, fresh.BindEmbeddedSession(ctx, staleOwner, "root"))
	require.Error(t, fresh.ReadyEmbeddedRoot(ctx, staleOwner, op.OperationID))
	require.Error(t, fresh.ReadyEmbeddedRoot(ctx, wrong, op.OperationID))
	require.NoError(t, fresh.BindEmbeddedSession(ctx, b, "root"), "cold bound retry retains original owner")
	require.NoError(t, fresh.ReadyEmbeddedRoot(ctx, b, op.OperationID))
	require.NoError(t, fresh.ReadyEmbeddedRoot(ctx, b, op.OperationID), "ready retry is idempotent")
	saved, err = fresh.ManagedAgent(ctx, b.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", saved.LifecycleState)
	require.Equal(t, agent.PiSessionID, saved.PiSessionID)
	require.Equal(t, agent.SessionEpoch, saved.SessionEpoch)
	require.Equal(t, agent.RuntimeGeneration, saved.RuntimeGeneration)
	require.Error(t, fresh.ReadyEmbeddedRoot(ctx, staleOwner, op.OperationID))
	require.Error(t, fresh.ReadyEmbeddedRoot(ctx, wrong, op.OperationID))
}

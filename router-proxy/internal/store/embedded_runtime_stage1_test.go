package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedRuntimeLaunchAtomicityAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	agent := ManagedAgentRecord{AgentID: "runtime-choice", Name: "runtime-choice", Cwd: t.TempDir(), Runtime: "embedded", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5"}, PiSessionID: "session", SessionEpoch: 1, RuntimeGeneration: "gen", ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "runtime-choice-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "runtime-choice-launch", CanonicalBodyHash: "body", ResponseStatus: 202, ResponseBody: []byte(`{}`)}
	_, err = s.db.Exec(`CREATE TRIGGER reject_runtime_choice BEFORE INSERT ON managed_agents WHEN NEW.runtime='embedded' BEGIN SELECT RAISE(ABORT,'runtime fault'); END`)
	require.NoError(t, err)
	require.Error(t, s.ReserveManagedLaunch(ctx, agent, op))
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM managed_http_operations WHERE operation_id=?`, op.OperationID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM managed_agents WHERE agent_id=?`, agent.AgentID).Scan(&n))
	require.Zero(t, n)
	_, err = s.db.Exec(`DROP TRIGGER reject_runtime_choice`)
	require.NoError(t, err)
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	saved, err := s.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "embedded", saved.Runtime)
	_, err = s.db.Exec(`UPDATE managed_agents SET runtime='rpc' WHERE agent_id=?`, agent.AgentID)
	require.Error(t, err, "existing identity cannot change runtime")
	require.Error(t, s.ReserveManagedLaunch(ctx, ManagedAgentRecord{AgentID: agent.AgentID, Name: agent.Name, Runtime: "rpc", SelectedModel: agent.SelectedModel, ManifestHash: agent.ManifestHash}, ManagedOperation{OperationID: "different", Method: "POST", RequestID: "different", ResponseBody: []byte(`{}`)}))
	saved, err = s.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "embedded", saved.Runtime)
	stock := agent
	stock.AgentID, stock.Name, stock.Runtime = "stock-default", "stock-default", ""
	stockOp := op
	stockOp.OperationID, stockOp.RequestID = "stock-default-launch", "stock-default-launch"
	require.NoError(t, s.ReserveManagedLaunch(ctx, stock, stockOp))
	stockSaved, err := s.ManagedAgent(ctx, stock.AgentID)
	require.NoError(t, err)
	require.Equal(t, "rpc", stockSaved.Runtime)
}

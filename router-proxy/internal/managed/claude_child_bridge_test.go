package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/stretchr/testify/require"
)

func TestClaudeChildBridgeAttemptAndCheckedAdmission(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	parent := subagentTestParent(t, db, "bridge-parent")
	child := store.EmbeddedBinding{AgentID: "bridge-child", SessionID: "bridge-child-session", Epoch: 1, Generation: parent.Generation}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{AgentID: child.AgentID, ParentAgentID: parent.AgentID, ParentSessionID: parent.SessionID, ParentEpoch: parent.Epoch, ParentGeneration: parent.Generation, Description: "task", SubagentType: "general-purpose", Status: "running"}))
	calls := 0
	bridge, cleanup, err := newClaudeChildBridge(db, parent, child, 1, "owner", "process", t.TempDir(), t.TempDir(), func(ctx context.Context, call ClaudeCodeMCPCall, tool agent.AgentTool, args map[string]any, recheck func(context.Context) error) (json.RawMessage, error) {
		calls++
		require.NoError(t, recheck(ctx))
		require.Equal(t, "read", tool.Name)
		return json.RawMessage(`{"content":[{"type":"text","text":"checked"}]}`), nil
	})
	require.NoError(t, err)
	defer cleanup()
	require.Len(t, bridge.grant().Tools, 4)
	call := ClaudeCodeMCPCall{Binding: child, Owner: "owner", Incarnation: "process", ToolUseID: "call", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)}
	_, err = bridge.execute(ctx, call)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	invalid := call
	invalid.Incarnation = "old"
	_, err = bridge.execute(ctx, invalid)
	require.Error(t, err)
	invalid = call
	invalid.Name = "mecha_worker"
	_, err = bridge.execute(ctx, invalid)
	require.Error(t, err)
	invalid = call
	invalid.Arguments = json.RawMessage(`{}`)
	_, err = bridge.execute(ctx, invalid)
	require.Error(t, err)
	bridge.attempt = 2
	_, err = bridge.execute(ctx, call)
	require.Error(t, err)
	bridge.attempt = 1
	require.NoError(t, db.UpdateManagedSubagentAttempt(ctx, parent, child.AgentID, 1, "running", store.ManagedSubagentResult{Status: "stopped"}))
	_, err = bridge.execute(ctx, call)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

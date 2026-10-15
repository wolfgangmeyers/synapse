package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sky-valley/pi/ai/providers"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestStopSubagentToolExecution(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_stop_tool.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-stop-tool")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	stopTool := lookupTool(t, tools, "stop_subagent")

	// Missing agent_id argument
	_, err = stopTool.Execute(ctx, "call-stop-missing", map[string]any{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent_id is required")

	// Create a running child record
	childRecord := store.ManagedSubagentRecord{
		AgentID:          "child-to-stop",
		Attempt:          1,
		ParentAgentID:    parent.AgentID,
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		Description:      "subagent to be stopped",
		SubagentType:     "general-purpose",
		Status:           "running",
	}
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, childRecord))

	// Execute stop_subagent
	res, err := stopTool.Execute(ctx, "call-stop-ok", map[string]any{"agent_id": "child-to-stop"}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content)

	tc, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)

	var resp map[string]string
	err = json.Unmarshal([]byte(tc.Text), &resp)
	require.NoError(t, err)
	require.Equal(t, "stopped", resp["status"])
	require.Equal(t, "child-to-stop", resp["agent_id"])

	// Check store status
	updated, err := db.ManagedSubagent(ctx, parent, "child-to-stop")
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, "stopped", updated.Status)
}

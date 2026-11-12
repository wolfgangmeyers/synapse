package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestWorkflowAssignDeactivateReplayWithSameOperationID(t *testing.T) {
	ctx := context.Background()
	mgr, s, owner, _ := setupWorkflowAdminToolsFixture(t)
	defer s.Close()
	defer mgr.Close(ctx)

	target := subagentTestParent(t, s, "deactivate-replay-target")
	tool := mgr.embeddedWorkflowAssignTool(owner)
	args := map[string]any{"agent_id": target.AgentID, "action": "deactivate"}

	first, err := tool.Execute(ctx, "deactivate-replay", args, nil)
	require.NoError(t, err)
	replay, err := tool.Execute(ctx, "deactivate-replay", args, nil)
	require.NoError(t, err)
	require.Equal(t, first, replay)

	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(replay.Content[0].(ai.TextContent).Text), &response))
	require.Equal(t, "ok", response["status"])
	require.Equal(t, target.AgentID, response["agent_id"])
	require.Nil(t, response["active_workflow_id"])
	require.Nil(t, response["active_workflow_state_id"])
}

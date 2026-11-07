package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPiParentChildUsesCurrentClaudeDefault(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "pi-parent-default-switch")
	_, err := s.db.ExecContext(ctx, `UPDATE managed_agents SET selected_model_provider='google', selected_model_id='parent-model' WHERE agent_id=?`, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.SetDefaultModel(ctx, &ManagedModel{Provider: "claude-code", ID: "haiku"}))
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: "switch-child", ParentAgentID: parent.AgentID, Status: "queued"}))
	require.NoError(t, s.SetDefaultModel(ctx, &ManagedModel{Provider: "google", ID: "other"}))
	child, err := s.ManagedAgent(ctx, "switch-child")
	require.NoError(t, err)
	require.Equal(t, ManagedModel{Provider: "claude-code", ID: "haiku"}, child.SelectedModel)
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: "pi-inherited-child", ParentAgentID: parent.AgentID, Status: "queued"}))
	child, err = s.ManagedAgent(ctx, "pi-inherited-child")
	require.NoError(t, err)
	require.Equal(t, ManagedModel{Provider: "google", ID: "parent-model"}, child.SelectedModel)
}

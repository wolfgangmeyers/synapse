package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedAgentActiveWorkflowState(t *testing.T) {
	ctx := context.Background()
	s, fence := readinessSourceFixture(t)
	agentID := fence.AgentID

	// Verify initially empty
	agent, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Empty(t, agent.ActiveWorkflowStateID)

	// Create a workflow state
	state, err := s.CreateWorkflowState(ctx, WorkflowStateRequest{
		Name:        "Build State",
		Description: "Build description",
		EntryText:   "Now beginning build phase...",
	})
	require.NoError(t, err)

	// Set active workflow state
	err = s.SetManagedAgentActiveWorkflowState(ctx, agentID, &state.ID)
	require.NoError(t, err)

	agent, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, state.ID, agent.ActiveWorkflowStateID)

	// Clear active workflow state
	err = s.SetManagedAgentActiveWorkflowState(ctx, agentID, nil)
	require.NoError(t, err)

	agent, err = s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.Empty(t, agent.ActiveWorkflowStateID)
}

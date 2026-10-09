package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedAgentWorktreeStore(t *testing.T) {
	ctx := context.Background()
	s, fence := readinessSourceFixture(t)
	agentID := fence.AgentID

	loaded, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.False(t, loaded.Worktree)
	require.Empty(t, loaded.WorktreeBasePath)
	require.Empty(t, loaded.WorktreeBranch)

	all, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, all)

	// Update worktree columns
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET worktree=1, worktree_base_path='/tmp/repo', worktree_branch='feature/test' WHERE agent_id=?`, agentID)
	require.NoError(t, err)

	updated, err := s.ManagedAgent(ctx, agentID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	require.True(t, updated.Worktree)
	require.Equal(t, "/tmp/repo", updated.WorktreeBasePath)
	require.Equal(t, "feature/test", updated.WorktreeBranch)

	allUpdated, err := s.ManagedAgents(ctx)
	require.NoError(t, err)
	var found *ManagedAgentRecord
	for _, a := range allUpdated {
		if a.AgentID == agentID {
			found = a
			break
		}
	}
	require.NotNil(t, found)
	require.True(t, found.Worktree)
	require.Equal(t, "/tmp/repo", found.WorktreeBasePath)
	require.Equal(t, "feature/test", found.WorktreeBranch)
}

package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Admission must establish the shared identity before any transport lease or
// embedded bootstrap. Runtime hydration is a read, not an identity repair path.
func TestManagedSubagentAdmissionUrgencyHydration(t *testing.T) {
	for _, state := range []string{"new", "active", "retired"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			s, _ := embeddedRedStore(t)
			parent := subagentParent(t, s, "admission-parent")
			id := "subagent-exact-ID"
			if state != "new" {
				_, err := s.db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at,urgent,identity_active) VALUES(?,?,1,?)`, id, "2026-01-01T00:00:00Z", state == "active")
				require.NoError(t, err)
			}
			require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued"}))
			agent, err := s.ManagedAgent(ctx, id)
			require.NoError(t, err, "actual child hydration must work without a shared lease or bootstrap")
			require.Equal(t, id, agent.AgentID)
			require.Equal(t, state == "active", agent.Urgent)
			var active bool
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT identity_active FROM agents WHERE agent_id=?`, id).Scan(&active))
			require.True(t, active)
			_, err = s.SetAgentUrgency(ctx, id, true)
			require.NoError(t, err, "admitted exact ID is writable")
			_, err = s.SetAgentUrgency(ctx, "subagent-exact-id", true)
			require.ErrorIs(t, err, ErrAgentNotFound, "identity must remain case-sensitive")
		})
	}
}

func TestManagedSubagentAdmissionUrgencyRollback(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "rollback-parent")
	// Fail after the managed identity insert, at child metadata admission.
	_, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_child_metadata BEFORE INSERT ON managed_subagents BEGIN SELECT RAISE(ABORT,'reject child metadata'); END`)
	require.NoError(t, err)
	for _, retired := range []bool{false, true} {
		id := "rollback-new"
		if retired {
			id = "rollback-retired"
			_, err = s.db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at,urgent,identity_active) VALUES(?,?,1,0)`, id, "2026-01-01T00:00:00Z")
			require.NoError(t, err)
		}
		err = s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued"})
		require.ErrorContains(t, err, "reject child metadata")
		var count int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id=?`, id).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM agents WHERE agent_id=?`, id).Scan(&count))
		if retired {
			require.Equal(t, 1, count)
			var active, urgent bool
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT identity_active,urgent FROM agents WHERE agent_id=?`, id).Scan(&active, &urgent))
			require.False(t, active)
			require.True(t, urgent, "failed admission must roll back reactivation")
		} else {
			require.Zero(t, count)
		}
	}
}

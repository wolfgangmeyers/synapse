package managed

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAdmissionFailsClosedOnPolicyReadErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("workflow lookup", func(t *testing.T) {
		db, b, _, _ := wakePersistenceFixtureWithClaim(t, false)
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		require.ErrorContains(t, admitWorkflowStateTool(canceled, db, b.AgentID, "scratchpad"), "workflow policy lookup failed")
	})
	t.Run("worker tools lookup", func(t *testing.T) {
		db, b, _, path := wakePersistenceFixtureWithClaim(t, false)
		raw, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		defer raw.Close()
		_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, b.AgentID, "2026-01-01T00:00:00Z")
		require.NoError(t, err)
		_, err = raw.Exec(`DROP TABLE managed_agent_tools`)
		require.NoError(t, err)
		require.ErrorContains(t, admitWorkflowStateTool(ctx, db, b.AgentID, "scratchpad"), "worker tool policy lookup failed")
	})
	t.Run("parent workflow lookup", func(t *testing.T) {
		db, b, _, path := wakePersistenceFixtureWithClaim(t, false)
		raw, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		defer raw.Close()
		_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, b.AgentID, "2026-01-01T00:00:00Z")
		require.NoError(t, err)
		_, err = raw.Exec(`UPDATE managed_agents SET parent_agent_id='missing-parent' WHERE agent_id=?`, b.AgentID)
		require.NoError(t, err)
		require.ErrorContains(t, admitWorkflowStateTool(ctx, db, b.AgentID, "scratchpad"), "parent workflow policy lookup failed")
	})
}

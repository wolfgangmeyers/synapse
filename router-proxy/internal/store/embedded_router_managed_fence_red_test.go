package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Independent behavioral probe: the old 065 row must not authorize writes
// merely because its own current bit was left set during managed restart.
func TestEmbeddedManagedCurrentRowCannotOutliveManagedFence(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	old := embeddedManagedAgent(t, s, "embedded-stale-managed")
	_, err := s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES(?,?,?,?,?,1)`, old.AgentID, old.SessionID, old.Epoch, old.Generation, "root")
	require.NoError(t, err)
	old = claimEmbeddedFixture(t, s, old)
	first := redAppend(t, s, old, "user", "committed before replacement")
	next := old
	next.SessionID = "replacement-session"
	next.Epoch++
	next.Generation = "replacement-generation"
	_, err = s.db.Exec(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, next.AgentID)
	require.NoError(t, err)
	var session, generation string
	var epoch int64
	require.NoError(t, s.db.QueryRow(`SELECT pi_session_id,session_epoch,runtime_generation FROM managed_agents WHERE agent_id=?`, old.AgentID).Scan(&session, &epoch, &generation))
	require.Equal(t, next.SessionID, session)
	require.Equal(t, next.Epoch, epoch)
	require.Equal(t, next.Generation, generation)
	_, err = s.AppendEmbeddedEntry(ctx, old, "user", []byte("stale callback"))
	require.Error(t, err)
	require.Error(t, s.RecordEmbeddedEffectIntent(ctx, old, "stale-intent", []byte("effect")))
	require.Error(t, s.CommitEmbeddedCheckpoint(ctx, old, first.Sequence, first.Digest, []byte("stale checkpoint")))
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	_, err = reopened.ReopenEmbedded(ctx, old)
	require.Error(t, err, "reopen must consult managed row rather than 065 current bit alone")
}

func TestEmbeddedManagedOrphanSessionInsertRejected(t *testing.T) {
	s, _ := embeddedRedStore(t)
	_, err := s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES('nonexistent-managed-agent','orphan-session',1,'generation','root',1)`)
	require.Error(t, err, "065 must reference the authoritative managed_agents row")
	require.Zero(t, embeddedRedCount(t, s, "embedded_sessions"))
}

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedSubagentRoleMigrationRejectsNewFKDebtAndRestoresConnection(t *testing.T) {
	ctx := context.Background()
	all := DefaultMigrations()
	idx := migrationIndex(t, all, "085_embedded_subagent_role")
	path := filepath.Join(t.TempDir(), "rollback.db")
	s, err := Open(ctx, path, Options{Migrations: all[:idx]})
	require.NoError(t, err)
	parent := subagentParent(t, s, "rollback-parent")
	redAppend(t, s, parent, "user", "preserve me")
	bad := all[idx]
	bad.SQL += ` INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest) VALUES('missing','missing',1,'missing',1,'genesis','user',X'01','digest');`
	err = s.applyEmbeddedSubagentRoleMigration085(ctx, bad)
	require.ErrorContains(t, err, "introduced foreign key violation")
	var enabled, legacy, applied int
	require.NoError(t, s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled))
	require.Equal(t, 1, enabled)
	require.NoError(t, s.db.QueryRowContext(ctx, `PRAGMA legacy_alter_table`).Scan(&legacy))
	require.Zero(t, legacy)
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='085_embedded_subagent_role'`).Scan(&applied))
	require.Zero(t, applied)
	require.Len(t, redReopen(t, s, parent).Entries, 1)
	require.NoError(t, s.applyEmbeddedSubagentRoleMigration085(ctx, all[idx]))
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE id='085_embedded_subagent_role'`).Scan(&applied))
	require.Equal(t, 1, applied)
	require.NoError(t, s.Close())
}

func TestEmbeddedSubagentRoleMigrationPreservesPopulatedGraph(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	all := DefaultMigrations()
	idx := migrationIndex(t, all, "085_embedded_subagent_role")
	before, err := Open(ctx, path, Options{Migrations: all[:idx]})
	require.NoError(t, err)
	parent := subagentParent(t, before, "migration-parent")
	entry := redAppend(t, before, parent, "user", "persist history")
	require.NoError(t, before.CommitEmbeddedCheckpoint(ctx, parent, entry.Sequence, entry.Digest, []byte("checkpoint")))
	_, err = before.db.ExecContext(ctx, `INSERT INTO embedded_own_reminder_calls(call_id,agent_id,session_id,epoch,role,action,args_digest,state,result_json) VALUES(?,?,?,?,?,'list','hash','observed','{}')`, "upgrade-reminder", parent.AgentID, parent.SessionID, parent.Epoch, "worker")
	require.NoError(t, err)
	childID := "subagent-migration-parent-one"
	require.NoError(t, before.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: childID, ParentAgentID: parent.AgentID, Status: "queued", MaxTurns: 0}))
	require.NoError(t, before.Close())
	upgraded, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer upgraded.Close()
	snapshot := redReopen(t, upgraded, parent)
	require.Len(t, snapshot.Entries, 1)
	require.Equal(t, []byte("persist history"), snapshot.Entries[0].Body)
	require.NotNil(t, snapshot.Checkpoint)
	require.Equal(t, []byte("checkpoint"), snapshot.Checkpoint.Body)
	require.Equal(t, 1, embeddedRedCount(t, upgraded, "embedded_own_reminder_calls WHERE call_id='upgrade-reminder'"))
	var issue string
	require.ErrorIs(t, upgraded.db.QueryRowContext(ctx, `PRAGMA foreign_key_check`).Scan(&issue), sql.ErrNoRows)
	var triggerCount int
	require.NoError(t, upgraded.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='embedded_sessions' AND name IN ('embedded_goal_continuation_session_invalidate','embedded_goal_snooze_preference_session_invalidate','embedded_own_reminder_session_cleanup')`).Scan(&triggerCount))
	require.Equal(t, 3, triggerCount)
	child := EmbeddedBinding{AgentID: childID, SessionID: childID + "-session", Epoch: 1, Generation: parent.Generation}
	require.Error(t, upgraded.BindEmbeddedSession(ctx, child, "worker"), "child identity cannot gain worker tools")
	require.Error(t, upgraded.BindEmbeddedSession(ctx, parent, "subagent"), "parent identity cannot gain child role")
	require.NoError(t, upgraded.BindEmbeddedSession(ctx, child, "subagent"))
	child = claimEmbeddedFixture(t, upgraded, child)
	role, err := upgraded.SavedEmbeddedSessionRole(ctx, child)
	require.NoError(t, err)
	require.Equal(t, "subagent", role)
	_, err = upgraded.AppendEmbeddedEntry(ctx, child, "user", []byte("child history"))
	require.NoError(t, err)
	require.NoError(t, upgraded.Close())
	again, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer again.Close()
	history := redReopen(t, again, child)
	require.Len(t, history.Entries, 1)
	role, err = again.SavedEmbeddedSessionRole(ctx, child)
	require.NoError(t, err)
	require.Equal(t, "subagent", role)
	require.ErrorIs(t, again.db.QueryRowContext(ctx, `PRAGMA foreign_key_check`).Scan(&issue), sql.ErrNoRows)
}

package store

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
	"modernc.org/sqlite/lib"
)

func TestLegacyRPCPreflightPreservesReopenedDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	for _, id := range []string{"parent", "child"} {
		_, err = s.db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?, '2020')`, id)
		require.NoError(t, err)
		desired, lifecycle := "running", "running"
		if id == "parent" {
			desired, lifecycle = "terminated", "terminated"
		}
		_, err = s.db.Exec(`INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,runtime) VALUES(?,?,0,'/tmp','p','m',?,?,'stopped',0,?,2,1,'hash','2020','2020','rpc')`, id, id, desired, lifecycle, id+"-session")
		require.NoError(t, err)
	}
	_, err = s.db.Exec(`INSERT INTO managed_subagents(agent_id,parent_agent_id,parent_session_id,parent_epoch,parent_generation,description,subagent_type,status,max_turns,created_at,updated_at) VALUES('child','parent','parent-session',2,'generation','in flight','worker','running',1,'2020','2020')`)
	require.NoError(t, err)
	var version int
	require.NoError(t, s.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	require.NoError(t, s.Close())
	_, err = Open(ctx, path, Options{RejectLegacyRPC: true})
	require.ErrorContains(t, err, "unsupported legacy RPC agent")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var status, message, updated, session string
	var terminal sql.NullString
	require.NoError(t, db.QueryRow(`SELECT status,error,updated_at,terminal_at FROM managed_subagents WHERE agent_id='child'`).Scan(&status, &message, &updated, &terminal))
	require.Equal(t, "running", status)
	require.Empty(t, message)
	require.Equal(t, "2020", updated)
	require.False(t, terminal.Valid)
	require.NoError(t, db.QueryRow(`SELECT pi_session_id FROM managed_agents WHERE agent_id='parent'`).Scan(&session))
	require.Equal(t, "parent-session", session)
	var after int
	require.NoError(t, db.QueryRow(`PRAGMA user_version`).Scan(&after))
	require.Equal(t, version, after)
}

func TestLegacyRPCPreflightBeforeRuntimeColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE managed_agents(agent_id TEXT PRIMARY KEY, pi_session_id TEXT); INSERT INTO managed_agents VALUES('old-child','unchanged-session'); CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES('unchanged');`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = Open(ctx, path, Options{RejectLegacyRPC: true})
	require.ErrorContains(t, err, "unsupported legacy RPC agent old-child")
	db, err = sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	var session, sentinel string
	require.NoError(t, db.QueryRow(`SELECT pi_session_id FROM managed_agents`).Scan(&session))
	require.NoError(t, db.QueryRow(`SELECT value FROM sentinel`).Scan(&sentinel))
	require.Equal(t, "unchanged-session", session)
	require.Equal(t, "unchanged", sentinel)
	var migrations int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='schema_migrations'`).Scan(&migrations))
	require.Zero(t, migrations)
}

func TestLegacyRPCPreflightFailsClosedOnUnreadableDatabase(t *testing.T) {
	ctx := context.Background()
	t.Run("corrupt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		contents := []byte("not a sqlite database")
		require.NoError(t, os.WriteFile(path, contents, 0600))
		s, err := Open(ctx, path, Options{RejectLegacyRPC: true})
		require.Nil(t, s)
		require.ErrorContains(t, err, "legacy RPC preflight")
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, contents, after)
	})
	t.Run("inaccessible", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private.db")
		db, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		_, err = db.Exec(`CREATE TABLE managed_agents(agent_id TEXT PRIMARY KEY, runtime TEXT); INSERT INTO managed_agents VALUES('private-agent', 'rpc')`)
		require.NoError(t, err)
		require.NoError(t, db.Close())
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(path, 0000))
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		if f, err := os.Open(path); err == nil {
			f.Close()
			t.Skip("current user can read mode 000 files")
		} else {
			require.ErrorIs(t, err, os.ErrPermission)
		}
		s, err := Open(ctx, path, Options{RejectLegacyRPC: true})
		require.Nil(t, s)
		require.ErrorContains(t, err, "legacy RPC preflight")
		var sqliteErr *sqlite.Error
		require.ErrorAs(t, err, &sqliteErr)
		require.Equal(t, sqlite3.SQLITE_CANTOPEN, sqliteErr.Code()&0xff)
		require.NoError(t, os.Chmod(path, 0600))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, contents, after)
	})
}

func TestLegacyRPCPreflightDetectsUncheckpointedWALRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE managed_agents(agent_id TEXT PRIMARY KEY, runtime TEXT); PRAGMA wal_checkpoint(TRUNCATE);`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO managed_agents VALUES('wal-agent','rpc')`)
	require.NoError(t, err)
	wal, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Positive(t, wal.Size())
	// An immutable reader ignores WAL, proving the row is not in the main file.
	mainOnly, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1")
	require.NoError(t, err)
	defer mainOnly.Close()
	var count int
	require.NoError(t, mainOnly.QueryRow(`SELECT count(*) FROM managed_agents`).Scan(&count))
	require.Zero(t, count)

	s, err := Open(ctx, path, Options{RejectLegacyRPC: true})
	require.Nil(t, s)
	require.ErrorContains(t, err, "unsupported legacy RPC agent wal-agent")
	var runtime string
	require.NoError(t, db.QueryRow(`SELECT runtime FROM managed_agents WHERE agent_id='wal-agent'`).Scan(&runtime))
	require.Equal(t, "rpc", runtime)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='schema_migrations'`).Scan(&count))
	require.Zero(t, count)
}

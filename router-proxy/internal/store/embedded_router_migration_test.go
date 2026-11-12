package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const embedded065ID = "065_embedded_pi_go_sessions"

func TestEmbeddedMigration065ApplyReopenRollbackRetryAndCoexistence(t *testing.T) {
	ctx := context.Background()
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, embedded065ID)
	// Only the migrations before 065 belong in the pre-upgrade fixture.
	// Later migrations are applied by the default reopen below.
	require.Equal(t, "062_managed_control_delivery_receipts", migrations[idx-1].ID)
	path := filepath.Join(t.TempDir(), "migration.db")
	prefix := migrations[:idx]
	s, err := Open(ctx, path, Options{Migrations: prefix})
	require.NoError(t, err)
	require.Zero(t, embeddedRedCount(t, s, "schema_migrations WHERE id='"+embedded065ID+"'"))
	// Model a row created by the pre-065 RPC-era schema. The current managed
	// launch API correctly requires migration 065, so seed historical state
	// directly rather than routing this fixture through that API.
	const agentID = "embedded-migration-agent"
	const sessionID = "embedded-migration-session"
	const generation = "embedded-migration-generation"
	_, err = s.db.Exec(`INSERT INTO managed_agents (
		agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,
		desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,
		session_epoch,process_pid,process_start_marker,process_incarnation,runtime_generation,
		manifest_revision,manifest_hash,created_at,updated_at
	) VALUES (?, ?, 1, ?, 'openai-codex', 'gpt-5', 'running', 'running', 'idle', 1, ?, 7,
		321, 'start', 'incarnation', ?, 1, ?, '2024-01-01T00:00:00.000Z', '2024-01-01T00:00:00.000Z')`,
		agentID, agentID, t.TempDir(), sessionID, generation, testManifestHash)
	require.NoError(t, err)
	managed := EmbeddedBinding{AgentID: agentID, SessionID: sessionID, Epoch: 7, Generation: generation}
	require.NoError(t, s.Close())
	broken := append([]Migration(nil), migrations...)
	broken[idx].SQL = "CREATE TABLE embedded_rollback_probe (id INTEGER);" + broken[idx].SQL + "; INSERT INTO absent_embedded_test_table VALUES (1);"
	_, err = Open(ctx, path, Options{Migrations: broken})
	require.ErrorContains(t, err, embedded065ID)
	s, err = Open(ctx, path, Options{Migrations: prefix})
	require.NoError(t, err)
	var absent int
	require.NoError(t, s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('embedded_rollback_probe','embedded_sessions')`).Scan(&absent))
	require.Zero(t, absent, "failed migration must roll back every DDL statement")
	var historicalSession string
	require.NoError(t, s.db.QueryRow(`SELECT pi_session_id FROM managed_agents WHERE agent_id=?`, managed.AgentID).Scan(&historicalSession))
	require.Equal(t, managed.SessionID, historicalSession)
	require.Zero(t, embeddedRedCount(t, s, "schema_migrations WHERE id='"+embedded065ID+"'"))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "schema_migrations WHERE id='"+embedded065ID+"'"))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	require.Equal(t, 1, embeddedRedCount(t, s, "schema_migrations WHERE id='"+embedded065ID+"'"))
	row, err := s.ManagedAgent(ctx, managed.AgentID)
	require.NoError(t, err)
	require.Equal(t, "rpc", row.Runtime)
	require.Equal(t, managed.Generation, row.RuntimeGeneration)
	for _, table := range []string{"embedded_sessions", "embedded_entries", "embedded_append_keys", "embedded_checkpoints", "embedded_effects", "embedded_attempts", "embedded_acks"} {
		require.Zero(t, embeddedRedCount(t, s, table))
	}
}

func TestEmbeddedMigration065Constraints(t *testing.T) {
	s, _ := embeddedRedStore(t)
	ctx := context.Background()
	b := redBind(t, s, "root", "session", 1, "generation", "root")
	_, err := s.db.Exec(`INSERT INTO embedded_sessions VALUES ('root','other',2,'generation','root',1)`)
	require.Error(t, err, "one current bind per agent")
	_, err = s.db.Exec(`INSERT INTO embedded_entries VALUES ('root','session',1,'wrong',1,'genesis','user',X'01','hash')`)
	require.Error(t, err, "entry generation must be bound")
	_, err = s.db.Exec(`INSERT INTO embedded_entries VALUES ('root','session',1,'generation',2,'genesis','user',X'01','hash')`)
	require.Error(t, err, "entry sequence and parent must be contiguous")
	first := redAppend(t, s, b, "user", "prompt")
	_, err = s.db.Exec(`INSERT INTO embedded_append_keys VALUES ('wrong-generation','root','session',1,'wrong',1)`)
	require.Error(t, err, "append key must reference exact entry generation")
	_, err = s.db.Exec(`INSERT INTO embedded_checkpoints VALUES ('root','session',1,'generation',1,'wrong',X'01','digest')`)
	require.Error(t, err, "checkpoint ancestor must match entry")
	require.NoError(t, s.CommitEmbeddedCheckpoint(ctx, b, 1, first.Digest, []byte("summary")))
	_, err = s.db.Exec(`INSERT INTO embedded_effects VALUES ('bad','root','session',1,'generation',X'01','observed',NULL)`)
	require.Error(t, err, "observed effect needs a result")
	envelope := testEnvelope("schema-mail", "mail body")
	envelope.To = "root"
	_, err = s.Enqueue(ctx, envelope)
	require.NoError(t, err)
	a := reserveEmbeddedMailTest(t, s, b, "schema-mail")
	_, err = s.db.Exec(`INSERT INTO embedded_attempts(attempt_id,message_id,message_incarnation_id,snapshot_id,sender,accepted_body,accepted_digest,agent_id,session_id,epoch,generation,append_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "bad-attempt", a.MessageID, a.MessageIncarnationID, a.SnapshotID, a.Sender, a.Body, a.AcceptedDigest, "other", a.Binding.SessionID, a.Binding.Epoch, a.Binding.Generation, "bad-key")
	require.Error(t, err, "cannot allocate second incarnation or wrong recipient")
	_, err = s.db.Exec(`INSERT INTO embedded_acks VALUES (?,?,?,?,?,?,?)`, a.AttemptID, a.MessageID, b.AgentID, b.SessionID, b.Epoch, b.Generation, first.Sequence)
	require.Error(t, err, "ACK without exact append key must fail")
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, a.AttemptID, []byte("mail body")))
	_, err = s.db.Exec(`INSERT INTO embedded_acks VALUES (?,?,?,?,?,?,?)`, a.AttemptID, "wrong-message", b.AgentID, b.SessionID, b.Epoch, b.Generation, first.Sequence)
	require.Error(t, err, "ACK must match exact attempt and message")
}

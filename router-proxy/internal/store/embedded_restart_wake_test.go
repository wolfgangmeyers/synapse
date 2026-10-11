package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedRestartWakeMigrationApplyReopenRollback(t *testing.T) {
	ctx := context.Background()
	migrations := DefaultMigrations()
	idx := migrationIndex(t, migrations, "080_embedded_restart_wakes")
	path := filepath.Join(t.TempDir(), "migration.db")
	prefix := migrations[:idx]
	s, err := Open(ctx, path, Options{Migrations: prefix})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	broken := append([]Migration(nil), migrations...)
	broken[idx].SQL += "; INSERT INTO absent_embedded_restart_wake_table VALUES (1);"
	_, err = Open(ctx, path, Options{Migrations: broken})
	require.ErrorContains(t, err, "080_embedded_restart_wakes")
	s, err = Open(ctx, path, Options{Migrations: prefix})
	require.NoError(t, err)
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('embedded_process_generation_counter','embedded_process_generations','embedded_restart_wake_slots')`).Scan(&count))
	require.Zero(t, count, "failed migration must roll back all three tables")
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='080_embedded_restart_wakes'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, s.Close())

	for i := 0; i < 2; i++ {
		s, err = Open(ctx, path, Options{})
		require.NoError(t, err)
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='080_embedded_restart_wakes'`).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('embedded_process_generation_counter','embedded_process_generations','embedded_restart_wake_slots')`).Scan(&count))
		require.Equal(t, 3, count)
		require.NoError(t, s.Close())
	}
}

func TestEmbeddedRestartWakeProcessGenerationAndMigration(t *testing.T) {
	s, path := embeddedRedStore(t)
	ctx := context.Background()
	var applied int
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id='080_embedded_restart_wakes'`).Scan(&applied))
	require.Equal(t, 1, applied)
	// Query the live WAL connection, not the database file copied without -wal.
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM schema_migrations WHERE id >= '063' AND id < '081' ORDER BY id`)
	require.NoError(t, err)
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	t.Logf("private WAL-aware applied IDs: %v", ids)
	require.Equal(t, []string{
		"065_embedded_pi_go_sessions",
		"066_embedded_goal_calls",
		"067_embedded_goal_applied_receipts",
		"068_embedded_goal_evaluations",
		"069_embedded_goal_continuations",
		"070_embedded_goal_snooze_preferences",
		"071_embedded_own_reminder_calls",
		"072_embedded_reminder_effect_owner",
		"073_embedded_own_compaction_requests",
		"074_embedded_own_compaction_claim",
		"075_embedded_own_reminder_crud",
		"076_embedded_interrupted_turn_notice",
		"077_embedded_nonmail_provider_entries",
		"078_conversation_send_receipt",
		"079_conversation_send_receipt_capacity",
		"080_embedded_restart_wakes",
	}, ids)
	a, err := s.MintEmbeddedProcessGeneration(ctx, "verified-incarnation-a")
	require.NoError(t, err)
	again, err := s.MintEmbeddedProcessGeneration(ctx, "verified-incarnation-a")
	require.NoError(t, err)
	require.Equal(t, a, again)
	b, err := s.MintEmbeddedProcessGeneration(ctx, "verified-incarnation-b")
	require.NoError(t, err)
	require.Equal(t, a+1, b)
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	old, err := reopened.MintEmbeddedProcessGeneration(ctx, "verified-incarnation-a")
	require.NoError(t, err)
	require.Equal(t, a, old, "a stale incarnation cannot mint a new generation")
	c, err := reopened.MintEmbeddedProcessGeneration(ctx, "verified-incarnation-c")
	require.NoError(t, err)
	require.Equal(t, b+1, c)
	_, err = reopened.db.ExecContext(ctx, `UPDATE embedded_process_generation_counter SET generation=9223372036854775807 WHERE singleton=1`)
	require.NoError(t, err)
	_, err = reopened.MintEmbeddedProcessGeneration(ctx, "overflow")
	require.ErrorContains(t, err, "exhausted")
}

func TestEmbeddedRestartWakeCrashWindowsAndOwnerFence(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	binding := redBind(t, s, "root", "session", 1, "runtime-1", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "process-one")
	require.NoError(t, err)
	// Crash before reservation: nothing has been published.
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots`).Scan(&n))
	require.Zero(t, n)
	plan, err := s.ReserveEmbeddedRestartWake(ctx, binding, g, "wake-a", "root-idle", "digest-a")
	require.NoError(t, err)
	require.Equal(t, "planned", plan.State)
	duplicate, err := s.ReserveEmbeddedRestartWake(ctx, binding, g, "wake-a", "root-idle", "digest-a")
	require.NoError(t, err)
	require.Equal(t, plan, duplicate)
	_, err = s.ReserveEmbeddedRestartWake(ctx, binding, g, "wake-a", "worker-idle", "digest-a")
	require.Error(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, binding, g, "wake-a", "root-idle", "digest-other")
	require.Error(t, err)
	// After reservation, before append/check: planned work is eligible once.
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	_, err = s.db.Exec(`UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00Z' WHERE agent_id='root'`)
	require.NoError(t, err)
	token, err := s.AcquireEmbeddedOwner(ctx, binding, "after-restart", 60_000_000_000)
	require.NoError(t, err)
	stale := binding
	stale.Epoch++
	binding.OwnerToken = token
	require.Error(t, s.ClaimEmbeddedRestartWake(ctx, stale, g, "wake-a"))
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, binding, g, "wake-a"))
	require.Error(t, s.ClaimEmbeddedRestartWake(ctx, binding, g, "wake-a"), "no second automatic call")
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, stale, g, "wake-a", 1, "invalid"))
	// A crash after claim, even before SDK dispatch, is UNKNOWN. Never replay.
	require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, binding, g, "wake-a"))
	got, err := s.EmbeddedRestartWake(ctx, binding, g, "wake-a")
	require.NoError(t, err)
	require.Equal(t, "unknown", got.State)
	require.Error(t, s.ClaimEmbeddedRestartWake(ctx, binding, g, "wake-a"))
}

func TestEmbeddedRestartWakeSlotsAndNoResurrection(t *testing.T) {
	ctx := context.Background()
	var nWakeSlots int
	s, _ := embeddedRedStore(t)
	defer s.Close()
	binding := redBind(t, s, "worker", "session", 1, "runtime", "worker")
	for i, key := range []string{"p1", "p2", "p3"} {
		g, err := s.MintEmbeddedProcessGeneration(ctx, key)
		require.NoError(t, err)
		id := "wake-" + key
		_, err = s.ReserveEmbeddedRestartWake(ctx, binding, g, id, "worker-idle", "digest")
		require.NoError(t, err)
		if i == 1 {
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, binding, g, id))
			final := wakeFinalFixture(t)
			seq, digest, err := s.embeddedHead(ctx, binding)
			require.NoError(t, err)
			entry, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, binding, g, id, seq, digest, "final-"+id, final)
			require.NoError(t, err)
			require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, binding, g, id, entry.Sequence, entry.Digest))
		}
	}
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id='worker'`).Scan(&nWakeSlots))
	require.Equal(t, 2, nWakeSlots)
	_, err := s.ReserveEmbeddedRestartWake(ctx, binding, 1, "wake-p1", "worker-idle", "digest")
	require.Error(t, err, "discarded older generation must not resurrect")
	_, err = s.ReserveEmbeddedRestartWake(ctx, binding, 3, "wake-p3-other", "worker-idle", "digest")
	require.Error(t, err, "one current wake per process")
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id='worker'`).Scan(&nWakeSlots))
	require.Equal(t, 2, nWakeSlots)
	_, err = s.db.Exec(`DELETE FROM managed_agents WHERE agent_id='worker'`)
	require.NoError(t, err)
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id='worker'`).Scan(&nWakeSlots))
	require.Zero(t, nWakeSlots)
}

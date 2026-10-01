package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// Each fixture owns its SQLite file. Close the sole WAL connection before
// reopening it; never copy a live .db without its WAL/SHM state.
type receiptLegacySnapshot struct {
	conversationJSON, deliveryJSON           string
	usedCount, usedBytes, maxCount, maxBytes int64
}

func receiptLegacyFixture(t *testing.T) (string, *deterministic.FixedClock, receiptLegacySnapshot) {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "legacy-router.db")
	migrations := DefaultMigrations()
	through081 := migrationIndex(t, migrations, "081_embedded_restart_wake_checked_origin") + 1
	legacy, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through081]})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacy.Close()) })
	_, err = lifecycleSend(legacy, "legacy-id", "original é payload")
	require.NoError(t, err)
	_, err = legacy.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=7,max_bytes=987654321 WHERE singleton=1`)
	require.NoError(t, err)
	var snapshot receiptLegacySnapshot
	require.NoError(t, legacy.db.QueryRowContext(ctx, `SELECT conversation_json,delivery_json FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&snapshot.conversationJSON, &snapshot.deliveryJSON))
	require.NoError(t, legacy.db.QueryRowContext(ctx, `SELECT used_count,used_bytes,max_count,max_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&snapshot.usedCount, &snapshot.usedBytes, &snapshot.maxCount, &snapshot.maxBytes))
	require.Equal(t, int64(1), snapshot.usedCount)
	require.Equal(t, int64(len("legacy-id")+len(snapshot.conversationJSON)+len(snapshot.deliveryJSON)), snapshot.usedBytes)
	require.Equal(t, int64(7), snapshot.maxCount)
	require.Equal(t, int64(987654321), snapshot.maxBytes)
	for _, id := range []string{"078_conversation_send_receipt", "079_conversation_send_receipt_capacity", "080_embedded_restart_wakes", "081_embedded_restart_wake_checked_origin"} {
		receiptMigrationCount(t, legacy, id, 1)
	}
	receiptMigrationCount(t, legacy, "082_conversation_send_receipt_lifecycle", 0)
	var journal string
	require.NoError(t, legacy.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal))
	require.Equal(t, "wal", journal)
	require.NoError(t, legacy.Close(), "checkpoint the owned WAL fixture before upgrading")
	require.NoError(t, clock.Advance(60*24*time.Hour))
	return path, clock, snapshot
}

func receiptMigrationCount(t *testing.T, s *Store, id string, want int) {
	t.Helper()
	var count int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=?`, id).Scan(&count))
	require.Equal(t, want, count, id)
}

func receiptLegacyUnchanged(t *testing.T, s *Store, before receiptLegacySnapshot) {
	t.Helper()
	var got receiptLegacySnapshot
	require.NoError(t, s.db.QueryRow(`SELECT conversation_json,delivery_json FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&got.conversationJSON, &got.deliveryJSON))
	require.NoError(t, s.db.QueryRow(`SELECT used_count,used_bytes,max_count,max_bytes FROM conversation_send_receipt_capacity WHERE singleton=1`).Scan(&got.usedCount, &got.usedBytes, &got.maxCount, &got.maxBytes))
	require.Equal(t, before, got, "migration must not rewrite the original serialized result or persisted counters/caps")
}

func TestReceiptLegacy081To082BaselineAndPayload(t *testing.T) {
	path, clock, before := receiptLegacyFixture(t)
	ctx := context.Background()
	baseline := clock.Now().UTC().Truncate(time.Millisecond)
	upgraded, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, upgraded.Close()) })
	for _, id := range []string{"078_conversation_send_receipt", "079_conversation_send_receipt_capacity", "080_embedded_restart_wakes", "081_embedded_restart_wake_checked_origin", "082_conversation_send_receipt_lifecycle"} {
		receiptMigrationCount(t, upgraded, id, 1)
	}
	receiptLegacyUnchanged(t, upgraded, before)
	var accepted, deadline string
	require.NoError(t, upgraded.db.QueryRow(`SELECT accepted_at,deadline_at FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&accepted, &deadline))
	require.Equal(t, formatTimestamp(baseline), accepted)
	require.Equal(t, formatTimestamp(baseline.Add(30*24*time.Hour)), deadline)
	var backed int
	require.NoError(t, upgraded.db.QueryRow(`SELECT receipt_backed FROM conversation_messages WHERE message_id='legacy-id'`).Scan(&backed))
	require.Equal(t, 1, backed, "old receipt-backed history must not become receipt-less fallback after pruning")
	require.NoError(t, upgraded.Close())
	require.NoError(t, clock.Advance(24*time.Hour))
	reopened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	receiptMigrationCount(t, reopened, "082_conversation_send_receipt_lifecycle", 1)
	receiptLegacyUnchanged(t, reopened, before)
	var reopenedAccepted, reopenedDeadline string
	require.NoError(t, reopened.db.QueryRow(`SELECT accepted_at,deadline_at FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&reopenedAccepted, &reopenedDeadline))
	require.Equal(t, accepted, reopenedAccepted, "reopen must not restart the baseline")
	require.Equal(t, deadline, reopenedDeadline)
}

func TestReceiptLegacy082FailureRollsBackSchemaAndBaseline(t *testing.T) {
	path, clock, before := receiptLegacyFixture(t)
	ctx := context.Background()
	migrations := DefaultMigrations()
	through081 := migrationIndex(t, migrations, "081_embedded_restart_wake_checked_origin") + 1
	legacy, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through081]})
	require.NoError(t, err)
	// Abort only the server-clock backfill. The real 082 SQL and runner remain
	// unchanged; DDL has already executed inside the runner's BEGIN IMMEDIATE.
	_, err = legacy.db.ExecContext(ctx, `CREATE TRIGGER fail_receipt_baseline BEFORE UPDATE ON conversation_send_receipts BEGIN SELECT RAISE(ABORT, 'owned 082 backfill failure'); END`)
	require.NoError(t, err)
	require.NoError(t, legacy.Close())
	_, err = Open(ctx, path, Options{Clock: clock})
	require.ErrorContains(t, err, "owned 082 backfill failure")
	inspect, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through081]})
	require.NoError(t, err)
	receiptMigrationCount(t, inspect, "082_conversation_send_receipt_lifecycle", 0)
	receiptLegacyUnchanged(t, inspect, before)
	for _, check := range []struct{ table, column string }{{"conversation_send_receipts", "accepted_at"}, {"conversation_send_receipts", "deadline_at"}, {"conversation_messages", "receipt_backed"}} {
		var count int
		require.NoError(t, inspect.db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, check.table, check.column).Scan(&count))
		require.Zero(t, count, check.table+"."+check.column)
	}
	var indexCount int
	require.NoError(t, inspect.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='conversation_send_receipts_deadline_idx'`).Scan(&indexCount))
	require.Zero(t, indexCount)
	_, err = inspect.db.ExecContext(ctx, `DROP TRIGGER fail_receipt_baseline`)
	require.NoError(t, err)
	require.NoError(t, inspect.Close())
	upgraded, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err, "the unmodified 082 should succeed after removing the owned fault")
	defer upgraded.Close()
	receiptMigrationCount(t, upgraded, "082_conversation_send_receipt_lifecycle", 1)
	receiptLegacyUnchanged(t, upgraded, before)
	var accepted, deadline string
	require.NoError(t, upgraded.db.QueryRow(`SELECT accepted_at,deadline_at FROM conversation_send_receipts WHERE message_id='legacy-id'`).Scan(&accepted, &deadline))
	require.Equal(t, formatTimestamp(clock.Now()), accepted)
	require.Equal(t, formatTimestamp(clock.Now().Add(30*24*time.Hour)), deadline)
}

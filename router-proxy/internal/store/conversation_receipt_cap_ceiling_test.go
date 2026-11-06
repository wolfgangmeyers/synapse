package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestReceiptCapCeilingUpgradeRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "cap-upgrade-fault.db")
	migrations := DefaultMigrations()
	through126 := migrationIndex(t, migrations, "126_conversation_receipt_thirty_minutes") + 1
	old, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through126]})
	require.NoError(t, err)
	_, err = lifecycleSend(old, "cap-fault-saved", "saved")
	require.NoError(t, err)
	count, bytes := lifecycleUsage(t, old)
	_, err = old.db.Exec(`UPDATE conversation_send_receipt_capacity SET max_bytes=314572800`)
	require.NoError(t, err)
	_, err = old.db.Exec(`CREATE TRIGGER fail_cap_127 BEFORE UPDATE ON conversation_send_receipt_capacity BEGIN SELECT RAISE(ABORT,'injected cap migration failure'); END`)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	_, err = Open(ctx, path, Options{Clock: clock})
	require.ErrorContains(t, err, "injected cap migration failure")
	inspect, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through126]})
	require.NoError(t, err)
	receiptMigrationCount(t, inspect, "127_conversation_receipt_cap_ceiling", 0)
	var maxBytes int64
	require.NoError(t, inspect.db.QueryRow(`SELECT max_bytes FROM conversation_send_receipt_capacity`).Scan(&maxBytes))
	require.Equal(t, int64(300*1024*1024), maxBytes)
	sweepReceiptAccounting(t, inspect, count, bytes)
	_, err = inspect.db.Exec(`DROP TRIGGER fail_cap_127`)
	require.NoError(t, err)
	require.NoError(t, inspect.Close())
	upgraded, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer upgraded.Close()
	receiptMigrationCount(t, upgraded, "127_conversation_receipt_cap_ceiling", 1)
	require.NoError(t, upgraded.db.QueryRow(`SELECT max_bytes FROM conversation_send_receipt_capacity`).Scan(&maxBytes))
	require.Equal(t, int64(200*1024*1024), maxBytes)
	sweepReceiptAccounting(t, upgraded, count, bytes)
}

// Exercise the actual 200 MiB boundary once. The padded JSON is still valid,
// and the capacity charge is calculated from the real stored SQLite bytes.
func TestReceiptCapCeilingUpgradeOverExistingUsage(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "over-cap.db")
	migrations := DefaultMigrations()
	through126 := migrationIndex(t, migrations, "126_conversation_receipt_thirty_minutes") + 1
	old, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through126]})
	require.NoError(t, err)
	first, err := lifecycleSend(old, "protected-small", "ordinary")
	require.NoError(t, err)
	_, err = lifecycleSend(old, "large-existing", "already accepted")
	require.NoError(t, err)
	_, bytes := lifecycleUsage(t, old)
	const ceiling = int64(200 * 1024 * 1024)
	padding := ceiling + 1 - bytes
	require.Positive(t, padding)
	err = old.runImmediate(ctx, func() error {
		if _, err := old.db.ExecContext(ctx, `UPDATE conversation_send_receipts SET conversation_json=conversation_json || printf('%.*c',?, ' ') WHERE message_id='large-existing'`, padding); err != nil {
			return err
		}
		_, err := old.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET used_bytes=used_bytes+?,max_bytes=? WHERE singleton=1`, padding, 300*1024*1024)
		return err
	})
	require.NoError(t, err)
	sweepReceiptAccounting(t, old, 2, ceiling+1)
	require.NoError(t, old.Close())

	upgraded, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer upgraded.Close()
	var maxBytes int64
	require.NoError(t, upgraded.db.QueryRow(`SELECT max_bytes FROM conversation_send_receipt_capacity`).Scan(&maxBytes))
	require.Equal(t, ceiling, maxBytes)
	sweepReceiptAccounting(t, upgraded, 2, ceiling+1)
	replay, err := lifecycleSend(upgraded, "protected-small", "ordinary")
	require.NoError(t, err)
	require.Equal(t, first.ConversationMessage, replay.ConversationMessage)
	_, err = lifecycleSend(upgraded, "over-cap-rejected", "new")
	require.ErrorAs(t, err, new(*ConversationReceiptCapacityError))
	sweepReceiptAccounting(t, upgraded, 2, ceiling+1)
	var rejected, deliveries int
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM messages WHERE id='over-cap-rejected'`).Scan(&rejected))
	require.Zero(t, rejected)
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&deliveries))
	require.Equal(t, 2, deliveries)
	require.NoError(t, clock.Advance(receiptHorizon))
	n, err := upgraded.PruneExpiredConversationReceipts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	sweepReceiptAccounting(t, upgraded, 0, 0)
	accepted, err := lifecycleSend(upgraded, "over-cap-recovered", "new")
	require.NoError(t, err)
	require.True(t, accepted.Created)
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&deliveries))
	require.Equal(t, 3, deliveries)
}

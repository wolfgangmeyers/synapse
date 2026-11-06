package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestReceiptThirtyMinuteUpgradePreservesCustomCapsAndWork(t *testing.T) {
	for _, tc := range []struct {
		name                string
		oldBytes, wantBytes int64
	}{
		{"old default", 64 * 1024 * 1024, 200 * 1024 * 1024},
		{"custom smaller", 12345678, 12345678},
		{"custom larger", 300 * 1024 * 1024, 200 * 1024 * 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "legacy.db")
			migrations := DefaultMigrations()
			through125 := migrationIndex(t, migrations, "125_claude_stdin_started") + 1
			old, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through125]})
			require.NoError(t, err)
			_, err = lifecycleSend(old, "old-queued", "old")
			require.NoError(t, err)
			require.NoError(t, clock.Advance(20*time.Minute))
			_, err = lifecycleSend(old, "recent-queued", "recent")
			require.NoError(t, err)
			_, err = lifecycleSend(old, "short-queued", "short")
			require.NoError(t, err)
			recentAcceptance := clock.Now()
			_, err = old.db.ExecContext(ctx, `UPDATE conversation_send_receipts SET deadline_at=strftime('%Y-%m-%dT%H:%M:%fZ',accepted_at,'+30 days')`)
			require.NoError(t, err)
			shortDeadline := formatTimestamp(recentAcceptance.Add(20 * time.Minute))
			_, err = old.db.ExecContext(ctx, `UPDATE conversation_send_receipts SET deadline_at=? WHERE message_id='short-queued'`, shortDeadline)
			require.NoError(t, err)
			_, err = old.db.ExecContext(ctx, `UPDATE conversation_send_receipt_capacity SET max_count=77,max_bytes=?`, tc.oldBytes)
			require.NoError(t, err)
			require.NoError(t, old.Close())
			require.NoError(t, clock.Advance(15*time.Minute))
			upgraded, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			var count, bytes, maxCount, maxBytes int64
			require.NoError(t, upgraded.db.QueryRow(`SELECT used_count,used_bytes,max_count,max_bytes FROM conversation_send_receipt_capacity`).Scan(&count, &bytes, &maxCount, &maxBytes))
			require.Equal(t, int64(2), count)
			require.Equal(t, int64(77), maxCount)
			require.Equal(t, tc.wantBytes, maxBytes)
			sweepReceiptAccounting(t, upgraded, count, bytes)
			var deadline string
			require.NoError(t, upgraded.db.QueryRow(`SELECT deadline_at FROM conversation_send_receipts WHERE message_id='recent-queued'`).Scan(&deadline))
			require.Equal(t, formatTimestamp(recentAcceptance.Add(receiptHorizon)), deadline)
			require.NoError(t, upgraded.db.QueryRow(`SELECT deadline_at FROM conversation_send_receipts WHERE message_id='short-queued'`).Scan(&deadline))
			require.Equal(t, shortDeadline, deadline)
			var deliveries, history int
			require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&deliveries))
			require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM conversation_messages`).Scan(&history))
			require.Equal(t, 3, deliveries)
			require.Equal(t, 3, history)
			require.NoError(t, upgraded.Close())
			reopened, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			defer reopened.Close()
			require.NoError(t, reopened.db.QueryRow(`SELECT deadline_at FROM conversation_send_receipts WHERE message_id='short-queued'`).Scan(&deadline))
			require.Equal(t, shortDeadline, deadline)
			receiptMigrationCount(t, reopened, "126_conversation_receipt_thirty_minutes", 1)
		})
	}
}

func TestReceiptThirtyMinuteUpgradeRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "fault.db")
	migrations := DefaultMigrations()
	through125 := migrationIndex(t, migrations, "125_claude_stdin_started") + 1
	old, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through125]})
	require.NoError(t, err)
	_, err = lifecycleSend(old, "saved", "body")
	require.NoError(t, err)
	_, err = old.db.Exec(`UPDATE conversation_send_receipt_capacity SET max_bytes=67108864`)
	require.NoError(t, err)
	_, err = old.db.Exec(`CREATE TRIGGER fail_receipt_126 BEFORE UPDATE ON conversation_send_receipts BEGIN SELECT RAISE(ABORT,'injected upgrade failure'); END`)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	_, err = Open(ctx, path, Options{Clock: clock})
	require.ErrorContains(t, err, "injected upgrade failure")
	inspect, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations[:through125]})
	require.NoError(t, err)
	receiptMigrationCount(t, inspect, "126_conversation_receipt_thirty_minutes", 0)
	var maxBytes, receipts, deliveries int
	require.NoError(t, inspect.db.QueryRow(`SELECT max_bytes FROM conversation_send_receipt_capacity`).Scan(&maxBytes))
	require.NoError(t, inspect.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
	require.NoError(t, inspect.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&deliveries))
	require.Equal(t, 64*1024*1024, maxBytes)
	require.Equal(t, 1, receipts)
	require.Equal(t, 1, deliveries)
	_, err = inspect.db.Exec(`DROP TRIGGER fail_receipt_126`)
	require.NoError(t, err)
	require.NoError(t, inspect.Close())
	upgraded, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer upgraded.Close()
	receiptMigrationCount(t, upgraded, "126_conversation_receipt_thirty_minutes", 1)
}

func TestReceiptThirtyMinuteUpgradeUsesPre082RecordedTime(t *testing.T) {
	path, clock, _ := receiptLegacyFixture(t) // send was recorded 60 days ago
	upgraded, err := Open(context.Background(), path, Options{Clock: clock})
	require.NoError(t, err)
	defer upgraded.Close()
	var receipts, messages, history int
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM conversation_send_receipts`).Scan(&receipts))
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&messages))
	require.NoError(t, upgraded.db.QueryRow(`SELECT count(*) FROM conversation_messages`).Scan(&history))
	require.Zero(t, receipts)
	require.Equal(t, 1, messages)
	require.Equal(t, 1, history)
	sweepReceiptAccounting(t, upgraded, 0, 0)
}

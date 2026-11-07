package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReceiptMigrationsFreshInstall(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"), Options{})
	require.NoError(t, err)
	defer s.Close()

	for _, id := range []string{
		"126_conversation_receipt_thirty_minutes",
		"127_conversation_receipt_cap_ceiling",
	} {
		var count int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE id=?`, id).Scan(&count))
		require.Equal(t, 1, count, "migration %s", id)
	}
	for _, name := range []string{"conversation_send_receipts", "conversation_send_receipt_capacity"} {
		var count int
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count))
		require.Equal(t, 1, count, "table %s", name)
	}
}

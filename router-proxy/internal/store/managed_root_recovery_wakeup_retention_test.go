package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedRootRecoveryWakeupRetentionIsBounded(t *testing.T) {
	durable, _, _, action := prepareManagedRootRecoveryWakeup(t)
	wakeup, err := durable.QueueManagedRootRecoveryWakeup(context.Background(), action.ActionID)
	require.NoError(t, err)
	require.NotNil(t, wakeup)
	_, err = durable.db.Exec(`UPDATE managed_root_recovery_wakeups SET expires_at='0001-01-01T00:00:00.000Z' WHERE action_id=?`, action.ActionID)
	require.NoError(t, err)
	removed, err := durable.PruneManagedRootRecovery(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	var count int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM managed_root_recovery_wakeups`).Scan(&count))
	require.Zero(t, count)
}

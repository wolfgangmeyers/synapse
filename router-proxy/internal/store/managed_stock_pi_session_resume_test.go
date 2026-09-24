package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration061AddsStockSessionReservationAndResumeArtifactLinks(t *testing.T) {
	durable := openTestStore(t)

	var reservations int
	require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='managed_stock_pi_session_reservations'`).Scan(&reservations))
	require.Equal(t, 1, reservations, "migration 061 must create the stock Pi session reservation ledger")

	for table, column := range map[string]string{
		"managed_orderly_stop_markers":  "resume_artifact_id",
		"managed_root_recovery_actions": "resume_artifact_id",
	} {
		var columns int
		require.NoError(t, durable.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&columns))
		require.Equalf(t, 1, columns, "%s must link the retained resume artifact", table)
	}
}

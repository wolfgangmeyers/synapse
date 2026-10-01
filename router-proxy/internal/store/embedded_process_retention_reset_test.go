package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedProcessMappingResetReleasesUnknownReferences(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := lifecycleEmbeddedFixture(t, s, "mapping-reset")
	old, err := s.MintEmbeddedProcessGeneration(ctx, "old-reset-process")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, old, "old-reset-wake", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, old, "old-reset-wake"))
	require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, b, old, "old-reset-wake"))
	for range EmbeddedProcessGenerationHistoryLimit * 2 {
		_, err = s.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
	}
	count, high := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit+1, count)
	next := b
	next.SessionID, next.Epoch, next.Generation, next.OwnerToken = "reset-session", b.Epoch+1, "reset-generation", 0
	_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, b.AgentID)
	require.NoError(t, err)
	require.NoError(t, s.ResetEmbeddedSession(ctx, b, next, "root"))
	count, after := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit, count)
	require.Equal(t, high, after)
	require.Zero(t, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
	_, err = s.MintEmbeddedProcessGeneration(ctx, "old-reset-process")
	var expired *EmbeddedProcessGenerationExpired
	require.ErrorAs(t, err, &expired)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, old, "old-reset-wake", "root-idle", "projection")
	require.Error(t, err, "reset does not revive an old UNKNOWN wake")
	requireGoalForeignKeys(t, s)
}

func TestEmbeddedProcessMappingSweepPrunesReleasedReferences(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	b := lifecycleEmbeddedFixture(t, s, "mapping-sweep")
	old, err := s.MintEmbeddedProcessGeneration(ctx, "old-sweep-process")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, old, "old-sweep-wake", "root-idle", "projection")
	require.NoError(t, err)
	for range EmbeddedProcessGenerationHistoryLimit * 2 {
		_, err = s.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
	}
	count, high := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit+1, count)
	// Model a reference retired by an older Store version, before its next sweep.
	_, err = s.db.ExecContext(ctx, `DELETE FROM embedded_restart_wake_slots WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	count, after := processGenerationCount(t, s)
	require.Equal(t, EmbeddedProcessGenerationHistoryLimit, count)
	require.Equal(t, high, after)
	requireGoalForeignKeys(t, s)
}

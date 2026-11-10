package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUILiveRetriesSampledHistoryBeforeNewerVersion(t *testing.T) {
	router, db, _ := newUILiveTestRouter(t)
	ctx := context.Background()
	reserveUILiveAgent(t, db, "alpha")
	require.NoError(t, router.uiLive.publishContext(ctx))
	require.NoError(t, db.MarkManagedFailed(ctx, "alpha", "FAIL", "failure"))
	sampled, err := router.uiLive.snapshot(ctx)
	require.NoError(t, err)
	facts := managedLifecycleHistoryFacts(router.uiLive.current, sampled)
	require.Len(t, facts, 1)
	_, err = db.RecordManagedCompleteHistoryWithPresentation(ctx, facts[0].agentID, "managed_lifecycle", "info", facts[0].message, facts[0].eventKey, facts[0].presentation)
	require.NoError(t, err)
	// Simulate an interrupted batch after its first durable write.
	router.uiLive.pending = sampled
	_, err = db.ReserveProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, router.uiLive.publishContext(ctx))
	require.NoError(t, router.uiLive.publishContext(ctx))
	entries, err := db.GetManagedCompleteHistory(ctx, "alpha")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

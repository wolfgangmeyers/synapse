package attachments

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorRunsStartupPeriodicAndShutdownSweeps(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store, err := New(Options{Directory: filepath.Join(t.TempDir(), "images"), Clock: clock, Retention: time.Hour})
	require.NoError(t, err)
	pending, err := store.WriteTemp("startup", 1, Input{MIMEType: "image/png", Bytes: pngFixture(1, 1)})
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(store.directory, pending.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))
	coordinator, err := NewCoordinator(store, nil, 10*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, coordinator.Start(context.Background()))
	require.True(t, coordinator.Running())
	require.Equal(t, 1, coordinator.SweepCount(), "startup sweep runs before the loop")
	_, _, err = store.Read(pending.ID)
	require.ErrorIs(t, err, os.ErrNotExist)
	deadline := time.Now().Add(time.Second)
	for coordinator.SweepCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	require.GreaterOrEqual(t, coordinator.SweepCount(), 2)
	beforeStop := coordinator.SweepCount()
	require.NoError(t, coordinator.Stop())
	require.False(t, coordinator.Running())
	require.Greater(t, coordinator.SweepCount(), beforeStop, "shutdown sweep runs after the loop stops")
	require.NoError(t, coordinator.Stop())
}

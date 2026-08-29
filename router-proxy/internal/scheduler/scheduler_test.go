package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestRunSkipsOverlappingSweepAndCapturesOneClockValue(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	var seen []time.Time
	scheduler, err := New(clock, func(_ context.Context, now time.Time) error {
		mu.Lock()
		calls++
		seen = append(seen, now)
		mu.Unlock()
		close(started)
		<-release
		return nil
	})
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() { _, runErr := scheduler.Run(context.Background()); result <- runErr }()
	<-started
	require.True(t, scheduler.Running())
	ran, err := scheduler.Run(context.Background())
	require.NoError(t, err)
	require.False(t, ran)
	require.NoError(t, clock.Advance(time.Minute))
	close(release)
	require.NoError(t, <-result)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, calls)
	require.Equal(t, []time.Time{time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}, seen)
}

func TestRunClearsRunningAfterFailure(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Now())
	want := context.Canceled
	scheduler, err := New(clock, func(ctx context.Context, _ time.Time) error { return ctx.Err() })
	require.NoError(t, err)
	ran, err := scheduler.Run(context.Background())
	require.True(t, ran)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = scheduler.Run(ctx)
	require.ErrorIs(t, err, want)
	require.False(t, scheduler.Running())
}

package deterministic_test

import (
	"errors"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestFixedClockRecordsCallsAndAdvancesMonotonically(t *testing.T) {
	start := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)

	require.Equal(t, start, clock.Now())
	require.NoError(t, clock.Advance(1500*time.Millisecond))
	want := start.Add(1500 * time.Millisecond)
	require.Equal(t, want, clock.Now())
	require.Equal(t, []time.Time{start, want}, clock.Calls())

	clock.ResetCalls()
	require.Empty(t, clock.Calls())
	require.Equal(t, want, clock.Now())
}

func TestFixedClockRejectsBackwardMovementWithoutChangingTime(t *testing.T) {
	start := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)

	err := clock.Advance(-time.Millisecond)
	require.EqualError(t, err, "deterministic clock cannot move backwards")
	require.Equal(t, start, clock.Now())
}

func TestSequenceIDSourceReturnsIDsInOrderAndRecordsExhaustion(t *testing.T) {
	source := deterministic.NewSequenceIDSource("id-1", "id-2")

	id, err := source.Next()
	require.NoError(t, err)
	require.Equal(t, "id-1", id)
	require.Equal(t, 1, source.Remaining())

	id, err = source.Next()
	require.NoError(t, err)
	require.Equal(t, "id-2", id)
	require.Equal(t, 0, source.Remaining())

	_, err = source.Next()
	require.ErrorIs(t, err, deterministic.ErrIDSourceExhausted)
	require.Equal(t, []string{"id-1", "id-2", "<exhausted>"}, source.Calls())

	source.ResetCalls()
	require.Empty(t, source.Calls())
}

func TestSequenceIDSourceCopiesInputAndCallLedger(t *testing.T) {
	ids := []string{"id-1"}
	source := deterministic.NewSequenceIDSource(ids...)
	ids[0] = "changed"

	id, err := source.Next()
	require.NoError(t, err)
	require.Equal(t, "id-1", id)

	calls := source.Calls()
	calls[0] = "changed"
	require.Equal(t, []string{"id-1"}, source.Calls())
}

func TestSequenceIDSourceExhaustionIsTheSentinelError(t *testing.T) {
	source := deterministic.NewSequenceIDSource()
	_, err := source.Next()
	require.True(t, errors.Is(err, deterministic.ErrIDSourceExhausted))
}

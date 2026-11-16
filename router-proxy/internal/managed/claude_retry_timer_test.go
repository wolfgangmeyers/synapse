package managed

import (
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

type advancingRetryClock struct {
	*deterministic.FixedClock
	advance time.Duration
}

func (c *advancingRetryClock) AfterFuncAt(deadline time.Time, f func()) func() {
	_ = c.Advance(c.advance)
	return c.FixedClock.AfterFuncAt(deadline, f)
}

func TestClaudeRetryTimerRegistrationAcrossClockAdvance(t *testing.T) {
	for _, advance := range []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second} {
		t.Run(advance.String(), func(t *testing.T) {
			start := time.Unix(100, 0)
			clock := &advancingRetryClock{FixedClock: deterministic.NewFixedClock(start), advance: advance}
			fired, cancel := claudeRetryTimer(clock, start.Add(10*time.Second))
			defer cancel()
			if advance < 10*time.Second {
				select {
				case <-fired:
					t.Fatal("fired early")
				default:
				}
				require.NoError(t, clock.Advance(10*time.Second-advance))
			}
			select {
			case <-fired:
			case <-time.After(time.Second):
				t.Fatal("deadline did not fire")
			}
		})
	}
}

func TestClaudeRetrySchedulePreservesAbsoluteEligibility(t *testing.T) {
	f := newFailureFixture(t)
	clock := deterministic.NewFixedClock(time.Unix(100, 0))
	f.m.clock = clock
	deadline := clock.Now().Add(10 * time.Second)
	require.NoError(t, clock.Advance(5*time.Second))
	lane := f.m.laneFor("absolute-retry")
	lane.serial.Lock()
	f.m.scheduleClaudeMailRetryAt("absolute-retry", deadline)
	require.Equal(t, deadline, lane.claudeRetryDeadline)
	lane.serial.Unlock()
}

// A relative-only clock can advance before registration. The fallback cannot
// infer that partial advance; it must still fire once its registered delay ends.
type relativeAdvancingClock struct{ clock *deterministic.FixedClock }

func (c *relativeAdvancingClock) Now() time.Time { return c.clock.Now() }
func (c *relativeAdvancingClock) AfterFunc(delay time.Duration, f func()) func() {
	_ = c.clock.Advance(5 * time.Second)
	return c.clock.AfterFunc(delay, f)
}
func TestClaudeRetryRelativeTimerPartialAdvanceCanWakeLate(t *testing.T) {
	start := time.Unix(100, 0)
	clock := &relativeAdvancingClock{clock: deterministic.NewFixedClock(start)}
	fired, cancel := claudeRetryTimer(clock, start.Add(10*time.Second))
	defer cancel()
	require.NoError(t, clock.clock.Advance(5*time.Second))
	select {
	case <-fired:
		t.Fatal("relative timer unexpectedly aligned")
	default:
	}
	require.NoError(t, clock.clock.Advance(5*time.Second))
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("relative timer did not fire")
	}
}

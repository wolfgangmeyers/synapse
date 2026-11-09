//go:build !linux

package managed

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeReapedCommandWaitsForAbsence(t *testing.T) {
	waitErr := errors.New("child exit")
	waited, confirmed, probes := false, false, 0
	err := waitClaudeCodeReapedCommand(func() error { waited = true; return waitErr }, func() ProcessProbeState {
		require.True(t, waited)
		probes++
		if probes < 3 {
			return ProcessPresent
		}
		return ProcessAbsent
	}, func() { confirmed = true }, time.Second)
	require.ErrorIs(t, err, waitErr)
	require.NotErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
	require.True(t, confirmed)
	require.Equal(t, 3, probes)
}

func TestClaudeReapedCommandRetainsUnconfirmedEvidence(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessPresent, ProcessOwned, ProcessProbeState("unknown")} {
		t.Run(string(state), func(t *testing.T) {
			waitErr := errors.New("child exit")
			confirmed, probes := false, 0
			err := waitClaudeCodeReapedCommand(func() error { return waitErr }, func() ProcessProbeState {
				probes++
				return state
			}, func() { confirmed = true }, 25*time.Millisecond)
			require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
			require.ErrorIs(t, err, waitErr)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.Greater(t, probes, 1)
			require.False(t, confirmed)
		})
	}
}

package managed

import (
	"context"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

type cleanupMatrixOps struct {
	state         ProcessProbeState
	termErr       error
	leaderlessErr error
	signals       []syscall.Signal
	waits         []ProcessProbeState
}

func (ops *cleanupMatrixOps) Probe(ProcessIdentity) ProcessProbe {
	return ProcessProbe{State: ops.state}
}
func (ops *cleanupMatrixOps) CloseProcessInput(Process) error { return nil }
func (ops *cleanupMatrixOps) SignalVerifiedProcessGroup(_ ProcessIdentity, signal syscall.Signal) error {
	if ops.termErr != nil {
		return ops.termErr
	}
	ops.signals = append(ops.signals, signal)
	if signal == syscall.SIGTERM {
		ops.state = ProcessPresent
	} else if signal == syscall.SIGKILL {
		ops.state = ProcessAbsent
	}
	return nil
}
func (ops *cleanupMatrixOps) SignalVerifiedProcessGroupAfterLeaderExit(ProcessIdentity) error {
	if ops.leaderlessErr != nil {
		return ops.leaderlessErr
	}
	ops.signals = append(ops.signals, syscall.SIGKILL)
	ops.state = ProcessAbsent
	return nil
}
func (ops *cleanupMatrixOps) WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe {
	if len(ops.waits) > 0 {
		state := ops.waits[0]
		ops.waits = ops.waits[1:]
		ops.state = state
	}
	return ProcessProbe{State: ops.state}
}

func TestReapRestartGroupRequiresSameAttemptOwnedProofForEscalation(t *testing.T) {
	identity := ProcessIdentity{PID: 701, StartMarker: "start", Incarnation: "incarnation"}
	tests := []struct {
		name          string
		initial       ProcessProbeState
		waits         []ProcessProbeState
		leaderlessErr error
		wantResult    bool
		wantSignals   []syscall.Signal
	}{
		{
			name:        "owned term then leaderless present kill then gone",
			initial:     ProcessOwned,
			waits:       []ProcessProbeState{ProcessOwned, ProcessPresent, ProcessAbsent},
			wantResult:  true,
			wantSignals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL},
		},
		{
			name:        "owned term then stubborn owned leader kill then gone",
			initial:     ProcessOwned,
			waits:       []ProcessProbeState{ProcessOwned, ProcessOwned, ProcessAbsent},
			wantResult:  true,
			wantSignals: []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL},
		},
		{
			name:        "mismatch sends no signal",
			initial:     ProcessMismatch,
			waits:       []ProcessProbeState{ProcessMismatch},
			wantSignals: nil,
		},
		{
			name:        "unknown sends no signal",
			initial:     ProcessUnknown,
			waits:       []ProcessProbeState{ProcessUnknown},
			wantSignals: nil,
		},
		{
			name:        "present sends no signal across calls",
			initial:     ProcessPresent,
			waits:       []ProcessProbeState{ProcessPresent},
			wantSignals: nil,
		},
		{
			name:          "leaderless escalation error sends term only",
			initial:       ProcessOwned,
			waits:         []ProcessProbeState{ProcessOwned, ProcessPresent},
			leaderlessErr: syscall.EPERM,
			wantSignals:   []syscall.Signal{syscall.SIGTERM},
		},
		{
			name:        "already gone is idempotent",
			initial:     ProcessAbsent,
			wantResult:  true,
			wantSignals: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := &cleanupMatrixOps{state: test.initial, waits: append([]ProcessProbeState(nil), test.waits...), leaderlessErr: test.leaderlessErr}
			manager := &Manager{processOps: ops, config: Config{Limits: Limits{GracefulStop: 1, TermStop: 1}}}
			got := manager.reapRestartGroup(context.Background(), identity, nil)
			require.Equal(t, test.wantResult, got)
			require.Equal(t, test.wantSignals, ops.signals)
		})
	}
}

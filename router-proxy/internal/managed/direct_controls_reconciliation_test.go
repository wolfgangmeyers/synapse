package managed

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type directControlProbeOps struct{ state ProcessProbeState }

func (ops directControlProbeOps) Probe(ProcessIdentity) ProcessProbe {
	return ProcessProbe{State: ops.state}
}
func (directControlProbeOps) CloseProcessInput(Process) error { return nil }
func (directControlProbeOps) SignalVerifiedProcessGroup(ProcessIdentity, syscall.Signal) error {
	return nil
}
func (ops directControlProbeOps) WaitForProcessGroupEmpty(context.Context, ProcessIdentity, <-chan error) ProcessProbe {
	return ProcessProbe{State: ops.state}
}

func TestDirectControlReconcilesUnownedReadyBindingWithoutWaitSignal(t *testing.T) {
	for _, state := range []ProcessProbeState{ProcessAbsent, ProcessPresent} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			manager, binding, fence, _, databasePath := readyInlineAllowanceFixture(t)
			sourceAttempt := queueManagedParentFinalForTest(t, manager.store, fence, "process-loss-source-turn")
			manager.processOps = directControlProbeOps{state: state}
			binding.waitSignal = make(chan struct{}) // The process never delivered Wait.
			published := make(chan struct{}, 2)
			manager.SetProjectionPublisher(func() { published <- struct{}{} })

			before, err := manager.store.GetProjectionVersion(ctx)
			require.NoError(t, err)
			err = manager.SetManagedInlineTools(ctx, fence.AgentID, true)
			require.EqualError(t, err, "managed agent is not ready for controls")

			agent, err := manager.store.ManagedAgent(ctx, fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "running", agent.LifecycleState)
			require.Equal(t, "stopped", agent.ActivityState)
			require.Equal(t, "PROCESS_EXITED", agent.LastErrorCode)
			require.False(t, agent.AcceptingPrompts)
			require.True(t, agent.SlotHeld)
			retry, retryErr := manager.store.ManagedCrashRecovery(ctx, fence.AgentID)
			require.NoError(t, retryErr)
			require.NotEmpty(t, retry.RetryAt)
			require.Equal(t, "The managed process is no longer available.", agent.LastErrorMessage)
			requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")

			after, err := manager.store.GetProjectionVersion(ctx)
			require.NoError(t, err)
			require.Equal(t, before+1, after)
			select {
			case <-published:
			case <-time.After(time.Second):
				t.Fatal("process-loss reconciliation did not publish the failed projection")
			}
			select {
			case <-published:
				t.Fatal("process-loss reconciliation published more than once")
			case <-time.After(25 * time.Millisecond):
			}

			_, _, err = manager.currentDirectControlBinding(ctx, fence.AgentID)
			require.EqualError(t, err, "managed agent is not ready for controls")

			_, stillBound := manager.bindingFor(fence.AgentID)
			require.False(t, stillBound)
			// A repeated unowned-control reconciliation must not reopen the exact
			// interrupted source work or publish another process-loss transition.
			requireManagedParentTerminalForTest(t, databasePath, sourceAttempt, "interrupted", "interrupted")
		})
	}
}

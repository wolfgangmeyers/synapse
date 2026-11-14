package managed

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type countingRestartRuntime struct {
	*resetBarrierRuntime
	interrupts atomic.Int32
}

func (r *countingRestartRuntime) Interrupt() {
	if r.interrupts.Add(1) == 1 {
		close(r.interrupted)
	}
}

// Both public calls pass the outer replay lookups before either can reserve.
func TestEmbeddedRestartConcurrentAdmissionReplaysWhileDraining(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	f.m.startWG.Wait()
	ctx := context.Background()
	lane := f.m.laneFor(owner.binding.AgentID)
	runtime := &countingRestartRuntime{resetBarrierRuntime: &resetBarrierRuntime{interrupted: make(chan struct{}), closed: make(chan struct{})}}
	require.True(t, lane.installEmbeddedRuntime(runtime, &embeddedPersistence{binding: owner.binding}))
	defer lane.removeEmbeddedRuntime(runtime)
	lane.serial.Lock()
	defer lane.serial.Unlock()

	arrived := make(chan struct{}, 2)
	proceed := make(chan struct{})
	f.m.embeddedRestartAdmissionTestHook = func() { arrived <- struct{}{}; <-proceed }
	req := RestartRequest{AgentID: owner.binding.AgentID, Method: "POST", CanonicalPath: "/restart", RequestID: "race-restart", CanonicalBodyHash: "hash"}
	type result struct {
		value RestartResult
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { r, err := f.m.ReserveRestart(ctx, req); results <- result{r, err} }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			close(proceed)
			t.Fatal("call did not reach admission")
		}
	}
	close(proceed)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEqual(t, first.value.Replayed, second.value.Replayed)
	require.Equal(t, first.value.Operation, second.value.Operation)
	select {
	case <-runtime.interrupted:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not interrupt runtime")
	}
	require.True(t, lane.embeddedResetRunning())
	for _, tc := range []struct {
		name   string
		change func(*RestartRequest)
	}{
		{"changed body", func(r *RestartRequest) { r.CanonicalBodyHash = "different" }},
		{"changed path", func(r *RestartRequest) { r.CanonicalPath = "/different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conflict := req
			tc.change(&conflict)
			_, err := f.m.ReserveRestart(ctx, conflict)
			require.IsType(t, &store.ManagedIdempotencyConflictError{}, err)
		})
	}
	other := req
	other.RequestID = "different-request"
	_, err := f.m.ReserveRestart(ctx, other)
	require.IsType(t, &store.ManagedAgentNotReadyError{}, err)
	require.EqualValues(t, 1, runtime.interrupts.Load())
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, req.RequestID))
}

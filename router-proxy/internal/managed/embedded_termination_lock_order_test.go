package managed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// Model mail dispatch holding serial immediately before its admission check.
func TestEmbeddedTerminationDoesNotBlockMailAdmissionWhileWaitingForSerial(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := makeSubagentTestManager(t, db, nil, 4)
	defer m.Close(ctx)
	binding := subagentTestParent(t, db, "termination-lock-order")
	lane := m.laneFor(binding.AgentID)
	lane.serial.Lock()
	request := TerminateRequest{AgentID: binding.AgentID, Method: "DELETE", CanonicalPath: "/v1/managed-agents/" + binding.AgentID, RequestID: "123e4567-e89b-42d3-a456-426614174299", CanonicalBodyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	done := make(chan error, 1)
	go func() { _, e := m.Terminate(ctx, request); done <- e }()
	// Wait until termination has either closed the admission gate or is holding
	// admission waiting for serial. Both states precede the mail admission check.
	require.Eventually(t, func() bool {
		if !lane.admission.TryLock() {
			return true
		}
		pending := lane.resetPending || lane.terminationPending > 0
		lane.admission.Unlock()
		return pending
	}, 3*time.Second, time.Millisecond)
	checked := make(chan bool, 1)
	go func() {
		lane.admission.Lock()
		pending := lane.resetPending || lane.terminationPending > 0
		lane.admission.Unlock()
		checked <- pending
	}()
	var pending bool
	select {
	case pending = <-checked:
	case <-time.After(time.Second):
		lane.serial.Unlock()
		<-checked
		<-done
		t.Fatal("termination held admission while waiting for mail's serial lock")
	}
	require.True(t, pending, "termination must fence new embedded admissions")
	lane.serial.Unlock()
	require.NoError(t, <-done)
	require.Eventually(t, func() bool { _, e := db.ManagedAgent(ctx, binding.AgentID); return e != nil }, 3*time.Second, time.Millisecond)
}

func TestTerminationFenceSurvivesResetReopenAndOtherTermination(t *testing.T) {
	lane := &managedAgentLane{}
	lane.admission.Lock()
	lane.terminationPending = 2
	lane.admission.Unlock()
	_, ok := lane.beginEmbeddedReset()
	require.True(t, ok)
	lane.reopenEmbeddedResetAdmissions()
	lane.endEmbeddedReset()
	require.True(t, lane.embeddedResetPending())
	require.False(t, lane.installEmbeddedRuntime(&resetBarrierRuntime{}, nil))
	lane.admission.Lock()
	lane.terminationPending--
	lane.admission.Unlock()
	require.True(t, lane.embeddedResetPending())
	lane.admission.Lock()
	lane.terminationPending--
	lane.admission.Unlock()
	require.False(t, lane.embeddedResetPending())
}

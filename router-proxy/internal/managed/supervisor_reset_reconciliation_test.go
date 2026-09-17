package managed

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func strandedResetFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, string, chan struct{}) {
	t.Helper()
	ctx := context.Background()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)
	manager.lanes[fence.AgentID] = binding.lane
	actionID := "123e4567-e89b-42d3-a456-426614174201"
	bodyHash := strings.Repeat("a", 64)
	action := store.ManagedLifecycleActionInput{
		ActionID: actionID, AgentID: fence.AgentID, Kind: "reset", Source: "direct", ExecutionOwner: "lifecycle",
		State: "admitted", Stage: "admission", SourceFence: &fence,
		PlannedTargetRuntimeGeneration: "123e4567-e89b-42d3-a456-426614174202",
		CreatedAt:                      "2026-09-17T00:00:00.000Z", UpdatedAt: "2026-09-17T00:00:00.000Z",
		Operation: store.ManagedOperation{
			OperationID: actionID, Method: "POST", CanonicalPath: "/v1/managed-agents/goal-agent/reset",
			RequestID: actionID, CanonicalBodyHash: bodyHash, State: "pending", ResponseStatus: 202,
			ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`), CreatedAt: "2026-09-17T00:00:00.000Z",
		},
	}
	require.NoError(t, durable.CreateManagedLifecycleAction(ctx, action))
	require.NoError(t, durable.FailManagedReset(ctx, actionID, "RESET_PROCESS_IDENTITY_UNSAFE", false, fence))
	exitSignal := make(chan struct{})
	binding.waitSignal = exitSignal
	return manager, durable, binding, fence, actionID, exitSignal
}

func TestResetReconciliationFailsClosedWhenProcessExitsAfterProof(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, _, actionID, exitSignal := strandedResetFixture(t)
	defer func() { _ = manager.Close(ctx); _ = durable.Close() }()

	entered := make(chan struct{})
	manager.beforeManagedResetReconcile = func(string) {
		// This hook runs after the last live-binding proof and immediately before
		// the wait-publication commit gate. Close the wait signal in that exact
		// handoff window; reconciliation must leave the durable failure untouched.
		close(entered)
		close(exitSignal)
	}
	result := make(chan error, 1)
	go func() { result <- manager.ReconcileManagedResetFailures(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("context unexpectedly cancelled")
	}
	require.NoError(t, <-result)

	// The process exit happened after the exact pre-write proof. The transaction
	// check prevents the old durable fence from being restored to running.
	action, err := durable.ManagedLifecycleAction(ctx, actionID)
	require.NoError(t, err)
	require.False(t, action.Retryable)
	agent, err := durable.ManagedAgent(ctx, "goal-agent")
	require.NoError(t, err)
	require.Equal(t, "failed", agent.LifecycleState)
	require.False(t, agent.AcceptingPrompts)
	manager.clearBinding(binding)
}

func TestResetReconciliationRejectsMismatchedBindingWithoutRepair(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, _, actionID, _ := strandedResetFixture(t)
	defer func() { _ = manager.Close(ctx); _ = durable.Close() }()
	binding.runtimeGeneration = "123e4567-e89b-42d3-a456-426614174299"

	require.NoError(t, manager.ReconcileManagedResetFailures(ctx))
	action, err := durable.ManagedLifecycleAction(ctx, actionID)
	require.NoError(t, err)
	require.False(t, action.Retryable)
	agent, err := durable.ManagedAgent(ctx, "goal-agent")
	require.NoError(t, err)
	require.Equal(t, "failed", agent.LifecycleState)
	require.False(t, agent.AcceptingPrompts)
}

func TestConcurrentResetReconciliationIsReloadSafeAndCreatesOneSuccessor(t *testing.T) {
	ctx := context.Background()
	manager, durable, _, fence, predecessorID, _ := strandedResetFixture(t)
	defer func() { _ = manager.Close(ctx); _ = durable.Close() }()

	entered := make(chan struct{})
	release := make(chan struct{})
	manager.beforeManagedResetReconcile = func(string) {
		close(entered)
		<-release
	}
	results := make(chan error, 2)
	go func() { results <- manager.ReconcileManagedResetFailures(ctx) }()
	<-entered
	go func() { results <- manager.reconcileStrandedManagedReset(ctx, fence.AgentID) }()
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)

	predecessor, err := durable.ManagedLifecycleAction(ctx, predecessorID)
	require.NoError(t, err)
	require.True(t, predecessor.Retryable)

	// The manager may now admit the same retried reset concurrently from two
	// request deliveries. Store idempotency and retry lineage must leave one
	// successor operation and one lifecycle action.
	bodyHash := strings.Repeat("a", 64)
	requestID := "123e4567-e89b-42d3-a456-426614174203"
	reservation := func() store.ManagedResetReservation {
		return store.ManagedResetReservation{
			AgentID: fence.AgentID, Method: "POST", CanonicalPath: "/v1/managed-agents/goal-agent/reset", RequestID: requestID,
			CanonicalBodyHash: bodyHash, SourceFence: fence, LiveFence: fence, RetryOf: predecessorID,
			InitialState: "admitted", InitialStage: "admission", PlannedTargetRuntimeGeneration: predecessor.PlannedTargetRuntimeGeneration,
			Operation: store.ManagedOperation{OperationID: requestID, Method: "POST", CanonicalPath: "/v1/managed-agents/goal-agent/reset", RequestID: requestID, CanonicalBodyHash: bodyHash, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`), CreatedAt: "2026-09-17T00:00:00.000Z"},
		}
	}
	admissions := make([]store.ManagedResetAdmission, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for i := range admissions {
		go func(index int) {
			defer wait.Done()
			admissions[index], errs[index] = durable.ReserveManagedReset(ctx, reservation())
		}(i)
	}
	wait.Wait()
	for _, reserveErr := range errs {
		require.NoError(t, reserveErr)
	}
	replayed := 0
	for _, admission := range admissions {
		if admission.Replayed {
			replayed++
		}
	}
	require.Equal(t, 1, replayed)
	created := 0
	for _, admission := range admissions {
		if admission.Action != nil {
			created++
			require.Equal(t, predecessorID, admission.Action.RetryOf)
		}
	}
	require.Equal(t, 1, created)
}

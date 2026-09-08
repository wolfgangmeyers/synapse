package managed

import (
	"context"
	"errors"
	"log"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// RestartAllRequest is canonical parent request metadata. Child identities,
// response bytes, graph rows, and admission fences remain store-owned.
type RestartAllRequest struct {
	Method, CanonicalPath, RequestID, CanonicalBodyHash string
}

// RestartAllResult exposes only the committed immutable parent operation.
type RestartAllResult struct {
	Operation *store.ManagedOperation
	Replayed  bool
}

// RestartAllDispatcher is the post-commit durable handoff boundary. It must
// not manufacture a second queue or derive work from a live process.
type RestartAllDispatcher func(context.Context, string, []string) error

// RestartAllDispatchFailureRecorder receives bounded, committed identifiers.
// It is diagnostic-only: failure never changes the parent response or graph.
type RestartAllDispatchFailureRecorder func(context.Context, string, []string, error)

// RestartAll asks the store to atomically reserve the whole durable batch.
// The manager holds no agent lane during admission. A Close that reaches the
// manager first rejects the request; one that waits for this call sees a
// complete recovery input after the store commits.
func (m *Manager) RestartAll(ctx context.Context, request RestartAllRequest) (RestartAllResult, error) {
	if request.Method != "POST" || request.CanonicalPath == "" || request.RequestID == "" || request.CanonicalBodyHash == "" {
		return RestartAllResult{}, errors.New("managed restart-all request is incomplete")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return RestartAllResult{}, errors.New("managed manager is closed")
	}
	admission, err := m.store.ReserveManagedRestartAll(ctx, store.ManagedRestartAllReservation{
		Method: request.Method, CanonicalPath: request.CanonicalPath, RequestID: request.RequestID, CanonicalBodyHash: request.CanonicalBodyHash,
		AllocateChild: func() (store.ManagedRestartAllChildIdentity, error) {
			actionID, actionErr := m.ids.Next()
			if actionErr != nil {
				return store.ManagedRestartAllChildIdentity{}, actionErr
			}
			generation, generationErr := m.ids.Next()
			if generationErr != nil {
				return store.ManagedRestartAllChildIdentity{}, generationErr
			}
			return store.ManagedRestartAllChildIdentity{ActionID: actionID, PlannedTargetRuntimeGeneration: generation}, nil
		},
	})
	m.mu.Unlock()
	if err != nil {
		return RestartAllResult{}, err
	}
	if admission.Operation == nil {
		return RestartAllResult{}, errors.New("managed restart-all admission did not return an operation")
	}
	if !admission.Replayed && len(admission.AcceptedChildActionIDs) != 0 {
		if dispatchErr := m.dispatchAdmittedRestartAllChildren(ctx, admission.Operation.OperationID, admission.AcceptedChildActionIDs); dispatchErr != nil {
			m.recordRestartAllDispatchFailure(admission.Operation.OperationID, admission.AcceptedChildActionIDs, dispatchErr)
		}
	}
	return RestartAllResult{Operation: admission.Operation, Replayed: admission.Replayed}, nil
}

func (m *Manager) dispatchAdmittedRestartAllChildren(ctx context.Context, parentID string, childIDs []string) error {
	if m.restartAllDispatcher != nil {
		return m.restartAllDispatcher(ctx, parentID, append([]string(nil), childIDs...))
	}
	for _, childID := range childIDs {
		actionID := childID
		if !m.startManagedTask(func() { m.ExecuteLifecycleRestartAction(actionID) }) {
			return errors.New("managed manager closed before restart-all dispatch")
		}
	}
	return nil
}

func (m *Manager) recordRestartAllDispatchFailure(parentID string, childIDs []string, err error) {
	if m.restartAllDispatchFailureRecorder != nil {
		m.restartAllDispatchFailureRecorder(context.Background(), parentID, append([]string(nil), childIDs...), err)
		return
	}
	log.Printf("managed restart-all dispatch failed parent=%s children=%v: %v", parentID, childIDs, err)
}

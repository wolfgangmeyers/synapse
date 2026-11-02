package managed

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

const currentRootManifestPath = "managed-tools-v1.json"

func loadCurrentRootManifest(t *testing.T) Manifest {
	t.Helper()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", currentRootManifestPath))
	require.NoError(t, err)
	return manifest
}

func rootHandlerTestExecutor(calls *atomic.Int32) RootControlOperationExecutor {
	return RootControlOperationExecutorFunc{
		ExecuteFunc: func(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
			calls.Add(1)
			return RootControlHandlerResult{Status: "ok", Content: "executed"}, nil
		},
		ReconcileFunc: func(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error) {
			calls.Add(1)
			return RootControlHandlerResult{Status: "ok", Content: "reconciled"}, nil
		},
	}
}

func currentRootHandlers(t *testing.T, manifest Manifest) []RootControlHandler {
	t.Helper()
	var calls atomic.Int32
	return currentRootHandlersWithCalls(t, manifest, &calls)
}

func currentRootHandlersWithCalls(t *testing.T, manifest Manifest, calls *atomic.Int32) []RootControlHandler {
	t.Helper()
	handlers, err := NewOperationalRootControlHandlers(manifest, rootHandlerTestExecutor(calls))
	require.NoError(t, err)
	return handlers
}

func handlerIndexByOperation(t *testing.T, handlers []RootControlHandler, operation string) int {
	t.Helper()
	for index, handler := range handlers {
		if handler.Identity().OperationID == operation {
			return index
		}
	}
	t.Fatalf("handler for %s is missing", operation)
	return -1
}

func TestRootControlHandlerRegistryClosesOverCurrentManifest(t *testing.T) {
	manifest := loadCurrentRootManifest(t)
	require.Equal(t, 2, manifest.Revision)
	require.Len(t, manifest.Tools, 16)

	operations := make(map[string]ManifestTool, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		_, duplicate := operations[tool.OperationID]
		require.False(t, duplicate, "current manifest repeats operation %q", tool.OperationID)
		operations[tool.OperationID] = tool
	}
	require.Len(t, operations, len(manifest.Tools))
	require.NotContains(t, operations, "ops.worker", "retired worker tool must not return to the manifest")

	search, found := operations["web.search"]
	require.True(t, found, "current manifest must include revision-two web.search")
	require.Equal(t, "web_search__4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac", search.Name)
	require.Equal(t, "def_"+search.DefinitionSHA256, search.DefinitionID)
	require.Equal(t, "4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac", search.DefinitionSHA256)

	var calls atomic.Int32
	handlers := currentRootHandlersWithCalls(t, manifest, &calls)
	registry, err := NewRootControlHandlerRegistry(manifest, handlers)
	require.NoError(t, err)
	require.Len(t, registry.byOperation, len(manifest.Tools))

	for _, tool := range manifest.Tools {
		handler, found := registry.Handler(tool)
		require.True(t, found, "manifest operation %s has no registry handler", tool.OperationID)
		require.NotNil(t, handler)
		identity := handler.Identity()
		require.Equal(t, RootControlHandlerIdentity{
			CapabilityID:     tool.CapabilityID,
			ToolName:         tool.Name,
			DefinitionID:     tool.DefinitionID,
			DefinitionSHA256: tool.DefinitionSHA256,
			OperationID:      tool.OperationID,
		}, identity, "handler identity for %s must match every immutable manifest field", tool.OperationID)
		if tool.OperationID == "mail.parent" {
			childOnly, childOnlyOwner := handler.(unavailableRootControlHandler)
			require.True(t, childOnlyOwner)
			require.True(t, childOnly.childOnly)
		} else {
			owner, operationalOwner := handler.(operationRootControlHandler)
			require.True(t, operationalOwner, "root operation %s must have its concrete operational owner", tool.OperationID)
			require.NotNil(t, owner.executor)
			executed, executeErr := handler.Execute(context.Background(), RootControlInvocation{OperationID: tool.OperationID})
			require.NoError(t, executeErr)
			require.Equal(t, "executed", executed.Content)
			reconciled, reconcileErr := handler.Reconcile(context.Background(), RootControlInvocation{OperationID: tool.OperationID})
			require.NoError(t, reconcileErr)
			require.Equal(t, "reconciled", reconciled.Content)
		}
	}
	require.Equal(t, int32(30), calls.Load(), "each available owner must delegate execute and reconcile exactly once")
}

func TestRootControlHandlerRegistryRejectsEveryClosureMutation(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func([]RootControlHandler) []RootControlHandler
	}{
		{
			name: "omitted operation",
			mutate: func(handlers []RootControlHandler) []RootControlHandler {
				return append([]RootControlHandler{}, handlers[1:]...)
			},
		},
		{
			name: "surplus operation",
			mutate: func(handlers []RootControlHandler) []RootControlHandler {
				return append(append([]RootControlHandler{}, handlers...), testRootHandler{identity: RootControlHandlerIdentity{
					CapabilityID: "surplus", ToolName: "surplus", DefinitionID: "def_" + strings.Repeat("a", 64), DefinitionSHA256: strings.Repeat("a", 64), OperationID: "surplus.operation",
				}})
			},
		},
		{
			name: "swapped immutable identity",
			mutate: func(handlers []RootControlHandler) []RootControlHandler {
				mutated := append([]RootControlHandler{}, handlers...)
				left := handlerIndexByOperation(t, mutated, "goal.control")
				right := handlerIndexByOperation(t, mutated, "goal.snooze")
				identity := mutated[right].Identity()
				identity.OperationID = mutated[left].Identity().OperationID
				mutated[left] = testRootHandler{identity: identity}
				return mutated
			},
		},
		{
			name: "duplicate owner",
			mutate: func(handlers []RootControlHandler) []RootControlHandler {
				return append(append([]RootControlHandler{}, handlers...), handlers[0])
			},
		},
		{
			name: "web.search removed",
			mutate: func(handlers []RootControlHandler) []RootControlHandler {
				mutated := make([]RootControlHandler, 0, len(handlers)-1)
				for _, handler := range handlers {
					if handler.Identity().OperationID != "web.search" {
						mutated = append(mutated, handler)
					}
				}
				return mutated
			},
		},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			manifest := loadCurrentRootManifest(t)
			handlers := mutation.mutate(currentRootHandlers(t, manifest))
			_, err := NewRootControlHandlerRegistry(manifest, handlers)
			require.Error(t, err, "registry construction must reject %s", mutation.name)
		})
	}
}

func TestRootControlChildOnlyHandlerRejectsBeforeBusinessSideEffect(t *testing.T) {
	manifest := loadCurrentRootManifest(t)
	var calls atomic.Int32
	handlers, err := NewOperationalRootControlHandlers(manifest, rootHandlerTestExecutor(&calls))
	require.NoError(t, err)
	registry, err := NewRootControlHandlerRegistry(manifest, handlers)
	require.NoError(t, err)

	var parent ManifestTool
	for _, tool := range manifest.Tools {
		if tool.OperationID == "mail.parent" {
			parent = tool
			break
		}
	}
	require.NotEmpty(t, parent.OperationID)
	handler, found := registry.Handler(parent)
	require.True(t, found)
	childOnly, ok := handler.(unavailableRootControlHandler)
	require.True(t, ok, "mail.parent must use the fixed child-only owner")
	require.True(t, childOnly.childOnly)

	result, err := handler.Execute(context.Background(), RootControlInvocation{OperationID: parent.OperationID})
	require.NoError(t, err)
	require.Equal(t, "error", result.Status)
	require.Equal(t, "CHILD_ONLY_OPERATION", result.ErrorCode)
	require.Equal(t, "This operation is available only to child sessions.", result.Content)
	require.Empty(t, result.Details)
	reconciled, err := handler.Reconcile(context.Background(), RootControlInvocation{OperationID: parent.OperationID})
	require.NoError(t, err)
	require.Equal(t, result, reconciled)
	require.Zero(t, calls.Load(), "child-only root rejection must not invoke the business executor")
}

type testRootHandler struct {
	identity RootControlHandlerIdentity
}

func (h testRootHandler) Identity() RootControlHandlerIdentity { return h.identity }
func (h testRootHandler) Execute(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	return RootControlHandlerResult{}, nil
}
func (h testRootHandler) Reconcile(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	return RootControlHandlerResult{}, nil
}

type panicRootHandler struct {
	identity RootControlHandlerIdentity
}

func (h panicRootHandler) Identity() RootControlHandlerIdentity { return h.identity }
func (h panicRootHandler) Execute(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	panic("private panic marker")
}
func (h panicRootHandler) Reconcile(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	panic("private panic marker")
}

func TestInvokeRootControlHandlerConvertsExecuteAndReconcilePanics(t *testing.T) {
	handler := panicRootHandler{identity: RootControlHandlerIdentity{OperationID: "panic"}}
	for _, test := range []struct {
		name      string
		reconcile bool
	}{
		{name: "execute"},
		{name: "reconcile", reconcile: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := invokeRootControlHandler(context.Background(), handler, RootControlInvocation{OperationID: "panic"}, test.reconcile)
			require.ErrorIs(t, err, errRootControlHandlerPanic)
			require.Empty(t, result)
			require.NotContains(t, err.Error(), "private panic marker")
		})
	}
}

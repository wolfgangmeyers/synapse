package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

// RootControlInvocation is the only handler input. It keeps the original
// request/result IDs and authoritative fence available for adapter idempotency;
// handlers never receive caller-supplied session or process identity.
type RootControlInvocation struct {
	RequestID, ResultID, ToolCallID string
	OperationID                     string
	Arguments                       any
	Fence                           store.ManagedRuntimeFence
}

type RootControlHandlerResult struct {
	Status, Content string
	Details         map[string]any
	ErrorCode       string
	ErrorMessage    string
	Retryable       bool
}

// RootControlHandler owns one immutable manifest operation. Reconcile is used
// after a crash in executing or side_effect_committed; it must use RequestID as
// its external correlation key and must never blindly repeat an uncertain call.
type RootControlHandler interface {
	Identity() RootControlHandlerIdentity
	Execute(context.Context, RootControlInvocation) (RootControlHandlerResult, error)
	Reconcile(context.Context, RootControlInvocation) (RootControlHandlerResult, error)
}

// RootControlOperationExecutor is the host boundary for all root-capable
// operations. It receives the immutable request ID and full runtime fence so
// the host can make its own external action idempotent before the ledger marks
// the side effect committed.
type RootControlOperationExecutor interface {
	ExecuteRootControl(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error)
	ReconcileRootControl(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error)
}

type RootControlOperationExecutorFunc struct {
	ExecuteFunc   func(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error)
	ReconcileFunc func(context.Context, RootControlHandlerIdentity, RootControlInvocation) (RootControlHandlerResult, error)
}

func (f RootControlOperationExecutorFunc) ExecuteRootControl(ctx context.Context, identity RootControlHandlerIdentity, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	if f.ExecuteFunc == nil {
		return RootControlHandlerResult{}, errors.New("root control executor is unavailable")
	}
	return f.ExecuteFunc(ctx, identity, invocation)
}
func (f RootControlOperationExecutorFunc) ReconcileRootControl(ctx context.Context, identity RootControlHandlerIdentity, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	if f.ReconcileFunc == nil {
		return f.ExecuteRootControl(ctx, identity, invocation)
	}
	return f.ReconcileFunc(ctx, identity, invocation)
}

type RootControlHandlerIdentity struct {
	CapabilityID, ToolName, DefinitionID, DefinitionSHA256, OperationID string
}

type RootControlHandlerRegistry struct{ byOperation map[string]RootControlHandler }

var rootControlOperationIdentity = map[string]struct{ capability, name string }{
	"goal.control":             {"goal", "goal"},
	"goal.snooze":              {"goal_snooze", "goal_snooze"},
	"context.control":          {"mecha_context", "mecha_context"},
	"ops.git_status":           {"mecha_git", "mecha_git"},
	"mail.send":                {"mecha_mailbox_send", "mecha_mailbox_send"},
	"mail.parent":              {"mecha_parent_message", "mecha_parent_message"},
	"ops.pr_status":            {"mecha_pr", "mecha_pr"},
	"ops.priority":             {"mecha_priority", "mecha_priority"},
	"recall.control":           {"mecha_recall", "mecha_recall"},
	"reminder.control":         {"mecha_reminder", "mecha_reminder"},
	"policy.inline_allowance":  {"mecha_request_inline_tool_allowance", "mecha_request_inline_tool_allowance"},
	"router.control":           {"mecha_router", "mecha_router"},
	"notifications.settled":    {"mecha_settled_notifications", "mecha_settled_notifications"},
	"ops.state":                {"mecha_state", "mecha_state"},
	"ops.verification_handoff": {"mecha_verification_handoff", "mecha_verification_handoff"},
	"web.search":               {"web_search", ""},
}

func NewRootControlHandlerRegistry(manifest Manifest, handlers []RootControlHandler) (*RootControlHandlerRegistry, error) {
	if len(manifest.Tools) != len(rootControlOperationIdentity) {
		return nil, errors.New("root control manifest is incomplete")
	}
	byOperation := map[string]ManifestTool{}
	for _, tool := range manifest.Tools {
		expected, found := rootControlOperationIdentity[tool.OperationID]
		if !found || expected.capability != tool.CapabilityID || !rootControlToolNameMatches(expected, tool) || tool.DefinitionID == "" || tool.DefinitionSHA256 == "" || tool.Parameters == nil {
			return nil, fmt.Errorf("root control manifest identity is invalid for %s", tool.Name)
		}
		if _, duplicate := byOperation[tool.OperationID]; duplicate {
			return nil, errors.New("root control manifest has duplicate operations")
		}
		byOperation[tool.OperationID] = tool
	}
	if handlers == nil {
		handlers = defaultRootControlHandlers(manifest)
	}
	registry := &RootControlHandlerRegistry{byOperation: map[string]RootControlHandler{}}
	for _, handler := range handlers {
		if handler == nil {
			return nil, errors.New("root control handler is nil")
		}
		identity := handler.Identity()
		tool, found := byOperation[identity.OperationID]
		if !found || identity.CapabilityID != tool.CapabilityID || identity.ToolName != tool.Name || identity.DefinitionID != tool.DefinitionID || identity.DefinitionSHA256 != tool.DefinitionSHA256 {
			return nil, errors.New("root control handler identity does not match manifest")
		}
		if _, duplicate := registry.byOperation[identity.OperationID]; duplicate {
			return nil, errors.New("root control handler is duplicated")
		}
		registry.byOperation[identity.OperationID] = handler
	}
	if len(registry.byOperation) != len(byOperation) {
		return nil, errors.New("root control handler registry is incomplete")
	}
	return registry, nil
}

func rootControlToolNameMatches(expected struct{ capability, name string }, tool ManifestTool) bool {
	if expected.name != "" {
		return tool.Name == expected.name
	}
	// Supplemental capabilities are immutable-definition names. The current
	// web search bridge is the only such root capability, so never accept a
	// short or caller-chosen alias at this trust boundary.
	return tool.Name == tool.CapabilityID+"__"+tool.DefinitionSHA256 && strings.HasPrefix(tool.Name, expected.capability+"__")
}

func (r *RootControlHandlerRegistry) Handler(tool ManifestTool) (RootControlHandler, bool) {
	if r == nil {
		return nil, false
	}
	handler, found := r.byOperation[tool.OperationID]
	if !found {
		return nil, false
	}
	identity := handler.Identity()
	return handler, identity.CapabilityID == tool.CapabilityID && identity.ToolName == tool.Name && identity.DefinitionID == tool.DefinitionID && identity.DefinitionSHA256 == tool.DefinitionSHA256 && identity.OperationID == tool.OperationID
}

type operationRootControlHandler struct {
	identity RootControlHandlerIdentity
	executor RootControlOperationExecutor
}

func (h operationRootControlHandler) Identity() RootControlHandlerIdentity { return h.identity }
func (h operationRootControlHandler) Execute(ctx context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	return h.executor.ExecuteRootControl(ctx, h.identity, invocation)
}
func (h operationRootControlHandler) Reconcile(ctx context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	return h.executor.ReconcileRootControl(ctx, h.identity, invocation)
}

// NewOperationalRootControlHandlers creates the closed concrete 17-operation
// owner set used by the router host. mail.parent remains a dedicated child-only
// handler; all other entries delegate through the correlation-aware host seam.
func NewOperationalRootControlHandlers(manifest Manifest, executor RootControlOperationExecutor) ([]RootControlHandler, error) {
	if executor == nil {
		return nil, errors.New("root control operation executor is required")
	}
	result := make([]RootControlHandler, 0, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		identity := RootControlHandlerIdentity{CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID}
		if tool.OperationID == "mail.parent" {
			result = append(result, unavailableRootControlHandler{identity: identity, childOnly: true})
			continue
		}
		result = append(result, operationRootControlHandler{identity: identity, executor: executor})
	}
	return result, nil
}

type unavailableRootControlHandler struct {
	identity  RootControlHandlerIdentity
	childOnly bool
}

func (h unavailableRootControlHandler) Identity() RootControlHandlerIdentity { return h.identity }
func (h unavailableRootControlHandler) Execute(_ context.Context, _ RootControlInvocation) (RootControlHandlerResult, error) {
	if h.childOnly {
		return RootControlHandlerResult{Status: "error", Content: "This operation is available only to child sessions.", Details: map[string]any{}, ErrorCode: "CHILD_ONLY_OPERATION", ErrorMessage: "this operation is available only to child sessions"}, nil
	}
	// The Go router has no direct implementation of the supervisor tool
	// services. Returning a correlated error is safer than pretending an effect
	// occurred; deployments inject a concrete owner through ManagerOptions.
	return RootControlHandlerResult{Status: "error", Content: "The managed control service is unavailable.", Details: map[string]any{}, ErrorCode: "HANDLER_UNAVAILABLE", ErrorMessage: "the managed control service is unavailable"}, nil
}
func (h unavailableRootControlHandler) Reconcile(ctx context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	return h.Execute(ctx, invocation)
}

func defaultRootControlHandlers(manifest Manifest) []RootControlHandler {
	result := make([]RootControlHandler, 0, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		result = append(result, unavailableRootControlHandler{identity: RootControlHandlerIdentity{CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID}, childOnly: tool.OperationID == "mail.parent"})
	}
	return result
}

package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	rootControlAppendTimeout          = 10 * time.Second
	piContextTooSmallError            = "Pi RPC error: Nothing to compact (session too small)"
	managedContextTooSmallErrorCode   = "CONTEXT_TOO_SMALL"
	managedContextTooSmallSafeSummary = "Compaction skipped: session is too small."
)

var errRootControlHandlerPanic = errors.New("managed root control handler failed internally")

// directRootControlReceiptAcknowledger lets the manager release a provisional
// direct-delivery settlement only after this exact result is durable.
type directRootControlReceiptAcknowledger interface {
	AcknowledgeDirectRootControlReceipt(requestID string, status directRootControlResultStatus)
}

type directRootControlRejector interface {
	RejectRootControlRequest(requestID string, fence store.ManagedRuntimeFence)
}

type rootControlEventReceiver interface {
	SetRootControlEventHandler(func(RootControlEvent))
}

func acknowledgeDirectRootControlReceipt(rpc RPC, requestID, resultStatus string) {
	acknowledger, ok := rpc.(directRootControlReceiptAcknowledger)
	if !ok {
		return
	}
	var status directRootControlResultStatus
	switch resultStatus {
	case "ok":
		status = directRootControlResultSuccess
	case "error":
		status = directRootControlResultError
	default:
		return
	}
	acknowledger.AcknowledgeDirectRootControlReceipt(requestID, status)
}

func rejectDirectRootControl(rpc RPC, requestID string, fence store.ManagedRuntimeFence) {
	if rejector, ok := rpc.(directRootControlRejector); ok {
		rejector.RejectRootControlRequest(requestID, fence)
	}
}

// installRootControlReceiver binds request intake only after the runtime has
// passed normal readiness and becomes the installed binding. This prevents an
// unready/replaced process from admitting root controls.
func (m *Manager) installRootControlReceiver(binding *managedBinding) {
	receiver, ok := binding.rpc.(rootControlEventReceiver)
	if !ok {
		return
	}
	receiver.SetRootControlEventHandler(func(event RootControlEvent) { m.handleRootControlEvent(binding, event) })
}

// startRootControlRecovery starts durable reconciliation only after callers
// have completed any synchronous launch work that must remain first in Pi's
// semantic FIFO.
func (m *Manager) startRootControlRecovery(binding *managedBinding) {
	go m.recoverRootControls(binding)
}

func (m *Manager) handleRootControlEvent(binding *managedBinding, event RootControlEvent) {
	if binding == nil {
		return
	}
	// The RPC client's shared semantic FIFO owns stdout order. Keep this
	// callback synchronous so a later parent settled event cannot overtake root
	// admission and policy application; the RPC reader remains independent and
	// continues consuming receipts while this handler waits.
	binding.rootControlSerial.Lock()
	defer binding.rootControlSerial.Unlock()
	ctx, cancel := context.WithTimeout(m.runCtx, 45*time.Second)
	defer cancel()
	m.admitAndRunRootControl(ctx, binding, event)
}

func (m *Manager) admitAndRunRootControl(ctx context.Context, binding *managedBinding, event RootControlEvent) {
	fence := bindingFence(binding.agentID, binding)
	// Every terminal admission return reports an exact rejection. The RPC then
	// applies its own fence check, so a stale manager cannot affect a new turn.
	reserved := false
	defer func() {
		if !reserved {
			rejectDirectRootControl(binding.rpc, event.RequestID, fence)
		}
	}()
	if !m.rootBindingCurrent(ctx, binding, fence) || event.RuntimeGeneration != fence.RuntimeGeneration || event.SessionEpoch != fence.SessionEpoch || event.ManifestSHA256 != fence.ManifestHash {
		m.emitEvent(managedRootControlRejectedEvent(binding.agentID))
		return
	}
	tool, found := m.rootManifestTool(event)
	if !found || ValidateManifestArguments(tool, event.Arguments) != nil {
		m.emitEvent(managedRootControlRejectedEvent(binding.agentID))
		return
	}
	arguments, err := canonicalJSON(event.Arguments)
	if err != nil {
		m.emitEvent(managedRootControlRejectedEvent(binding.agentID))
		return
	}
	argumentHash := sha256.Sum256(arguments)
	fingerprintValue := map[string]any{"request_id": event.RequestID, "tool_call_id": event.ToolCallID, "capability_id": event.CapabilityID, "tool_name": event.ToolName, "definition_id": event.DefinitionID, "definition_sha256": event.DefinitionSHA256, "operation_id": event.OperationID, "manifest_sha256": event.ManifestSHA256, "runtime_generation": event.RuntimeGeneration, "session_epoch": event.SessionEpoch, "arguments": event.Arguments}
	fingerprintBytes, err := canonicalJSON(fingerprintValue)
	if err != nil {
		m.emitEvent(managedRootControlRejectedEvent(binding.agentID))
		return
	}
	fingerprint := sha256.Sum256(fingerprintBytes)
	// A result UUID is generated once on first admission. The root-control
	// serializer orders duplicate control events without blocking direct work.
	resultID := ""
	existing, lookupErr := m.store.ManagedControlRequest(ctx, event.RequestID)
	if lookupErr == nil {
		resultID = existing.ResultID
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		return
	}
	if resultID == "" {
		resultID, err = m.ids.Next()
		if err != nil {
			return
		}
	}
	reservation, err := m.store.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{RequestID: event.RequestID, ResultID: resultID, AgentID: binding.agentID, ToolCallID: event.ToolCallID, CapabilityID: event.CapabilityID, ToolName: event.ToolName, DefinitionID: event.DefinitionID, OperationID: event.OperationID, CanonicalArgumentsSHA256: fmt.Sprintf("%x", argumentHash), RequestFingerprint: fmt.Sprintf("%x", fingerprint), Fence: fence, ReceivedAt: formatManagedTime(m.clock.Now())})
	if err != nil || reservation.RequestID == "" {
		m.emitEvent(managedRootControlRejectedEvent(binding.agentID))
		return
	}
	reserved = true
	if !reservation.Replayed {
		// Admission is durable at this point. The typed event contains only the
		// closed scalar argument projection, never the caller's raw object.
		m.emitEvent(managedRootControlRequestEvent(binding.agentID, event))
	}
	if event.OperationID == "mail.send" {
		arguments, ok := event.Arguments.(map[string]any)
		recipient, recipientOK := arguments["recipient"].(string)
		if !ok || !recipientOK || m.store.ReserveManagedQuietSendAuthorization(ctx, event.RequestID, binding.agentID, recipient) != nil {
			return
		}
	}
	m.resumeRootControl(ctx, binding, event.RequestID, event.Arguments)
}

// definitionSHA256ForStoredControl reconstructs only the installed immutable
// definition hash for a row that predates the S4 envelope field. Its runtime
// fence pins the same manifest hash, and the tuple is rechecked before a
// handler is selected; no caller-supplied identity is used on recovery.
func (m *Manager) definitionSHA256ForStoredControl(row *store.ManagedControlRequest) string {
	if row == nil {
		return ""
	}
	for _, tool := range m.manifest.Tools {
		if tool.CapabilityID == row.CapabilityID && tool.Name == row.ToolName && tool.DefinitionID == row.DefinitionID && tool.OperationID == row.OperationID {
			return tool.DefinitionSHA256
		}
	}
	return ""
}

func (m *Manager) rootManifestTool(event RootControlEvent) (ManifestTool, bool) {
	for _, tool := range m.manifest.Tools {
		if tool.CapabilityID == event.CapabilityID && tool.Name == event.ToolName && tool.DefinitionID == event.DefinitionID && tool.DefinitionSHA256 == event.DefinitionSHA256 && tool.OperationID == event.OperationID {
			return tool, true
		}
	}
	return ManifestTool{}, false
}

func (m *Manager) rootBindingCurrent(ctx context.Context, binding *managedBinding, fence store.ManagedRuntimeFence) bool {
	if binding == nil || binding.agentID != fence.AgentID || !m.liveBindingForFence(ctx, binding, fence) {
		return false
	}
	m.mu.Lock()
	current := !m.closed && m.bindings[binding.agentID] == binding
	m.mu.Unlock()
	return current
}

func (m *Manager) recoverRootControls(binding *managedBinding) {
	if binding == nil {
		return
	}
	binding.rootControlSerial.Lock()
	defer binding.rootControlSerial.Unlock()
	ctx, cancel := context.WithTimeout(m.runCtx, 45*time.Second)
	defer cancel()
	fence := bindingFence(binding.agentID, binding)
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return
	}
	rows, err := m.store.ListRestorableManagedControlRequests(ctx, fence)
	if err != nil {
		return
	}
	for _, row := range rows {
		m.resumeRootControl(ctx, binding, row.RequestID, nil)
	}
}

func (m *Manager) resumeRootControl(ctx context.Context, binding *managedBinding, requestID string, suppliedArguments any) {
	fence := bindingFence(binding.agentID, binding)
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return
	}
	row, err := m.store.ManagedControlRequest(ctx, requestID)
	if err != nil || !sameBindingFence(row.Fence, fence) {
		return
	}
	// Durable results no longer need a live tool definition or handler.
	switch row.State {
	case "completed", "failed":
		m.appendRootControlResult(ctx, binding, *row)
		return
	case "append_pending", "awaiting_append":
		m.reconcileRootControlAppend(ctx, binding, *row)
		return
	}
	// Retirement is recovery-only, never admission or execution. Keep durable
	// delivery above this branch and the full current binding fence before it.
	if row.OperationID == "ops.worker" && row.CapabilityID == "mecha_worker" && row.ToolName == "mecha_worker" {
		m.recoverRetiredWorkerControl(ctx, binding, row)
		return
	}
	tool, found := m.rootManifestTool(RootControlEvent{CapabilityID: row.CapabilityID, ToolName: row.ToolName, DefinitionID: row.DefinitionID, DefinitionSHA256: m.definitionSHA256ForStoredControl(row), OperationID: row.OperationID})
	handler, available := m.rootControlHandlers.Handler(tool)
	if !found || !available {
		return
	}
	arguments := suppliedArguments
	if arguments == nil {
		// Arguments are deliberately not reconstructed from converted tool input.
		// The immutable hash is sufficient for fencing recovery; adapters receive
		// nil unless they keep an idempotent request-side record of their own.
		arguments = map[string]any{}
	}
	owner, err := m.ids.Next()
	if err != nil {
		return
	}
	invocation := RootControlInvocation{RequestID: row.RequestID, ResultID: row.ResultID, ToolCallID: row.ToolCallID, OperationID: row.OperationID, Arguments: arguments, Fence: fence}
	switch row.State {
	case "received":
		claim, err := m.store.ClaimManagedControlHandler(ctx, row.RequestID, owner, fence)
		if err != nil {
			return
		}
		result, err := m.executeRootControlHandler(ctx, binding, handler, invocation, false)
		m.finishRootControlHandler(ctx, binding, handler, claim, result, err, false)
	case "executing", "side_effect_committed":
		claim, err := m.store.RecoverManagedControlHandler(ctx, row.RequestID, owner, fence)
		if err != nil {
			return
		}
		result, err := m.executeRootControlHandler(ctx, binding, handler, invocation, true)
		m.finishRootControlHandler(ctx, binding, handler, claim, result, err, claim.Request.State == "side_effect_committed")
	}
}

// recoverRetiredWorkerControl settles only the removed worker operation. A
// received request has no effect to recover. An uncertain request may consult
// the host ledger, but must never reach Execute (including fallback executors).
func (m *Manager) recoverRetiredWorkerControl(ctx context.Context, binding *managedBinding, row *store.ManagedControlRequest) {
	owner, err := m.ids.Next()
	if err != nil {
		return
	}
	var claim *store.ManagedControlHandlerClaim
	switch row.State {
	case "received":
		claim, err = m.store.ClaimManagedControlHandler(ctx, row.RequestID, owner, row.Fence)
	case "executing", "side_effect_committed":
		claim, err = m.store.RecoverManagedControlHandler(ctx, row.RequestID, owner, row.Fence)
	default:
		return
	}
	if err != nil {
		return
	}
	result := RootControlHandlerResult{Status: "error", Content: "The retired worker operation is unavailable.", Details: map[string]any{}, ErrorCode: "ROOT_OPERATION_UNAVAILABLE", ErrorMessage: "the retired worker operation has no recoverable host result"}
	if row.State != "received" && m.rootControlHandlers != nil {
		// Operational handlers share the host executor. Borrow only its explicit
		// reconcile boundary; never register a retired handler in the live catalog.
		for _, candidate := range m.rootControlHandlers.byOperation {
			handler, ok := candidate.(operationRootControlHandler)
			if !ok || handler.executor == nil {
				continue
			}
			switch f := handler.executor.(type) {
			case RootControlOperationExecutorFunc:
				if f.ReconcileFunc == nil {
					continue // This helper otherwise falls back to Execute.
				}
			case *RootControlOperationExecutorFunc:
				if f == nil || f.ReconcileFunc == nil {
					continue // A nil pointer cannot reconcile; otherwise avoid Execute fallback.
				}
			}
			handler.identity = RootControlHandlerIdentity{CapabilityID: row.CapabilityID, ToolName: row.ToolName, DefinitionID: row.DefinitionID, OperationID: row.OperationID}
			invocation := RootControlInvocation{RequestID: row.RequestID, ResultID: row.ResultID, ToolCallID: row.ToolCallID, OperationID: row.OperationID, Arguments: map[string]any{}, Fence: row.Fence}
			saved, reconcileErr := invokeRootControlHandler(ctx, handler, invocation, true)
			if reconcileErr == nil && saved.Status == "ok" {
				result = saved
			}
			break
		}
	}
	m.finishRootControlHandler(ctx, binding, nil, claim, result, nil, row.State == "side_effect_committed")
}

// invokeRootControlHandler contains only the dynamic handler call. A handler
// panic becomes a fixed error so finishRootControlHandler can persist the
// terminal nonretryable result without exposing the recovered value.
func invokeRootControlHandler(ctx context.Context, handler RootControlHandler, invocation RootControlInvocation, reconcile bool) (result RootControlHandlerResult, err error) {
	defer func() {
		if recover() != nil {
			result = RootControlHandlerResult{}
			err = errRootControlHandlerPanic
		}
	}()
	if reconcile {
		return handler.Reconcile(ctx, invocation)
	}
	return handler.Execute(ctx, invocation)
}

// executeRootControlHandler keeps context.compact on Pi's typed RPC boundary.
// The Python root-control adapter can retain process-local settings, but it
// cannot produce a Pi compaction or its native lifecycle events. A recovered
// uncertain compact is deliberately not replayed: duplicating a completed
// compaction would discard a second span of conversation history.
func (m *Manager) executeRootControlHandler(ctx context.Context, binding *managedBinding, handler RootControlHandler, invocation RootControlInvocation, reconcile bool) (RootControlHandlerResult, error) {
	if invocation.OperationID == "policy.inline_allowance" {
		if reconcile {
			// Allowances are deliberately process-local and never restored from a
			// stale request. A recovered handler has no retained raw arguments, so
			// it must not guess a new credit grant.
			return RootControlHandlerResult{}, errors.New("inline allowance outcome is unknown after recovery")
		}
		return m.executeManagedInlineAllowance(ctx, binding, invocation)
	}
	if invocation.OperationID == "goal.control" {
		if reconcile {
			// The desired revision is the idempotency boundary. A recovered handler
			// never creates a second revision for the same root request.
			return m.managedGoalStatus(ctx, invocation.Fence.AgentID)
		}
		return m.executeManagedGoalControl(ctx, binding, invocation)
	}
	if invocation.OperationID == "goal.snooze" {
		if reconcile {
			return m.managedGoalStatus(ctx, invocation.Fence.AgentID)
		}
		return m.executeManagedGoalSnooze(ctx, binding, invocation)
	}
	if invocation.OperationID != "context.control" {
		return invokeRootControlHandler(ctx, handler, invocation, reconcile)
	}
	arguments, ok := invocation.Arguments.(map[string]any)
	if !ok || arguments["action"] != "compact" {
		return invokeRootControlHandler(ctx, handler, invocation, reconcile)
	}
	return RootControlHandlerResult{
		Status:       "error",
		ErrorCode:    "COMPACTION_DEPRECATED",
		ErrorMessage: "Compaction is deprecated; context reset and scratchpad are used instead",
	}, nil
}

func (m *Manager) finishRootControlHandler(ctx context.Context, binding *managedBinding, _ RootControlHandler, claim *store.ManagedControlHandlerClaim, result RootControlHandlerResult, executeErr error, alreadyCommitted bool) {
	if claim == nil {
		return
	}
	fence := bindingFence(binding.agentID, binding)
	if executeErr != nil {
		// A handler failure is still a terminal answer to the dynamic invocation.
		// Leaving it as a failed ledger row strands Pi's pending request forever,
		// even though the caller must be told that no credit was granted.
		result = RootControlHandlerResult{
			Status:       "error",
			Content:      "The managed control operation could not be completed.",
			Details:      map[string]any{},
			ErrorCode:    "HANDLER_FAILED",
			ErrorMessage: boundedRootError(executeErr),
		}
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	if result.Status != "ok" && result.Status != "error" {
		m.failRootControlHandler(ctx, binding, claim, fence, "HANDLER_INVALID_RESULT", "handler returned an invalid result status")
		return
	}
	if result.Status == "ok" {
		result.ErrorCode, result.ErrorMessage, result.Retryable = "", "", false
	}
	// Delivery retry is owned by the append ledger. A normal handler error is
	// correlated completion rather than a request re-execution instruction.
	result.Retryable = false
	if result.Status == "error" && (result.ErrorCode == "" || !validManifestText(result.ErrorCode, 128, false)) {
		m.failRootControlHandler(ctx, binding, claim, fence, "HANDLER_INVALID_RESULT", "handler returned an invalid error code")
		return
	}
	if !alreadyCommitted {
		if err := m.store.CommitManagedControlSideEffect(ctx, claim.Request.RequestID, claim.Owner, fence); err != nil {
			return
		}
	}
	details, err := canonicalJSON(result.Details)
	if err != nil {
		m.failRootControlHandler(ctx, binding, claim, fence, "HANDLER_INVALID_RESULT", "handler returned invalid details")
		return
	}
	payload := rootControlPayload(claim.Request, result, result.Details)
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return
	}
	completed, err := m.store.CompleteManagedControl(ctx, claim.Request.RequestID, claim.Owner, fence, store.ManagedControlResult{Status: result.Status, Content: result.Content, DetailsJSON: string(details), PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256(canonical)), DecodedPayloadBytes: len(canonical), ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage, Retryable: result.Retryable})
	if err == nil && completed != nil {
		m.emitEvent(managedRootControlResultEvent(*completed))
		m.recordRootControlHistory(ctx, binding.agentID, claim.Request.ToolName, claim.Request.OperationID, claim.Request.RequestID, result)
		if result.Status == "ok" && claim.Request.OperationID == "web.search" {
			m.recordManagedGoalActivity(binding, fence, managedGoalActivity{Kind: "tool", ID: claim.Request.RequestID})
		}
		m.appendRootControlResult(ctx, binding, *completed)
	}
}

func (m *Manager) failRootControlHandler(ctx context.Context, binding *managedBinding, claim *store.ManagedControlHandlerClaim, fence store.ManagedRuntimeFence, code, detail string) {
	if claim == nil || binding == nil {
		return
	}
	details := map[string]any{}
	result := RootControlHandlerResult{
		Status:       "error",
		Content:      "The managed control operation could not be completed.",
		Details:      details,
		ErrorCode:    code,
		ErrorMessage: detail,
	}
	canonicalDetails, err := canonicalJSON(details)
	if err != nil {
		return
	}
	canonicalPayload, err := canonicalJSON(rootControlPayload(claim.Request, result, details))
	if err != nil {
		return
	}
	failed, err := m.store.FailManagedControlWithResult(ctx, claim.Request.RequestID, claim.Owner, fence, store.ManagedControlResult{
		Status:              result.Status,
		Content:             result.Content,
		DetailsJSON:         string(canonicalDetails),
		PayloadSHA256:       fmt.Sprintf("%x", sha256.Sum256(canonicalPayload)),
		DecodedPayloadBytes: len(canonicalPayload),
		ErrorCode:           result.ErrorCode,
		ErrorMessage:        result.ErrorMessage,
	})
	if err != nil || failed == nil {
		return
	}
	m.emitEvent(managedRootControlResultEvent(*failed))
	m.appendRootControlResult(ctx, binding, *failed)
}

// recordRootControlHistory uses immutable control identity plus a closed
// classification of the terminal result. Raw result content and error text can
// contain untrusted data and must never cross into the timeline projection.
func (m *Manager) recordRootControlHistory(ctx context.Context, agentID, toolName, operationID, requestID string, result RootControlHandlerResult) {
	if toolName == "" || requestID == "" {
		return
	}
	outcome := "success"
	level, message := "info", "Ran "+toolName
	presentation := &store.ManagedHistoryPresentation{Kind: "tool", ToolName: toolName, Outcome: outcome, Summary: "Completed successfully"}
	if toolName == "goal" {
		message = "Updated goal"
	}
	if toolName == "mecha_reminder" {
		message = "Reminder set"
		presentation = &store.ManagedHistoryPresentation{Kind: "reminder", Outcome: outcome, Summary: "Completed successfully"}
	}
	if result.Status == "error" {
		outcome = "error"
		level = "error"
		presentation.Outcome = "error"
		presentation.Summary = "The operation could not be completed."
		presentation.Error = &store.ManagedHistoryError{Code: "TOOL_FAILED", Retryable: false, DetailAvailable: false}
		switch toolName {
		case "goal":
			message = "Goal update failed"
		case "mecha_reminder":
			message = "Reminder failed"
		case "bash":
			message = "Bash failed"
		default:
			message = "Tool failed"
		}
		// Pi exposes this expected no-op only as stable RPC error text.
		// Require the exact operation, router error class, and full text before
		// replacing redacted failure copy with a fixed safe classification.
		if operationID == "context.control" && result.ErrorCode == "HANDLER_FAILED" && result.ErrorMessage == piContextTooSmallError {
			outcome = "warning"
			level = "warning"
			message = "Compaction skipped"
			presentation.Outcome = "warning"
			presentation.Summary = managedContextTooSmallSafeSummary
			presentation.Error = &store.ManagedHistoryError{Code: managedContextTooSmallErrorCode, Retryable: false, DetailAvailable: false}
		}
	}
	entry, err := m.store.RecordManagedCompleteHistoryWithPresentation(ctx, agentID, "managed_control", level, message, "managed-control:"+requestID+":"+outcome, presentation)
	if err == nil {
		m.publishCompleteHistory(entry)
	}
}

func rootControlPayload(row store.ManagedControlRequest, result RootControlHandlerResult, details map[string]any) map[string]any {
	payload := map[string]any{"protocol": "mecha.control/v1", "result_id": row.ResultID, "request_id": row.RequestID, "target_session_epoch": row.Fence.SessionEpoch, "runtime_generation": row.Fence.RuntimeGeneration, "manifest_sha256": row.Fence.ManifestHash, "status": result.Status, "content": result.Content, "details": details}
	if result.Status == "error" {
		payload["error"] = map[string]any{"code": result.ErrorCode, "message": result.ErrorMessage, "retryable": result.Retryable}
	}
	return payload
}

func boundedRootError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.ToValidUTF8(err.Error(), "�")
	if len(text) <= 2_000 {
		return text
	}
	limit := 2_000
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func (m *Manager) appendRootControlResult(ctx context.Context, binding *managedBinding, row store.ManagedControlRequest) {
	rpc, ok := binding.rpc.(RootControlRPC)
	appendableFailure := row.State == "failed" && row.ResultStatus == "error" && row.AppendAttempt == 0
	if !ok || (row.State != "completed" && !appendableFailure) {
		return
	}
	fence := bindingFence(binding.agentID, binding)
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return
	}
	owner, err := m.ids.Next()
	if err != nil {
		return
	}
	token, err := rpc.ArmRootControlResult(row.RequestID, row.ResultEntryID)
	if err != nil {
		return
	}
	defer token.Release()
	claim, err := m.store.ClaimManagedControlAppend(ctx, row.RequestID, owner, fmt.Sprintf("%d", token.Watermark()), fence)
	if err != nil {
		return
	}
	// Claim stores the owner. Re-read it so the same durable identity fences
	// submit, receipt, and retry; a token alone never authorizes those writes.
	owner = claim.Owner
	payload, err := m.store.ManagedControlResultEnvelope(ctx, row.RequestID)
	if err != nil {
		return
	}
	canonical, err := canonicalJSON(payload)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(canonical)) != row.ResultPayloadSHA256 {
		m.retryRootControlAppend(ctx, binding, row.RequestID, owner, claim.Request.AppendAttempt, fence, "result payload integrity check failed")
		return
	}
	_, submitErr := rpc.SubmitRootControlResult(ctx, token, payload)
	// Once submission starts, its result is uncertain. Persist reconciliation
	// before inspecting the response so recovery can never append it again.
	if err := m.store.MarkManagedControlAppendSubmitted(ctx, row.RequestID, owner, claim.Request.AppendAttempt, fence); err != nil {
		return
	}
	if submitErr != nil {
		m.reconcileRootControlAppend(ctx, binding, claim.Request)
		return
	}
	m.reconcileRootControlAppend(ctx, binding, claim.Request)
}

func (m *Manager) reconcileRootControlAppend(ctx context.Context, binding *managedBinding, row store.ManagedControlRequest) {
	stored, err := m.store.ManagedControlRequest(ctx, row.RequestID)
	if err != nil {
		return
	}
	row = *stored
	rpc, ok := binding.rpc.(RootControlRPC)
	if !ok {
		return
	}
	fence := bindingFence(binding.agentID, binding)
	if !m.rootBindingCurrent(ctx, binding, fence) {
		return
	}
	payload, err := m.store.ManagedControlResultEnvelope(ctx, row.RequestID)
	if err != nil {
		return
	}
	if row.State == "append_pending" {
		if err := m.store.MarkManagedControlAppendSubmitted(ctx, row.RequestID, row.AppendOwner, row.AppendAttempt, fence); err != nil {
			return
		}
	}
	receipt, err := rpc.ReconcileRootControlResult(ctx, payload)
	if err != nil || receipt.RequestID != row.RequestID || receipt.ResultID != row.ResultID || receipt.PayloadSHA256 != row.ResultPayloadSHA256 || receipt.RuntimeGeneration != fence.RuntimeGeneration || receipt.SessionEpoch != fence.SessionEpoch || receipt.ManifestSHA256 != fence.ManifestHash {
		return
	}
	if m.store.DeliverManagedControlReceipt(ctx, store.ManagedControlReceipt{RequestID: row.RequestID, AppendOwner: row.AppendOwner, EntryIdentity: receipt.ReceiptID, PayloadSHA256: receipt.PayloadSHA256, ReceiptID: receipt.ReceiptID, Attempt: row.AppendAttempt, Fence: fence}) == nil {
		acknowledgeDirectRootControlReceipt(binding.rpc, row.RequestID, row.ResultStatus)
	}
}

func (m *Manager) retryRootControlAppend(ctx context.Context, binding *managedBinding, requestID, owner string, attempt int, fence store.ManagedRuntimeFence, detail string) {
	if err := m.store.RetryManagedControlAppend(ctx, requestID, owner, attempt, fence, detail); err != nil {
		return
	}
	row, err := m.store.ManagedControlRequest(ctx, requestID)
	if err != nil || row.State != "completed" || row.NextAppendAt == "" {
		return
	}
	due, err := time.Parse("2006-01-02T15:04:05.000Z", row.NextAppendAt)
	if err != nil {
		return
	}
	delay := due.Sub(m.clock.Now())
	if delay < 0 {
		delay = 0
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-m.runCtx.Done():
			return
		case <-timer.C:
			binding.rootControlSerial.Lock()
			defer binding.rootControlSerial.Unlock()
			if m.rootBindingCurrent(m.runCtx, binding, fence) {
				m.resumeRootControl(m.runCtx, binding, requestID, nil)
			}
		}
	}()
}

func validRootControlReceipt(event map[string]any, row store.ManagedControlRequest, watermark, armed uint64) bool {
	return watermark > armed && validRootControlResult(event, row, false)
}

func rootControlReceiptEntryIdentity(event map[string]any, row store.ManagedControlRequest) string {
	message := nestedMessage(event)
	for _, candidate := range []map[string]any{event, message} {
		if candidate == nil {
			continue
		}
		// Pi's stdout receipt uses entryId; persisted records carry id. Keep the
		// real opaque identity for the durable delivery receipt in either shape.
		for _, key := range []string{"entryId", "entry_id", "id"} {
			entryID, ok := candidate[key].(string)
			if ok && entryID != "" {
				return entryID
			}
		}
	}
	// Some installed Pi versions stream only the authenticated custom envelope.
	// Its result ID is immutable, fenced, and payload-bound, so it remains a
	// durable receipt identity when Pi omits an entry identity altogether.
	return "root-result:" + row.ResultID
}

func validRootControlEntry(entry map[string]any, row store.ManagedControlRequest) bool {
	return validRootControlResult(entry, row, true)
}

// validRootControlResult authenticates a root result from either Pi transport.
// Recovery can demand the reserved entry ID; the live receipt cannot because
// stock Pi persists a fresh opaque ID despite receiving the requested one.
func validRootControlResult(entry map[string]any, row store.ManagedControlRequest, requireReservedEntryID bool) bool {
	if entry == nil {
		return false
	}
	message := nestedMessage(entry)
	if message == nil {
		message = entry
	}
	if requireReservedEntryID {
		id, idOK := entry["id"].(string)
		if !idOK {
			id, idOK = message["id"].(string)
		}
		if !idOK || id != row.ResultEntryID {
			return false
		}
	}
	if message == nil || message["customType"] != "mecha.control.result.v1" || message["display"] != false || message["content"] != row.ResultContent {
		return false
	}
	details, ok := message["details"].(map[string]any)
	if !ok {
		return false
	}
	if row.ResultStatus == "ok" {
		if exactKeys(details, "protocol", "result_id", "request_id", "target_session_epoch", "runtime_generation", "manifest_sha256", "status", "details") != nil {
			return false
		}
	} else if row.ResultStatus == "error" {
		if exactKeys(details, "protocol", "result_id", "request_id", "target_session_epoch", "runtime_generation", "manifest_sha256", "status", "details", "error") != nil {
			return false
		}
	} else {
		return false
	}
	if details["protocol"] != "mecha.control/v1" || details["result_id"] != row.ResultID || details["request_id"] != row.RequestID || !rootControlEpochMatches(details["target_session_epoch"], row.Fence.SessionEpoch) || details["runtime_generation"] != row.Fence.RuntimeGeneration || details["manifest_sha256"] != row.Fence.ManifestHash || details["status"] != row.ResultStatus {
		return false
	}
	payload := map[string]any{"protocol": details["protocol"], "result_id": details["result_id"], "request_id": details["request_id"], "target_session_epoch": details["target_session_epoch"], "runtime_generation": details["runtime_generation"], "manifest_sha256": details["manifest_sha256"], "status": details["status"], "content": row.ResultContent, "details": details["details"]}
	if row.ResultStatus == "error" {
		errorValue, ok := details["error"].(map[string]any)
		if !ok || exactKeys(errorValue, "code", "message", "retryable") != nil || errorValue["code"] != row.ErrorCode || errorValue["message"] != row.ErrorDetail || errorValue["retryable"] != false {
			return false
		}
		payload["error"] = errorValue
	}
	canonical, err := canonicalJSON(payload)
	return err == nil && fmt.Sprintf("%x", sha256.Sum256(canonical)) == row.ResultPayloadSHA256
}

func rootControlEpochMatches(value any, expected int64) bool {
	switch epoch := value.(type) {
	case json.Number:
		parsed, err := epoch.Int64()
		return err == nil && parsed == expected
	case int:
		return int64(epoch) == expected
	case int64:
		return epoch == expected
	case float64:
		return epoch == float64(expected)
	default:
		return false
	}
}

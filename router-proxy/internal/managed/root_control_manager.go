package managed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const rootControlAppendTimeout = 10 * time.Second

// installRootControlReceiver binds request intake only after the runtime has
// passed normal readiness and becomes the installed binding. This prevents an
// unready/replaced process from admitting root controls.
func (m *Manager) installRootControlReceiver(binding *managedBinding) {
	rpc, ok := binding.rpc.(RootControlRPC)
	if !ok {
		return
	}
	rpc.SetRootControlEventHandler(func(event RootControlEvent) { m.handleRootControlEvent(binding, event) })
	go m.recoverRootControls(binding)
}

func (m *Manager) handleRootControlEvent(binding *managedBinding, event RootControlEvent) {
	if binding == nil {
		return
	}
	go func() {
		binding.rootControlSerial.Lock()
		defer binding.rootControlSerial.Unlock()
		ctx, cancel := context.WithTimeout(m.runCtx, 45*time.Second)
		defer cancel()
		m.admitAndRunRootControl(ctx, binding, event)
	}()
}

func (m *Manager) admitAndRunRootControl(ctx context.Context, binding *managedBinding, event RootControlEvent) {
	fence := bindingFence(binding.agentID, binding)
	if !m.rootBindingCurrent(ctx, binding, fence) || event.RuntimeGeneration != fence.RuntimeGeneration || event.SessionEpoch != fence.SessionEpoch || event.ManifestSHA256 != fence.ManifestHash {
		return
	}
	tool, found := m.rootManifestTool(event)
	if !found || ValidateManifestArguments(tool, event.Arguments) != nil {
		return
	}
	arguments, err := canonicalJSON(event.Arguments)
	if err != nil {
		return
	}
	argumentHash := sha256.Sum256(arguments)
	fingerprintValue := map[string]any{"request_id": event.RequestID, "tool_call_id": event.ToolCallID, "capability_id": event.CapabilityID, "tool_name": event.ToolName, "definition_id": event.DefinitionID, "definition_sha256": event.DefinitionSHA256, "operation_id": event.OperationID, "manifest_sha256": event.ManifestSHA256, "runtime_generation": event.RuntimeGeneration, "session_epoch": event.SessionEpoch, "arguments": event.Arguments}
	fingerprintBytes, err := canonicalJSON(fingerprintValue)
	if err != nil {
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
		return
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
	case "completed":
		m.appendRootControlResult(ctx, binding, *row)
	case "append_pending", "awaiting_append":
		m.reconcileRootControlAppend(ctx, binding, *row)
	}
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
	if invocation.OperationID != "context.control" {
		if reconcile {
			return handler.Reconcile(ctx, invocation)
		}
		return handler.Execute(ctx, invocation)
	}
	arguments, ok := invocation.Arguments.(map[string]any)
	if !ok || arguments["action"] != "compact" {
		if reconcile {
			return handler.Reconcile(ctx, invocation)
		}
		return handler.Execute(ctx, invocation)
	}
	if reconcile {
		return RootControlHandlerResult{}, errors.New("native Pi compaction outcome is unknown after recovery")
	}
	compact, ok := binding.rpc.(NativeCompactionRPC)
	if !ok {
		return RootControlHandlerResult{}, errors.New("managed Pi does not support native compaction")
	}
	instructions := ""
	if value, present := arguments["customInstructions"]; present {
		text, textOK := value.(string)
		if !textOK {
			return RootControlHandlerResult{}, errors.New("managed compaction instructions are invalid")
		}
		instructions = text
	}
	if err := compact.Compact(ctx, instructions); err != nil {
		return RootControlHandlerResult{}, err
	}
	details := map[string]any{"action": "compact", "native_compaction": true}
	if value, present := arguments["postCompactionNotification"]; present {
		note, textOK := value.(string)
		if !textOK {
			return RootControlHandlerResult{}, errors.New("managed post-compaction notification is invalid")
		}
		if note != "" {
			// Notification delivery is a post-success convenience. The completed
			// compaction remains truthful even if Pi cannot append that handoff.
			envelope := map[string]any{"protocol": "mecha.post-compaction/v1", "notification": note, "runtime_generation": invocation.Fence.RuntimeGeneration, "session_epoch": invocation.Fence.SessionEpoch, "manifest_sha256": invocation.Fence.ManifestHash}
			if err := compact.PostCompactionNotification(ctx, envelope); err != nil {
				details["post_compaction_notification"] = "unavailable"
			} else {
				details["post_compaction_notification"] = "delivered"
			}
		}
	}
	return RootControlHandlerResult{Status: "ok", Content: "Pi context compacted.", Details: details}, nil
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
		_ = m.store.FailManagedControl(ctx, claim.Request.RequestID, claim.Owner, fence, "HANDLER_INVALID_RESULT", "handler returned an invalid result status")
		return
	}
	if result.Status == "ok" {
		result.ErrorCode, result.ErrorMessage, result.Retryable = "", "", false
	}
	// Delivery retry is owned by the append ledger. A normal handler error is
	// correlated completion rather than a request re-execution instruction.
	result.Retryable = false
	if result.Status == "error" && (result.ErrorCode == "" || !validManifestText(result.ErrorCode, 128, false)) {
		_ = m.store.FailManagedControl(ctx, claim.Request.RequestID, claim.Owner, fence, "HANDLER_INVALID_RESULT", "handler returned an invalid error code")
		return
	}
	if !alreadyCommitted {
		if err := m.store.CommitManagedControlSideEffect(ctx, claim.Request.RequestID, claim.Owner, fence); err != nil {
			return
		}
	}
	details, err := canonicalJSON(result.Details)
	if err != nil {
		_ = m.store.FailManagedControl(ctx, claim.Request.RequestID, claim.Owner, fence, "HANDLER_INVALID_RESULT", "handler returned invalid details")
		return
	}
	payload := rootControlPayload(claim.Request, result, result.Details)
	canonical, err := canonicalJSON(payload)
	if err != nil {
		return
	}
	completed, err := m.store.CompleteManagedControl(ctx, claim.Request.RequestID, claim.Owner, fence, store.ManagedControlResult{Status: result.Status, Content: result.Content, DetailsJSON: string(details), PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256(canonical)), DecodedPayloadBytes: len(canonical), ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage, Retryable: result.Retryable})
	if err == nil && completed != nil {
		outcome := "success"
		if result.Status == "error" {
			outcome = "error"
		}
		m.recordRootControlHistory(ctx, binding.agentID, claim.Request.ToolName, outcome, claim.Request.RequestID)
		m.appendRootControlResult(ctx, binding, *completed)
	}
}

// recordRootControlHistory uses the immutable tool name and terminal result.
// Arguments and result content can contain human-entered text, so they never
// cross into the timeline projection.
func (m *Manager) recordRootControlHistory(ctx context.Context, agentID, toolName, outcome, requestID string) {
	if toolName == "" || requestID == "" {
		return
	}
	level, message := "info", "Ran "+toolName
	presentation := &store.ManagedHistoryPresentation{Kind: "tool", ToolName: toolName, Outcome: outcome, Summary: "Completed successfully"}
	if toolName == "goal" {
		message = "Updated goal"
	}
	if toolName == "mecha_reminder" {
		message = "Reminder set"
		presentation = &store.ManagedHistoryPresentation{Kind: "reminder", Outcome: outcome, Summary: "Completed successfully"}
	}
	if outcome == "error" {
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
	text := err.Error()
	if len(text) > 2_000 {
		return text[:2_000]
	}
	return text
}

func (m *Manager) appendRootControlResult(ctx context.Context, binding *managedBinding, row store.ManagedControlRequest) {
	rpc, ok := binding.rpc.(RootControlRPC)
	if !ok || row.State != "completed" {
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
	wait, err := rpc.SubmitRootControlResult(ctx, token, payload)
	if err != nil {
		// A command response can be lost after Pi persisted the explicit entry.
		// Probe that one ID before spending another bounded append attempt.
		m.reconcileRootControlAppend(ctx, binding, claim.Request)
		return
	}
	if err := m.store.MarkManagedControlAppendSubmitted(ctx, row.RequestID, owner, claim.Request.AppendAttempt, fence); err != nil {
		return
	}
	// Stock Pi assigns its own persisted entry ID even when the command requests
	// a deterministic one. Its stdout receipt still carries the complete fenced
	// result envelope, so accept that exact event instead of retrying a result
	// the user has already received. Builds that do not emit it retain the
	// reserved-ID lookup in the timeout recovery path below.
	deadline, cancel := context.WithTimeout(ctx, rootControlAppendTimeout)
	defer cancel()
	for {
		select {
		case receipt, open := <-wait:
			if !open {
				m.reconcileRootControlAppend(ctx, binding, claim.Request)
				return
			}
			if !validRootControlReceipt(receipt.Event, row, receipt.Watermark, token.Watermark()) {
				continue
			}
			entryIdentity := rootControlReceiptEntryIdentity(receipt.Event, row)
			_ = m.store.DeliverManagedControlReceipt(ctx, store.ManagedControlReceipt{RequestID: row.RequestID, AppendOwner: owner, EntryIdentity: entryIdentity, Attempt: claim.Request.AppendAttempt, Fence: fence})
			return
		case <-deadline.Done():
			m.reconcileRootControlAppend(ctx, binding, claim.Request)
			return
		}
	}
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
	entry, err := rpc.GetRootControlEntry(ctx, row.ResultEntryID)
	if err == nil && validRootControlEntry(entry, row) {
		if row.State == "append_pending" {
			_ = m.store.MarkManagedControlAppendSubmitted(ctx, row.RequestID, row.AppendOwner, row.AppendAttempt, fence)
		}
		_ = m.store.DeliverManagedControlReceipt(ctx, store.ManagedControlReceipt{RequestID: row.RequestID, AppendOwner: row.AppendOwner, EntryIdentity: row.ResultEntryID, Attempt: row.AppendAttempt, Fence: fence})
		return
	}
	m.retryRootControlAppend(ctx, binding, row.RequestID, row.AppendOwner, row.AppendAttempt, fence, "result entry was absent during recovery")
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

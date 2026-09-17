package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// rootAdmissionAppendToken is deliberately inert. The fake RPC has one
// ordered result watermark, so append admission cannot depend on wall-clock or
// a live Pi process.
type rootAdmissionAppendToken struct{}

func (rootAdmissionAppendToken) Watermark() uint64 { return 0 }
func (rootAdmissionAppendToken) Release()          {}

type rootAdmissionRPC struct {
	*goalProjectionRPC

	mu           sync.Mutex
	handler      func(RootControlEvent)
	submissions  []map[string]any
	acknowledged []string
	rejected     []string
	submitted    chan struct{}
	receiptGate  <-chan struct{}
	armErr       error
	submitErr    error
	recoverEntry bool
}

func (r *rootAdmissionRPC) SetRootControlEventHandler(handler func(RootControlEvent)) {
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
}

func (r *rootAdmissionRPC) emit(event RootControlEvent) {
	r.mu.Lock()
	handler := r.handler
	r.mu.Unlock()
	if handler != nil {
		handler(event)
	}
}

func (r *rootAdmissionRPC) ArmRootControlResult(string, string) (RootControlAppendToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.armErr != nil {
		return nil, r.armErr
	}
	return rootAdmissionAppendToken{}, nil
}

func (r *rootAdmissionRPC) SubmitRootControlResult(_ context.Context, _ RootControlAppendToken, payload map[string]any) (<-chan RootControlResultReceipt, error) {
	r.mu.Lock()
	r.submissions = append(r.submissions, cloneJSONRecord(payload))
	gate := r.receiptGate
	submitted := r.submitted
	submitErr := r.submitErr
	r.mu.Unlock()
	if submitted != nil {
		close(submitted)
	}
	if submitErr != nil {
		return nil, submitErr
	}
	if gate != nil {
		<-gate
	}

	details := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			details[key] = value
		}
	}
	receipts := make(chan RootControlResultReceipt, 1)
	receipts <- RootControlResultReceipt{Event: map[string]any{
		"id":         "opaque-pi-result-entry",
		"customType": "mecha.control.result.v1",
		"display":    false,
		"content":    payload["content"],
		"details":    details,
	}, Watermark: 1}
	close(receipts)
	return receipts, nil
}

func (r *rootAdmissionRPC) GetRootControlEntry(_ context.Context, entryID string) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.recoverEntry || len(r.submissions) == 0 {
		return nil, errors.New("exact entry lookup is not configured")
	}
	payload := r.submissions[len(r.submissions)-1]
	details := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			details[key] = value
		}
	}
	return map[string]any{"id": entryID, "customType": "mecha.control.result.v1", "display": false, "content": payload["content"], "details": details}, nil
}

func (r *rootAdmissionRPC) submissionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.submissions)
}

func (r *rootAdmissionRPC) AcknowledgeDirectRootControlReceipt(requestID string, _ directRootControlResultStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acknowledged = append(r.acknowledged, requestID)
}

func (r *rootAdmissionRPC) RejectRootControlRequest(requestID string, _ store.ManagedRuntimeFence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejected = append(r.rejected, requestID)
}

func (r *rootAdmissionRPC) acknowledgments() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.acknowledged...)
}

func (r *rootAdmissionRPC) rejections() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.rejected...)
}

type rootAdmissionHandler struct {
	identity RootControlHandlerIdentity

	mu     sync.Mutex
	calls  []RootControlInvocation
	result RootControlHandlerResult
}

type panicOnceRootAdmissionHandler struct {
	identity RootControlHandlerIdentity

	mu    sync.Mutex
	calls int
}

func (h *panicOnceRootAdmissionHandler) Identity() RootControlHandlerIdentity { return h.identity }

func (h *panicOnceRootAdmissionHandler) Execute(_ context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	h.mu.Lock()
	h.calls++
	calls := h.calls
	h.mu.Unlock()
	if calls == 1 {
		panic("private panic marker")
	}
	return RootControlHandlerResult{Status: "ok", Content: "accepted", Details: map[string]any{"accepted": true}}, nil
}

func (h *panicOnceRootAdmissionHandler) Reconcile(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	panic("private panic marker")
}

func (h *panicOnceRootAdmissionHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func (h *rootAdmissionHandler) Identity() RootControlHandlerIdentity { return h.identity }

func (h *rootAdmissionHandler) Execute(_ context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, invocation)
	h.mu.Unlock()
	h.mu.Lock()
	result := h.result
	h.mu.Unlock()
	if result.Status != "" {
		return result, nil
	}
	return RootControlHandlerResult{Status: "ok", Content: "accepted", Details: map[string]any{"accepted": true}}, nil
}

func (h *rootAdmissionHandler) Reconcile(context.Context, RootControlInvocation) (RootControlHandlerResult, error) {
	return RootControlHandlerResult{Status: "ok", Content: "reconciled", Details: map[string]any{"reconciled": true}}, nil
}

func (h *rootAdmissionHandler) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func newRootAdmissionFixture(t *testing.T) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, ManifestTool, *rootAdmissionRPC, *rootAdmissionHandler) {
	return newRootAdmissionFixtureWithReceiver(t, true)
}

func newRootAdmissionFixtureWithReceiver(t *testing.T, installReceiver bool) (*Manager, *store.Store, *managedBinding, store.ManagedRuntimeFence, ManifestTool, *rootAdmissionRPC, *rootAdmissionHandler) {
	t.Helper()
	manager, durable, binding, fence, baseRPC := readyGoalProjectionFixture(t)
	manager.ids = deterministic.NewSequenceIDSource(
		"123e4567-e89b-42d3-a456-426614174101",
		"123e4567-e89b-42d3-a456-426614174102",
		"123e4567-e89b-42d3-a456-426614174103",
		"123e4567-e89b-42d3-a456-426614174104",
		"123e4567-e89b-42d3-a456-426614174105",
		"123e4567-e89b-42d3-a456-426614174106",
	)

	var tool ManifestTool
	for _, candidate := range manager.manifest.Tools {
		if candidate.OperationID == "ops.git_status" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)
	handler := &rootAdmissionHandler{identity: RootControlHandlerIdentity{
		CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID,
		DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID,
	}}
	handlers := defaultRootControlHandlers(manager.manifest)
	for index, candidate := range manager.manifest.Tools {
		if candidate.OperationID == tool.OperationID {
			handlers[index] = handler
		}
	}
	registry, err := NewRootControlHandlerRegistry(manager.manifest, handlers)
	require.NoError(t, err)
	manager.rootControlHandlers = registry

	rpc := &rootAdmissionRPC{goalProjectionRPC: baseRPC}
	binding.rpc = rpc
	if installReceiver {
		manager.installRootControlReceiver(binding)
	}
	return manager, durable, binding, fence, tool, rpc, handler
}

func rootControlFrameForEvent(fence store.ManagedRuntimeFence, tool ManifestTool, requestID string, arguments any) map[string]any {
	frame := rootControlParserFrame(tool, nil)
	details := frame["message"].(map[string]any)["details"].(map[string]any)
	details["request_id"] = requestID
	details["manifest_sha256"] = fence.ManifestHash
	details["runtime_generation"] = fence.RuntimeGeneration
	details["session_epoch"] = fence.SessionEpoch
	details["arguments"] = arguments
	return frame
}

func rootAdmissionEvent(fence store.ManagedRuntimeFence, tool ManifestTool, requestID string, arguments any) RootControlEvent {
	return RootControlEvent{
		RequestID: requestID, ToolCallID: "root-call-1", CapabilityID: tool.CapabilityID,
		ToolName: tool.Name, DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256,
		OperationID: tool.OperationID, ManifestSHA256: fence.ManifestHash,
		RuntimeGeneration: fence.RuntimeGeneration, SessionEpoch: fence.SessionEpoch,
		Arguments: arguments,
	}
}

func rootAdmissionArguments(task string) map[string]any {
	return map[string]any{"action": "status", "task": task}
}

func cloneRootAdmissionBinding(binding *managedBinding) *managedBinding {
	return &managedBinding{
		agentID: binding.agentID, lane: binding.lane, process: binding.process, rpc: binding.rpc,
		identity: binding.identity, sessionID: binding.sessionID, sessionEpoch: binding.sessionEpoch,
		runtimeGeneration: binding.runtimeGeneration, manifestRevision: binding.manifestRevision,
		manifestHash: binding.manifestHash,
	}
}

func waitForManagedControl(t *testing.T, durable *store.Store, requestID string, state string) *store.ManagedControlRequest {
	t.Helper()
	var row *store.ManagedControlRequest
	require.Eventually(t, func() bool {
		candidate, err := durable.ManagedControlRequest(context.Background(), requestID)
		if err != nil {
			return false
		}
		row = candidate
		return candidate.State == state
	}, time.Second, time.Millisecond)
	return row
}

func requireManagedControlDelivered(t *testing.T, durable *store.Store, requestID string) *store.ManagedControlRequest {
	t.Helper()
	row, err := durable.ManagedControlRequest(context.Background(), requestID)
	require.NoError(t, err)
	require.Equal(t, "delivered", row.State)
	return row
}

func assertRejectedRootAdmission(t *testing.T, durable *store.Store, rpc *rootAdmissionRPC, handler *rootAdmissionHandler, fence store.ManagedRuntimeFence, requestID string, before store.ManagedAgentRecord) {
	t.Helper()
	require.Never(t, func() bool { return handler.callCount() != 0 }, 100*time.Millisecond, time.Millisecond)
	_, err := durable.ManagedControlRequest(context.Background(), requestID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
	require.NoError(t, err)
	require.Empty(t, page.Entries)
	require.Zero(t, rpc.submissionCount())
	after, err := durable.ManagedAgent(context.Background(), fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, *after)
	source, err := durable.ManagedReadinessSource(context.Background(), fence.AgentID)
	require.NoError(t, err)
	require.NotNil(t, source.Fence)
	require.Equal(t, fence, *source.Fence)
	require.NotEmpty(t, source.PolicyConfirmedAt)
	require.NotEmpty(t, source.SnapshotConfirmedAt)
}

func TestRootControlAdmissionRejectsEachFenceFieldMutation(t *testing.T) {
	cases := []struct {
		name          string
		mutateEvent   func(*RootControlEvent)
		mutateBinding func(*managedBinding)
	}{
		{name: "baseline accepted"},
		{name: "foreign agent identity", mutateBinding: func(binding *managedBinding) { binding.agentID = "foreign-agent" }},
		{name: "foreign process binding identity", mutateBinding: func(binding *managedBinding) { binding.identity.PID++ }},
		{name: "runtime generation", mutateEvent: func(event *RootControlEvent) { event.RuntimeGeneration = "123e4567-e89b-42d3-a456-426614174199" }},
		{name: "session epoch", mutateEvent: func(event *RootControlEvent) { event.SessionEpoch++ }},
		{name: "manifest hash", mutateEvent: func(event *RootControlEvent) { event.ManifestSHA256 = strings.Repeat("b", 64) }},
		{name: "manifest revision", mutateBinding: func(binding *managedBinding) { binding.manifestRevision++ }},
		{name: "malformed request identity", mutateEvent: func(event *RootControlEvent) { event.RequestID = "not-a-request-id" }},
		{name: "malformed tool call identity", mutateEvent: func(event *RootControlEvent) { event.ToolCallID = "" }},
		{name: "tool tuple", mutateEvent: func(event *RootControlEvent) { event.ToolName = "foreign_tool" }},
		{name: "capability tuple", mutateEvent: func(event *RootControlEvent) { event.CapabilityID = "foreign_capability" }},
		{name: "definition ID tuple", mutateEvent: func(event *RootControlEvent) {
			event.DefinitionID = "def_" + strings.Repeat("b", 64)
			event.DefinitionSHA256 = strings.Repeat("b", 64)
		}},
		{name: "definition hash tuple", mutateEvent: func(event *RootControlEvent) { event.DefinitionSHA256 = strings.Repeat("b", 64) }},
		{name: "operation tuple", mutateEvent: func(event *RootControlEvent) { event.OperationID = "ops.foreign" }},
		{name: "argument schema", mutateEvent: func(event *RootControlEvent) {
			event.Arguments = map[string]any{"action": "status", "task": "router", "unexpected": true}
		}},
		{name: "argument type", mutateEvent: func(event *RootControlEvent) { event.Arguments = map[string]any{"action": 7, "task": "router"} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manager, durable, binding, fence, tool, rpc, handler := newRootAdmissionFixture(t)
			before, err := durable.ManagedAgent(context.Background(), fence.AgentID)
			require.NoError(t, err)
			// Each subtest gets an independent fixture. Keep request IDs explicit and
			// valid for every case except the intentional malformed request mutation.
			requestID := "123e4567-e89b-42d3-a456-4266141741" + fmt.Sprintf("%02d", len(test.name))
			event := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router"))
			if test.mutateEvent != nil {
				test.mutateEvent(&event)
			}
			if test.mutateBinding != nil {
				clone := cloneRootAdmissionBinding(binding)
				test.mutateBinding(clone)
				manager.installRootControlReceiver(clone)
			}
			if test.name == "baseline accepted" {
				rpc.emit(event)
				row := waitForManagedControl(t, durable, requestID, "delivered")
				require.Equal(t, 1, handler.callCount())
				require.Equal(t, 1, rpc.submissionCount())
				require.Equal(t, []string{requestID}, rpc.acknowledgments(), "manager acknowledges only after the durable receipt transitions this result to delivered")
				require.NotEmpty(t, row.ResultID)
				page, pageErr := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
				require.NoError(t, pageErr)
				require.Len(t, page.Entries, 1)
				require.Equal(t, "Ran mecha_git", page.Entries[0].Message)
				return
			}
			rpc.emit(event)
			assertRejectedRootAdmission(t, durable, rpc, handler, fence, requestID, *before)
			require.Empty(t, rpc.acknowledgments(), "rejected/non-durable admission must not release direct settlement")
			// Manager-only malformed IDs are harmless no-ops in the real RPC, but
			// the fixture records the exact received event ID for admission proof.
			expectedRejectedID := event.RequestID
			require.Equal(t, []string{expectedRejectedID}, rpc.rejections(), "each terminal pre-reservation rejection is released exactly once")
		})
	}
}

func TestRootControlHandlerPanicCompletesOnceAndKeepsRPCUsable(t *testing.T) {
	manager, durable, binding, fence, tool, _, _ := newRootAdmissionFixtureWithReceiver(t, false)
	manager.eventSink = func(Event) { panic("private sink marker") }
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() {
		_ = client.Close()
		_ = stdinWriter.Close()
	})

	handler := &panicOnceRootAdmissionHandler{identity: RootControlHandlerIdentity{
		CapabilityID: tool.CapabilityID, ToolName: tool.Name, DefinitionID: tool.DefinitionID,
		DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID,
	}}
	handlers := defaultRootControlHandlers(manager.manifest)
	for index, candidate := range manager.manifest.Tools {
		if candidate.OperationID == tool.OperationID {
			handlers[index] = handler
		}
	}
	registry, err := NewRootControlHandlerRegistry(manager.manifest, handlers)
	require.NoError(t, err)
	manager.rootControlHandlers = registry
	binding.rpc = client
	client.SetRootControlEventHandler(func(event RootControlEvent) { manager.handleRootControlEvent(binding, event) })

	requests := readJSONLRequests(stdinReader)
	firstRequestID := "123e4567-e89b-42d3-a456-426614174301"
	require.NoError(t, writeRPC(stdoutWriter, rootControlFrameForEvent(fence, tool, firstRequestID, rootAdmissionArguments("panic"))))
	firstAppend := requireRequest(t, requests)
	require.Equal(t, "prompt", firstAppend["type"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": firstAppend["id"], "type": "response", "command": "prompt", "success": true}))
	firstAwaitingAppend := waitForManagedControl(t, durable, firstRequestID, "awaiting_append")
	firstPayload, err := durable.ManagedControlResultEnvelope(context.Background(), firstRequestID)
	require.NoError(t, err)
	firstDetails := make(map[string]any, len(firstPayload)-1)
	for key, value := range firstPayload {
		if key != "content" {
			firstDetails[key] = value
		}
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{
		"type": "message_end", "message": map[string]any{"customType": "mecha.control.result.v1", "display": false, "content": firstPayload["content"], "details": firstDetails},
	}))
	firstDelivered := waitForManagedControl(t, durable, firstRequestID, "delivered")
	require.Equal(t, "error", firstDelivered.ResultStatus)
	require.Equal(t, "HANDLER_FAILED", firstDelivered.ErrorCode)
	require.Equal(t, "managed root control handler failed internally", firstDelivered.ErrorDetail)
	require.False(t, firstDelivered.Retryable)
	require.NotContains(t, firstDelivered.ErrorDetail, "private panic marker")
	require.NotContains(t, firstDelivered.ResultContent, "private panic marker")
	require.Equal(t, firstAwaitingAppend.ResultID, firstDelivered.ResultID)
	require.Equal(t, 1, handler.callCount())

	getStateDone := make(chan error, 1)
	go func() {
		_, getStateErr := client.GetState(context.Background())
		getStateDone <- getStateErr
	}()
	getState := requireRequest(t, requests)
	require.Equal(t, "get_state", getState["type"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": getState["id"], "type": "response", "command": "get_state", "success": true, "data": map[string]any{}}))
	require.NoError(t, <-getStateDone)
	require.Nil(t, client.terminalError(), "handler panic must not terminalize the RPC client")

	secondRequestID := "123e4567-e89b-42d3-a456-426614174302"
	require.NoError(t, writeRPC(stdoutWriter, rootControlFrameForEvent(fence, tool, secondRequestID, rootAdmissionArguments("valid"))))
	secondAppend := requireRequest(t, requests)
	require.Equal(t, "prompt", secondAppend["type"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": secondAppend["id"], "type": "response", "command": "prompt", "success": true}))
	secondPayload := func() map[string]any {
		var payload map[string]any
		require.Eventually(t, func() bool {
			payload, err = durable.ManagedControlResultEnvelope(context.Background(), secondRequestID)
			return err == nil
		}, time.Second, time.Millisecond)
		return payload
	}()
	secondDetails := make(map[string]any, len(secondPayload)-1)
	for key, value := range secondPayload {
		if key != "content" {
			secondDetails[key] = value
		}
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{
		"type": "message_end", "message": map[string]any{"customType": "mecha.control.result.v1", "display": false, "content": secondPayload["content"], "details": secondDetails},
	}))
	secondDelivered := waitForManagedControl(t, durable, secondRequestID, "delivered")
	require.Equal(t, "ok", secondDelivered.ResultStatus)
	require.Equal(t, 2, handler.callCount())
	require.Nil(t, client.terminalError())

	page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2, "each request gets one terminal history entry")
}

func TestInvalidRootControlResultSettlesDirectTurnAndAllowsNextMessage(t *testing.T) {
	manager, durable, binding, fence, tool, _, handler := newRootAdmissionFixtureWithReceiver(t, false)
	handler.result = RootControlHandlerResult{Status: "error", Content: "ignored", Details: map[string]any{}, ErrorMessage: "ignored"}
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() {
		_ = client.Close()
		_ = stdinWriter.Close()
	})
	binding.rpc = client
	manager.installRootControlReceiver(binding)
	requests := readJSONLRequests(stdinReader)
	activity := make(chan map[string]any, 2)
	client.SetParentActivityEventHandler(func(event map[string]any) { activity <- event })

	firstAttempt := directMailboxAttempt("invalid-root-message")
	firstAttempt.Fence = fence
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstEvents, err := DirectMailboxRPC(client).PromptManagedMailbox(firstCtx, firstAttempt)
	require.NoError(t, err)
	firstPrompt := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": firstPrompt["id"], "type": "response", "command": "prompt", "success": true, "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-invalid", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": firstAttempt.MessageID, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch}}}))
	select {
	case event := <-firstEvents:
		require.NotNil(t, event.Receipt)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct append receipt")
	}

	requestID := "123e4567-e89b-42d3-a456-426614174390"
	require.NoError(t, writeRPC(stdoutWriter, rootControlFrameForEvent(fence, tool, requestID, rootAdmissionArguments("invalid-result"))))
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		_, pending := client.directMailbox.pendingRootRequests[requestID]
		return pending
	}, time.Second, time.Millisecond)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	select {
	case event := <-firstEvents:
		t.Fatalf("invalid root result settled before durable error receipt: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case event := <-activity:
		t.Fatalf("invalid root result published idle before durable error receipt: %#v", event)
	case <-time.After(50 * time.Millisecond):
	}

	rootAppend := requireRequest(t, requests)
	require.Equal(t, "prompt", rootAppend["type"])
	var payload map[string]any
	require.Eventually(t, func() bool {
		payload, err = durable.ManagedControlResultEnvelope(context.Background(), requestID)
		return err == nil
	}, time.Second, time.Millisecond)
	require.Equal(t, "error", payload["status"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": rootAppend["id"], "type": "response", "command": "prompt", "success": true, "data": map[string]any{}}))
	resultDetails := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			resultDetails[key] = value
		}
	}
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "opaque-invalid-result", "customType": "mecha.control.result.v1", "display": false, "content": payload["content"], "details": resultDetails}}))
	row := waitForManagedControl(t, durable, requestID, "delivered")
	require.Equal(t, "error", row.ResultStatus)
	require.Equal(t, "HANDLER_INVALID_RESULT", row.ErrorCode)
	select {
	case event := <-firstEvents:
		require.True(t, event.Settled)
		require.Nil(t, event.Reply)
	case <-time.After(time.Second):
		t.Fatal("invalid root result did not release direct settlement")
	}
	select {
	case event := <-activity:
		require.Equal(t, "idle", event["state"])
	case <-time.After(time.Second):
		t.Fatal("invalid root result did not publish parent idle")
	}

	cancelFirst()
	for range firstEvents {
	}
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.directMailbox == nil
	}, time.Second, time.Millisecond)

	secondAttempt := directMailboxAttempt("after-invalid-root")
	secondAttempt.Fence = fence
	secondEvents, err := DirectMailboxRPC(client).PromptManagedMailbox(context.Background(), secondAttempt)
	require.NoError(t, err)
	secondPrompt := requireRequest(t, requests)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": secondPrompt["id"], "type": "response", "command": "prompt", "success": true, "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-next", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": secondAttempt.MessageID, "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	var gotReceipt, gotSettled bool
	for !gotReceipt || !gotSettled {
		select {
		case event := <-secondEvents:
			gotReceipt = gotReceipt || event.Receipt != nil
			gotSettled = gotSettled || event.Settled
		case <-time.After(time.Second):
			t.Fatal("next direct message was not accepted after invalid root result")
		}
	}
	require.True(t, gotReceipt)
	require.True(t, gotSettled)
}

func TestFailedRootControlResultRecoversWhenAppendArmFailsBeforeClaim(t *testing.T) {
	manager, durable, binding, fence, tool, rpc, handler := newRootAdmissionFixtureWithReceiver(t, false)
	// An invalid handler result is durably retained as a failed result before
	// append admission. A transient arm failure must not make that result
	// disappear from the next recovery scan.
	handler.result = RootControlHandlerResult{Status: "error", Content: "ignored", Details: map[string]any{}, ErrorMessage: "missing code"}
	rpc.armErr = errors.New("append arm temporarily unavailable")
	requestID := "123e4567-e89b-42d3-a456-426614174356"
	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("append-recovery")))
	failed := waitForManagedControl(t, durable, requestID, "failed")
	require.Equal(t, "HANDLER_INVALID_RESULT", failed.ErrorCode)
	require.Equal(t, "error", failed.ResultStatus)
	require.Zero(t, failed.AppendAttempt)
	require.Empty(t, rpc.acknowledgments())

	restorable, err := durable.ListRestorableManagedControlRequests(context.Background(), fence)
	require.NoError(t, err)
	require.Len(t, restorable, 1)
	require.Equal(t, requestID, restorable[0].RequestID)

	rpc.mu.Lock()
	rpc.armErr = nil
	rpc.mu.Unlock()
	manager.resumeRootControl(context.Background(), binding, requestID, nil)
	delivered := waitForManagedControl(t, durable, requestID, "delivered")
	require.Equal(t, "error", delivered.ResultStatus)
	require.Equal(t, 1, rpc.submissionCount())
}

func TestRootControlDurableReceiptFailureDoesNotAcknowledgeDirectSettlement(t *testing.T) {
	manager, durable, binding, fence, tool, rpc, handler := newRootAdmissionFixtureWithReceiver(t, false)
	rpc.submitted = make(chan struct{})
	releaseReceipt := make(chan struct{})
	rpc.receiptGate = releaseReceipt
	requestID := "123e4567-e89b-42d3-a456-426614174290"
	done := make(chan struct{})
	go func() {
		manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router")))
		close(done)
	}()
	select {
	case <-rpc.submitted:
	case <-time.After(time.Second):
		t.Fatal("full root-control submission did not reach its durable receipt boundary")
	}
	require.Equal(t, 1, handler.callCount(), "validation and handler execution complete before the injected delivery failure")
	require.NoError(t, durable.Close())
	close(releaseReceipt)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("root-control append did not return after durable delivery failure")
	}
	require.Empty(t, rpc.acknowledgments(), "failed DeliverManagedControlReceipt must keep direct settlement provisional")
}

func TestRootControlRecoveryDeliveryAcknowledgesOnlyAfterDurableReceipt(t *testing.T) {
	manager, durable, binding, fence, tool, rpc, _ := newRootAdmissionFixtureWithReceiver(t, false)
	rpc.submitErr = errors.New("lost command response after Pi persisted the entry")
	rpc.recoverEntry = true
	requestID := "123e4567-e89b-42d3-a456-426614174291"
	manager.admitAndRunRootControl(context.Background(), binding, rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router")))
	row := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, []string{requestID}, rpc.acknowledgments(), "recovery acknowledges only after its exact persisted entry is durably delivered")
	require.NotEmpty(t, row.ResultEntryID)
}

func TestRootControlDuplicateRequestReusesResultIDAndRejectsChangedFingerprint(t *testing.T) {
	manager, durable, binding, fence, tool, rpc, handler := newRootAdmissionFixtureWithReceiver(t, false)
	requestID := "123e4567-e89b-42d3-a456-426614174201"
	event := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router"))
	manager.admitAndRunRootControl(context.Background(), binding, event)
	first := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, 1, handler.callCount())
	require.Equal(t, 1, rpc.submissionCount())

	// Run admission synchronously so the handler/result completion is the
	// synchronization point. The exact persisted request is replayed without a
	// second handler claim or result append.
	manager.admitAndRunRootControl(context.Background(), binding, event)
	exactReplay := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, first.ResultID, exactReplay.ResultID)
	require.Equal(t, 1, handler.callCount())
	require.Equal(t, 1, rpc.submissionCount())

	changed := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("another-task"))
	manager.admitAndRunRootControl(context.Background(), binding, changed)
	conflict := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, first.ResultID, conflict.ResultID)
	require.Equal(t, 1, handler.callCount())
	require.Equal(t, 1, rpc.submissionCount())
	page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
}

func rootControlParserFrame(tool ManifestTool, mutation func(map[string]any)) map[string]any {
	details := map[string]any{
		"protocol":           "mecha.control/v1",
		"request_id":         "123e4567-e89b-42d3-a456-426614174250",
		"tool_call_id":       "parser-call",
		"capability_id":      tool.CapabilityID,
		"tool_name":          tool.Name,
		"definition_id":      tool.DefinitionID,
		"definition_sha256":  tool.DefinitionSHA256,
		"operation_id":       tool.OperationID,
		"manifest_sha256":    strings.Repeat("a", 64),
		"runtime_generation": "123e4567-e89b-42d3-a456-426614174251",
		"session_epoch":      1,
		"context_ref":        "root",
		"arguments":          rootAdmissionArguments("router"),
	}
	frame := map[string]any{"type": "message_end", "message": map[string]any{
		"role": "custom", "customType": "mecha.control.request.v1", "display": false,
		"content": "Router control request queued.", "timestamp": float64(0), "details": details,
	}}
	if mutation != nil {
		mutation(frame)
	}
	return frame
}

func TestRootControlMalformedCandidatesNeverReachReadinessBacklog(t *testing.T) {
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	var tool ManifestTool
	for _, candidate := range manifest.Tools {
		if candidate.OperationID == "ops.git_status" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)

	cases := []struct {
		name       string
		mutate     func(map[string]any)
		rawPayload string
		wantEvent  bool
	}{
		{name: "valid root control is outside readiness backlog", wantEvent: true},
		{name: "display mutation", mutate: func(frame map[string]any) { frame["message"].(map[string]any)["display"] = true }},
		{name: "content mutation", mutate: func(frame map[string]any) { frame["message"].(map[string]any)["content"] = "ordinary message" }},
		{name: "unknown detail key", mutate: func(frame map[string]any) {
			frame["message"].(map[string]any)["details"].(map[string]any)["foreign"] = true
		}},
		{name: "wrong epoch type", mutate: func(frame map[string]any) {
			frame["message"].(map[string]any)["details"].(map[string]any)["session_epoch"] = "1"
		}},
		{name: "missing details", mutate: func(frame map[string]any) { frame["message"].(map[string]any)["details"] = "malformed" }},
		{name: "duplicate JSON detail key", rawPayload: `{"type":"message_end","message":{"customType":"mecha.control.request.v1","display":false,"content":"Router control request queued.","details":{"protocol":"mecha.control/v1","protocol":"mecha.control/v1"}}}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			stdoutReader, stdoutWriter := io.Pipe()
			client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
			t.Cleanup(func() { _ = client.Close() })
			events := make(chan RootControlEvent, 1)
			client.SetRootControlEventHandler(func(event RootControlEvent) { events <- event })
			if test.rawPayload != "" {
				_, err = fmt.Fprintln(stdoutWriter, test.rawPayload)
				require.NoError(t, err)
			} else {
				require.NoError(t, writeRPC(stdoutWriter, rootControlParserFrame(tool, test.mutate)))
			}
			require.Eventually(t, func() bool {
				client.mu.Lock()
				defer client.mu.Unlock()
				return client.eventSequence >= 1 || client.terminal != nil
			}, time.Second, time.Millisecond)
			if test.wantEvent {
				select {
				case <-events:
				case <-time.After(time.Second):
					t.Fatal("valid root control event did not reach its control callback")
				}
			} else {
				select {
				case event := <-events:
					t.Fatalf("malformed root control reached callback: %#v", event)
				default:
				}
			}
			client.mu.Lock()
			require.Empty(t, client.backlog, "root-control candidates must not become readiness receipts")
			client.mu.Unlock()
		})
	}
}

func TestRootControlAdmissionUsesCanonicalArgumentHashForFingerprint(t *testing.T) {
	manager, durable, binding, fence, tool, rpc, handler := newRootAdmissionFixtureWithReceiver(t, false)
	requestID := "123e4567-e89b-42d3-a456-426614174202"
	firstEvent := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router"))
	manager.admitAndRunRootControl(context.Background(), binding, firstEvent)
	first := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, 1, handler.callCount())

	// A valid schema value with a different canonical argument hash is a
	// conflicting reuse of the same request ID, not a second operation.
	changed := rootAdmissionEvent(fence, tool, requestID, rootAdmissionArguments("router-2"))
	manager.admitAndRunRootControl(context.Background(), binding, changed)
	second := requireManagedControlDelivered(t, durable, requestID)
	require.Equal(t, first.ResultID, second.ResultID)
	require.Equal(t, 1, handler.callCount())
	require.Equal(t, 1, rpc.submissionCount())
}

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

	mu          sync.Mutex
	handler     func(RootControlEvent)
	submissions []map[string]any
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
	return rootAdmissionAppendToken{}, nil
}

func (r *rootAdmissionRPC) SubmitRootControlResult(_ context.Context, _ RootControlAppendToken, payload map[string]any) (<-chan RootControlResultReceipt, error) {
	r.mu.Lock()
	r.submissions = append(r.submissions, cloneJSONRecord(payload))
	r.mu.Unlock()

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

func (r *rootAdmissionRPC) GetRootControlEntry(context.Context, string) (map[string]any, error) {
	return nil, errors.New("exact entry lookup is not used by this deterministic receipt")
}

func (r *rootAdmissionRPC) submissionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.submissions)
}

type rootAdmissionHandler struct {
	identity RootControlHandlerIdentity

	mu    sync.Mutex
	calls []RootControlInvocation
}

func (h *rootAdmissionHandler) Identity() RootControlHandlerIdentity { return h.identity }

func (h *rootAdmissionHandler) Execute(_ context.Context, invocation RootControlInvocation) (RootControlHandlerResult, error) {
	h.mu.Lock()
	h.calls = append(h.calls, invocation)
	h.mu.Unlock()
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
				require.NotEmpty(t, row.ResultID)
				page, pageErr := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 20)
				require.NoError(t, pageErr)
				require.Len(t, page.Entries, 1)
				require.Equal(t, "Ran mecha_git", page.Entries[0].Message)
				return
			}
			rpc.emit(event)
			assertRejectedRootAdmission(t, durable, rpc, handler, fence, requestID, *before)
		})
	}
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
		"customType": "mecha.control.request.v1", "display": false,
		"content": "Router control request queued.", "details": details,
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

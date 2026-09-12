package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const managedInlineAllowanceTestManifestHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type inlineAllowanceRPC struct {
	*launchRPC
	policy map[string]any
}

func (r *inlineAllowanceRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.policy, _ = envelope["policy"].(map[string]any)
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *inlineAllowanceRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	inline, _ := r.policy["inline"].(map[string]any)
	recall, _ := r.policy["recall"].(map[string]any)
	settled, _ := r.policy["settled"].(map[string]any)
	goalLock, _ := r.policy["goal_lock"].(map[string]any)
	compaction, _ := r.policy["compaction"].(map[string]any)
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{
		"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"],
		"settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"],
	}}, nil
}

func readyInlineAllowanceFixture(t *testing.T) (*Manager, *managedBinding, store.ManagedRuntimeFence, *inlineAllowanceRPC) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "inline-agent", Name: "inline-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "inline-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedInlineAllowanceTestManifestHash}
	op := store.ManagedOperation{OperationID: "inline-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "inline-launch", CanonicalBodyHash: "inline-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 100, StartMarker: "inline-marker", Incarnation: "inline-incarnation"}, "inline-generation"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "inline-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"inline-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"inline-snapshot"}`), SHA256: managedInlineAllowanceTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	rpc := &inlineAllowanceRPC{launchRPC: &launchRPC{session: fence.PiSessionID, active: expectedManagedActiveTools(manifest)}}
	process := &launchProcess{identity: ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: directControlProbeOps{state: ProcessOwned}})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, binding, fence, rpc
}

func TestHandlerFailureCompletesDynamicInlineAllowanceRequest(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, _ := readyInlineAllowanceFixture(t)
	requestID := "123e4567-e89b-42d3-a456-426614174005"
	reservation, err := manager.store.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{
		RequestID: requestID, ResultID: "123e4567-e89b-42d3-a456-426614174006", AgentID: fence.AgentID,
		ToolCallID: "call-inline", CapabilityID: "mecha_request_inline_tool_allowance", ToolName: "mecha_request_inline_tool_allowance",
		DefinitionID: "def_" + managedInlineAllowanceTestManifestHash, OperationID: "policy.inline_allowance",
		CanonicalArgumentsSHA256: managedInlineAllowanceTestManifestHash, RequestFingerprint: managedInlineAllowanceTestManifestHash,
		Fence: fence, ReceivedAt: "2026-09-12T00:00:00.000Z",
	})
	require.NoError(t, err)
	claim, err := manager.store.ClaimManagedControlHandler(ctx, requestID, "123e4567-e89b-42d3-a456-426614174007", fence)
	require.NoError(t, err)

	manager.finishRootControlHandler(ctx, binding, nil, claim, RootControlHandlerResult{}, errors.New("managed inline tools are disabled"), false)

	completed, err := manager.store.ManagedControlRequest(ctx, reservation.RequestID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.State)
	require.Equal(t, "error", completed.ResultStatus)
	require.Equal(t, "HANDLER_FAILED", completed.ErrorCode)
	require.Equal(t, "The managed control operation could not be completed.", completed.ResultContent)
}

func TestHandlerFailureWithUTF8BoundaryCompletesDynamicInlineAllowanceRequest(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, _ := readyInlineAllowanceFixture(t)
	requestID := "123e4567-e89b-42d3-a456-426614174008"
	reservation, err := manager.store.ReserveManagedControlRequest(ctx, store.ManagedControlRequestInput{
		RequestID: requestID, ResultID: "123e4567-e89b-42d3-a456-426614174009", AgentID: fence.AgentID,
		ToolCallID: "call-inline-utf8", CapabilityID: "mecha_request_inline_tool_allowance", ToolName: "mecha_request_inline_tool_allowance",
		DefinitionID: "def_" + managedInlineAllowanceTestManifestHash, OperationID: "policy.inline_allowance",
		CanonicalArgumentsSHA256: managedInlineAllowanceTestManifestHash, RequestFingerprint: managedInlineAllowanceTestManifestHash,
		Fence: fence, ReceivedAt: "2026-09-12T00:00:00.000Z",
	})
	require.NoError(t, err)
	claim, err := manager.store.ClaimManagedControlHandler(ctx, requestID, "123e4567-e89b-42d3-a456-426614174010", fence)
	require.NoError(t, err)

	manager.finishRootControlHandler(ctx, binding, nil, claim, RootControlHandlerResult{}, errors.New(strings.Repeat("a", 1_998)+"€"), false)

	completed, err := manager.store.ManagedControlRequest(ctx, reservation.RequestID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.State)
	require.Equal(t, "error", completed.ResultStatus)
	require.Equal(t, "HANDLER_FAILED", completed.ErrorCode)
	require.LessOrEqual(t, len(completed.ErrorDetail), 2_000)
	require.True(t, utf8.ValidString(completed.ErrorDetail))
}

type nativeInlineAllowanceRPC struct {
	*rpcClient
	policyMu sync.Mutex
	policy   map[string]any
}

func (r *nativeInlineAllowanceRPC) ApplyPolicy(_ context.Context, envelope map[string]any) (map[string]any, error) {
	r.policyMu.Lock()
	r.policy, _ = envelope["policy"].(map[string]any)
	r.policyMu.Unlock()
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *nativeInlineAllowanceRPC) WaitForPolicyApplied(_ context.Context, generation string, epoch int64) (map[string]any, error) {
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	inline, _ := r.policy["inline"].(map[string]any)
	recall, _ := r.policy["recall"].(map[string]any)
	settled, _ := r.policy["settled"].(map[string]any)
	goalLock, _ := r.policy["goal_lock"].(map[string]any)
	compaction, _ := r.policy["compaction"].(map[string]any)
	return map[string]any{"protocol": "mecha.policy/v1", "runtime_generation": generation, "session_epoch": epoch, "applied": map[string]any{
		"inline_enabled": inline["enabled"], "inline_credits": inline["credits"], "recall_content": recall["content"],
		"settled_enabled": settled["enabled"], "goal_lock_enabled": goalLock["enabled"], "compaction_deferred": compaction["deferred"],
	}}, nil
}

func (r *nativeInlineAllowanceRPC) inlineCredit(toolName string) (int64, bool) {
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	inline, _ := r.policy["inline"].(map[string]any)
	credits, _ := inline["credits"].(map[string]any)
	value, ok := credits[toolName].(int64)
	return value, ok
}

func readyNativeInlineAllowanceFixture(t *testing.T) (*Manager, *managedBinding, store.ManagedRuntimeFence, *nativeInlineAllowanceRPC, <-chan map[string]any, io.WriteCloser) {
	t.Helper()
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	agent := store.ManagedAgentRecord{AgentID: "native-inline-agent", Name: "native-inline-agent", SlotHeld: true, Cwd: "/tmp", SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "native-inline-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: managedInlineAllowanceTestManifestHash}
	op := store.ManagedOperation{OperationID: "native-inline-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "native-inline-launch", CanonicalBodyHash: "native-inline-body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, durable.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: 101, StartMarker: "native-inline-marker", Incarnation: "native-inline-incarnation"}, "123e4567-e89b-42d3-a456-426614174011"))
	stored, err := durable.ManagedAgent(ctx, agent.AgentID)
	require.NoError(t, err)
	fence := storedAgentFence(stored)
	require.NoError(t, durable.ConfirmManagedPolicyApplied(ctx, fence, store.DefaultManagedReadinessPolicy()))
	snapshot := store.ManagedChildSnapshot{SnapshotID: "native-inline-snapshot", OwnerSessionID: fence.PiSessionID, Sequence: 0, Canonical: []byte(`{"owner_session_id":"native-inline-session","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"native-inline-snapshot"}`), SHA256: managedInlineAllowanceTestManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(ctx, fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(ctx, fence))

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	rpc := &nativeInlineAllowanceRPC{rpcClient: NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)}
	process := &launchProcess{identity: ProcessIdentity{PID: fence.ProcessPID, StartMarker: fence.ProcessStartMarker, Incarnation: fence.ProcessIncarnation}, stopped: make(chan struct{})}
	manager, err := NewManager(ManagerOptions{Store: durable, Config: Config{Enabled: false}, Manifest: manifest, IDSource: deterministic.NewSequenceIDSource("123e4567-e89b-42d3-a456-426614174012", "123e4567-e89b-42d3-a456-426614174013", "123e4567-e89b-42d3-a456-426614174014"), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }, ProcessGroupOps: directControlProbeOps{state: ProcessOwned}})
	require.NoError(t, err)
	binding := &managedBinding{agentID: fence.AgentID, lane: &managedAgentLane{}, process: process, rpc: rpc, identity: process.identity, sessionID: fence.PiSessionID, sessionEpoch: fence.SessionEpoch, runtimeGeneration: fence.RuntimeGeneration, manifestRevision: fence.ManifestRevision, manifestHash: fence.ManifestHash}
	manager.bindings[fence.AgentID] = binding
	manager.installRootControlReceiver(binding)
	t.Cleanup(func() { _ = manager.Close(context.Background()); _ = durable.Close() })
	return manager, binding, fence, rpc, readJSONLRequests(stdinReader), stdoutWriter
}

func TestNativeInlineAllowanceControlAdmitsOnlyCurrentFencedRequestAndDeliversResult(t *testing.T) {
	ctx := context.Background()
	manager, _, fence, rpc, requests, stdout := readyNativeInlineAllowanceFixture(t)
	require.NoError(t, manager.SetManagedInlineTools(ctx, fence.AgentID, true))
	var tool ManifestTool
	for _, candidate := range manager.manifest.Tools {
		if candidate.OperationID == "policy.inline_allowance" {
			tool = candidate
			break
		}
	}
	require.NotEmpty(t, tool.Name)
	request := func(requestID, generation string, display bool) map[string]any {
		return map[string]any{"type": "custom_message", "customType": "mecha.control.request.v1", "display": display, "content": "Router control request queued.", "details": map[string]any{
			"protocol": "mecha.control/v1", "request_id": requestID, "tool_call_id": "native-inline-call", "capability_id": tool.CapabilityID, "tool_name": tool.Name,
			"definition_id": tool.DefinitionID, "definition_sha256": tool.DefinitionSHA256, "operation_id": tool.OperationID, "manifest_sha256": fence.ManifestHash,
			"runtime_generation": generation, "session_epoch": fence.SessionEpoch, "context_ref": "root", "arguments": map[string]any{"toolName": "bash", "usageCount": int64(1)},
		}}
	}
	malformedID := "123e4567-e89b-42d3-a456-426614174015"
	require.NoError(t, writeRPC(stdout, request(malformedID, fence.RuntimeGeneration, true)))
	staleID := "123e4567-e89b-42d3-a456-426614174016"
	require.NoError(t, writeRPC(stdout, request(staleID, "123e4567-e89b-42d3-a456-426614174017", false)))
	require.Eventually(t, func() bool {
		rpc.mu.Lock()
		defer rpc.mu.Unlock()
		return rpc.eventSequence >= 2
	}, time.Second, time.Millisecond)
	require.Never(t, func() bool {
		row, err := manager.store.ManagedControlRequest(ctx, malformedID)
		return err == nil && row != nil
	}, 50*time.Millisecond, time.Millisecond)
	require.Never(t, func() bool {
		row, err := manager.store.ManagedControlRequest(ctx, staleID)
		return err == nil && row != nil
	}, 50*time.Millisecond, time.Millisecond)

	requestID := "123e4567-e89b-42d3-a456-426614174018"
	require.NoError(t, writeRPC(stdout, request(requestID, fence.RuntimeGeneration, false)))
	prompt := requireRequest(t, requests)
	command, ok := prompt["message"].(string)
	require.True(t, ok)
	encoded := strings.TrimPrefix(command, "/mecha-control-result-v1 ")
	payloadBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadBytes, &payload))
	require.Equal(t, requestID, payload["request_id"])
	require.Equal(t, "ok", payload["status"])
	require.Equal(t, "Granted 1 inline bash use(s).", payload["content"])
	credit, projected := rpc.inlineCredit("bash")
	require.True(t, projected)
	require.EqualValues(t, 1, credit)

	require.NoError(t, writeRPC(stdout, map[string]any{"id": prompt["id"], "type": "response", "data": map[string]any{}}))
	resultDetails := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			resultDetails[key] = value
		}
	}
	require.NoError(t, writeRPC(stdout, map[string]any{"type": "custom_message", "id": "native-inline-result", "customType": "mecha.control.result.v1", "display": false, "content": payload["content"], "details": resultDetails}))
	require.Eventually(t, func() bool {
		row, err := manager.store.ManagedControlRequest(ctx, requestID)
		return err == nil && row.State == "delivered"
	}, time.Second, time.Millisecond)
	row, err := manager.store.ManagedControlRequest(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, "ok", row.ResultStatus)
	require.Equal(t, "native-inline-result", row.ReceiptEntryIdentity)
}

func TestManagedInlineAllowanceProjectsRequestedCreditBeforeSuccess(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, rpc := readyInlineAllowanceFixture(t)
	// Default deny remains in force until the managed inline setting is enabled.
	_, err := manager.executeRootControlHandler(ctx, binding, nil, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174004", OperationID: "policy.inline_allowance",
		Arguments: map[string]any{"toolName": "bash", "usageCount": int64(1)}, Fence: fence,
	}, false)
	require.Error(t, err)
	require.NoError(t, manager.SetManagedInlineTools(ctx, fence.AgentID, true))

	result, err := manager.executeRootControlHandler(ctx, binding, nil, RootControlInvocation{
		RequestID: "123e4567-e89b-42d3-a456-426614174003", OperationID: "policy.inline_allowance",
		Arguments: map[string]any{"toolName": "bash", "usageCount": int64(2)}, Fence: fence,
	}, false)
	require.NoError(t, err)
	require.Equal(t, "ok", result.Status)
	inline := rpc.policy["inline"].(map[string]any)
	require.Equal(t, true, inline["enabled"])
	require.Equal(t, int64(2), inline["credits"].(map[string]any)["bash"])
}

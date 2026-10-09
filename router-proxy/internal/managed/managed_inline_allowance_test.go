package managed

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func readyInlineAllowanceFixture(t *testing.T) (*Manager, *managedBinding, store.ManagedRuntimeFence, *inlineAllowanceRPC, string) {
	t.Helper()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
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
	return manager, binding, fence, rpc, databasePath
}

func TestHandlerFailureCompletesDynamicInlineAllowanceRequest(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, _, _ := readyInlineAllowanceFixture(t)
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
	manager, binding, fence, _, _ := readyInlineAllowanceFixture(t)
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
	policyMu      sync.Mutex
	policy        map[string]any
	useNativePipe bool
}

func (r *nativeInlineAllowanceRPC) ApplyPolicy(ctx context.Context, envelope map[string]any) (map[string]any, error) {
	r.policyMu.Lock()
	r.policy, _ = envelope["policy"].(map[string]any)
	nativePipe := r.useNativePipe
	r.policyMu.Unlock()
	if nativePipe {
		return r.rpcClient.ApplyPolicy(ctx, envelope)
	}
	return map[string]any{"success": true, "command": "prompt"}, nil
}

func (r *nativeInlineAllowanceRPC) WaitForPolicyApplied(ctx context.Context, generation string, epoch int64) (map[string]any, error) {
	r.policyMu.Lock()
	nativePipe := r.useNativePipe
	if !nativePipe {
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
	r.policyMu.Unlock()
	return r.rpcClient.WaitForPolicyApplied(ctx, generation, epoch)
}

func (r *nativeInlineAllowanceRPC) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (RootControlPersistedReceipt, error) {
	r.policyMu.Lock()
	nativePipe := r.useNativePipe
	r.policyMu.Unlock()
	if nativePipe {
		return r.rpcClient.ReconcileRootControlResult(ctx, payload)
	}
	requestID, _ := payload["request_id"].(string)
	resultID, _ := payload["result_id"].(string)
	generation, _ := payload["runtime_generation"].(string)
	manifest, _ := payload["manifest_sha256"].(string)
	epoch, _ := readinessInteger(payload["target_session_epoch"])
	canonical, _ := canonicalJSON(payload)
	hash := fmt.Sprintf("%x", sha256.Sum256(canonical))
	return RootControlPersistedReceipt{
		ReceiptID:         "native-inline-result",
		RequestID:         requestID,
		ResultID:          resultID,
		PayloadSHA256:     hash,
		RuntimeGeneration: generation,
		SessionEpoch:      epoch,
		ManifestSHA256:    manifest,
	}, nil
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

	require.NoError(t, writeRPC(stdout, map[string]any{"id": prompt["id"], "type": "response", "command": "prompt", "success": true, "data": map[string]any{}}))
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

func TestNativeBuiltinLifecycleCannotStallManagedAllowanceControlPipe(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, rpc, requests, stdout := readyNativeInlineAllowanceFixture(t)
	require.NoError(t, manager.SetManagedInlineTools(ctx, fence.AgentID, true))
	rpc.policyMu.Lock()
	rpc.useNativePipe = true
	rpc.policyMu.Unlock()
	manager.installBuiltinToolLifecycleReceiver(binding)

	// Keep the direct mailbox turn open. d9 requires its receipt matcher to leave
	// the following policy receipt for the fenced policy waiter.
	attempt := directMailboxAttempt("message-a")
	attempt.Fence = fence
	events, err := binding.rpc.(DirectMailboxRPC).PromptManagedMailbox(ctx, attempt)
	require.NoError(t, err)
	mailboxPrompt := requireRequest(t, requests)
	require.Equal(t, "prompt", mailboxPrompt["type"])
	require.NoError(t, writeRPC(stdout, map[string]any{"id": mailboxPrompt["id"], "type": "response", "command": mailboxPrompt["type"], "success": true, "data": map[string]any{}}))
	require.NoError(t, writeRPC(stdout, map[string]any{"type": "message_end", "message": map[string]any{"id": "mailbox-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": "message-a", "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch}}}))
	select {
	case event := <-events:
		require.NotNil(t, event.Receipt)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct mailbox receipt")
	}

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockOnce sync.Once
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		if entry.Presentation.Kind != "tool" || entry.Presentation.Outcome != "active" {
			return
		}
		blockOnce.Do(func() { close(blocked) })
		<-release
	})

	var allowanceTool ManifestTool
	for _, candidate := range manager.manifest.Tools {
		if candidate.OperationID == "policy.inline_allowance" {
			allowanceTool = candidate
			break
		}
	}
	require.NotEmpty(t, allowanceTool.Name)
	requestID := "123e4567-e89b-42d3-a456-426614174019"
	require.NoError(t, writeRPC(stdout, map[string]any{"type": "custom_message", "customType": "mecha.control.request.v1", "display": false, "content": "Router control request queued.", "details": map[string]any{
		"protocol": "mecha.control/v1", "request_id": requestID, "tool_call_id": "native-pipe-call", "capability_id": allowanceTool.CapabilityID, "tool_name": allowanceTool.Name,
		"definition_id": allowanceTool.DefinitionID, "definition_sha256": allowanceTool.DefinitionSHA256, "operation_id": allowanceTool.OperationID, "manifest_sha256": fence.ManifestHash,
		"runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "context_ref": "root", "arguments": map[string]any{"toolName": "bash", "usageCount": int64(1)},
	}}))
	policyPrompt := requireRequest(t, requests)
	// The root callback owns this earlier policy receipt. Deliver it before waiting
	// for the later builtin lifecycle callback, because the shared semantic FIFO
	// cannot run that callback until the root callback has finished.
	require.Equal(t, "prompt", policyPrompt["type"])
	require.NoError(t, writeRPC(stdout, map[string]any{"id": policyPrompt["id"], "type": "response", "command": policyPrompt["type"], "success": true, "data": map[string]any{}}))
	policyReceipt := map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"customType": "mecha.policy.applied.v1",
			"details": map[string]any{
				"protocol": "mecha.policy/v1", "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch,
				"applied": map[string]any{
					"inline_enabled": true, "inline_credits": map[string]any{"bash": int64(1)}, "recall_content": "",
					"settled_enabled": false, "goal_lock_enabled": false, "compaction_deferred": false,
				},
			},
		},
	}
	require.NoError(t, writeRPC(stdout, policyReceipt))
	appendPrompt := requireRequest(t, requests)
	require.Equal(t, "prompt", appendPrompt["type"])
	require.NoError(t, writeRPC(stdout, map[string]any{"id": appendPrompt["id"], "type": "response", "command": appendPrompt["type"], "success": true, "data": map[string]any{}}))
	command, commandOK := appendPrompt["message"].(string)
	encoded := strings.TrimPrefix(command, "/mecha-control-result-v1 ")
	payloadBytes, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
	var payload map[string]any
	require.True(t, commandOK && encoded != command && decodeErr == nil && json.Unmarshal(payloadBytes, &payload) == nil, "managed root append payload was invalid")
	content, contentOK := payload["content"].(string)
	require.True(t, contentOK, "managed root append payload had no content")
	resultDetails := make(map[string]any, len(payload)-1)
	for key, value := range payload {
		if key != "content" {
			resultDetails[key] = value
		}
	}
	rootReceipt := map[string]any{"type": "custom_message", "id": "native-pipe-result", "customType": "mecha.control.result.v1", "display": false, "content": content, "details": resultDetails}
	// Finish the earlier root callback before introducing the blocked builtin work.
	require.NoError(t, writeRPC(stdout, rootReceipt))

	toolStartWritten := make(chan error, 1)
	go func() {
		toolStartWritten <- writeRPC(stdout, map[string]any{"type": "tool_execution_start", "toolCallId": "bash-call", "toolName": "bash", "args": map[string]any{}})
	}()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("builtin lifecycle handler did not enter its blocked persistence path")
	}

	framesDelivered := make(chan error, 1)
	go func() {
		if err := writeRPC(stdout, map[string]any{"type": "tool_execution_start", "toolCallId": "read-call", "toolName": "read", "args": map[string]any{}}); err != nil {
			framesDelivered <- err
			return
		}
		if err := writeRPC(stdout, map[string]any{"type": "tool_execution_end", "toolCallId": "read-call", "toolName": "read", "result": map[string]any{}, "isError": false}); err != nil {
			framesDelivered <- err
			return
		}
		framesDelivered <- writeRPC(stdout, map[string]any{"type": "tool_execution_end", "toolCallId": "bash-call", "toolName": "bash", "result": map[string]any{}, "isError": false})
	}()

	select {
	case err := <-framesDelivered:
		require.NoError(t, err)
	case <-time.After(time.Second):
		close(release)
		t.Fatal("blocked builtin lifecycle work stalled the sole RPC stdout reader")
	}
	close(release)
	require.NoError(t, <-toolStartWritten)
	require.Eventually(t, func() bool {
		row, lookupErr := manager.store.ManagedControlRequest(ctx, requestID)
		return lookupErr == nil && row.State == "delivered" && row.ReceiptEntryIdentity == "native-pipe-result"
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		page, historyErr := manager.store.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		if historyErr != nil {
			return false
		}
		completed := map[string]bool{}
		for _, entry := range page.Entries {
			if entry.Presentation != nil && entry.Presentation.Outcome == "success" {
				completed[entry.Presentation.ToolName] = true
			}
		}
		return completed["read"] && completed["bash"]
	}, time.Second, time.Millisecond)
}

func TestManagedInlineAllowanceProjectsRequestedCreditBeforeSuccess(t *testing.T) {
	ctx := context.Background()
	manager, binding, fence, rpc, _ := readyInlineAllowanceFixture(t)
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

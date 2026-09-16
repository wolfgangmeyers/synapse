package managed

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type composedRPCProcess struct {
	identity ProcessIdentity
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	done     chan struct{}
	once     sync.Once
}

func (p *composedRPCProcess) Identity() ProcessIdentity { return p.identity }
func (p *composedRPCProcess) Stdin() io.WriteCloser     { return p.stdin }
func (p *composedRPCProcess) Stdout() io.ReadCloser     { return p.stdout }
func (p *composedRPCProcess) Wait() error               { <-p.done; return nil }
func (p *composedRPCProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type composedRPCFactory struct {
	process *composedRPCProcess
	rpc     RPC
}

func (f composedRPCFactory) Start(context.Context, CommandSpec) (Process, error) {
	return f.process, nil
}
func (f composedRPCFactory) Open(context.Context, Process) (RPC, error) { return f.rpc, nil }

func TestManagedDirectRPCComposedBehavioralGate(t *testing.T) {
	const sessionID = "123e4567-e89b-42d3-a456-426614174301"
	const runtimeGeneration = "123e4567-e89b-42d3-a456-426614174302"
	ctx := context.Background()
	config, paths := managedLaunchConfigFixture(t)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	process := &composedRPCProcess{identity: ProcessIdentity{PID: 1701, StartMarker: "start", Incarnation: "incarnation"}, stdin: stdinWriter, stdout: stdoutReader, done: make(chan struct{})}
	promptSeen := make(chan struct{}, 1)
	promptResponseRelease := make(chan struct{})
	getEntriesSeen := make(chan struct{}, 1)
	policyReceiptDetails := policyDetails(runtimeGeneration, 1)
	policyReceiptDetails["applied"].(map[string]any)["inline_enabled"] = false
	_, err = normalizeAppliedPolicyReceipt(policyReceiptDetails["applied"])
	require.NoError(t, err)
	policyReceiptEvent := map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.policy.applied.v1", "details": policyReceiptDetails}}
	encodedPolicyReceiptEvent, err := json.Marshal(policyReceiptEvent)
	require.NoError(t, err)
	parsedPolicyReceipt, err := strictJSON(encodedPolicyReceiptEvent)
	require.NoError(t, err)
	parsedPolicyReceiptEvent, ok := parsedPolicyReceipt.(map[string]any)
	require.True(t, ok)
	parsedPolicyReceiptDetails := receiptDetails(parsedPolicyReceiptEvent)
	require.NotNil(t, parsedPolicyReceiptDetails)
	_, err = normalizeAppliedPolicyReceipt(parsedPolicyReceiptDetails["applied"])
	require.NoError(t, err)
	go func() {
		for request := range readJSONLRequests(stdinReader) {
			id, _ := request["id"].(string)
			typ, _ := request["type"].(string)
			response := map[string]any{"id": id, "type": "response", "command": typ, "success": true}
			switch typ {
			case "get_state":
				response["data"] = map[string]any{"sessionId": sessionID}
			case "get_commands":
				response["data"] = map[string]any{"commands": append(append([]string{}, hiddenCommands...), legacyCommands...)}
			case "get_active_tools":
				response["data"] = map[string]any{"tools": expectedManagedActiveTools(manifest)}
			case "get_entries":
				response["data"] = map[string]any{"entries": []any{}, "leafId": "cursor-before-message"}
				select {
				case getEntriesSeen <- struct{}{}:
				default:
				}
			case "prompt":
				// Private control prompts acknowledge with the exact four-field RPC
				// response envelope. Their semantic payload follows as message_end.
			}
			message, _ := request["message"].(string)
			if strings.HasPrefix(message, "/mecha-mailbox-deliver-v1 ") {
				select {
				case promptSeen <- struct{}{}:
				default:
				}
				<-promptResponseRelease
			}
			if writeRPC(stdoutWriter, response) != nil {
				return
			}
			if typ == "prompt" {
				switch {
				case strings.HasPrefix(message, "/mecha-bootstrap-v1 "):
					_ = writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.tools.accepted.v1", "details": map[string]any{"protocol": "mecha.tools/v1", "runtime_generation": runtimeGeneration, "session_epoch": 1, "manifest_sha256": manifest.CanonicalSHA256, "manifest_revision": manifest.Revision, "active_tools": expectedManagedActiveTools(manifest)}}})
				case strings.HasPrefix(message, "/mecha-policy-apply-v1 "):
					_ = writeRPC(stdoutWriter, parsedPolicyReceiptEvent)
				case strings.HasPrefix(message, "/mecha-child-snapshot-v1 "):
					_ = writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.child.snapshot.v1", "details": func() map[string]any {
						details := snapshotDetails()
						details["owner_session_id"] = sessionID
						return details
					}()}})
				}
			}
		}
	}()
	manager, err := NewManager(ManagerOptions{Store: durable, Config: config, Manifest: manifest, ProcessFactory: composedRPCFactory{process: process}, RPCFactory: composedRPCFactory{rpc: client}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), IDSource: deterministic.NewSequenceIDSource(sessionID, runtimeGeneration), ProcessGroupLeaderVerifier: func(ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close(ctx); _ = client.Close() })
	_, err = manager.Launch(ctx, LaunchRequest{Name: "composed-agent", Cwd: paths["work"], RequestID: "composed-launch"})
	require.NoError(t, err)
	if !assert.Eventually(t, func() bool {
		agent, e := durable.ManagedAgent(ctx, "composed-agent")
		return e == nil && agent.AcceptingPrompts
	}, time.Second, time.Millisecond) {
		agent, e := durable.ManagedAgent(ctx, "composed-agent")
		require.NoError(t, e)
		t.Fatalf("readiness failed: lifecycle=%q activity=%q accepting=%v error_code=%q error_message=%q", agent.LifecycleState, agent.ActivityState, agent.AcceptingPrompts, agent.LastErrorCode, agent.LastErrorMessage)
	}

	ordinary := make([]map[string]any, 0, len(realPiOrdinaryEventFixtures()))
	for i, fixture := range realPiOrdinaryEventFixtures() {
		encoded, err := json.Marshal(fixture)
		require.NoError(t, err)
		decoded, err := strictJSON(encoded)
		require.NoError(t, err)
		decodedObject, ok := decoded.(map[string]any)
		require.Truef(t, ok, "ordinary fixture %d (%v) must decode to an object", i, fixture["type"])
		typ, _ := decodedObject["type"].(string)
		require.Truef(t, ordinaryAgentSessionEvent(decodedObject), "ordinary fixture %d type=%q must pass ordinaryAgentSessionEvent", i, typ)
		ordinary = append(ordinary, decodedObject)
	}
	require.NotEmpty(t, ordinary)
	client.mu.Lock()
	ordinaryBaseline := client.eventSequence
	client.mu.Unlock()
	for i := 0; i < 129; i++ {
		require.NoError(t, writeRPC(stdoutWriter, ordinary[i%len(ordinary)]))
	}
	require.Eventually(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.eventSequence >= ordinaryBaseline+129
	}, time.Second, time.Millisecond)
	require.NoError(t, client.terminalError())
	preDirectHistory, err := durable.ManagedHistoryPage(ctx, "composed-agent", "complete", 0, 100)
	require.NoError(t, err)
	preDirectIDs := make(map[string]struct{}, len(preDirectHistory.Entries))
	for _, entry := range preDirectHistory.Entries {
		preDirectIDs[entry.ID] = struct{}{}
	}

	manager.SetDirectReplyPublisher(func(context.Context, store.ManagedDirectAttempt, DirectMailboxReply) error { return nil })
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "composed-message", From: "sender", To: "composed-agent", Body: "hello", CreatedAt: "2026-09-15T08:00:00.000Z"})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(ctx, "composed-message")
	require.NoError(t, err)
	manager.OfferDirect("composed-agent")
	select {
	case <-getEntriesSeen:
	case <-time.After(time.Second):
		t.Fatal("real get_entries was not called")
	}
	select {
	case <-promptSeen:
	case <-time.After(time.Second):
		t.Fatal("real mailbox prompt was not called")
	}
	attempt, err := durable.ManagedDirectAttempt(ctx, "composed-message", 1)
	require.NoError(t, err)
	require.Equal(t, "direct_prompted", attempt.State)
	close(promptResponseRelease)
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_start"}))
	const directToolCallID = "composed-direct-turn-tool"
	cloneToolFrame := func(kind string) map[string]any {
		for _, fixture := range realPiOrdinaryEventFixtures() {
			if fixture["type"] != kind {
				continue
			}
			clone := make(map[string]any, len(fixture))
			for key, value := range fixture {
				clone[key] = value
			}
			clone["toolCallId"] = directToolCallID
			clone["toolName"] = "bash"
			return clone
		}
		return nil
	}
	directToolStart := cloneToolFrame("tool_execution_start")
	directToolEnd := cloneToolFrame("tool_execution_end")
	require.NotNil(t, directToolStart)
	require.NotNil(t, directToolEnd)
	directToolName, ok := directToolStart["toolName"].(string)
	require.True(t, ok)
	require.True(t, ordinaryAgentSessionEvent(directToolStart))
	require.True(t, ordinaryAgentSessionEvent(directToolEnd))
	require.NoError(t, writeRPC(stdoutWriter, directToolStart))
	require.NoError(t, writeRPC(stdoutWriter, directToolEnd))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "append-entry", "customType": "mecha.mailbox.appended.v1", "details": map[string]any{"protocol": "mecha.mailbox/v1", "message_id": attempt.MessageID, "runtime_generation": attempt.Fence.RuntimeGeneration, "session_epoch": attempt.Fence.SessionEpoch}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": "reply-entry", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "done"}}}}))
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "agent_settled"}))
	require.Eventually(t, func() bool {
		message, e := durable.MessageFor(ctx, "composed-message")
		return e == nil && message == nil
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		agent, e := durable.ManagedAgent(ctx, "composed-agent")
		return e == nil && agent.ActivityState == "idle"
	}, time.Second, time.Millisecond)
	// Successful acknowledgement consumes the transient direct-attempt row.
	// Wait for the independently persisted direct-turn tool projection.
	require.Eventually(t, func() bool {
		page, pageErr := durable.ManagedHistoryPage(ctx, "composed-agent", "complete", 0, 100)
		if pageErr != nil {
			return false
		}
		for _, entry := range page.Entries {
			if _, existedBeforeDirect := preDirectIDs[entry.ID]; !existedBeforeDirect && entry.Message == "Ran "+directToolName && entry.Presentation != nil && entry.Presentation.Kind == "tool" && entry.Presentation.ToolName == directToolName && entry.Presentation.Outcome == "success" {
				return true
			}
		}
		return false
	}, time.Second, time.Millisecond)
	history, err := durable.ManagedHistoryPage(ctx, "composed-agent", "complete", 0, 100)
	require.NoError(t, err)
	require.Equal(t, "complete", history.View)
	var directComplete []store.ManagedHistoryEntry
	for _, entry := range history.Entries {
		if _, existedBeforeDirect := preDirectIDs[entry.ID]; existedBeforeDirect {
			continue
		}
		if entry.AgentID == "composed-agent" && entry.Message == "Ran "+directToolName && entry.Presentation != nil && entry.Presentation.Kind == "tool" && entry.Presentation.ToolName == directToolName && entry.Presentation.Outcome == "success" {
			directComplete = append(directComplete, entry)
		}
		if entry.Message == "Message delivery failed" && entry.Presentation != nil {
			require.NotEqual(t, "error", entry.Presentation.Kind, "the direct turn must not add a delivery-error projection")
			require.NotEqual(t, "error", entry.Presentation.Outcome, "the direct turn must not add a delivery-error projection")
		}
	}
	require.Len(t, directComplete, 1, "the unique direct-turn tool must have one successful Complete projection")
	require.Nil(t, directComplete[0].Presentation.Error)
}

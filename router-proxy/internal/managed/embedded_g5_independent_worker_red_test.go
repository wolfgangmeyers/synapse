package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// RED: the first agent is an ordinary independent Manager.Launch worker, not
// a root. Its provider calls the real SDK host tool in its own sender turn.
func TestEmbeddedG5IndependentWorkerSDKLaunchRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	firstProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer firstProvider.Unregister()
	secondProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer secondProvider.Unregister()
	const callID = "worker-sdk-call-1"
	args := map[string]any{"action": "launch", "name": "g5-independent-second", "cwd": root, "instruction": "second independent task"}
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	observed := make(chan observation, 2)
	firstProvider.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var tools []ai.Tool
			for _, msg := range h.Messages {
				if system, ok := msg.(ai.SystemMessage); ok {
					tools = append(tools, system.ToolsAdded...)
				}
			}
			observed <- observation{tools: tools}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", args, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					copy := r
					result = &copy
				}
			}
			observed <- observation{result: result}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first reply"}}, ai.StopStop)
		},
	})
	secondProvider.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "second done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "second reply"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
		if saved.AgentID == "g5-independent-first" {
			return firstProvider.GetModel()
		}
		return secondProvider.GetModel()
	}, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	first, err := m.Launch(ctx, LaunchRequest{Name: "g5-independent-first", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "launch another independent worker", RequestID: "a5555555-5555-4555-8555-555555555560"})
	require.NoError(t, err)
	firstBinding := store.EmbeddedBinding{AgentID: first.Agent.AgentID, SessionID: first.Agent.PiSessionID, Epoch: first.Agent.SessionEpoch, Generation: first.Agent.RuntimeGeneration}
	role, err := db.EmbeddedSessionRole(ctx, firstBinding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	var initial observation
	select {
	case initial = <-observed:
	case <-time.After(4 * time.Second):
		t.Fatal("first worker provider did not run")
	}
	var registered bool
	for _, tool := range initial.tools {
		if tool.Name == "mecha_worker" {
			registered = true
		}
	}
	require.True(t, registered, "first worker SDK registration is absent (first behavioral RED; assertions below not executed)")
	var continuation observation
	select {
	case continuation = <-observed:
	case <-time.After(4 * time.Second):
		t.Fatal("sender lane blocked before same-turn SDK continuation")
	}
	require.NotNil(t, continuation.result, "correlated toolResult must arrive before final")
	require.Equal(t, callID, continuation.result.ToolCallID)
	require.False(t, continuation.result.IsError)
	require.NotEmpty(t, continuation.result.Content)
	var accepted struct {
		Status      string `json:"status"`
		WorkerID    string `json:"worker_id"`
		OperationID string `json:"operation_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(continuation.result.Content[0].(ai.TextContent).Text), &accepted))
	require.Equal(t, "accepted", accepted.Status)
	require.Equal(t, "g5-independent-second", accepted.WorkerID)
	require.Equal(t, embeddedWorkerRequestID(firstBinding, callID), accepted.OperationID)
	second, err := db.ManagedAgent(ctx, accepted.WorkerID)
	require.NoError(t, err)
	require.NotEqual(t, first.Agent.AgentID, second.AgentID)
	require.NotEqual(t, first.Agent.PiSessionID, second.PiSessionID)
	require.NotEqual(t, first.Agent.RuntimeGeneration, second.RuntimeGeneration)
	secondBinding := store.EmbeddedBinding{AgentID: second.AgentID, SessionID: second.PiSessionID, Epoch: second.SessionEpoch, Generation: second.RuntimeGeneration}
	role, err = db.EmbeddedSessionRole(ctx, secondBinding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.embeddedOwners[first.Agent.AgentID].binding.OwnerToken > 0 && m.embeddedOwners[second.AgentID].binding.OwnerToken > 0
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	firstOwner, secondOwner := m.embeddedOwners[first.Agent.AgentID], m.embeddedOwners[second.AgentID]
	m.mu.Unlock()
	require.NotEqual(t, firstOwner.binding.AgentID, secondOwner.binding.AgentID)
	require.NotEqual(t, firstOwner.identity, secondOwner.identity)
	replay, err := m.embeddedWorkerLaunchTool(firstOwner).Execute(ctx, callID, args, nil)
	require.NoError(t, err)
	require.Contains(t, replay.Content[0].(ai.TextContent).Text, `"replayed":true`)
	conflictArgs := map[string]any{"action": "launch", "name": "g5-independent-third", "cwd": root, "instruction": "different task"}
	_, err = m.embeddedWorkerLaunchTool(firstOwner).Execute(ctx, callID, conflictArgs, nil)
	require.Error(t, err, "same sender SDK call ID with different args must conflict")
	require.Error(t, db.RenewEmbeddedOwner(ctx, store.EmbeddedBinding{AgentID: second.AgentID, SessionID: second.PiSessionID, Epoch: second.SessionEpoch, Generation: second.RuntimeGeneration}, secondOwner.identity, embeddedOwnerTTL), "zero-token sender cannot claim second worker")
	for _, target := range []string{first.Agent.AgentID, second.AgentID} {
		id := "a5555555-5555-4555-8555-555555555561"
		if target == second.AgentID {
			id = "a5555555-5555-4555-8555-555555555562"
		}
		_, err = db.Enqueue(ctx, protocol.Envelope{ID: id, From: "external-sender", To: target, Body: "addressed mail", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
		require.NoError(t, err)
		resolved, err := db.ResolveMessageTarget(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "managed", resolved.SourceKind)
		m.OfferDirect(target)
		// This manager-only fixture has no transport reply publisher. It proves
		// addressed delivery and processing ACK, not a published reply. The real
		// transport reply path is covered by TestEmbeddedReplyHTTPCompletedSenderFetchRED.
		require.Eventually(t, func() bool {
			mail, e := db.MessageFor(ctx, id)
			return e == nil && mail.State == domain.MessageAcknowledged
		}, 4*time.Second, 10*time.Millisecond)
	}
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 2)
}

func TestEmbeddedG5IndependentWorkerCallIDBindingRED(t *testing.T) {
	a := store.EmbeddedBinding{AgentID: "first", SessionID: "session-one", Epoch: 1, Generation: "generation-one"}
	b := store.EmbeddedBinding{AgentID: "second", SessionID: "session-two", Epoch: 1, Generation: "generation-two"}
	require.Equal(t, embeddedWorkerRequestID(a, "sdk-call"), embeddedWorkerRequestID(a, "sdk-call"), "same ID/different args must collide for explicit conflict, not fork an operation")
	require.NotEqual(t, embeddedWorkerRequestID(a, "sdk-call"), embeddedWorkerRequestID(a, "other-call"))
	require.NotEqual(t, embeddedWorkerRequestID(a, "sdk-call"), embeddedWorkerRequestID(b, "sdk-call"))
	a.SessionID = "rotated"
	require.NotEqual(t, embeddedWorkerRequestID(a, "sdk-call"), embeddedWorkerRequestID(store.EmbeddedBinding{AgentID: "first", SessionID: "session-one", Epoch: 1, Generation: "generation-one"}, "sdk-call"))
}

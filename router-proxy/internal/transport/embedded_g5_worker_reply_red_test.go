package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// RED: worker A's actual SDK tool launches worker B. The router (not a test
// publisher) must publish B's completed addressed-mail reply back to A.
func TestEmbeddedG5WorkerIssuedLaunchAddressedReplyTransportRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	firstProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	secondProvider := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(firstProvider.Unregister)
	t.Cleanup(secondProvider.Unregister)
	const firstID, secondID, callID = "g5-transport-first", "g5-transport-second", "g5-transport-sdk-call"
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
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", map[string]any{"action": "launch", "name": secondID, "cwd": root, "instruction": "independent second task"}, callID)}, ai.StopToolUse)
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
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first ready"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "first received reply"}}, ai.StopStop)
		},
	})
	secondProvider.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "second ready"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "second addressed reply"}}, ai.StopStop)
		},
	})
	m, err := managed.NewManager(managed.ManagerOptions{CodexCredentialStore: testfixture.CodexAdmission(t),
		Store: db, Manifest: manifest,
		Config:        managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
			if saved.AgentID == firstID {
				return firstProvider.GetModel()
			}
			return secondProvider.GetModel()
		},
	})
	require.NoError(t, err)
	// New wires the actual persistEmbeddedReply publisher into the manager.
	router, err := New(Options{Store: db, Managed: m})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(ctx); _ = db.Close() })
	// The real httptest listener is bound before the SDK gets its current
	// process authority; New alone is not the Router.Listen lifecycle gate.
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "worker-reply-"+server.Listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, m.SetEmbeddedStartupGeneration(generation))
	first, err := m.Launch(ctx, managed.LaunchRequest{Name: firstID, Cwd: root, Runtime: "embedded", InitialInstruction: "launch independent worker", RequestID: "a5555555-5555-4555-8555-555555555570"})
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
	require.True(t, registered, "SDK registration absent: behavioral checks below not executed")
	var continuation observation
	select {
	case continuation = <-observed:
	case <-time.After(4 * time.Second):
		t.Fatal("worker SDK tool continuation missing")
	}
	require.NotNil(t, continuation.result)
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
	require.Equal(t, secondID, accepted.WorkerID)
	require.NotEmpty(t, accepted.OperationID)
	second, err := db.ManagedAgent(ctx, secondID)
	require.NoError(t, err)
	require.NotEqual(t, first.Agent.AgentID, second.AgentID)
	require.NotEqual(t, first.Agent.PiSessionID, second.PiSessionID)
	require.NotEqual(t, first.Agent.RuntimeGeneration, second.RuntimeGeneration)
	secondBinding := store.EmbeddedBinding{AgentID: second.AgentID, SessionID: second.PiSessionID, Epoch: second.SessionEpoch, Generation: second.RuntimeGeneration}
	role, err = db.EmbeddedSessionRole(ctx, secondBinding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	require.Eventually(t, func() bool { a, e := db.ManagedAgent(ctx, secondID); return e == nil && a.LifecycleState == "running" }, 4*time.Second, 10*time.Millisecond)

	const mailID = "a5555555-5555-4555-8555-555555555571"
	payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":%q,"body":"worker-to-worker mail","created_at":"2026-09-27T00:00:00.000Z"}`, mailID, firstID, secondID)
	response, err := http.Post(server.URL+"/v1/messages", "application/json", strings.NewReader(payload))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusCreated, response.StatusCode)
	sqlDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer sqlDB.Close()
	var attemptID, sender, boundAgent, boundSession, boundGeneration string
	var boundEpoch int64
	require.Eventually(t, func() bool {
		return sqlDB.QueryRowContext(ctx, `SELECT attempt_id,sender,agent_id,session_id,epoch,generation FROM embedded_attempts WHERE message_id=?`, mailID).Scan(&attemptID, &sender, &boundAgent, &boundSession, &boundEpoch, &boundGeneration) == nil
	}, 4*time.Second, 10*time.Millisecond, "mail attempt must be reserved by B")
	require.NotEmpty(t, attemptID)
	require.Equal(t, firstID, sender)
	require.Equal(t, secondID, boundAgent)
	require.Equal(t, second.PiSessionID, boundSession)
	require.Equal(t, second.SessionEpoch, boundEpoch)
	require.Equal(t, second.RuntimeGeneration, boundGeneration)
	require.Eventually(t, func() bool {
		var ackAttempt string
		return sqlDB.QueryRowContext(ctx, `SELECT attempt_id FROM embedded_acks WHERE attempt_id=?`, attemptID).Scan(&ackAttempt) == nil && ackAttempt == attemptID
	}, 4*time.Second, 10*time.Millisecond, "processing ACK must be bound to B's accepted attempt")
	require.Eventually(t, func() bool {
		var state string
		return sqlDB.QueryRowContext(ctx, `SELECT state FROM messages WHERE id=?`, mailID).Scan(&state) == nil && state == "acknowledged"
	}, 4*time.Second, 10*time.Millisecond, "B must processing-ACK the exact accepted attempt")
	var replyID, replyFrom, replyTo, replyBody, createdAt, outboxState string
	require.Eventually(t, func() bool {
		return sqlDB.QueryRowContext(ctx, `SELECT reply_id,sender,recipient,body,created_at,state FROM embedded_reply_outbox WHERE attempt_id=?`, attemptID).Scan(&replyID, &replyFrom, &replyTo, &replyBody, &createdAt, &outboxState) == nil
	}, 4*time.Second, 10*time.Millisecond, "B must freeze final reply for the accepted attempt")
	require.NotEmpty(t, replyID)
	require.NotEmpty(t, createdAt)
	require.Equal(t, secondID, replyFrom)
	require.Equal(t, firstID, replyTo)
	require.Equal(t, "second addressed reply", replyBody)
	// The outbox alone is insufficient: this must be the actual router publisher.
	require.Eventually(t, func() bool {
		response, e := http.Get(server.URL + "/v1/conversations?owner=" + firstID + "&peer=" + secondID)
		if e != nil {
			return false
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return false
		}
		var view struct {
			Messages []struct {
				ID        string `json:"id"`
				From      string `json:"from"`
				To        string `json:"to"`
				Body      string `json:"body"`
				CreatedAt string `json:"created_at"`
			} `json:"messages"`
		}
		if json.NewDecoder(response.Body).Decode(&view) != nil {
			return false
		}
		for _, r := range view.Messages {
			if r.ID == replyID && r.From == replyFrom && r.To == replyTo && r.Body == replyBody && r.CreatedAt == createdAt {
				return true
			}
		}
		return false
	}, 4*time.Second, 10*time.Millisecond, "frozen B reply must be published and retrievable by original sender A")
}

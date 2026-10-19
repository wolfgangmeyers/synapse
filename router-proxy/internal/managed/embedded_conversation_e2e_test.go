package managed_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// readWsConversationMessage reads frames from ws until a conversation_message frame is received or timeout expires.
func readWsConversationMessage(t *testing.T, ctx context.Context, conn *websocket.Conn, timeout time.Duration) map[string]any {
	t.Helper()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		_, data, err := conn.Read(timeoutCtx)
		require.NoError(t, err, "failed reading from websocket while waiting for conversation_message")

		var frame map[string]any
		if json.Unmarshal(data, &frame) == nil && frame["type"] == "conversation_message" {
			return frame
		}
	}
}

// -----------------------------------------------------------------------------
// Test 1: TestDirectUserChat_ImmediateConversationReply
// Direct user chat -> immediate mock reply -> WebSocket conversation_message frame verified.
// -----------------------------------------------------------------------------
func TestDirectUserChat_ImmediateConversationReply(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	expectedReplyText := "Hello! I am ready to help you with your task."
	var sawTurn atomic.Bool

	step1 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		sawTurn.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: expectedReplyText},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "chat-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "11111111-1111-1111-1111-111111111111",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Register WebSocket client for the user to verify WebSocket frame receipt
	clientConn := h.registerWebSocketClient(t, "user-client", "inst-user-1")
	defer clientConn.Close(websocket.StatusNormalClosure, "done")

	// Read channel for WebSocket conversation_message frames
	wsReceived := make(chan map[string]any, 10)
	readCtx, readCancel := context.WithCancel(h.ctx)
	defer readCancel()

	go func() {
		for {
			_, data, err := clientConn.Read(readCtx)
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(data, &frame) == nil && frame["type"] == "conversation_message" {
				select {
				case wsReceived <- frame:
				default:
				}
			}
		}
	}()

	// Send inbound direct chat message from user
	userMsgID := "msg-direct-chat-1"
	status := h.postMessage(t, userMsgID, "user-client", launched.Agent.AgentID, "Hello assistant!")
	require.Equal(t, http.StatusCreated, status)

	// The user's message should have triggered a conversation_message frame
	var firstFrame map[string]any
	select {
	case firstFrame = <-wsReceived:
		msgObj, ok := firstFrame["message"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, userMsgID, msgObj["id"])
		require.Equal(t, "user-client", msgObj["from"])
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for user conversation_message frame")
	}

	// Wait for agent to process and return to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	require.True(t, sawTurn.Load(), "embedded turn must have executed")

	// The assistant's reply should also arrive as a conversation_message frame
	var replyFrame map[string]any
	select {
	case replyFrame = <-wsReceived:
		msgObj, ok := replyFrame["message"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, launched.Agent.AgentID, msgObj["from"])
		require.Contains(t, msgObj["body"], expectedReplyText)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for assistant conversation_message frame")
	}

	// Query GET /v1/conversations to verify persistent conversation history
	req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, h.httpURL+"/v1/conversations?owner=user-client&peer="+launched.Agent.AgentID, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var convResp map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&convResp))
	messages, ok := convResp["messages"].([]any)
	require.True(t, ok)
	require.GreaterOrEqual(t, len(messages), 2, "conversation must record both user message and assistant reply")
}

// -----------------------------------------------------------------------------
// Test 2: TestMidTurnSteering_DirectUserMessage
// User message arrives while mock LLM is executing a tool -> session steered cleanly with direct user prompt.
// -----------------------------------------------------------------------------
func TestMidTurnSteering_DirectUserMessage(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	started := make(chan int, 1)
	var gateMu sync.Mutex
	var pid int
	closing := false

	var sawSteeringPrompt atomic.Bool

	step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": "sleep 1; echo 'running long task'"}, "call-bash-steer-1"),
		}, ai.StopToolUse)
	}

	step2 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "Stop what you are doing and report status") {
							sawSteeringPrompt.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Mid-turn steering acknowledged: reporting status now."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	restore := managed.SetEmbeddedBashAfterStartForTest(h.manager, func(p int) {
		gateMu.Lock()
		if closing {
			gateMu.Unlock()
			_ = syscall.Kill(-p, syscall.SIGKILL)
			return
		}
		pid = p
		gateMu.Unlock()
		started <- p
	})
	defer func() {
		gateMu.Lock()
		closing = true
		p := pid
		gateMu.Unlock()
		if p > 0 {
			_ = syscall.Kill(-p, syscall.SIGKILL)
		}
		restore()
	}()

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:             "steer-test-worker",
		Cwd:              h.root,
		Runtime:          "embedded",
		Model:            &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:        "22222222-2222-2222-2222-222222222222",
		AllowInlineTools: true,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send initial message to start tool execution
	status := h.postMessage(t, "msg-initial-turn", "user", launched.Agent.AgentID, "Start long background task")
	require.Equal(t, http.StatusCreated, status)

	// Wait for bash tool to actually start running
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bash tool to start")
	}

	// While tool is in flight, send direct mid-turn steering message
	steerMsgID := "msg-steer-action-777"
	status = h.postMessage(t, steerMsgID, "user", launched.Agent.AgentID, "Stop what you are doing and report status")
	require.Equal(t, http.StatusCreated, status)

	// Verify steering message is acknowledged mid-turn
	require.Eventually(t, func() bool {
		msg, err := h.db.MessageFor(h.ctx, steerMsgID)
		return err == nil && msg != nil && msg.State == domain.MessageAcknowledged
	}, 3*time.Second, 20*time.Millisecond, "steering message must be acknowledged mid-turn")

	// Wait for turn to complete
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 20*time.Millisecond)

	require.True(t, sawSteeringPrompt.Load(), "steering prompt must be delivered to model in transcript")
}

// -----------------------------------------------------------------------------
// Test 3: TestMechaMailboxSend_InterAgentActivity
// mecha_mailbox_send tool execution -> queues in mailbox -> zero duplicate chat messages.
// -----------------------------------------------------------------------------
func TestMechaMailboxSend_InterAgentActivity(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mailboxTarget := "supervisor"
	mailboxSubject := "Status Report"
	mailboxBody := "All tasks in sprint have succeeded."

	step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("mecha_mailbox_send", map[string]any{
				"recipient": mailboxTarget,
				"subject":   mailboxSubject,
				"body":      mailboxBody,
			}, "call-mailbox-send-1"),
		}, ai.StopToolUse)
	}

	step2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "I have notified the supervisor of task completion."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "interagent-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "33333333-3333-3333-3333-333333333333",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send message to trigger worker
	status := h.postMessage(t, "msg-trigger-mailbox", "user", launched.Agent.AgentID, "Notify supervisor about sprint completion")
	require.Equal(t, http.StatusCreated, status)

	// Wait for worker to finish and return to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// 1. Verify that mecha_mailbox_send queued message into messages table for the target
	rawDB := h.sqlDB(t)
	var recipientQueued string
	var queuedBody string
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT recipient, body FROM messages WHERE sender=? AND recipient=?`, launched.Agent.AgentID, mailboxTarget).Scan(&recipientQueued, &queuedBody))
	require.Equal(t, mailboxTarget, recipientQueued)
	require.Equal(t, mailboxBody, queuedBody)

	// 2. Verify tool execution was recorded in managed_complete_history
	var historyCount int
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_tool'`, launched.Agent.AgentID).Scan(&historyCount))
	require.GreaterOrEqual(t, historyCount, 1, "mecha_mailbox_send tool call must be recorded in complete history")

	// 3. Verify zero duplicate chat messages in conversation_messages with the mailbox body
	var duplicateConvCount int
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT count(*) FROM conversation_messages WHERE sender=? AND body=?`, launched.Agent.AgentID, mailboxBody).Scan(&duplicateConvCount))
	require.Equal(t, 0, duplicateConvCount, "mailbox payload must not be duplicated into conversation_messages as a chat bubble")
}

// -----------------------------------------------------------------------------
// Test 4: TestMultiUserAttribution
// Multi-user attribution (e.g. wolfgang, alice) -> both can send to the shared agent room,
// and messages record proper attribution in conversation_messages.
// -----------------------------------------------------------------------------
func TestMultiUserAttribution(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var (
		sawWolfgang atomic.Bool
		sawAlice    atomic.Bool
	)

	dispatcher := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "wolfgang") || strings.Contains(txt.Text, "from Wolfgang") || strings.Contains(txt.Text, "Message from Wolfgang") {
							sawWolfgang.Store(true)
						}
						if strings.Contains(txt.Text, "alice") || strings.Contains(txt.Text, "from Alice") || strings.Contains(txt.Text, "Message from Alice") {
							sawAlice.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Message received and acknowledged."},
		}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = dispatcher
	}
	reg.SetResponses(steps)

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "shared-room-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "44444444-4444-4444-4444-444444444444",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Register WebSocket client for wolfgang
	wolfgangConn := h.registerWebSocketClient(t, "wolfgang", "inst-wolfgang")
	defer wolfgangConn.Close(websocket.StatusNormalClosure, "done")

	// Register WebSocket client for alice
	aliceConn := h.registerWebSocketClient(t, "alice", "inst-alice")
	defer aliceConn.Close(websocket.StatusNormalClosure, "done")

	// User 1 (wolfgang) sends a message
	wolfgangMsgID := "msg-user-wolfgang-1"
	status := h.postMessage(t, wolfgangMsgID, "wolfgang", launched.Agent.AgentID, "Message from Wolfgang: please check logs")
	require.Equal(t, http.StatusCreated, status)

	// Wait for turn to finish
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// User 2 (alice) sends a message
	aliceMsgID := "msg-user-alice-1"
	status = h.postMessage(t, aliceMsgID, "alice", launched.Agent.AgentID, "Message from Alice: please check deployment status")
	require.Equal(t, http.StatusCreated, status)

	// Wait for turn to finish
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Check attribution in conversation_messages table
	rawDB := h.sqlDB(t)
	var wolfgangSender, aliceSender string
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT sender FROM conversation_messages WHERE message_id=?`, wolfgangMsgID).Scan(&wolfgangSender))
	require.Equal(t, "wolfgang", wolfgangSender)

	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT sender FROM conversation_messages WHERE message_id=?`, aliceMsgID).Scan(&aliceSender))
	require.Equal(t, "alice", aliceSender)

	// Verify conversation list for both users has their attribution preserved
	for _, user := range []string{"wolfgang", "alice"} {
		req, err := http.NewRequestWithContext(h.ctx, http.MethodGet, fmt.Sprintf("%s/v1/conversations?owner=%s&peer=%s", h.httpURL, user, launched.Agent.AgentID), nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var convResp map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&convResp))
		messages, ok := convResp["messages"].([]any)
		require.True(t, ok)
		require.NotEmpty(t, messages)

		// Confirm that the user's message in the thread correctly attributes sender
		foundUserMsg := false
		for _, m := range messages {
			mMap, ok := m.(map[string]any)
			if ok && mMap["from"] == user {
				foundUserMsg = true
				break
			}
		}
		require.True(t, foundUserMsg, "conversation thread for "+user+" must contain message with from="+user)
	}
}

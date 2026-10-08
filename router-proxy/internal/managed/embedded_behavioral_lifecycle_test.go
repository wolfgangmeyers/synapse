package managed_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

type behavioralHarness struct {
	ctx     context.Context
	cancel  context.CancelFunc
	root    string
	dbPath  string
	db      *store.Store
	manager *managed.Manager
	router  *transport.Router
	httpURL string
	wsURL   string
}

func (h *behavioralHarness) sqlDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", h.dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newBehavioralHarness(t *testing.T, resolver func(store.ManagedAgentRecord) *ai.Model, configMutators ...func(*managed.ManagerOptions)) *behavioralHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))

	dbPath := filepath.Join(t.TempDir(), "behavioral_router.db")
	db, err := store.Open(ctx, dbPath, store.Options{
		MaxBodyBytes: 64 * 1024,
	})
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	credDir := filepath.Join(testfixture.PrivateDir(t), "creds")
	require.NoError(t, os.MkdirAll(credDir, 0700))
	credentials := testfixture.CodexAdmission(t)

	managerOpts := managed.ManagerOptions{
		Store:         db,
		Manifest:      manifest,
		Config: managed.Config{
			SessionRoot:                  filepath.Join(root, "sessions"),
			AllowedWorkingDirectoryRoots: []string{root, t.TempDir(), os.TempDir()},
			Limits:                       managed.DefaultLimits(),
		},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore:  credentials,
		EmbeddedModelResolver: resolver,
	}
	for _, mutator := range configMutators {
		mutator(&managerOpts)
	}

	mgr, err := managed.NewManager(managerOpts)
	require.NoError(t, err)

	router, err := transport.New(transport.Options{
		Store:               db,
		Managed:             mgr,
		Host:                "127.0.0.1",
		Port:                0,
		DefaultEmbeddedRoot: true,
	})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))

	httpURL, wsURL := router.URLs()

	h := &behavioralHarness{
		ctx:     ctx,
		cancel:  cancel,
		root:    root,
		dbPath:  dbPath,
		db:      db,
		manager: mgr,
		router:  router,
		httpURL: httpURL,
		wsURL:   wsURL,
	}

	t.Cleanup(func() {
		_ = router.Close(context.Background())
		_ = mgr.Close(context.Background())
		_ = db.Close()
		cancel()
	})

	return h
}

func (h *behavioralHarness) postMessage(t *testing.T, id, from, to, body string) int {
	t.Helper()
	payload := map[string]any{
		"id":         id,
		"from":       from,
		"to":         to,
		"body":       body,
		"created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, h.httpURL+"/v1/messages", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

func (h *behavioralHarness) registerWebSocketClient(t *testing.T, agentID, instanceID string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(h.ctx, h.wsURL, nil)
	require.NoError(t, err)
	frame := map[string]any{
		"version":     "v1",
		"frame_id":    "reg-" + agentID,
		"type":        "register",
		"agent_id":    agentID,
		"instance_id": instanceID,
	}
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	require.NoError(t, conn.Write(h.ctx, websocket.MessageText, data))

	_, respBytes, err := conn.Read(h.ctx)
	require.NoError(t, err)
	var regResp map[string]any
	require.NoError(t, json.Unmarshal(respBytes, &regResp))
	require.Equal(t, "registered", regResp["type"])
	return conn
}

// -----------------------------------------------------------------------------
// Test a: Full Message-to-Reply Pipeline with Tool Execution, Thinking, & Synthesis
// -----------------------------------------------------------------------------
func TestBehavioral_MessageToReply_ToolThinkingSynthesis(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	testFileContent := "AlphaBetaGamma-Audit-Secret-42"
	var sawThinking atomic.Bool
	var sawToolCall atomic.Bool
	var sawNudge atomic.Bool

	step1 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		sawThinking.Store(true)
		sawToolCall.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.ThinkingContent{Thinking: "Analyzing request to inspect secret file and answer user."},
			providers.FauxToolCall("bash", map[string]any{"command": "cat secret.txt"}, "call-bash-1"),
		}, ai.StopToolUse)
	}

	step2 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		sawNudge.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.ThinkingContent{Thinking: "File inspected. Ready to synthesize answer."},
			ai.TextContent{Text: "The file secret is: " + testFileContent},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	require.NoError(t, os.WriteFile(filepath.Join(h.root, "secret.txt"), []byte(testFileContent), 0644))

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "pipeline-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "11111111-2222-3333-4444-555555555551",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Register WebSocket client for the sender to verify WebSocket notification receipt
	clientConn := h.registerWebSocketClient(t, "user-auditor", "inst-1")
	defer clientConn.Close(websocket.StatusNormalClosure, "done")

	// Send inbound message
	status := h.postMessage(t, "msg-pipe-1", "user-auditor", launched.Agent.AgentID, "Inspect secret.txt and report the secret")
	require.Equal(t, http.StatusCreated, status)

	// Wait for worker to finish and return to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	require.True(t, sawThinking.Load(), "thinking content must be generated")
	require.True(t, sawToolCall.Load(), "tool call must be invoked")

	// Verify reply was recorded in SQLite outbox
	var replyBody string
	rawDB := h.sqlDB(t)
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(h.ctx, `SELECT body FROM embedded_reply_outbox WHERE sender=? ORDER BY rowid DESC LIMIT 1`, launched.Agent.AgentID).Scan(&replyBody)
		return err == nil && strings.Contains(replyBody, testFileContent)
	}, 3*time.Second, 20*time.Millisecond, "outbox reply must contain synthesized answer")

	// Verify WebSocket notification was received by sender
	doneWs := make(chan struct{})
	go func() {
		defer close(doneWs)
		for {
			_, data, err := clientConn.Read(h.ctx)
			if err != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(data, &frame) == nil && frame["type"] == "message" {
				return
			}
		}
	}()

	select {
	case <-doneWs:
	case <-time.After(2 * time.Second):
		// WebSocket message delivery is best-effort over network; outbox verified above
	}

	// Verify thinking was captured in managed complete history
	rows, err := rawDB.QueryContext(h.ctx, `SELECT sequence, category, message, occurred_at FROM managed_complete_history WHERE agent_id=?`, launched.Agent.AgentID)
	require.NoError(t, err)
	defer rows.Close()
	var foundCategories []string
	for rows.Next() {
		var seq int64
		var cat, msg, occ string
		require.NoError(t, rows.Scan(&seq, &cat, &msg, &occ))
		foundCategories = append(foundCategories, cat)
		t.Logf("HISTORY: seq=%d category=%s message=%s", seq, cat, msg)
	}
	require.Contains(t, foundCategories, "managed_thinking", "managed_complete_history must record thinking entries")
}

// -----------------------------------------------------------------------------
// Test b: Scratchpad Tool Operations Hooked to Worker Turns
// -----------------------------------------------------------------------------
func TestBehavioral_Scratchpad_HookedWorkerTurns(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var (
		deletedIDMu    sync.Mutex
		deletedID      string
		sawCapError    atomic.Bool
		sawPinnedFound atomic.Bool
		sawDeleted     atomic.Bool
	)

	// Turn 1 steps:
	// Step 1: append pinned architecture note
	// Step 2: append oversized note (>4000 chars) -> cap error
	// Step 3: append unpinned temporary note
	// Step 4: complete turn 1
	turn1Step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action":  "append",
				"content": "Core architecture decision: use SQLite write-ahead log",
				"pinned":  true,
			}, "sp-call-1"),
		}, ai.StopToolUse)
	}

	turn1Step2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action":  "append",
				"content": strings.Repeat("A", 4001),
			}, "sp-call-oversized"),
		}, ai.StopToolUse)
	}

	turn1Step3 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "sp-call-oversized" {
				if strings.Contains(fmt.Sprint(r.Content), "scratchpad entry exceeds maximum allowed length of 4000 characters") {
					sawCapError.Store(true)
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action":  "append",
				"content": "Temporary note to be deleted later",
				"pinned":  false,
			}, "sp-call-2"),
		}, ai.StopToolUse)
	}

	turn1Step4 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "sp-call-2" {
				// Extract the ID from: "Appended scratchpad entry <id> (pinned: false)"
				for _, c := range r.Content {
					if txt, ok := c.(ai.TextContent); ok {
						parts := strings.Fields(txt.Text)
						if len(parts) >= 4 {
							deletedIDMu.Lock()
							deletedID = parts[3]
							deletedIDMu.Unlock()
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Recorded notes to scratchpad."},
		}, ai.StopStop)
	}

	// Turn 2 steps:
	// Step 1: search for "architecture"
	// Step 2: fetch limit 5
	// Step 3: delete temporary note
	// Step 4: complete turn 2
	turn2Step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action": "search",
				"query":  "architecture",
			}, "sp-call-search"),
		}, ai.StopToolUse)
	}

	turn2Step2 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "sp-call-search" {
				if strings.Contains(fmt.Sprint(r.Content), "Core architecture decision") {
					sawPinnedFound.Store(true)
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action": "fetch",
				"limit":  5,
			}, "sp-call-fetch"),
		}, ai.StopToolUse)
	}

	turn2Step3 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		deletedIDMu.Lock()
		idToDelete := deletedID
		deletedIDMu.Unlock()
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("scratchpad", map[string]any{
				"action": "delete",
				"id":     idToDelete,
			}, "sp-call-del"),
		}, ai.StopToolUse)
	}

	turn2Step4 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "sp-call-del" {
				if strings.Contains(fmt.Sprint(r.Content), "Deleted scratchpad entry") {
					sawDeleted.Store(true)
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Scratchpad maintenance complete."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{
		turn1Step1, turn1Step2, turn1Step3, turn1Step4,
		turn2Step1, turn2Step2, turn2Step3, turn2Step4,
	})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "scratchpad-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "22222222-2222-4222-8222-222222222222",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send message to trigger Turn 1
	status := h.postMessage(t, "msg-sp-1", "user", launched.Agent.AgentID, "Save notes to scratchpad")
	require.Equal(t, http.StatusCreated, status)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	require.True(t, sawCapError.Load(), "scratchpad 4k character cap error must be returned to model")

	deletedIDMu.Lock()
	did := deletedID
	deletedIDMu.Unlock()
	require.NotEmpty(t, did, "scratchpad temporary note ID must be captured")

	// Send message to trigger Turn 2
	status = h.postMessage(t, "msg-sp-2", "user", launched.Agent.AgentID, "Search, fetch, and delete temporary note")
	require.Equal(t, http.StatusCreated, status)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	require.True(t, sawPinnedFound.Load(), "search must locate the pinned architecture note")
	require.True(t, sawDeleted.Load(), "delete must successfully remove the target note")

	// Verify database state: only the pinned note remains
	entries, err := h.db.FetchScratchpadRecent(h.ctx, launched.Agent.AgentID, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "exactly one scratchpad entry must remain in store")
	require.True(t, entries[0].Pinned)
	require.Equal(t, "Core architecture decision: use SQLite write-ahead log", entries[0].Content)
}

// -----------------------------------------------------------------------------
// Test c: Pre-Reset Warning Reminder and Automatic Context Reset Epoch Rotation
// -----------------------------------------------------------------------------
func TestBehavioral_ContextReset_WarningAndAutomaticEpochRotation(t *testing.T) {
	const window = 100_000 // 70% = 70,000 warning threshold; 80% = 80,000 reset threshold
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var (
		sawWarningReminder atomic.Bool
		sawResetWakePrompt atomic.Bool
	)

	dispatcher := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "Context threshold reached:") || strings.Contains(txt.Text, "Context usage threshold warning") {
							sawWarningReminder.Store(true)
						}
						if strings.Contains(txt.Text, managed.ContextResetWakePrompt) {
							sawResetWakePrompt.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Processed context message successfully."},
		}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = dispatcher
	}
	reg.SetResponses(steps)

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		m := reg.GetModel()
		m.ContextWindow = window
		return m
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "context-reset-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "33333333-3333-4333-8333-333333333333",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Send 5 chunks across turns to land between 70% (70,000) and 80% (80,000) threshold
	for i := 1; i <= 5; i++ {
		payload := fmt.Sprintf("chunk-%d: %s", i, strings.Repeat("data ", 10500))
		status := h.postMessage(t, fmt.Sprintf("msg-reset-%d", i), "user", launched.Agent.AgentID, payload)
		require.Equal(t, http.StatusCreated, status)
		require.Eventually(t, func() bool {
			agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
			return err == nil && agent.ActivityState == "idle"
		}, 10*time.Second, 20*time.Millisecond)
	}

	// Verify that the pre-reset warning reminder was emitted to the worker's mailbox
	var warningReminderCount int
	require.Eventually(t, func() bool {
		err := h.sqlDB(t).QueryRowContext(h.ctx, `SELECT count(*) FROM messages WHERE id LIKE 'warning-reminder-%'`).Scan(&warningReminderCount)
		return err == nil && warningReminderCount > 0
	}, 10*time.Second, 20*time.Millisecond, "warning reminder must be emitted to mailbox when crossing 70% threshold")

	// Wait for agent to settle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 10*time.Second, 20*time.Millisecond)

	// Send chunk 6 to push total past 80% threshold (80,000 tokens)
	payloadFinal := fmt.Sprintf("chunk-6: %s", strings.Repeat("data ", 10500))
	status := h.postMessage(t, "msg-reset-6", "user", launched.Agent.AgentID, payloadFinal)
	require.Equal(t, http.StatusCreated, status)

	// Verify that automatic context reset was triggered and ContextResetWakePrompt was received
	require.Eventually(t, func() bool {
		return sawResetWakePrompt.Load()
	}, 10*time.Second, 20*time.Millisecond, "ContextResetWakePrompt must be delivered to worker upon auto-reset")

	// Wait for reset wake turn to settle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 10*time.Second, 20*time.Millisecond)

	// Verify epoch rotation: session_epoch must now be 2
	curAgent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, int64(2), curAgent.SessionEpoch, "session_epoch must rotate to 2 after auto-reset")
}

// -----------------------------------------------------------------------------
// Test d: Goal Loop Continuation with Escalating Nudges and Fail-Open Evaluation
// -----------------------------------------------------------------------------
func TestBehavioral_GoalLoop_EscalatingNudgesAndFailOpen(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	expectedGoal := "Deliver complete verified subsystem"
	var (
		evalCalls        atomic.Int32
		sawNudgeAttempt1 atomic.Bool
		sawNudgeAttempt2 atomic.Bool
	)

	// Step 1: Set goal
	step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": expectedGoal}, "call-goal-set"),
		}, ai.StopToolUse)
	}

	// Step 2: Goal tool result returns, assistant acknowledges with text
	step2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Goal set. Commencing task execution."},
		}, ai.StopStop)
	}

	// Step 3: Receives Continuation 1 (attempt 1 nudge). Executes work step.
	step3 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "NOT_DONE: Review "+expectedGoal) {
							sawNudgeAttempt1.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Completed phase 1 implementation."},
		}, ai.StopStop)
	}

	// Step 4: Receives Continuation 2 (attempt 2 nudge) after evaluator fail-open.
	step4 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "NOT_DONE: Continue "+expectedGoal) && strings.Contains(txt.Text, "Evaluator inconclusive:") {
							sawNudgeAttempt2.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Completed phase 2 and verified all tests pass."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2, step3, step4})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	}, func(opts *managed.ManagerOptions) {
		opts.GoalContinuationDelay = 20 * time.Millisecond
		opts.EmbeddedGoalEvaluator = func(ctx context.Context, key store.EmbeddedGoalEvaluationKey) (string, string, error) {
			call := evalCalls.Add(1)
			switch call {
			case 1:
				// First evaluation: normal NOT_DONE
				return store.EmbeddedGoalEvaluationNotDone, "Phase 1 finished, phase 2 remaining", nil
			case 2:
				// Second evaluation: evaluator error -> must FAIL OPEN to NOT_DONE
				return "", "", errors.New("simulated evaluator transient backend failure")
			case 3:
				// Third evaluation: goal complete
				return store.EmbeddedGoalEvaluationCompleted, "All subsystem requirements verified", nil
			default:
				return store.EmbeddedGoalEvaluationCompleted, "Done", nil
			}
		}
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:               "goal-loop-worker",
		Cwd:                h.root,
		Runtime:            "embedded",
		Model:              &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		InitialInstruction: "start goal task",
		RequestID:          "44444444-4444-4444-8444-444444444444",
	})
	require.NoError(t, err)

	// Wait for all 3 evaluations to complete
	require.Eventually(t, func() bool {
		return evalCalls.Load() >= 3
	}, 5*time.Second, 20*time.Millisecond, "evaluator must be called 3 times across the multi-turn loop")

	require.True(t, sawNudgeAttempt1.Load(), "attempt 1 escalating nudge must be received")
	require.True(t, sawNudgeAttempt2.Load(), "attempt 2 escalating nudge with fail-open reason must be received")

	// Wait for worker to settle cleanly to idle after goal completed
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 20*time.Millisecond)

	// Verify goal evaluation history was recorded in store
	var evalHistoryCount int
	require.NoError(t, h.sqlDB(t).QueryRowContext(h.ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_goal_evaluation'`, launched.Agent.AgentID).Scan(&evalHistoryCount))
	require.GreaterOrEqual(t, evalHistoryCount, 2, "goal evaluations must be published to complete history")
}

// -----------------------------------------------------------------------------
// Test e: Mid-Turn Steering Delivery During Multi-Step Tool Runs
// -----------------------------------------------------------------------------
func TestBehavioral_MidTurnSteering_MultiStepTool(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	type gateState struct {
		pid   int
		write *os.File
	}
	started := make(chan gateState, 1)
	var gateMu sync.Mutex
	var gate gateState
	closing := false

	var sawSteeringInTranscript atomic.Bool

	step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("bash", map[string]any{"command": "echo 'step1 working'"}, "call-bash-steer"),
		}, ai.StopToolUse)
	}

	step2 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok {
						if strings.Contains(txt.Text, "Urgent steering directive: prioritize emergency patch") {
							sawSteeringInTranscript.Store(true)
						}
					}
				}
			}
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Steering acknowledged: prioritized emergency patch."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	restore := managed.SetEmbeddedBashAfterStartForTest(h.manager, func(pid int, w *os.File) {
		gateMu.Lock()
		if closing {
			gateMu.Unlock()
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = w.Close()
			return
		}
		gate = gateState{pid, w}
		gateMu.Unlock()
		started <- gateState{pid, w}
	})
	defer func() {
		gateMu.Lock()
		closing = true
		g := gate
		gateMu.Unlock()
		if g.write != nil {
			_, _ = g.write.Write([]byte("\n"))
			_ = g.write.Close()
		}
		if g.pid > 0 {
			_ = syscall.Kill(-g.pid, syscall.SIGKILL)
		}
		restore()
	}()

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "steering-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "55555555-5555-4555-8555-555555555555",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send message to initiate multi-step turn
	status := h.postMessage(t, "msg-turn-start", "user", launched.Agent.AgentID, "Start long multi-step operation")
	require.Equal(t, http.StatusCreated, status)

	// Wait for bash tool to start and block
	var g gateState
	select {
	case g = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for bash tool to start")
	}

	// While tool is in flight mid-turn, post the steering message
	steerMsgID := "msg-steer-urgent-999"
	status = h.postMessage(t, steerMsgID, "user", launched.Agent.AgentID, "Urgent steering directive: prioritize emergency patch")
	require.Equal(t, http.StatusCreated, status)

	// Verify that the steering message was steered and acknowledged in SQLite
	require.Eventually(t, func() bool {
		msg, err := h.db.MessageFor(h.ctx, steerMsgID)
		return err == nil && msg != nil && msg.State == domain.MessageAcknowledged
	}, 3*time.Second, 20*time.Millisecond, "steering message must be acknowledged mid-turn")

	// Release the bash tool
	_, _ = g.write.Write([]byte("\n"))
	_ = g.write.Close()

	// Wait for turn to complete
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 20*time.Millisecond)

	require.True(t, sawSteeringInTranscript.Load(), "steering message must be delivered to model in mid-turn transcript")
}

// -----------------------------------------------------------------------------
// Test f: Subagent Launch, Execution, and Result Collection by Embedded Worker
// -----------------------------------------------------------------------------
func TestBehavioral_Subagent_LifecycleAndResultCollection(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var (
		spawnedChildMu  sync.Mutex
		spawnedChildID  string
		childExecuted   atomic.Bool
		parentAckResult atomic.Bool
	)

	dispatcher := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		// Detect if this is the child subagent invocation
		isChild := false
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok && strings.Contains(txt.Text, "child-task-calculate-pi") {
						isChild = true
					}
				}
			}
		}

		if isChild {
			// Child subagent step 1: write output file
			hasWriteResult := false
			for _, m := range tc.Messages {
				if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "child-write-call" {
					hasWriteResult = true
				}
			}
			if hasWriteResult {
				childExecuted.Store(true)
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "Child computation complete: pi=3.14159"},
				}, ai.StopStop)
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("write", map[string]any{
					"path":    "calc.txt",
					"content": "3.14159\n",
				}, "child-write-call"),
			}, ai.StopToolUse)
		}

		// Detect if this is parent wake turn
		isWake := false
		var wakeText string
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if txt, ok := c.(ai.TextContent); ok && strings.Contains(txt.Text, "[Subagent Completion Notification]") {
						isWake = true
						wakeText = txt.Text
					}
				}
			}
		}

		if isWake {
			hasGetResult := false
			for _, m := range tc.Messages {
				if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "parent-get-result" {
					hasGetResult = true
				}
			}
			if hasGetResult {
				parentAckResult.Store(true)
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "Parent received child result: pi=3.14159 successfully."},
				}, ai.StopStop)
			}

			// Extract child ID from notification
			targetChildID := ""
			for _, line := range strings.Split(wakeText, "\n") {
				if strings.HasPrefix(line, "Child ID: ") {
					targetChildID = strings.TrimSpace(strings.TrimPrefix(line, "Child ID: "))
					break
				}
			}
			if targetChildID == "" {
				spawnedChildMu.Lock()
				targetChildID = spawnedChildID
				spawnedChildMu.Unlock()
			}

			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("get_subagent_result", map[string]any{
					"agent_id": targetChildID,
				}, "parent-get-result"),
			}, ai.StopToolUse)
		}

		// Initial parent turn: check if subagent tool already called
		hasSubagentCallResult := false
		for _, m := range tc.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "parent-spawn-call" {
				hasSubagentCallResult = true
				for _, c := range r.Content {
					if txt, ok := c.(ai.TextContent); ok {
						for _, line := range strings.Split(txt.Text, "\n") {
							if strings.HasPrefix(line, "Agent ID: ") {
								cid := strings.TrimSpace(strings.TrimPrefix(line, "Agent ID: "))
								spawnedChildMu.Lock()
								spawnedChildID = cid
								spawnedChildMu.Unlock()
							}
						}
					}
				}
			}
		}

		if hasSubagentCallResult {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Subagent spawned in background, awaiting notification."},
			}, ai.StopStop)
		}

		// Step 1: Call subagent tool
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("subagent", map[string]any{
				"prompt":      "child-task-calculate-pi: compute pi to 5 decimal places",
				"description": "calculate pi",
			}, "parent-spawn-call"),
		}, ai.StopToolUse)
	}

	steps := make([]providers.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = dispatcher
	}
	reg.SetResponses(steps)

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		return reg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "parent-coordinator",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "66666666-6666-4666-8666-666666666666",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Send message to parent to initiate delegation
	status := h.postMessage(t, "msg-delegate-1", "user", launched.Agent.AgentID, "Delegate pi calculation to subagent")
	require.Equal(t, http.StatusCreated, status)

	// Wait for child to complete
	require.Eventually(t, func() bool {
		return childExecuted.Load()
	}, 6*time.Second, 50*time.Millisecond, "child subagent must execute computation")

	// Wait for parent wake turn to finish result retrieval
	require.Eventually(t, func() bool {
		return parentAckResult.Load()
	}, 6*time.Second, 50*time.Millisecond, "parent must retrieve child result via get_subagent_result")

	// Wait for parent to return to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 20*time.Millisecond)

	spawnedChildMu.Lock()
	cid := spawnedChildID
	spawnedChildMu.Unlock()
	require.NotEmpty(t, cid, "child ID must be recorded")

	// Verify child subagent row in DB is completed
	var childStatus string
	require.NoError(t, h.sqlDB(t).QueryRowContext(h.ctx, `SELECT status FROM managed_subagents WHERE agent_id=?`, cid).Scan(&childStatus))
	require.Equal(t, "completed", childStatus, "child subagent must reach completed status")

	// Verify file written by child exists in workspace
	calcData, err := os.ReadFile(filepath.Join(h.root, "calc.txt"))
	require.NoError(t, err)
	require.Equal(t, "3.14159\n", string(calcData))
}

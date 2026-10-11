package managed_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func testRawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000")
	require.NoError(t, err)
	_, _ = db.Exec("PRAGMA busy_timeout=5000;")
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// =============================================================================
// Stage 3 Behavioral Functional Test Suite: Subagents & Workers Without Leases
// =============================================================================

// TestBehavioral_Subagent_LongIdleParent_SettlementWake verifies Scenario 1:
// 1. Parent worker starts turn, launches subagent, and completes turn (sits idle).
// 2. Idle duration > 30s-60s lease window passes (simulated via expired session timestamp).
// 3. Subagent executes tool calls across multiple steps and settles (completed).
// 4. Subagent settlement succeeds with NO "stale embedded owner" or lease errors.
// 5. Parent worker receives subagent completion notice and wakes up to continue conversation.
func TestBehavioral_Subagent_LongIdleParent_SettlementWake(t *testing.T) {
	parentReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer parentReg.Unregister()

	childReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer childReg.Unregister()

	parentIdleGate := make(chan struct{})
	defer func() {
		select {
		case <-parentIdleGate:
		default:
			close(parentIdleGate)
		}
	}()

	var parentTurn1Ran atomic.Bool
	var parentTurn1AckRan atomic.Bool
	var parentTurn2Ran atomic.Bool
	var childStep1Ran atomic.Bool
	var childStep2Ran atomic.Bool
	var recordedChildID atomic.Pointer[string]

	const secretToken = "leasefree-subagent-verification-token-99"

	// Parent steps:
	// Turn 1: Receives initial message, spawns subagent via tool call, completes initial turn.
	// Turn 2: Triggered by subagent wake, queries get_subagent_result, synthesizes final reply.
	parentStep1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("subagent", map[string]any{
				"prompt":      "Inspect task.txt and extract token",
				"description": "extract token",
			}, "call-subagent-1"),
		}, ai.StopToolUse)
	}

	parentStep1Ack := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1AckRan.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Subagent launched in background. Waiting for completion notice."},
		}, ai.StopStop)
	}

	parentStep2Wake := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn2Ran.Store(true)
		childID := ""
		if ptr := recordedChildID.Load(); ptr != nil {
			childID = *ptr
		}
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("get_subagent_result", map[string]any{
				"agent_id": childID,
			}, "call-get-result-1"),
		}, ai.StopToolUse)
	}

	parentStep2Final := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Parent finalized: subagent found " + secretToken},
		}, ai.StopStop)
	}

	parentReg.SetResponses([]providers.FauxResponseStep{parentStep1, parentStep1Ack, parentStep2Wake, parentStep2Final})

	// Child steps:
	// Wait until parent completes turn 1 and goes idle.
	// Step 1: Tool call to read task.txt
	// Step 2: Conclude with secret token
	childStep1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		<-parentIdleGate
		childStep1Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("read", map[string]any{"path": "task.txt"}, "child-read-1"),
		}, ai.StopToolUse)
	}

	childStep2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		childStep2Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Found secret in task.txt: " + secretToken},
		}, ai.StopStop)
	}

	childReg.SetResponses([]providers.FauxResponseStep{childStep1, childStep2})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		if rec.ParentAgentID != "" || strings.HasPrefix(rec.AgentID, "subagent-") {
			return childReg.GetModel()
		}
		return parentReg.GetModel()
	})

	// Create test file inside parent workspace
	require.NoError(t, os.WriteFile(filepath.Join(h.root, "task.txt"), []byte(secretToken), 0644))

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "leasefree-parent-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "22222222-3333-4444-5555-666666666661",
	})
	require.NoError(t, err)

	// Wait for parent worker to become ready and idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Post message to parent to trigger Turn 1 (spawning subagent)
	status := h.postMessage(t, "msg-sub-1", "user-lead", launched.Agent.AgentID, "Please delegate token extraction to subagent")
	require.Equal(t, http.StatusCreated, status)

	rawDB := testRawDB(t, h.dbPath)

	// Wait for parent to finish Turn 1 and return to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle" && parentTurn1AckRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent must finish turn 1 and go idle")

	// Verify child subagent was created in SQLite
	var childID string
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(h.ctx, `SELECT agent_id FROM managed_subagents WHERE parent_agent_id=? ORDER BY created_at DESC LIMIT 1`, launched.Agent.AgentID).Scan(&childID)
		return err == nil && childID != ""
	}, 3*time.Second, 10*time.Millisecond, "subagent record must exist in managed_subagents")
	recordedChildID.Store(&childID)

	// Simulate long-idle duration > 30s-60s lease window by deliberately setting
	// parent's owner_expires_at to a past timestamp in SQLite.
	// Under the old lease semantics, this would cause subagent settlement to fail
	// with "stale embedded owner". Under lease-free semantics, this has NO negative effect!
	pastTime := time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	require.Eventually(t, func() bool {
		_, execErr := rawDB.ExecContext(h.ctx, `UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, pastTime, launched.Agent.AgentID)
		return execErr == nil
	}, 3*time.Second, 20*time.Millisecond)

	// Release subagent to execute while parent is idle and has expired lease
	close(parentIdleGate)

	// Wait for child subagent to complete both steps and settle
	require.Eventually(t, func() bool {
		var subStatus, resultText, subError string
		err := rawDB.QueryRowContext(h.ctx, `SELECT status, result_text, error FROM managed_subagents WHERE agent_id=?`, childID).Scan(&subStatus, &resultText, &subError)
		return err == nil && subStatus == "completed" && strings.Contains(resultText, secretToken) && subError == ""
	}, 5*time.Second, 20*time.Millisecond, "child subagent must settle to completed without error")

	require.True(t, childStep1Ran.Load(), "child subagent step 1 must execute")
	require.True(t, childStep2Ran.Load(), "child subagent step 2 must execute")

	// Verify parent receives wake notice and processes Turn 2
	require.Eventually(t, func() bool {
		return parentTurn2Ran.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent worker must wake up and run turn 2")

	// Verify parent transcript contains final synthesized reply with secretToken
	require.Eventually(t, func() bool {
		var lastAssistantBody string
		err := rawDB.QueryRowContext(h.ctx, `SELECT CAST(body AS TEXT) FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq DESC LIMIT 1`, launched.Agent.AgentID).Scan(&lastAssistantBody)
		return err == nil && strings.Contains(lastAssistantBody, secretToken)
	}, 5*time.Second, 20*time.Millisecond, "parent must synthesize reply containing secret token")

	// Verify parent returns to idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Verify complete history has no lease or stale owner failures
	var errorCount int
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND message LIKE '%stale embedded owner%'`, launched.Agent.AgentID).Scan(&errorCount))
	require.Zero(t, errorCount, "must have zero stale embedded owner errors in complete history")
}

// TestBehavioral_Subagent_LongRunning_SteerAndProgress verifies Scenario 2:
// 1. Subagent runs for multiple steps while parent is completely idle.
// 2. Mid-flight progress updates and steering messages persist cleanly to SQLite.
// 3. get_subagent_result and steer_subagent work seamlessly regardless of parent idle duration.
func TestBehavioral_Subagent_LongRunning_SteerAndProgress(t *testing.T) {
	parentReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer parentReg.Unregister()

	childReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer childReg.Unregister()

	parentIdleGate := make(chan struct{})
	childStep2Entered := make(chan struct{})
	steerDeliveredGate := make(chan struct{})
	defer func() {
		for _, ch := range []chan struct{}{parentIdleGate, steerDeliveredGate} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	}()

	var parentTurn1Ran atomic.Bool
	var parentTurn1AckRan atomic.Bool
	var parentWakeRan atomic.Bool
	var sawSteer atomic.Bool

	// Parent step 1: spawns subagent and goes idle
	parentStep1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("subagent", map[string]any{
				"prompt":      "Multi-step progress and steer task",
				"description": "multi-step task",
			}, "call-sub-multi-1"),
		}, ai.StopToolUse)
	}

	parentStep1Ack := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1AckRan.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Subagent running multi-step task. Going idle."},
		}, ai.StopStop)
	}

	parentStep2Wake := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentWakeRan.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Parent acknowledges subagent completion."},
		}, ai.StopStop)
	}

	parentReg.SetResponses([]providers.FauxResponseStep{parentStep1, parentStep1Ack, parentStep2Wake})

	// Child step 1: wait for parent to be idle, then write step1.txt
	childStep1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		<-parentIdleGate
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("write", map[string]any{
				"path":    "step1.txt",
				"content": "phase-1-intermediate-progress",
			}, "child-write-1"),
		}, ai.StopToolUse)
	}

	// Child step 2: after tool result from step 1, pause until test delivers steer_subagent
	childStep2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		select {
		case childStep2Entered <- struct{}{}:
		default:
		}
		<-steerDeliveredGate
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Step 1 complete, standing by for steered direction."},
		}, ai.StopStop)
	}

	// Child step 3: receives steering message prompt from agent loop, writes step2-steered.txt
	childStep3 := func(tc ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		for _, m := range tc.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if textContent, ok := c.(ai.TextContent); ok && strings.Contains(textContent.Text, "steered-direction") {
						sawSteer.Store(true)
					}
				}
			}
		}

		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("write", map[string]any{
				"path":    "step2-steered.txt",
				"content": "phase-2-steered-output-complete",
			}, "child-write-2"),
		}, ai.StopToolUse)
	}

	// Child step 4: finish execution after step2 write completes
	childStep4 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Subagent completed all steps including steer."},
		}, ai.StopStop)
	}

	childReg.SetResponses([]providers.FauxResponseStep{childStep1, childStep2, childStep3, childStep4})

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		if rec.ParentAgentID != "" || strings.HasPrefix(rec.AgentID, "subagent-") {
			return childReg.GetModel()
		}
		return parentReg.GetModel()
	})

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "steer-parent-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "33333333-4444-5555-6666-777777777771",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	status := h.postMessage(t, "msg-steer-1", "user-lead", launched.Agent.AgentID, "Start multi-step subagent")
	require.Equal(t, http.StatusCreated, status)

	rawDB := testRawDB(t, h.dbPath)

	// Wait for child to be registered
	var childID string
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(h.ctx, `SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?`, launched.Agent.AgentID).Scan(&childID)
		return err == nil && childID != ""
	}, 3*time.Second, 10*time.Millisecond)

	// Wait for parent to finish Turn 1 and go idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && parentTurn1AckRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent must finish turn 1 and go idle")

	// Set parent session owner_expires_at to expired timestamp to prove lease-free semantics
	pastTime := time.Now().UTC().Add(-20 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	require.Eventually(t, func() bool {
		_, execErr := rawDB.ExecContext(h.ctx, `UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, pastTime, launched.Agent.AgentID)
		return execErr == nil
	}, 3*time.Second, 20*time.Millisecond)

	// Now release child to run step 1 while parent is idle
	close(parentIdleGate)

	// Wait for child step 2 to be entered (meaning step 1 tool call finished)
	select {
	case <-childStep2Entered:
	case <-time.After(5 * time.Second):
		t.Fatal("child step 2 was not entered")
	}

	// Verify step1.txt exists on disk
	step1Data, err := os.ReadFile(filepath.Join(h.root, "step1.txt"))
	require.NoError(t, err)
	require.Equal(t, "phase-1-intermediate-progress", string(step1Data))

	// Verify parent is still idle
	parentRec, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", parentRec.ActivityState)

	// Verify mid-flight progress in SQLite while subagent is running and parent is idle
	var subStatus string
	var toolUseCount int
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT status, tool_use_count FROM managed_subagents WHERE agent_id=?`, childID).Scan(&subStatus, &toolUseCount))
	require.Equal(t, "running", subStatus)
	require.GreaterOrEqual(t, toolUseCount, 1, "tool use count must reflect mid-flight progress")

	// Test get_subagent_result tool snapshot retrieval while parent is idle
	parentBinding := store.EmbeddedBinding{
		AgentID:    launched.Agent.AgentID,
		SessionID:  parentRec.PiSessionID,
		Epoch:      parentRec.SessionEpoch,
		Generation: parentRec.RuntimeGeneration,
	}
	publicTools := h.manager.EmbeddedSubagentPublicTools(parentBinding)
	var foundGetResult bool
	for _, tool := range publicTools {
		if tool.Name == "get_subagent_result" {
			foundGetResult = true
			result, err := tool.Execute(h.ctx, "test-get-result", map[string]any{"agent_id": childID}, nil)
			require.NoError(t, err, "get_subagent_result must succeed while parent is idle")
			require.NotEmpty(t, result.Content)
			break
		}
	}
	require.True(t, foundGetResult, "get_subagent_result tool must be provided")

	// Steer subagent mid-flight via SteerSubagent while parent is idle
	err = h.manager.SteerSubagent(h.ctx, parentBinding, childID, "steered-direction: execute phase 2 now")
	require.NoError(t, err, "steer_subagent must succeed while parent is idle with expired lease")

	// Unblock child step 2 so agent loop processes the steering message
	close(steerDeliveredGate)

	// Wait for child subagent to complete
	require.Eventually(t, func() bool {
		var st string
		err := rawDB.QueryRowContext(h.ctx, `SELECT status FROM managed_subagents WHERE agent_id=?`, childID).Scan(&st)
		return err == nil && st == "completed"
	}, 5*time.Second, 20*time.Millisecond, "subagent must settle to completed")

	require.True(t, sawSteer.Load(), "subagent must observe intermediate steering message in context")

	// Verify step2-steered.txt exists on disk
	require.Eventually(t, func() bool {
		step2Data, err := os.ReadFile(filepath.Join(h.root, "step2-steered.txt"))
		return err == nil && string(step2Data) == "phase-2-steered-output-complete"
	}, 5*time.Second, 20*time.Millisecond, "step2-steered.txt must be written")

	// Verify terminal counters in SQLite
	var finalTurns, finalTools int
	require.NoError(t, rawDB.QueryRowContext(h.ctx, `SELECT turn_count, tool_use_count FROM managed_subagents WHERE agent_id=?`, childID).Scan(&finalTurns, &finalTools))
	require.GreaterOrEqual(t, finalTurns, 2)
	require.GreaterOrEqual(t, finalTools, 2)

	// Wait for parent worker to receive wake notice and finish Turn 2
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && parentWakeRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent must handle subagent completion wake and return to idle")
}

// TestBehavioral_Subagent_Stop_LeaseFreeSemantics verifies Scenario 3:
// 1. Administrative stop via POST /v1/managed-agents/:id/subagents/:child_id/stop works instantly.
// 2. Memory context canceled immediately.
// 3. Database marked stopped/failed cleanly without lease fences.
// 4. Parent notified via system notice mailbox message without lease delays.
func TestBehavioral_Subagent_Stop_LeaseFreeSemantics(t *testing.T) {
	parentReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer parentReg.Unregister()

	childReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer childReg.Unregister()

	parentIdleGate := make(chan struct{})
	childStarted := make(chan struct{})
	childUnblock := make(chan struct{})
	defer func() {
		for _, ch := range []chan struct{}{parentIdleGate, childUnblock} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	}()

	var parentTurn1Ran atomic.Bool
	var parentTurn1AckRan atomic.Bool
	var parentWakeRan atomic.Bool

	parentStep1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("subagent", map[string]any{
				"prompt":      "Long-running subagent to be stopped",
				"description": "to be stopped",
			}, "call-sub-stop-1"),
		}, ai.StopToolUse)
	}

	parentStep1Ack := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentTurn1AckRan.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Subagent running. Going idle."},
		}, ai.StopStop)
	}

	parentStep2Wake := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		parentWakeRan.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Acknowledged subagent termination notice."},
		}, ai.StopStop)
	}

	parentReg.SetResponses([]providers.FauxResponseStep{parentStep1, parentStep1Ack, parentStep2Wake})

	// Child step: wait for parent idle, signal started, then loop on tool calls
	steps := make([]providers.FauxResponseStep, 10)
	for i := 0; i < 10; i++ {
		idx := i + 1
		steps[i] = func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-parentIdleGate
			select {
			case childStarted <- struct{}{}:
			default:
			}
			time.Sleep(30 * time.Millisecond)
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("read", map[string]any{"path": "task.txt"}, fmt.Sprintf("call-%d", idx)),
			}, ai.StopToolUse)
		}
	}

	childReg.SetResponses(steps)

	h := newBehavioralHarness(t, func(rec store.ManagedAgentRecord) *ai.Model {
		if rec.ParentAgentID != "" || strings.HasPrefix(rec.AgentID, "subagent-") {
			return childReg.GetModel()
		}
		return parentReg.GetModel()
	})

	require.NoError(t, os.WriteFile(filepath.Join(h.root, "task.txt"), []byte("sample-data"), 0644))

	launched, err := h.manager.Launch(h.ctx, managed.LaunchRequest{
		Name:      "stop-parent-worker",
		Cwd:       h.root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "44444444-5555-6666-7777-888888888881",
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	status := h.postMessage(t, "msg-stop-1", "user-lead", launched.Agent.AgentID, "Spawn stoppable subagent")
	require.Equal(t, http.StatusCreated, status)

	rawDB := testRawDB(t, h.dbPath)

	var childID string
	require.Eventually(t, func() bool {
		err := rawDB.QueryRowContext(h.ctx, `SELECT agent_id FROM managed_subagents WHERE parent_agent_id=?`, launched.Agent.AgentID).Scan(&childID)
		return err == nil && childID != ""
	}, 3*time.Second, 10*time.Millisecond)

	// Wait for parent worker to finish Turn 1 and go idle
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && parentTurn1AckRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent must be idle before child runs")

	// Set parent session lease to expired timestamp to prove lease-free stop semantics
	pastTime := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	require.Eventually(t, func() bool {
		_, execErr := rawDB.ExecContext(h.ctx, `UPDATE embedded_sessions SET owner_expires_at=? WHERE agent_id=?`, pastTime, launched.Agent.AgentID)
		return execErr == nil
	}, 3*time.Second, 20*time.Millisecond)

	// Release child to start running while parent is idle
	close(parentIdleGate)

	// Wait for child to start running
	select {
	case <-childStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start in time")
	}

	// Send administrative stop via HTTP endpoint: POST /v1/managed-agents/:id/subagents/:child_id/stop
	stopURL := fmt.Sprintf("%s/v1/managed-agents/%s/subagents/%s/stop", h.httpURL, launched.Agent.AgentID, childID)
	req, err := http.NewRequestWithContext(h.ctx, http.MethodPost, stopURL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "administrative stop endpoint must return 200 OK")

	// Verify database marked stopped
	require.Eventually(t, func() bool {
		var st string
		err := rawDB.QueryRowContext(h.ctx, `SELECT status FROM managed_subagents WHERE agent_id=?`, childID).Scan(&st)
		return err == nil && (st == "stopped" || st == "failed")
	}, 3*time.Second, 20*time.Millisecond, "subagent status in DB must transition to stopped")

	// Verify system notice mailbox message was delivered to parent worker
	expectedMsgID := "subagent-terminated-" + childID + "-1"
	require.Eventually(t, func() bool {
		msg, err := h.db.MessageFor(h.ctx, expectedMsgID)
		return err == nil && msg != nil && msg.To == launched.Agent.AgentID
	}, 3*time.Second, 20*time.Millisecond, "subagent-terminated mailbox notification must be delivered to parent")

	msg, err := h.db.MessageFor(h.ctx, expectedMsgID)
	require.NoError(t, err)
	require.NotNil(t, msg)
	require.NotNil(t, msg.Subject)
	require.Contains(t, *msg.Subject, "[System Notice] Subagent Terminated: "+childID)

	// Verify parent receives termination notice wake and processes Turn 2
	require.Eventually(t, func() bool {
		agent, err := h.db.ManagedAgent(h.ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && parentWakeRan.Load()
	}, 5*time.Second, 20*time.Millisecond, "parent must wake up to handle termination notice and return to idle")
}

// TestBehavioral_Worker_RestartRecovery_WithoutLeases verifies Scenario 4:
// 1. Active worker survives restart.
// 2. Recovers current session epoch and binding without attempting to reacquire or wait for expiring 30s leases.
// 3. Immediately admits and executes new turns upon restart.
func TestBehavioral_Worker_RestartRecovery_WithoutLeases(t *testing.T) {
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turn1Ran atomic.Bool
	var turn2Ran atomic.Bool

	step1 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		turn1Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Worker turn 1 executed before restart."},
		}, ai.StopStop)
	}

	step2 := func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		turn2Ran.Store(true)
		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "Worker turn 2 executed immediately after restart with zero lease delay."},
		}, ai.StopStop)
	}

	reg.SetResponses([]providers.FauxResponseStep{step1, step2})

	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	dbPath := filepath.Join(t.TempDir(), "restart_recovery_nolease.db")

	db, err := store.Open(ctx, dbPath, store.Options{MaxBodyBytes: 64 * 1024})
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	credentials := testfixture.CodexAdmission(t)

	managerOpts := managed.ManagerOptions{
		Store:    db,
		Manifest: manifest,
		Config: managed.Config{
			SessionRoot:                  filepath.Join(root, "sessions"),
			AllowedWorkingDirectoryRoots: []string{root, t.TempDir(), os.TempDir()},
			Limits:                       managed.DefaultLimits(),
		},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore:  credentials,
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
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
	httpURL, _ := router.URLs()

	launched, err := mgr.Launch(ctx, managed.LaunchRequest{
		Name:      "restart-survivor-worker",
		Cwd:       root,
		Runtime:   "embedded",
		Model:     &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID: "55555555-6666-7777-8888-999999999991",
	})
	require.NoError(t, err)

	// Wait for worker to become ready and idle
	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.LifecycleState == "running" && agent.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Post message for Turn 1
	postMsg := func(url, id, to, body string) int {
		payload := map[string]any{
			"id":         id,
			"from":       "user-lead",
			"to":         to,
			"body":       body,
			"created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		}
		data, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/messages", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := http.DefaultClient.Do(req)
		require.NoError(t, doErr)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	status := postMsg(httpURL, "msg-pre-restart", launched.Agent.AgentID, "Execute pre-restart turn")
	require.Equal(t, http.StatusCreated, status)

	require.Eventually(t, func() bool {
		agent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && turn1Ran.Load()
	}, 5*time.Second, 20*time.Millisecond)

	// Capture pre-restart session state
	preRestartAgent, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	expectedSessionID := preRestartAgent.PiSessionID
	expectedEpoch := preRestartAgent.SessionEpoch

	// Simulate immediate crash/restart (< 50ms, far below the old 30s lease TTL)
	require.NoError(t, router.Close(ctx))
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	// Reopen Store, Manager, and Router on the exact same database
	reopenedDB, err := store.Open(ctx, dbPath, store.Options{MaxBodyBytes: 64 * 1024})
	require.NoError(t, err)
	defer reopenedDB.Close()

	reopenedManagerOpts := managed.ManagerOptions{
		Store:    reopenedDB,
		Manifest: manifest,
		Config: managed.Config{
			SessionRoot:                  filepath.Join(root, "sessions"),
			AllowedWorkingDirectoryRoots: []string{root, t.TempDir(), os.TempDir()},
			Limits:                       managed.DefaultLimits(),
		},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore:  credentials,
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	}

	reopenedMgr, err := managed.NewManager(reopenedManagerOpts)
	require.NoError(t, err)
	defer reopenedMgr.Close(ctx)

	reopenedRouter, err := transport.New(transport.Options{
		Store:               reopenedDB,
		Managed:             reopenedMgr,
		Host:                "127.0.0.1",
		Port:                0,
		DefaultEmbeddedRoot: true,
	})
	require.NoError(t, err)

	// router.Listen runs runStartup which invokes RecoverEmbeddedRoots
	startRecover := time.Now()
	require.NoError(t, reopenedRouter.Listen(ctx))
	recoveryDuration := time.Since(startRecover)
	defer reopenedRouter.Close(ctx)

	// Recovery must happen immediately without waiting for any 30s expiring lease!
	require.Less(t, recoveryDuration, 5*time.Second, "restart recovery must be instantaneous without 30s lease wait")

	// Verify worker survived restart with preserved session epoch and binding
	postRestartAgent, err := reopenedDB.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", postRestartAgent.LifecycleState, "worker must retain running state across restart")
	require.Equal(t, "idle", postRestartAgent.ActivityState, "worker must be reset to idle across restart")
	require.Equal(t, expectedSessionID, postRestartAgent.PiSessionID, "pi session ID must be preserved")
	require.Equal(t, expectedEpoch, postRestartAgent.SessionEpoch, "session epoch must be preserved")

	// Post message to recovered worker immediately
	reopenedHTTPURL, _ := reopenedRouter.URLs()
	status = postMsg(reopenedHTTPURL, "msg-post-restart", launched.Agent.AgentID, "Execute post-restart turn")
	require.Equal(t, http.StatusCreated, status)

	// Verify Turn 2 executes immediately and completes successfully
	require.Eventually(t, func() bool {
		agent, err := reopenedDB.ManagedAgent(ctx, launched.Agent.AgentID)
		return err == nil && agent.ActivityState == "idle" && turn2Ran.Load()
	}, 5*time.Second, 20*time.Millisecond, "worker must immediately execute turn 2 after restart")

	// Verify outbox contains Turn 2 reply
	rawDB := testRawDB(t, dbPath)

	var replyBody string
	require.NoError(t, rawDB.QueryRowContext(ctx, `SELECT body FROM embedded_reply_outbox WHERE sender=? ORDER BY rowid DESC LIMIT 1`, launched.Agent.AgentID).Scan(&replyBody))
	require.Contains(t, replyBody, "Worker turn 2 executed immediately after restart with zero lease delay.")
}

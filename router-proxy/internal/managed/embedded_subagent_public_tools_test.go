package managed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupTool(t *testing.T, tools []agent.AgentTool, name string) agent.AgentTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found in tool slice", name)
	return agent.AgentTool{}
}

func TestEmbeddedSubagentPublicToolsConstructionAndChildSurfaceExclusion(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_construct.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-construct")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// 1. Primary constructor returns exactly the 3 public tools
	tools := mgr.EmbeddedSubagentPublicTools(parent)
	require.Len(t, tools, 3)
	require.Equal(t, "subagent", tools[0].Name)
	require.Equal(t, "get_subagent_result", tools[1].Name)
	require.Equal(t, "steer_subagent", tools[2].Name)

	// Verify parameter schema contains only S3-specified parameters:
	// prompt, description, subagent_type, max_turns, resume.
	// Model/thinking/inherit_context must not be advertised.
	subSchema := tools[0].Parameters
	require.NotNil(t, subSchema)
	require.Contains(t, subSchema.Properties, "prompt")
	require.Contains(t, subSchema.Properties, "description")
	require.Contains(t, subSchema.Properties, "subagent_type")
	require.Contains(t, subSchema.Properties, "max_turns")
	require.Contains(t, subSchema.Properties, "resume")
	require.NotContains(t, subSchema.Properties, "model")
	require.NotContains(t, subSchema.Properties, "thinking")
	require.NotContains(t, subSchema.Properties, "inherit_context")
	require.NotContains(t, subSchema.Properties, "run_in_background")

	// 2. Child nested surface strictly excluded:
	// The child subagent toolset (NewEmbeddedSubagentTools) must ONLY contain confined read, edit, write, bash.
	// It must NOT contain subagent, get_subagent_result, or steer_subagent.
	childTools, cleanup, err := mgr.EmbeddedSubagentTools(t.TempDir())
	require.NoError(t, err)
	defer cleanup()

	for _, ct := range childTools {
		require.NotEqual(t, "subagent", ct.Name, "child must not have subagent tool")
		require.NotEqual(t, "get_subagent_result", ct.Name, "child must not have get_subagent_result tool")
		require.NotEqual(t, "steer_subagent", ct.Name, "child must not have steer_subagent tool")
		require.NotEqual(t, "mecha_worker", ct.Name, "child must not have supervisor tools")
		require.NotEqual(t, "mecha_mailbox_send", ct.Name, "child must not have supervisor tools")
	}

	// 3. Confirm primary constructor registration in embedded_session.go matches exactly
	sessionSrc, err := os.ReadFile("embedded_session.go")
	require.NoError(t, err)
	require.Contains(t, string(sessionSrc), "m.EmbeddedSubagentPublicTools(binding)",
		"registration in embedded_session.go must use accepted primary constructor signature")
}

func TestSubagentToolImmediateResponseUnder50ms(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_immediate.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-immediate")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "completed fast"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")

	// Measure launch latency
	start := time.Now()
	res, err := subTool.Execute(ctx, "call-imm-1", map[string]any{
		"prompt":      "quick task",
		"description": "deterministic speed test",
	}, nil)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Less(t, elapsed, 50*time.Millisecond, "subagent spawn must return immediately (<50ms)")

	require.NotEmpty(t, res.Content)
	text := res.Content[0].(ai.TextContent).Text
	assert.Contains(t, text, "Agent started in background.")
	assert.Contains(t, text, "Agent ID: subagent-parent-immediate-")
	assert.Contains(t, text, "Type: general-purpose")
	assert.Contains(t, text, "Description: deterministic speed test")

	close(gate)
}

func TestSubagentToolUnknownTypeFallback(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_fallback.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-fallback")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "fallback done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")

	// Unknown type: "exotic-custom-researcher" falls back to "general-purpose"
	res, err := subTool.Execute(ctx, "call-fb-1", map[string]any{
		"prompt":        "investigate fallback",
		"description":   "fallback test",
		"subagent_type": "exotic-custom-researcher",
	}, nil)
	require.NoError(t, err)

	text := res.Content[0].(ai.TextContent).Text
	assert.Contains(t, text, "Type: general-purpose")

	close(gate)
}

func TestSubagentToolValidationAndUnsupportedArguments(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_valid.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-valid")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")
	getTool := lookupTool(t, tools, "get_subagent_result")
	steerTool := lookupTool(t, tools, "steer_subagent")

	// 1. Missing prompt
	_, err = subTool.Execute(ctx, "v1", map[string]any{
		"description": "no prompt",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prompt is required")

	// 2. Invalid max_turns
	_, err = subTool.Execute(ctx, "v2", map[string]any{
		"prompt":    "do work",
		"max_turns": 0,
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_turns must be an integer >= 1")

	_, err = subTool.Execute(ctx, "v2b", map[string]any{
		"prompt":    "do work",
		"max_turns": -5,
	}, nil)
	require.Error(t, err)

	// 3. Stale run_in_background rejected without echoing value
	_, err = subTool.Execute(ctx, "v3", map[string]any{
		"prompt":            "do work",
		"run_in_background": false,
	}, nil)
	require.Error(t, err)
	assert.Equal(t, "unsupported argument \"run_in_background\"", err.Error())

	// 4. Unsupported invented parameters rejected without echoing values
	for _, param := range []string{"model", "thinking", "inherit_context", "cwd", "parent"} {
		_, err = subTool.Execute(ctx, "v-unsupported-"+param, map[string]any{
			"prompt": "do work",
			param:    "secret_value",
		}, nil)
		require.Error(t, err)
		assert.Equal(t, fmt.Sprintf("unsupported argument %q", param), err.Error())
	}

	// 5. Stale wait on get_subagent_result rejected
	_, err = getTool.Execute(ctx, "v4", map[string]any{
		"agent_id": "any-id",
		"wait":     true,
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported argument \"wait\"")
	assert.Contains(t, err.Error(), "snapshot-only")

	// 6. Missing agent_id on get_subagent_result
	_, err = getTool.Execute(ctx, "v5", map[string]any{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent_id is required")

	// 7. Missing message on steer_subagent
	_, err = steerTool.Execute(ctx, "v6", map[string]any{
		"agent_id": "any-id",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message is required")
}

func TestSubagentRunningFourthPullThrottledAndTerminalUnlimited(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_throttle.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-throttle")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			msg := providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "final report result"}}, ai.StopStop)
			msg.Usage = ai.Usage{Input: 50, Output: 50, TotalTokens: 100}
			return msg
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools1 := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools1, "subagent")
	getTool1 := lookupTool(t, tools1, "get_subagent_result")

	// Spawn subagent (held running at gate)
	res, err := subTool.Execute(ctx, "sp-1", map[string]any{
		"prompt":      "run long task",
		"description": "throttle test",
	}, nil)
	require.NoError(t, err)
	lines := strings.Split(res.Content[0].(ai.TextContent).Text, "\n")
	var childID string
	for _, line := range lines {
		if strings.HasPrefix(line, "Agent ID: ") {
			childID = strings.TrimPrefix(line, "Agent ID: ")
			break
		}
	}
	require.NotEmpty(t, childID)

	// Ensure subagent is running in store
	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		return err == nil && rec.Status == "running"
	}, 2*time.Second, 10*time.Millisecond)

	// Pull 1: Allowed, returns mandatory warning
	p1, err := getTool1.Execute(ctx, "pull-1", map[string]any{"agent_id": childID}, nil)
	require.NoError(t, err)
	text1 := p1.Content[0].(ai.TextContent).Text
	assert.Contains(t, text1, SubagentSnapshotWarning)
	assert.Contains(t, text1, "[Subagent Running Snapshot]")

	// Pull 2: Allowed
	_, err = getTool1.Execute(ctx, "pull-2", map[string]any{"agent_id": childID}, nil)
	require.NoError(t, err)

	// Pull 3 (via a newly constructed tools slice on same manager): Throttle must be shared per manager!
	tools2 := mgr.EmbeddedSubagentPublicTools(parent)
	getTool2 := lookupTool(t, tools2, "get_subagent_result")
	_, err = getTool2.Execute(ctx, "pull-3", map[string]any{"agent_id": childID}, nil)
	require.NoError(t, err)

	// Pull 4: Rejected under rolling 60s limit
	_, err = getTool2.Execute(ctx, "pull-4", map[string]any{"agent_id": childID}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSubagentPollThrottled)

	// Release gate so subagent settles
	close(gate)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		return err == nil && rec.Status == "completed"
	}, 3*time.Second, 20*time.Millisecond)

	// Once terminal, pulls are NEVER rejected (P3)
	for i := 1; i <= 6; i++ {
		termRes, err := getTool1.Execute(ctx, fmt.Sprintf("term-pull-%d", i), map[string]any{
			"agent_id": childID,
			"verbose":  true,
		}, nil)
		require.NoError(t, err, "terminal pull %d must never reject", i)
		termText := termRes.Content[0].(ai.TextContent).Text
		assert.Contains(t, termText, "[Subagent Result Report]")
		assert.Contains(t, termText, "final report result")
		assert.NotContains(t, termText, SubagentSnapshotWarning)
	}
}

func TestSubagentForeignAndStaleIDsDenied(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_fences.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parentA := subagentTestParent(t, db, "parent-fence-a")
	parentB := subagentTestParent(t, db, "parent-fence-b")
	staleA := parentA
	staleA.Epoch = 99 // Stale epoch

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "fence test done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parentA)
	registerParentOwner(mgr, parentB)

	// Spawn child under parentA
	toolsA := mgr.EmbeddedSubagentPublicTools(parentA)
	subToolA := lookupTool(t, toolsA, "subagent")
	res, err := subToolA.Execute(ctx, "sp-a", map[string]any{
		"prompt":      "owned by parent A",
		"description": "parentage isolation test",
	}, nil)
	require.NoError(t, err)
	var childID string
	for _, line := range strings.Split(res.Content[0].(ai.TextContent).Text, "\n") {
		if strings.HasPrefix(line, "Agent ID: ") {
			childID = strings.TrimPrefix(line, "Agent ID: ")
			break
		}
	}
	require.NotEmpty(t, childID)

	// 1. Foreign parent B cannot get_subagent_result
	toolsB := mgr.EmbeddedSubagentPublicTools(parentB)
	getToolB := lookupTool(t, toolsB, "get_subagent_result")
	_, err = getToolB.Execute(ctx, "get-b", map[string]any{"agent_id": childID}, nil)
	require.Error(t, err, "foreign parent must be denied")

	// 2. Foreign parent B cannot steer_subagent
	steerToolB := lookupTool(t, toolsB, "steer_subagent")
	_, err = steerToolB.Execute(ctx, "steer-b", map[string]any{
		"agent_id": childID,
		"message":  "intruder steer",
	}, nil)
	require.Error(t, err, "foreign parent must be denied steer")

	// 3. Stale epoch parent A cannot get_subagent_result
	staleToolsA := mgr.EmbeddedSubagentPublicTools(staleA)
	getStaleA := lookupTool(t, staleToolsA, "get_subagent_result")
	_, err = getStaleA.Execute(ctx, "get-stale", map[string]any{"agent_id": childID}, nil)
	require.Error(t, err, "stale parent must be denied get")

	// 4. Stale epoch parent A cannot steer_subagent
	steerStaleA := lookupTool(t, staleToolsA, "steer_subagent")
	_, err = steerStaleA.Execute(ctx, "steer-stale", map[string]any{
		"agent_id": childID,
		"message":  "stale steer",
	}, nil)
	require.Error(t, err, "stale parent must be denied steer")

	// 5. Non-existent child ID denied
	getToolA := lookupTool(t, toolsA, "get_subagent_result")
	_, err = getToolA.Execute(ctx, "get-none", map[string]any{"agent_id": "nonexistent-id"}, nil)
	require.Error(t, err, "nonexistent child ID must return error")

	steerToolA := lookupTool(t, toolsA, "steer_subagent")
	_, err = steerToolA.Execute(ctx, "steer-none", map[string]any{
		"agent_id": "nonexistent-id",
		"message":  "ghost steer",
	}, nil)
	require.Error(t, err, "nonexistent child ID must return error")

	close(gate)
}

func TestSubagentQueuedSteerAndTerminalSteerError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_steer.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-steer")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "child 1 done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "child 2 done"}}, ai.StopStop)
		},
	})

	// Capacity 1: Child 1 runs, Child 2 is queued
	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")
	steerTool := lookupTool(t, tools, "steer_subagent")

	// Child 1 starts running
	res1, err := subTool.Execute(ctx, "c1", map[string]any{
		"prompt":      "task 1",
		"description": "running child",
	}, nil)
	require.NoError(t, err)
	var child1ID string
	for _, l := range strings.Split(res1.Content[0].(ai.TextContent).Text, "\n") {
		if strings.HasPrefix(l, "Agent ID: ") {
			child1ID = strings.TrimPrefix(l, "Agent ID: ")
		}
	}

	// Child 2 is queued
	res2, err := subTool.Execute(ctx, "c2", map[string]any{
		"prompt":      "task 2",
		"description": "queued child",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, res2.Content[0].(ai.TextContent).Text, "Agent queued in background.")
	var child2ID string
	for _, l := range strings.Split(res2.Content[0].(ai.TextContent).Text, "\n") {
		if strings.HasPrefix(l, "Agent ID: ") {
			child2ID = strings.TrimPrefix(l, "Agent ID: ")
		}
	}

	// 1. Queued steer: calling steer on queued child buffers message successfully
	steerRes, err := steerTool.Execute(ctx, "st-q", map[string]any{
		"agent_id": child2ID,
		"message":  "direction while queued",
	}, nil)
	require.NoError(t, err)
	assert.Contains(t, steerRes.Content[0].(ai.TextContent).Text, "Steering message queued for agent")

	// Release gate so child 1 completes
	gate <- struct{}{}

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, child1ID)
		return err == nil && rec.Status == "completed"
	}, 3*time.Second, 20*time.Millisecond)

	// 2. Terminal steer error: steering terminal child 1 must return error
	_, err = steerTool.Execute(ctx, "st-term", map[string]any{
		"agent_id": child1ID,
		"message":  "too late",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot steer terminal subagent")

	// Finish child 2
	close(gate)
}

func TestSubagentPersistedCountersAndFullVerboseReportAfterReopen(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_persisted_reopen.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	parent := subagentTestParent(t, db, "parent-reopen")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	// Child runs with an actual tool call to generate complete SDK transcript:
	// Turn 1: Assistant calls write tool
	// Turn 2: Assistant receives tool result and produces final answer
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-write-output", Name: "write", Arguments: map[string]any{
					"path":    "computation.txt",
					"content": "42",
				}},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "computation complete: exact value 42"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")

	res, err := subTool.Execute(ctx, "sp-reopen", map[string]any{
		"prompt":      "compute value into computation.txt",
		"description": "reopen test",
	}, nil)
	require.NoError(t, err)
	var childID string
	for _, l := range strings.Split(res.Content[0].(ai.TextContent).Text, "\n") {
		if strings.HasPrefix(l, "Agent ID: ") {
			childID = strings.TrimPrefix(l, "Agent ID: ")
		}
	}
	require.NotEmpty(t, childID)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		return err == nil && rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	recBeforeClose, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, "completed", recBeforeClose.Status)
	require.Equal(t, 2, recBeforeClose.TurnCount)
	require.Equal(t, 1, recBeforeClose.ToolUseCount)
	require.Equal(t, "computation complete: exact value 42", recBeforeClose.ResultText)

	// Close manager and store to simulate complete process exit
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	// Reopen fresh store and fresh manager without in-memory task registry
	reopenedDB, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer reopenedDB.Close()

	reopenedMgr := makeSubagentTestManager(t, reopenedDB, reg, 4)
	defer reopenedMgr.Close(ctx)
	registerParentOwner(reopenedMgr, parent)

	// Confirm record is genuinely persisted in store after reopen
	recReopened, err := reopenedDB.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, recBeforeClose.Status, recReopened.Status)
	require.Equal(t, recBeforeClose.TurnCount, recReopened.TurnCount)
	require.Equal(t, recBeforeClose.ToolUseCount, recReopened.ToolUseCount)
	require.Equal(t, recBeforeClose.TotalTokens, recReopened.TotalTokens)
	require.Equal(t, recBeforeClose.ResultText, recReopened.ResultText)

	// Retrieve full verbose report via get_subagent_result tool on fresh reopened manager
	reopenedTools := reopenedMgr.EmbeddedSubagentPublicTools(parent)
	getTool := lookupTool(t, reopenedTools, "get_subagent_result")

	reportRes, err := getTool.Execute(ctx, "get-reopened-verbose", map[string]any{
		"agent_id": childID,
		"verbose":  true,
	}, nil)
	require.NoError(t, err)
	reportText := reportRes.Content[0].(ai.TextContent).Text

	// Confirm exact counters and status from persisted store record
	assert.Contains(t, reportText, "[Subagent Result Report]")
	assert.Contains(t, reportText, fmt.Sprintf("Child ID: %s", childID))
	assert.Contains(t, reportText, "Description: reopen test")
	assert.Contains(t, reportText, "Status: completed")
	assert.Contains(t, reportText, fmt.Sprintf("Turns: %d", recReopened.TurnCount))
	assert.Contains(t, reportText, fmt.Sprintf("Tool Uses: %d", recReopened.ToolUseCount))
	assert.Contains(t, reportText, fmt.Sprintf("Lifetime Tokens: %d", recReopened.TotalTokens))
	assert.Contains(t, reportText, "Result:\ncomputation complete: exact value 42")

	// Confirm genuinely persisted transcript was materialized from ManagedSubagentTranscript:
	// Includes user prompt, tool call, tool result, and final assistant message.
	assert.Contains(t, reportText, "Transcript:\n")
	assert.Contains(t, reportText, "[user]")
	assert.Contains(t, reportText, "compute value into computation.txt")
	assert.Contains(t, reportText, "[assistant]")
	assert.Contains(t, reportText, "Tool call: write(map[content:42 path:computation.txt])")
	assert.Contains(t, reportText, "[tool_result: write]")
	assert.Contains(t, reportText, "computation complete: exact value 42")
}

func TestSubagentToolVerboseErrorOnCorruptChainAndClosedDB(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_corrupt_chain.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	parent := subagentTestParent(t, db, "parent-corrupt")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-w", Name: "write", Arguments: map[string]any{"path": "f.txt", "content": "1"}},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "done"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	registerParentOwner(mgr, parent)

	tools := mgr.EmbeddedSubagentPublicTools(parent)
	subTool := lookupTool(t, tools, "subagent")
	getTool := lookupTool(t, tools, "get_subagent_result")

	res, err := subTool.Execute(ctx, "sp-c", map[string]any{"prompt": "write f.txt"}, nil)
	require.NoError(t, err)
	var childID string
	for _, l := range strings.Split(res.Content[0].(ai.TextContent).Text, "\n") {
		if strings.HasPrefix(l, "Agent ID: ") {
			childID = strings.TrimPrefix(l, "Agent ID: ")
		}
	}
	require.NotEmpty(t, childID)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		return err == nil && rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	// Tamper with embedded_entries using modernc sqlite directly to simulate corrupt chain
	// matching owner store test pattern: UPDATE embedded_entries SET body=X'01' WHERE agent_id=? AND seq=2
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `UPDATE embedded_entries SET body=X'01' WHERE agent_id=? AND seq=2`, childID)
	require.NoError(t, err)

	// 1. Tool call with verbose: true must NOT silently return empty transcript; it must return clear error
	_, err = getTool.Execute(ctx, "get-corrupt", map[string]any{
		"agent_id": childID,
		"verbose":  true,
	}, nil)
	require.Error(t, err, "tampered chain must return error on verbose pull")
	assert.Contains(t, err.Error(), "corrupt embedded entry chain")

	// 2. Closed DB: calling verbose pull when DB is closed must return DB closed error, not silent empty
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	reopenedTools := mgr.EmbeddedSubagentPublicTools(parent)
	reopenedGet := lookupTool(t, reopenedTools, "get_subagent_result")
	_, err = reopenedGet.Execute(ctx, "get-closed", map[string]any{
		"agent_id": childID,
		"verbose":  true,
	}, nil)
	require.Error(t, err, "closed DB must return error on verbose pull")
	assert.Contains(t, err.Error(), "closed")
}

func TestFetchSubagentTranscriptActiveStatusPropagatesClosedDBError(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_active_closed.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	parent := subagentTestParent(t, db, "parent-active-closed")
	childID := "child-active-1"
	require.NoError(t, db.CreateManagedSubagent(ctx, parent, store.ManagedSubagentRecord{
		AgentID:       childID,
		ParentAgentID: parent.AgentID,
		Status:        "running",
	}))

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	mgr := makeSubagentTestManager(t, db, reg, 4)
	registerParentOwner(mgr, parent)

	out, err := mgr.fetchSubagentTranscript(ctx, parent, childID, "running")
	require.NoError(t, err)
	assert.Empty(t, out)

	// Close DB: fetchSubagentTranscript with running/queued must NOT swallow store closed error.
	require.NoError(t, db.Close())

	for _, activeStatus := range []string{"running", "queued"} {
		_, err = mgr.fetchSubagentTranscript(ctx, parent, childID, activeStatus)
		require.Error(t, err, "fetchSubagentTranscript must return error on closed DB for active status %s", activeStatus)
		assert.Contains(t, err.Error(), "store is closed")
	}
}



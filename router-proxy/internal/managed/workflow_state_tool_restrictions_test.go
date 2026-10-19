package managed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWorkflowStateToolRestrictionsEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var mu sync.Mutex
	var observedToolsAcrossTurns [][]string
	var attemptedBashResult atomic.Pointer[ai.ToolResultMessage]

	stepNumber := 0

	fauxStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		mu.Lock()
		defer mu.Unlock()
		stepNumber++
		t.Logf("fauxStep called stepNumber=%d len(messages)=%d", stepNumber, len(h.Messages))

		// Inspect current tools visible in history
		currentTools := ai.GetCurrentTools(h.Messages)
		var names []string
		for _, tool := range currentTools {
			names = append(names, tool.Name)
		}
		sort.Strings(names)
		t.Logf("observed tool names: %v", names)
		observedToolsAcrossTurns = append(observedToolsAcrossTurns, names)

		// Check if any tool result message for bash was returned
		for _, msg := range h.Messages {
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolName == "bash" {
				attemptedBashResult.Store(&r)
			}
		}

		if stepNumber == 3 {
			// In step 3 (when restricted to read and mecha_mailbox_send), try to call "bash"
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo restricted"}, "call_bash_test_1"),
			}, ai.StopToolUse)
		}

		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "step done"},
		}, ai.StopStop)
	}

	// Supply steps
	steps := make([]providers.FauxResponseStep, 20)
	for i := range steps {
		steps[i] = fauxStep
	}
	reg.SetResponses(steps)

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedMailBackoff:   10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// 1. Create a workflow state with restricted tools: read, mecha_mailbox_send
	ws1, err := db.CreateWorkflowState(ctx, store.WorkflowStateRequest{
		Name:         "RestrictedState",
		Description:  "Only read and mecha_mailbox_send",
		AllowedTools: []string{"read", "mecha_mailbox_send"},
	})
	require.NoError(t, err)

	// 2. Launch worker without any active workflow state
	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:               "test-worker-agent",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:          "11111111-1111-4111-8111-111111111111",
		InitialInstruction: "launch turn prompt",
	})
	require.NoError(t, err)
	agentID := launched.Agent.AgentID

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Verify turn 1 (unrestricted) tools included bash and read
	mu.Lock()
	require.GreaterOrEqual(t, len(observedToolsAcrossTurns), 1)
	turn1Tools := observedToolsAcrossTurns[0]
	mu.Unlock()

	hasBash := false
	hasRead := false
	for _, tool := range turn1Tools {
		if tool == "bash" {
			hasBash = true
		}
		if tool == "read" {
			hasRead = true
		}
	}
	require.True(t, hasBash, "turn 1 must include bash")
	require.True(t, hasRead, "turn 1 must include read")

	// 3. Set active workflow state to RestrictedState
	err = db.SetManagedAgentActiveWorkflowState(ctx, agentID, &ws1.ID)
	require.NoError(t, err)

	// Send mail to trigger turn 2
	turn2MsgID := "22222222-2222-4222-8222-222222222222"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turn2MsgID,
		From:      "supervisor",
		To:        agentID,
		Body:      "turn 2 prompt",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turn2MsgID)
	require.NoError(t, err)
	manager.OfferDirect(agentID)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	mu.Lock()
	require.GreaterOrEqual(t, len(observedToolsAcrossTurns), 2)
	turn2Tools := observedToolsAcrossTurns[1]
	mu.Unlock()

	// In turn 2, tools must be strictly filtered to read and mecha_mailbox_send
	// plus implicitly allowed control tools (extend_tool_timeout, mecha_tool_timeout, mecha_workflow_transition, workflow_transition)
	for _, expectedTool := range []string{"mecha_mailbox_send", "read"} {
		require.Contains(t, turn2Tools, expectedTool)
	}
	require.NotContains(t, turn2Tools, "bash")
	require.NotContains(t, turn2Tools, "write")

	// 4. Trigger turn 3 where the faux model attempts to invoke "bash" while in RestrictedState
	turn3MsgID := "33333333-3333-4333-8333-333333333333"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turn3MsgID,
		From:      "supervisor",
		To:        agentID,
		Body:      "turn 3 prompt calling bash",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turn3MsgID)
	require.NoError(t, err)
	manager.OfferDirect(agentID)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Verify that the admission fence rejected bash
	bashResult := attemptedBashResult.Load()
	require.NotNil(t, bashResult, "must have received tool result for bash call")
	require.True(t, bashResult.IsError, "bash tool call must be an error result")

	// 5. Clear active workflow state (back to unrestricted)
	err = db.SetManagedAgentActiveWorkflowState(ctx, agentID, nil)
	require.NoError(t, err)

	// Trigger turn 4
	turn4MsgID := "44444444-4444-4444-8444-444444444444"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        turn4MsgID,
		From:      "supervisor",
		To:        agentID,
		Body:      "turn 4 prompt unrestricted",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, turn4MsgID)
	require.NoError(t, err)
	manager.OfferDirect(agentID)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	mu.Lock()
	lastTurnTools := observedToolsAcrossTurns[len(observedToolsAcrossTurns)-1]
	mu.Unlock()

	hasBashAfterClear := false
	for _, tool := range lastTurnTools {
		if tool == "bash" {
			hasBashAfterClear = true
		}
	}
	require.True(t, hasBashAfterClear, "turn 4 must have bash restored after clearing active workflow state")

	require.NotEmpty(t, bashResult.Content)
	textPart, ok := bashResult.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.True(t, strings.Contains(textPart.Text, "tool 'bash' is not allowed in active workflow state") || strings.Contains(textPart.Text, "Tool bash not found"), "expected rejection message, got: %s", textPart.Text)
}

func TestWorkerDisabledToolAdmissionBlocking(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var attemptedBashResult atomic.Pointer[ai.ToolResultMessage]
	stepNumber := 0

	fauxStep := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		stepNumber++
		for _, msg := range h.Messages {
			if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolName == "bash" {
				attemptedBashResult.Store(&r)
			}
		}

		if stepNumber == 2 {
			// In step 2, call bash which was disabled for worker
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("bash", map[string]any{"command": "echo should-be-blocked"}, "call_bash_test_worker_disabled"),
			}, ai.StopToolUse)
		}

		return providers.FauxAssistantMessage(ai.ContentList{
			ai.TextContent{Text: "step done"},
		}, ai.StopStop)
	}

	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = fauxStep
	}
	reg.SetResponses(steps)

	manager, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		EmbeddedMailBackoff:   10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	// Launch worker agent
	launched, err := manager.Launch(ctx, LaunchRequest{
		Name:               "test-worker-disabled-tools",
		Cwd:                root,
		Runtime:            "embedded",
		Model:              &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		RequestID:          "22222222-1111-4111-8111-111111111111",
		InitialInstruction: "launch prompt",
	})
	require.NoError(t, err)
	agentID := launched.Agent.AgentID

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	// Disable tool "bash" specifically for this agent
	err = db.SetAgentTool(ctx, agentID, "bash", false)
	require.NoError(t, err)

	// Send message to trigger step 2 where faux model attempts to run bash
	msgID := "33333333-1111-4111-8111-111111111111"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        msgID,
		From:      "supervisor",
		To:        agentID,
		Body:      "call bash please",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, msgID)
	require.NoError(t, err)
	manager.OfferDirect(agentID)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, agentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 5*time.Second, 20*time.Millisecond)

	bashResult := attemptedBashResult.Load()
	require.NotNil(t, bashResult, "must receive tool result for bash call")
	require.True(t, bashResult.IsError, "bash tool call must be an error result when disabled")
	require.NotEmpty(t, bashResult.Content)
	textPart, ok := bashResult.Content[0].(ai.TextContent)
	require.True(t, ok)
	expectedReason := fmt.Sprintf("tool \"bash\" is disabled for worker %q", agentID)
	require.Contains(t, textPart.Text, expectedReason)
}


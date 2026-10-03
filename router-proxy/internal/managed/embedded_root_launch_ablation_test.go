package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// The fake provider receives the real Manager/SDK root declaration. No Codex
// live credential or HTTP request is involved. Admission uses a private fake
// Codex credential. Keep observations in memory only.
func TestEmbeddedRootLaunchAblationDeclaration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_root_launch_ablation_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	model := Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}
	type capture struct {
		history ai.TranscriptContext
		model   ai.Model
		agentID string
		role    string
	}
	var captureMu sync.Mutex
	modelAgents := map[*ai.Model]string{}
	agentRoles := map[string]string{}
	providerAgents := map[string]int{}
	// Each independent agent has its own one-turn script. The faux queue
	// contains the same dispatcher twice; queue ordinal never selects a role.
	agentResponses := map[string][]providers.FauxResponseStep{
		"ablation-root":   {providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "synthetic done"}}, ai.StopStop))},
		"ablation-worker": {providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "synthetic done"}}, ai.StopStop))},
	}
	seen := make(chan capture, 2)
	step := func(h ai.TranscriptContext, options *ai.SimpleStreamOptions, state *providers.FauxState, model *ai.Model) *ai.AssistantMessage {
		captureMu.Lock()
		agentID := modelAgents[model]
		role := agentRoles[agentID]
		turn := providerAgents[agentID]
		responses := agentResponses[agentID]
		providerAgents[agentID]++
		captureMu.Unlock()
		require.NotEmpty(t, agentID, "provider model must be bound to its own saved agent")
		require.Less(t, turn, len(responses), "no original provider replay or extra turn")
		seen <- capture{history: h, model: *model, agentID: agentID, role: role}
		return responses[turn](h, options, state, model)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(model),
		CodexCredentialStore: testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(a store.ManagedAgentRecord) *ai.Model {
			resolved := *reg.GetModel()
			binding := store.EmbeddedBinding{AgentID: a.AgentID, SessionID: a.PiSessionID, Epoch: a.SessionEpoch, Generation: a.RuntimeGeneration}
			role, roleErr := db.EmbeddedSessionRole(ctx, binding)
			require.NoError(t, roleErr, "saved role must exist before the first provider request")
			captureMu.Lock()
			modelAgents[&resolved] = a.AgentID
			agentRoles[a.AgentID] = role
			captureMu.Unlock()
			return &resolved
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	defer func() {
		if t.Failed() {
			m.mu.Lock()
			_, rootPublished := m.embeddedOwners["ablation-root"]
			_, workerPublished := m.embeddedOwners["ablation-worker"]
			m.mu.Unlock()
			captureMu.Lock()
			t.Logf("failure snapshot root_owner_published=%t worker_owner_published=%t provider_declarations_by_agent=%v", rootPublished, workerPublished, providerAgents)
			captureMu.Unlock()
			cwdPrivacyManagedSnapshot(t, filepath.Join(root, "router.db"))
		}
	}()
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "ablation-root", Cwd: root, Runtime: "embedded", Model: &model, InitialInstruction: "synthetic instruction", RequestID: "c1000000-0000-4000-8000-000000000001"})
	require.NoError(t, err)
	_, err = m.Launch(ctx, LaunchRequest{Name: "ablation-worker", Cwd: root, Runtime: "embedded", Model: &model, InitialInstruction: "synthetic instruction", RequestID: "c1000000-0000-4000-8000-000000000002"})
	require.NoError(t, err)
	captures := map[string]capture{}
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for len(captures) < 2 {
		select {
		case c := <-seen:
			require.Contains(t, agentResponses, c.agentID)
			_, duplicate := captures[c.agentID]
			require.False(t, duplicate, "each declaration must come from a different saved agent")
			captures[c.agentID] = c
		case <-deadline.C:
			t.Fatal("fake provider did not receive both independent initial declarations")
		}
	}
	rootCapture, workerCapture := captures["ablation-root"], captures["ablation-worker"]
	require.Equal(t, "root", rootCapture.role)
	require.Equal(t, "worker", workerCapture.role)
	captureMu.Lock()
	observedCalls := map[string]int{}
	for agentID, count := range providerAgents {
		observedCalls[agentID] = count
	}
	captureMu.Unlock()
	require.Equal(t, map[string]int{"ablation-root": 1, "ablation-worker": 1}, observedCalls)
	rootTools := declaredTools(t, rootCapture.history)
	workerTools := declaredTools(t, workerCapture.history)
	// Addressed mailbox is Manager-owned, independent of a host external tool.
	// Keep this literal contract independent of either captured declaration.
	require.Nil(t, m.embeddedExternalTool)
	wantNames := []string{"mecha_mailbox_send", "goal", "goal_snooze", "mecha_context", "mecha_reminder", "bash", "read", "edit", "write", "subagent", "get_subagent_result", "steer_subagent", "mecha_worker"}
	require.Equal(t, wantNames, toolNames(workerTools))
	wantRootNames := append([]string(nil), wantNames...)
	if embeddedRootLaunchAblation {
		wantRootNames = wantRootNames[:12]
	}
	require.Equal(t, wantRootNames, toolNames(rootTools))
	mailbox := workerTools[0].Parameters
	require.NotNil(t, mailbox)
	require.Equal(t, "object", mailbox.Type)
	require.Equal(t, []string{"recipient", "body"}, mailbox.Required)
	require.Len(t, mailbox.Properties, 3)
	require.NotNil(t, mailbox.AdditionalAllowed)
	require.False(t, *mailbox.AdditionalAllowed)
	for _, property := range []string{"recipient", "body", "subject"} {
		require.NotNil(t, mailbox.Properties[property])
		require.Equal(t, "string", mailbox.Properties[property].Type)
	}
	for _, check := range []struct {
		index   int
		actions []any
	}{
		{1, []any{"set", "status", "off"}},
		{3, []any{"status", "compact"}},
		{4, []any{"list", "create", "update", "delete"}},
		{12, []any{"list", "launch", "send", "cleanup"}},
	} {
		tool := workerTools[check.index]
		require.NotNil(t, tool.Parameters, tool.Name)
		require.Contains(t, tool.Parameters.Required, "action", tool.Name)
		require.NotNil(t, tool.Parameters.AdditionalAllowed, tool.Name)
		require.False(t, *tool.Parameters.AdditionalAllowed, tool.Name)
		action := tool.Parameters.Properties["action"]
		require.NotNil(t, action, tool.Name)
		require.Equal(t, "string", action.Type, tool.Name)
		require.Equal(t, check.actions, action.Enum, "closed approved action enum for %s", tool.Name)
	}
	expected := append([]ai.Tool(nil), workerTools...)
	if embeddedRootLaunchAblation {
		expected = expected[:len(expected)-1]
	}
	require.Equal(t, toolNames(expected), toolNames(rootTools), "only root launch declaration may differ")
	require.Equal(t, canonical(t, expected), canonical(t, rootTools), "other schemas and descriptions must be unchanged")
	require.Equal(t, canonical(t, rootCapture.model), canonical(t, workerCapture.model), "model must be identical")
	// Compare the complete SDK provider transcript after removing only the
	// root's missing declaration. This includes input and system instructions.
	normalized := rootCapture.history
	for i, message := range normalized.Messages {
		if system, ok := message.(ai.SystemMessage); ok {
			system.ToolsAdded = workerTools
			normalized.Messages = append([]ai.Message(nil), normalized.Messages...)
			normalized.Messages[i] = system
		}
	}
	require.Equal(t, canonical(t, ablationInitialTimestamps(workerCapture.history)), canonical(t, ablationInitialTimestamps(normalized)), "non-tool provider input changed")
}

// Only the independently generated initial system/user timestamps differ.
// Clone the message slice and typed values; retain every other field and message.
func ablationInitialTimestamps(h ai.TranscriptContext) ai.TranscriptContext {
	if h.Messages != nil {
		messages := make([]ai.Message, len(h.Messages))
		copy(messages, h.Messages)
		h.Messages = messages
	}
	if len(h.Messages) > 0 {
		if system, ok := h.Messages[0].(ai.SystemMessage); ok {
			system.Timestamp = 0
			h.Messages[0] = system
		}
	}
	if len(h.Messages) > 1 {
		if user, ok := h.Messages[1].(ai.UserMessage); ok {
			user.Timestamp = 0
			h.Messages[1] = user
		}
	}
	return h
}

func TestEmbeddedRootLaunchAblationTimestampComparison(t *testing.T) {
	base := ai.TranscriptContext{Messages: []ai.Message{
		ai.SystemMessage{Content: ai.ContentList{ai.TextContent{Text: "system"}}, Timestamp: 11},
		ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "user"}}, Timestamp: 12},
		ai.AssistantMessage{Content: ai.ContentList{ai.ToolCall{ID: "call", Name: "read", Arguments: map[string]any{"path": "file"}}}, Timestamp: 13},
		ai.ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: ai.ContentList{ai.TextContent{Text: "result"}}, Timestamp: 14},
	}}
	changed := func(index int, message ai.Message) ai.TranscriptContext {
		h := base
		h.Messages = append([]ai.Message(nil), base.Messages...)
		h.Messages[index] = message
		return h
	}
	timestamps := changed(0, ai.SystemMessage{Content: ai.ContentList{ai.TextContent{Text: "system"}}, Timestamp: 21})
	timestamps.Messages[1] = ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "user"}}, Timestamp: 22}
	original := canonical(t, base)
	for _, tc := range []struct {
		name      string
		histories []ai.TranscriptContext
		equal     bool
	}{
		{"initial-timestamps-only", []ai.TranscriptContext{timestamps}, true},
		{"semantic-content", []ai.TranscriptContext{changed(1, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "different"}}, Timestamp: 12})}, false},
		{"role-order-correlation-and-later-timestamp", []ai.TranscriptContext{
			changed(1, ai.SystemMessage{Content: ai.ContentList{ai.TextContent{Text: "user"}}, Timestamp: 12}),
			{Messages: []ai.Message{base.Messages[1], base.Messages[0], base.Messages[2], base.Messages[3]}},
			changed(2, ai.AssistantMessage{Content: ai.ContentList{ai.ToolCall{ID: "different", Name: "read", Arguments: map[string]any{"path": "file"}}}, Timestamp: 13}),
			changed(3, ai.ToolResultMessage{ToolCallID: "different", ToolName: "read", Content: ai.ContentList{ai.TextContent{Text: "result"}}, Timestamp: 14}),
			changed(3, ai.ToolResultMessage{ToolCallID: "call", ToolName: "read", Content: ai.ContentList{ai.TextContent{Text: "result"}}, Timestamp: 24}),
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, h := range tc.histories {
				if tc.equal {
					require.Equal(t, canonical(t, ablationInitialTimestamps(base)), canonical(t, ablationInitialTimestamps(h)))
				} else {
					require.NotEqual(t, canonical(t, ablationInitialTimestamps(base)), canonical(t, ablationInitialTimestamps(h)))
				}
			}
		})
	}
	require.Equal(t, original, canonical(t, base), "comparison must not mutate captured messages")
}

func declaredTools(t *testing.T, h ai.TranscriptContext) []ai.Tool {
	t.Helper()
	for _, message := range h.Messages {
		if system, ok := message.(ai.SystemMessage); ok {
			return system.ToolsAdded
		}
	}
	t.Fatal("missing real SDK system declaration")
	return nil
}
func toolNames(tools []ai.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

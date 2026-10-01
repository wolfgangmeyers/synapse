package managed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// RED: a root and an independently launched, parentless worker must each see
// the fixed goal tool in their real SDK provider turn. Stop at advertisement;
// a proposed call is not proof that an unregistered tool ran.
func TestEmbeddedG5GoalLifecycleSDKRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	type caller struct {
		name, goal string
		provider   *providers.FauxProviderRegistration
		seen       chan observation
		binding    store.EmbeddedBinding
	}
	// Separate providers ensure a result can only come from the saved caller's
	// session, never from a shared provider script or an inferred parent.
	callers := []caller{
		{name: "g5-goal-root", goal: "finish root goal", seen: make(chan observation, 5)},
		{name: "g5-goal-worker", goal: "finish worker goal", seen: make(chan observation, 5)},
	}
	for i := range callers {
		c := &callers[i]
		reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
		defer reg.Unregister()
		c.provider = reg
		steps := make([]providers.FauxResponseStep, 0, 5)
		for n, action := range []string{"set", "status", "off", "status"} {
			n, action := n, action
			steps = append(steps, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				var observation observation
				for _, message := range h.Messages {
					if system, ok := message.(ai.SystemMessage); ok {
						observation.tools = append(observation.tools, system.ToolsAdded...)
					}
					if result, ok := message.(ai.ToolResultMessage); ok && n > 0 && result.ToolCallID == c.name+"-goal-"+string(rune('a'+n-1)) {
						copy := result
						observation.result = &copy
					}
				}
				c.seen <- observation
				args := map[string]any{"action": action}
				if action == "set" {
					args["goal"] = c.goal
				}
				return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", args, c.name+"-goal-"+string(rune('a'+n)))}, ai.StopToolUse)
			})
		}
		steps = append(steps, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var observed observation
			for _, message := range h.Messages {
				if result, ok := message.(ai.ToolResultMessage); ok && result.ToolCallID == c.name+"-goal-d" {
					copy := result
					observed.result = &copy
				}
			}
			c.seen <- observed
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		})
		reg.SetResponses(steps)
	}
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:               Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:        CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		CodexCredentialStore: testfixture.CodexAdmission(t),
		EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
			for i := range callers {
				if saved.AgentID == callers[i].name {
					return callers[i].provider.GetModel()
				}
			}
			return nil
		},
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	for i := range callers {
		c := &callers[i]
		request := LaunchRequest{Name: c.name, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set your own goal", RequestID: "a5555555-5555-4555-8555-55555555559" + string(rune('0'+i))}
		var launched LaunchResult
		if i == 0 {
			launched, err = m.LaunchEmbeddedRoot(ctx, request)
		} else {
			launched, err = m.Launch(ctx, request)
		}
		require.NoError(t, err)
		c.binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
		role, err := db.EmbeddedSessionRole(ctx, c.binding)
		require.NoError(t, err)
		if i == 0 {
			require.Equal(t, "root", role)
		} else {
			require.Equal(t, "worker", role)
		}
	}
	require.NotEqual(t, callers[0].binding.AgentID, callers[1].binding.AgentID)
	require.NotEqual(t, callers[0].binding.SessionID, callers[1].binding.SessionID)
	require.NotEqual(t, callers[0].binding.Generation, callers[1].binding.Generation)

	for i := range callers {
		c := &callers[i]
		var first observation
		select {
		case first = <-c.seen:
		case <-time.After(4 * time.Second):
			t.Fatalf("%s provider did not receive SDK turn", c.name)
		}
		advertised := false
		for _, tool := range first.tools {
			if tool.Name == "goal" {
				advertised = true
			}
		}
		require.True(t, advertised, "%s: goal not advertised (first genuine RED; all downstream result, isolation, revision and reopen assertions unexecuted)", c.name)
	}
	// After advertisement, each SDK continuation must correlate to the exact
	// call, show its own goal/revision, and never reveal the other caller's goal.
	for i := range callers {
		c := &callers[i]
		other := callers[1-i].goal
		for n, action := range []string{"set", "status", "off", "status"} {
			var next observation
			select {
			case next = <-c.seen:
			case <-time.After(4 * time.Second):
				t.Fatalf("%s %s: missing SDK continuation", c.name, action)
			}
			require.NotNil(t, next.result, "%s %s: missing correlated SDK toolResult", c.name, action)
			require.Equal(t, c.name+"-goal-"+string(rune('a'+n)), next.result.ToolCallID)
			require.Equal(t, "goal", next.result.ToolName)
			require.False(t, next.result.IsError)
			require.NotEmpty(t, next.result.Content)
			text := next.result.Content[0].(ai.TextContent).Text
			require.NotContains(t, text, other)
			if n < 2 {
				require.Contains(t, text, c.goal)
			} else {
				require.NotContains(t, text, c.goal)
			}
		}
	}
	// Reopen the durable store, not the in-memory Manager or faux transcript.
	require.NoError(t, m.Close(ctx))
	require.NoError(t, db.Close())
	reopened, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	for _, c := range callers {
		policy, err := reopened.ManagedGoalPolicyFor(ctx, c.name)
		require.NoError(t, err)
		require.Equal(t, c.name, policy.AgentID)
		require.False(t, policy.Config.Enabled)
		require.Empty(t, policy.Config.Goal)
		require.EqualValues(t, 2, policy.DesiredRevision)
		binding := inspectManagedOwnerBinding(t, path, c.binding)
		snapshot, err := reopened.ReopenEmbedded(ctx, binding)
		require.NoError(t, err)
		var ordered []string
		for _, entry := range snapshot.Entries {
			if entry.Kind != "toolResult" {
				continue
			}
			var result ai.ToolResultMessage
			require.NoError(t, json.Unmarshal(entry.Body, &result))
			require.Equal(t, "goal", result.ToolName)
			ordered = append(ordered, result.ToolCallID)
		}
		require.Equal(t, []string{c.name + "-goal-a", c.name + "-goal-b", c.name + "-goal-c", c.name + "-goal-d"}, ordered)
	}
}

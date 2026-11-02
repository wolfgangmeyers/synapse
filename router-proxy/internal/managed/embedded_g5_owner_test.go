package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// The caller's tool arguments cannot turn an ordinary parentless worker into
// a root. Exercise the actual SDK admission, not a direct Store mutation.
func TestEmbeddedG5WorkerSDKRootRoleSpoofDeniedBeforeIntent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-worker-root-spoof"
	args := map[string]any{"action": "launch", "name": "spoof-child", "cwd": root, "instruction": "task", "role": "root"}
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	observed := make(chan observation, 2)
	reg.SetResponses([]providers.FauxResponseStep{
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
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	launched, err := m.Launch(ctx, LaunchRequest{Name: "spoof-sender", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "try launch", RequestID: "a5555555-5555-4555-8555-555555555589"})
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	role, err := db.SavedEmbeddedSessionRole(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "worker", role)
	var first observation
	select {
	case first = <-observed:
	case <-time.After(4 * time.Second):
		t.Fatal("parentless worker provider did not run")
	}
	var registered *ai.Tool
	for i := range first.tools {
		if first.tools[i].Name == "mecha_worker" {
			registered = &first.tools[i]
		}
	}
	require.NotNil(t, registered, "worker must reach the real host SDK registration")
	require.False(t, registered.Parameters.Check(map[string]any{"action": "launch", "name": "spoof-child", "cwd": root, "instruction": "task"}), "launch must not remain available to a worker")
	require.False(t, registered.Parameters.Check(args), "role must be rejected by the closed launch schema")
	select {
	case second := <-observed:
		require.NotNil(t, second.result, "denial must correlate with the spoofed call")
		require.Equal(t, callID, second.result.ToolCallID)
		require.Equal(t, "mecha_worker", second.result.ToolName)
		require.True(t, second.result.IsError, "spoof must not be an accepted launch")
		require.NotEmpty(t, second.result.Content)
		require.Contains(t, second.result.Content[0].(ai.TextContent).Text, `Validation failed for tool "mecha_worker"`, "SDK must reject arguments before Store admission, not return managed-disabled 503")
		require.Contains(t, second.result.Content[0].(ai.TextContent).Text, "must not have additional properties")
	case <-time.After(4 * time.Second):
		t.Fatal("no correlated SDK denial continuation")
	}
	reader, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer reader.Close()
	var effects int
	require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effects))
	require.Zero(t, effects, "rejected caller authority must never commit an effect intent")
	_, err = db.ManagedOperation(ctx, embeddedWorkerRequestID(binding, callID))
	require.ErrorIs(t, err, sql.ErrNoRows)
	_, err = db.ManagedAgent(ctx, "spoof-child")
	require.ErrorIs(t, err, sql.ErrNoRows)
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	require.Equal(t, launched.Agent.AgentID, agents[0].AgentID)
	require.Equal(t, binding.SessionID, agents[0].PiSessionID)
	require.Equal(t, binding.Epoch, agents[0].SessionEpoch)
	require.Equal(t, binding.Generation, agents[0].RuntimeGeneration)
	role, err = db.SavedEmbeddedSessionRole(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, "worker", role, "only LaunchEmbeddedRoot can reserve root")
}

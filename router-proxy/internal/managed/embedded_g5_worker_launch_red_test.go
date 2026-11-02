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
)

// A stale model call is rejected by the real SDK before reserving a worker.
func TestEmbeddedWorkerLaunchRejectedBySDK(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	const callID = "g5-worker-call-1"
	args := map[string]any{"action": "launch", "name": "g5-independent", "cwd": root, "instruction": "independent task"}
	type observation struct {
		tools  []ai.Tool
		result *ai.ToolResultMessage
	}
	observed := make(chan observation, 2)
	reg.SetResponses([]providers.FauxResponseStep{
		func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var tools []ai.Tool
			for _, msg := range history.Messages {
				if system, ok := msg.(ai.SystemMessage); ok {
					tools = append(tools, system.ToolsAdded...)
				}
			}
			observed <- observation{tools: tools}
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", args, callID)}, ai.StopToolUse)
		},
		func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var result *ai.ToolResultMessage
			for _, msg := range history.Messages {
				if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == callID {
					copy := r
					result = &copy
				}
			}
			observed <- observation{result: result}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{
		Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	setEmbeddedRecoveryGeneration(t, ctx, db, m)
	_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-root", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "launch independent worker", RequestID: "a5555555-5555-4555-8555-555555555555"})
	require.NoError(t, err)
	var first observation
	select {
	case first = <-observed:
	case <-time.After(3 * time.Second):
		t.Fatal("provider was not called")
	}
	var fixed bool
	for _, tool := range first.tools {
		if tool.Name == "mecha_worker" {
			fixed = true
		}
	}
	require.True(t, fixed, "embedded SDK provider lacks the fixed host-registered worker management tool")
	select {
	case second := <-observed:
		require.NotNil(t, second.result, "same-turn SDK toolResult must retain original call ID")
		require.True(t, second.result.IsError)
	case <-time.After(3 * time.Second):
		t.Fatal("no same-turn provider continuation")
	}
	workers, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Len(t, workers, 1, "rejected launch must not save an independent worker")
	parent, err := db.ManagedAgent(ctx, "g5-root")
	require.NoError(t, err)
	binding := store.EmbeddedBinding{AgentID: parent.AgentID, SessionID: parent.PiSessionID, Epoch: parent.SessionEpoch, Generation: parent.RuntimeGeneration}
	_, err = db.ManagedOperation(ctx, embeddedWorkerRequestID(binding, callID))
	require.ErrorIs(t, err, sql.ErrNoRows, "rejected launch must not reserve an operation")
}

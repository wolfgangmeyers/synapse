package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// One trusted Go registration, no dynamic provider tool catalog. An unknown
// name or arguments incompatible with the registered schema never reach the
// checked intent callback or the external Execute boundary.
func TestEmbeddedG1FixedToolAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, call string
		args       map[string]any
		allowed    bool
	}{
		{"unknown-name", "not_registered", map[string]any{"ticket": "ok"}, false},
		{"schema-mismatch", "g1_test_external_effect", map[string]any{"ticket": 42}, false},
		{"registered-fixed", "g1_test_external_effect", map[string]any{"ticket": "ok"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			path := filepath.Join(root, "admission.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			var executed atomic.Int32
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tc.call, tc.args, "admission-"+tc.name)}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
				},
			})
			tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
				executed.Add(1)
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "executed"}}}, nil
			})
			tool.Parameters = ai.Object(ai.Prop("ticket", ai.String()))
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: testfixture.CodexAdmission(t)})
			require.NoError(t, err)
			defer m.Close(ctx)
			binding := g1Launch(t, m, root)
			require.Eventually(t, func() bool {
				a, e := db.ManagedAgent(ctx, binding.AgentID)
				return e == nil && (a.LifecycleState == "running" || a.LifecycleState == "failed")
			}, 3*time.Second, 10*time.Millisecond)
			check, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer check.Close()
			var intents int
			require.NoError(t, check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_effects WHERE agent_id=?`, binding.AgentID).Scan(&intents))
			if tc.allowed {
				require.Equal(t, 1, intents)
				require.EqualValues(t, 1, executed.Load())
			} else {
				require.Zero(t, intents)
				require.Zero(t, executed.Load())
			}
		})
	}
}

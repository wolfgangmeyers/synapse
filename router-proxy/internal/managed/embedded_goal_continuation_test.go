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
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedGoalContinuationDoneAndOffDoNotStart(t *testing.T) {
	for _, tc := range []struct{ name, action string }{{"done", "set"}, {"off", "off"}} {
		t.Run(tc.name, func(t *testing.T) {
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
			var requests atomic.Int32
			reg.SetResponses([]providers.FauxResponseStep{
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "set", "goal": "finish"}, "initial-set")}, ai.StopToolUse)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					if tc.action == "off" {
						return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("goal", map[string]any{"action": "off"}, "turn-off")}, ai.StopToolUse)
					}
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "initial final"}}, ai.StopStop)
				},
				func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					requests.Add(1)
					return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "off final"}}, ai.StopStop)
				},
			})
			var evaluations atomic.Int32
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
				Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
				EmbeddedGoalEvaluator: func(context.Context, store.EmbeddedGoalEvaluationKey) (string, string, error) {
					evaluations.Add(1)
					return store.EmbeddedGoalEvaluationDone, "finished", nil
				},
			})
			require.NoError(t, err)
			defer m.Close(ctx)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			launched, err := m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "g5-no-cont-" + tc.name, Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: "set goal", RequestID: "a5555555-5555-4555-8555-555555555584"})
			require.NoError(t, err)
			wantRequests := int32(2)
			if tc.action == "off" {
				wantRequests = 3
			}
			require.Eventually(t, func() bool { return requests.Load() == wantRequests }, 3*time.Second, 10*time.Millisecond)
			if tc.name == "done" {
				require.Eventually(t, func() bool { return evaluations.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
			} else {
				require.Zero(t, evaluations.Load())
			}
			time.Sleep(100 * time.Millisecond)
			require.Equal(t, wantRequests, requests.Load())
			if tc.name == "done" {
				require.Equal(t, int32(1), evaluations.Load())
			} else {
				require.Zero(t, evaluations.Load())
			}
			readOnly, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
			require.NoError(t, err)
			defer readOnly.Close()
			var count int
			require.NoError(t, readOnly.QueryRowContext(ctx, `SELECT count(*) FROM embedded_goal_continuations WHERE agent_id=?`, launched.Agent.AgentID).Scan(&count))
			require.Zero(t, count)
		})
	}
}

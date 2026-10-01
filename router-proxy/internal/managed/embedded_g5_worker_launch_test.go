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

func TestEmbeddedG5WorkerLaunchReplayAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second map[string]any
		wantWorkers   int
	}{
		{"replay", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job"}, map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job"}, 1},
		{"conflicting-args", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job"}, map[string]any{"action": "launch", "name": "other", "cwd": "ROOT", "instruction": "job"}, 1},
		{"unknown-parent", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job", "parent": "root"}, nil, 0},
		{"unknown-role", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job", "role": "root"}, nil, 0},
		{"missing-instruction", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT"}, nil, 0},
		{"coercion-rejected", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": 42}, nil, 0},
		{"unsafe-cwd", map[string]any{"action": "launch", "name": "child", "cwd": "/dev/null", "instruction": "job"}, nil, 0},
		{"unknown-model-key", map[string]any{"action": "launch", "name": "child", "cwd": "ROOT", "instruction": "job", "model": map[string]any{"provider": "x", "id": "y", "role": "root"}}, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			const provider, modelID = "openai-codex", "gpt-5.6-sol"
			// Successful replay/conflict calls select the admitted Codex identity.
			// Negative admission cases keep their original arguments.
			if tc.wantWorkers == 1 {
				tc.first["model"] = map[string]any{"provider": provider, "id": modelID}
				tc.second["model"] = map[string]any{"provider": provider, "id": modelID}
			}
			if tc.first["cwd"] == "ROOT" {
				tc.first["cwd"] = root
			}
			if tc.second != nil && tc.second["cwd"] == "ROOT" {
				tc.second["cwd"] = root
			}
			calls := []map[string]any{tc.first}
			if tc.second != nil {
				calls = append(calls, tc.second)
			}
			steps := make([]providers.FauxResponseStep, 0, len(calls)+1)
			continued := make(chan struct{}, 1)
			type toolObservation struct{ found, isError bool }
			toolResults := make(chan toolObservation, 1)
			for _, args := range calls {
				args := args
				steps = append(steps, func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
					return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_worker", args, "same-call-id")}, ai.StopToolUse)
				})
			}
			steps = append(steps, func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
				result := toolObservation{}
				for _, msg := range h.Messages {
					if r, ok := msg.(ai.ToolResultMessage); ok && r.ToolCallID == "same-call-id" {
						result.found, result.isError = true, r.IsError

					}
				}
				toolResults <- result
				continued <- struct{}{}
				return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
			})
			reg.SetResponses(steps)
			m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: provider, ID: modelID, Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(saved store.ManagedAgentRecord) *ai.Model {
				if saved.AgentID == "parent" {
					return reg.GetModel()
				}
				return nil
			}})
			require.NoError(t, err)
			defer m.Close(ctx)
			setEmbeddedRecoveryGeneration(t, ctx, db, m)
			_, err = m.LaunchEmbeddedRoot(ctx, LaunchRequest{Name: "parent", Cwd: root, Runtime: "embedded", InitialInstruction: "work", Model: &Model{Provider: provider, ID: modelID}, RequestID: "a5555555-5555-4555-8555-555555555550"})
			require.NoError(t, err)
			if tc.name != "conflicting-args" && tc.name != "replay" && tc.name != "unsafe-cwd" && tc.name != "coercion-rejected" {
				select {
				case <-continued:
				case <-time.After(4 * time.Second):
					t.Fatal("provider did not continue")
				}
			}
			if tc.name == "coercion-rejected" {
				defer func() {
					parent, parentErr := db.ManagedAgent(ctx, "parent")
					agents, agentsErr := db.ManagedAgents(ctx)
					if parentErr == nil && agentsErr == nil {
						t.Logf("diagnostic selected_model=%s/%s lifecycle=%s error_code=%s agent_count=%d", parent.SelectedModel.Provider, parent.SelectedModel.ID, parent.LifecycleState, parent.LastErrorCode, len(agents))
					} else {
						t.Logf("diagnostic parent_error=%v agents_error=%v", parentErr, agentsErr)
					}
					select {
					case result := <-toolResults:
						t.Logf("diagnostic correlated_tool_result=%t is_error=%t", result.found, result.isError)
					default:
						t.Log("diagnostic no provider continuation/tool result")
					}
				}()
			}
			require.Eventually(t, func() bool {
				agents, e := db.ManagedAgents(ctx)
				if e != nil {
					return false
				}
				parent, e := db.ManagedAgent(ctx, "parent")
				if tc.name == "conflicting-args" || tc.name == "replay" || tc.name == "unsafe-cwd" {
					return e == nil && parent.LifecycleState == "failed" && len(agents) == tc.wantWorkers+1
				}
				if tc.name == "coercion-rejected" {
					return e == nil && parent.LifecycleState == "running" && len(agents) == 1
				}
				return e == nil && (parent.LifecycleState == "running" || parent.LifecycleState == "failed") && len(agents) == tc.wantWorkers+1
			}, 4*time.Second, 10*time.Millisecond)
			if tc.name == "coercion-rejected" {
				select {
				case <-continued:
				case <-time.After(4 * time.Second):
					t.Fatal("checked tool result did not return to parent provider")
				}
				select {
				case result := <-toolResults:
					require.True(t, result.found, "missing correlated tool result")
					require.True(t, result.isError, "coercion must return a checked error tool result")
					t.Logf("diagnostic correlated_tool_result=%t is_error=%t", result.found, result.isError)
				default:
					t.Fatal("missing correlated tool result")
				}
			}
			agents, err := db.ManagedAgents(ctx)
			require.NoError(t, err)
			require.Len(t, agents, tc.wantWorkers+1)
			if tc.name == "coercion-rejected" {
				parent, err := db.ManagedAgent(ctx, "parent")
				require.NoError(t, err)
				require.Equal(t, "running", parent.LifecycleState)
				read, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
				require.NoError(t, err)
				defer read.Close()
				var binding store.EmbeddedBinding
				require.Eventually(t, func() bool {
					m.mu.Lock()
					owner, ok := m.embeddedOwners[parent.AgentID]
					m.mu.Unlock()
					if !ok || owner.binding.AgentID != parent.AgentID || owner.binding.SessionID != parent.PiSessionID || owner.binding.Epoch != parent.SessionEpoch || owner.binding.Generation != parent.RuntimeGeneration || owner.binding.OwnerToken <= 0 {
						return false
					}
					var token int64
					var identity string
					queryErr := read.QueryRowContext(ctx, `SELECT owner_token,owner_id FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, parent.AgentID, parent.PiSessionID, parent.SessionEpoch, parent.RuntimeGeneration).Scan(&token, &identity)
					if queryErr != nil || token != owner.binding.OwnerToken || identity != owner.identity {
						return false
					}
					binding = owner.binding
					return true
				}, 4*time.Second, 10*time.Millisecond, "current owner must be published before checking rejected input")
				_, err = db.RecoverEmbeddedEffect(ctx, binding, "same-call-id")
				require.ErrorIs(t, err, sql.ErrNoRows, "rejected call must not leave a durable effect")
				var intents int
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_effects WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND effect_id=?`, binding.AgentID, binding.SessionID, binding.Epoch, binding.Generation, "same-call-id").Scan(&intents))
				require.Zero(t, intents, "rejected input must leave neither intent nor effect")
				t.Run("invalid-owner-precedence", func(t *testing.T) {
					invalid := binding
					invalid.OwnerToken = 0
					_, invalidErr := db.RecoverEmbeddedEffect(ctx, invalid, "same-call-id")
					require.ErrorContains(t, invalidErr, "stale embedded owner")
				})
			}
			if tc.name == "replay" {
				// A checked original can be retrieved by the existing launch helper,
				// but cannot become a second SDK execution/producer receipt.
				select {
				case <-continued:
					t.Fatal("reused original SDK call continued as a fresh invocation")
				default:
				}
				read, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
				require.NoError(t, err)
				defer read.Close()
				var checked int
				require.NoError(t, read.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='parent' AND kind='toolResult'`).Scan(&checked))
				require.Equal(t, 1, checked, "original checked result is not repeated")
			}
			if tc.wantWorkers == 1 {
				child, err := db.ManagedAgent(ctx, "child")
				require.NoError(t, err)
				require.Equal(t, "embedded", child.Runtime)
				role, err := db.EmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: child.AgentID, SessionID: child.PiSessionID, Epoch: child.SessionEpoch, Generation: child.RuntimeGeneration})
				if child.LifecycleState != "failed" {
					require.NoError(t, err)
					require.Equal(t, "worker", role)
				}
			}
		})
	}
}

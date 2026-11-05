package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// URG-004: compose actual SDK admission/checked persistence with the router's
// coordinator and real observer sockets. This is not browser fault injection.
func TestAgentUrgencySDKComposedLossAndOrdering(t *testing.T) {
	for _, scenario := range []string{"lost-result", "native-rest-order"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			db, _ := storetest.OpenTestStore(t)
			root := t.TempDir()
			reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
			defer reg.Unregister()
			manager, err := managed.NewManager(managed.ManagerOptions{
				Store: db, Manifest: mustEffectManifest(t),
				Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
				CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
				EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
				CodexCredentialStore:  testfixture.CodexAdmission(t),
			})
			require.NoError(t, err)
			generation, err := db.MintEmbeddedProcessGeneration(ctx, "composed-urgency-test")
			require.NoError(t, err)
			require.NoError(t, manager.SetEmbeddedStartupGeneration(generation))
			router, err := New(Options{Store: db, Managed: manager})
			require.NoError(t, err)
			defer router.Close(context.Background())
			// Request entry is an existing HTTP boundary, not a production fault hook.
			restEntered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/urgency") {
					select {
					case restEntered <- struct{}{}:
					default:
					}
				}
				router.ServeHTTP(w, r)
			}))
			defer server.Close()
			server.Client().Timeout = 15 * time.Second
			const caller = "composed-urgency-root"
			_, err = manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: caller, Cwd: root, Runtime: "embedded", RequestID: "a1111111-1111-4111-8111-111111111111"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				row, e := db.ManagedAgent(ctx, caller)
				return e == nil && row.LifecycleState == "running"
			}, 3*time.Second, 10*time.Millisecond)
			row, err := db.ManagedAgent(ctx, caller)
			require.NoError(t, err)
			binding := store.EmbeddedBinding{AgentID: caller, SessionID: row.PiSessionID, Epoch: row.SessionEpoch, Generation: row.RuntimeGeneration}
			ui := dialUILive(t, server, "https://arbitrary.example")
			defer ui.CloseNow()
			readUILiveFrame(t, ui)
			readUILiveFrame(t, ui)
			offline := dialUILive(t, server, "")
			readUILiveFrame(t, offline)
			readUILiveFrame(t, offline)
			require.NoError(t, offline.Close(websocket.StatusNormalClosure, "offline before fault"))
			legacy, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/agents", nil)
			require.NoError(t, err)
			defer legacy.CloseNow()
			require.NoError(t, legacy.Write(ctx, websocket.MessageText, []byte(`{"version":"v1","frame_id":"observer-register","type":"register","agent_id":"composed-observer","instance_id":"observer-instance","conversation_sync":"v1"}`)))
			require.Equal(t, "registered", readUILiveFrame(t, legacy)["type"])

			// Every request issues a provider-originated tool call through the actual
			// session, not a public setter or a reconstructed invocation witness.
			promptNumber := 0
			prompt := func(id string) int {
				promptNumber++
				reg.SetResponses([]providers.FauxResponseStep{
					providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("mecha_agent_urgency", map[string]any{"urgent": true}, id)}, ai.StopToolUse)),
					providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "request settled"}}, ai.StopStop)),
				})
				req, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/managed-agents/"+caller+"/prompt", strings.NewReader(`{"message":"explicitly set urgency true"}`))
				require.NoError(t, e)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Request-ID", fmt.Sprintf("b1111111-1111-4111-8111-%012d", promptNumber))
				resp, e := server.Client().Do(req)
				require.NoError(t, e)
				defer resp.Body.Close()
				return resp.StatusCode
			}
			checked := func(id string) *ai.ToolResultMessage {
				snapshot, e := db.ReopenEmbedded(ctx, binding)
				require.NoError(t, e)
				for _, entry := range snapshot.Entries {
					if entry.Kind != "toolResult" {
						continue
					}
					msg, e := ai.UnmarshalMessage(entry.Body)
					require.NoError(t, e)
					if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == id {
						return &result
					}
				}
				return nil
			}
			assertUnknown := func(id string) {
				effect, e := db.RecoverEmbeddedEffect(ctx, binding, id)
				require.NoError(t, e)
				require.Equal(t, "unknown", effect.State)
				require.Empty(t, effect.Result)
				require.Nil(t, checked(id), "uncertain result must not become checked success/error")
			}
			assertProjection := func(value bool) {
				saved, e := db.ManagedAgent(ctx, caller)
				require.NoError(t, e)
				require.Equal(t, value, saved.Urgent)
				for _, path := range []string{"/v1/managed-agents", "/v1/managed-agents/" + caller} {
					resp, e := server.Client().Get(server.URL + path)
					require.NoError(t, e)
					require.Equal(t, 200, resp.StatusCode)
					var payload map[string]any
					require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
					resp.Body.Close()
					if path == "/v1/managed-agents" {
						require.Equal(t, value, payload["agents"].([]any)[0].(map[string]any)["urgent"])
					} else {
						require.Equal(t, value, payload["agent"].(map[string]any)["urgent"])
					}
				}
				reconnect := dialUILive(t, server, "")
				defer reconnect.CloseNow()
				snapshot := readUILiveFrame(t, reconnect)
				require.Equal(t, "managed_agents_snapshot", snapshot["type"])
				require.Equal(t, value, snapshot["agents"].([]any)[0].(map[string]any)["urgent"])
			}
			// Filter unrelated normal lifecycle/history frames, retain ordered urgency.
			readUrgency := func(conn *websocket.Conn, want []bool) {
				var previous float64
				for _, value := range want {
					for {
						frame := readUILiveFrame(t, conn)
						if version, ok := frame["projection_version"].(float64); ok {
							require.GreaterOrEqual(t, version, previous)
							previous = version
						}
						if frame["type"] != "agent_urgency_changed" {
							continue
						}
						require.Equal(t, caller, frame["agent_id"])
						require.Equal(t, value, frame["urgent"])
						break
					}
				}
			}
			if scenario == "lost-result" {
				const lostID = "composed-lost-urgency"
				committed := false
				manager.SetEmbeddedUrgencyCoordinator(func(mutate func() (store.AgentUrgencyResult, bool, error)) (store.AgentUrgencyResult, error) {
					result, e := router.coordinateAgentUrgency(mutate)
					if e != nil {
						return result, e
					}
					saved, e := db.ManagedAgent(ctx, caller)
					require.NoError(t, e)
					require.True(t, saved.Urgent, "fault is strictly after real domain COMMIT and router publication")
					committed = true
					return result, errors.New("test: checked native result lost after commit/publication")
				})
				_ = prompt(lostID) // HTTP may settle recovery; durable disposition is authoritative.
				require.True(t, committed)
				assertUnknown(lostID)
				readUrgency(ui, []bool{true})
				readUrgency(legacy, []bool{true})
				assertProjection(true)
				manager.SetEmbeddedUrgencyCoordinator(router.coordinateAgentUrgency)
				status, _ := urgencyPUT(t, server.Client(), server.URL, caller, `{"urgent":false}`)
				require.Equal(t, 200, status)
				_ = prompt(lostID)
				assertUnknown(lostID)
				assertProjection(false)
				require.Equal(t, 200, prompt("composed-fresh-urgency"))
				result := checked("composed-fresh-urgency")
				require.NotNil(t, result)
				require.False(t, result.IsError)
				require.JSONEq(t, `{"agent_id":"composed-urgency-root","urgent":true}`, result.Content[0].(ai.TextContent).Text)
				effect, e := db.RecoverEmbeddedEffect(ctx, binding, "composed-fresh-urgency")
				require.NoError(t, e)
				require.Equal(t, "observed", effect.State)
				readUrgency(ui, []bool{false, true})
				readUrgency(legacy, []bool{false, true})
				assertProjection(true)
			} else {
				entered, release := make(chan struct{}), make(chan struct{})
				defer func() {
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				manager.SetEmbeddedUrgencyCoordinator(func(mutate func() (store.AgentUrgencyResult, bool, error)) (store.AgentUrgencyResult, error) {
					return router.coordinateAgentUrgency(func() (store.AgentUrgencyResult, bool, error) {
						close(entered) // actual native invocation admitted, coordinator ordering lock held
						select {
						case <-release:
						case <-ctx.Done():
							return store.AgentUrgencyResult{}, false, ctx.Err()
						}
						return mutate()
					})
				})
				nativeDone := make(chan int, 1)
				go func() { nativeDone <- prompt("composed-racing-native") }()
				select {
				case <-entered:
				case status := <-nativeDone:
					t.Fatalf("native finished before admission barrier: HTTP %d result=%+v", status, checked("composed-racing-native"))
				case <-ctx.Done():
					t.Fatal("native coordinator barrier not reached")
				}
				restDone := make(chan int, 1)
				go func() {
					status, _ := urgencyPUT(t, server.Client(), server.URL, caller, `{"urgent":false}`)
					restDone <- status
				}()
				select {
				case <-restEntered:
				case <-ctx.Done():
					t.Fatal("REST did not enter HTTP while native held coordinator")
				}
				close(release)
				require.Equal(t, 200, <-nativeDone)
				require.Equal(t, 200, <-restDone)
				result := checked("composed-racing-native")
				require.NotNil(t, result)
				require.False(t, result.IsError)
				readUrgency(ui, []bool{true, false})
				readUrgency(legacy, []bool{true, false})
				assertProjection(false)
			}
		})
	}
}

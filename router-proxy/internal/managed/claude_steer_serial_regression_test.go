package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The fake CLI sends the actual stdout replay while an HTTP MCP tool holds the
// bridge's persistence lock. echoed is set by readTurn immediately before it
// attempts that lock, so it is a synchronization point, not a timing guess.
func TestClaudeSteerMailConcurrentMCP(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("bridge_failure=%v", fail), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			r, p, _, _ := claudeRuntimeFixture(t, `{"steer_mid_turn":true,"turns":[[{"mcp_call":{"id":"held","name":"scratchpad","input":{}}},{"steer":"answer"}]]}`)
			require.NoError(t, p.db.ReconcileManagedRecipientSources(ctx))
			_, err := p.db.Enqueue(ctx, protocol.Envelope{ID: "serial-mail", From: "sender", To: p.binding.AgentID, Body: "steered", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
			require.NoError(t, err)
			_, err = p.db.ResolveMessageTarget(ctx, "serial-mail")
			require.NoError(t, err)
			attempt, err := p.db.ReserveEmbeddedMail(ctx, p.binding, "serial-mail")
			require.NoError(t, err)
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			a := &embeddedTurnToolAuthority{persistence: p, beforeToolExecute: func(context.Context, agent.BeforeToolCallContext) error { return nil }}
			bridge := newClaudeCodeTurnBridge(a)
			r.bridge = bridge
			r.toolProducer = bridge.producer
			a.tools = []agent.AgentTool{{Name: "scratchpad", Execute: func(ctx context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return agent.AgentToolResult{}, ctx.Err()
				}
				if fail {
					p.binding.OwnerToken++
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "tool work"}}}, nil
			}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var rpc struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name      string            `json:"name"`
						Arguments json.RawMessage   `json:"arguments"`
						Meta      map[string]string `json:"_meta"`
					} `json:"params"`
				}
				if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				switch rpc.Method {
				case "notifications/initialized":
					w.WriteHeader(http.StatusAccepted)
				case "initialize":
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26"}}`, rpc.ID)
				case "tools/list":
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, rpc.ID)
				case "ping":
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, rpc.ID)
				case "tools/call":
					result, err := bridge.execute(req.Context(), ClaudeCodeMCPCall{ToolUseID: rpc.Params.Meta["claudecode/toolUseId"], Name: rpc.Params.Name, Arguments: rpc.Params.Arguments})
					if err != nil {
						fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32000,"message":"tool failed"}}`, rpc.ID)
					} else {
						fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, rpc.ID, result)
					}
				default:
					http.Error(w, "unexpected method", 400)
				}
			}))
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				server.Close()
			}()
			path := filepath.Join(t.TempDir(), "mcp.json")
			data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"mecha": map[string]any{"type": "http", "url": server.URL, "headers": map[string]string{}}}})
			require.NoError(t, os.WriteFile(path, data, 0600))
			r.config.MCPConfigFile = path
			done := make(chan error, 1)
			go func() { _, err := r.Run(ctx, "initial"); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("tool did not enter")
			}
			outcome, err := r.steerInput(ctx, buildSteerUserMessage(attempt, 1), attempt, "")
			require.NoError(t, err)
			require.Equal(t, claudeSteerAccepted, outcome)
			for {
				r.mu.Lock()
				echoed := false
				for _, st := range r.steers {
					echoed = echoed || st.echoed
				}
				r.mu.Unlock()
				if echoed {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("turn ended before echo: %v", err)
				case <-ctx.Done():
					t.Fatal("stdout echo not reached")
				default:
					runtime.Gosched()
				}
			}
			// Query the actual durable ACK while the bridge still owns serial.
			msg, err := p.db.MessageFor(ctx, "serial-mail")
			require.NoError(t, err)
			require.NotEqual(t, domain.MessageAcknowledged, msg.State)
			close(release)
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("turn did not finish after tool release")
			}
			if fail {
				require.ErrorContains(t, err, "UNKNOWN")
				p.binding.OwnerToken--
			} else {
				require.NoError(t, err)
			}
			msg, err = p.db.MessageFor(ctx, "serial-mail")
			require.NoError(t, err)
			if fail {
				require.NotEqual(t, domain.MessageAcknowledged, msg.State)
			} else {
				require.Equal(t, domain.MessageAcknowledged, msg.State)
			}
			snap, err := p.db.ReopenEmbedded(ctx, p.binding)
			require.NoError(t, err)
			toolIndex, mailIndex := -1, -1
			for i, e := range snap.Entries {
				if e.Kind == "toolResult" {
					toolIndex = i
				}
				if e.Kind == "user" && string(e.Body) != "" && containsSteered(e.Body) {
					mailIndex = i
				}
			}
			if fail {
				require.Equal(t, -1, mailIndex)
			} else {
				require.NotEqual(t, -1, toolIndex)
				require.Greater(t, mailIndex, toolIndex)
			}
		})
	}
}

func containsSteered(body []byte) bool { return strings.Contains(string(body), "steered") }

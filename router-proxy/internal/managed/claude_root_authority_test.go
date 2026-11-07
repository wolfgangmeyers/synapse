package managed

import (
	"context"
	"encoding/json"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestClaudeRootToolIsolationArguments(t *testing.T) {
	args := claudeCodeProcessArgs(ClaudeCodeProcessConfig{SessionID: "session", Model: "sonnet", MCPConfigFile: "/tmp/mcp", RootAuthority: true})
	for _, pair := range [][2]string{{"--tools", ""}} {
		i := slices.Index(args, pair[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
			t.Fatalf("missing isolated argument %q %q: %q", pair[0], pair[1], args)
		}
	}
	if slices.Contains(args, "--setting-sources") {
		t.Fatalf("root must not rely on undocumented empty settings source: %q", args)
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Fatalf("missing strict MCP: %q", args)
	}
	for _, name := range []string{"bash", "read", "edit", "write", InlineToolAllowanceToolName} {
		if !claudeCodeApplicableRoleTool("root", name) {
			t.Fatalf("root missing policy tool %s", name)
		}
	}
	if claudeCodeApplicableRoleTool("worker", "bash") {
		t.Fatal("worker native tool behavior changed")
	}
}

func TestClaudeRootGrantContainsOnlyRoleCatalog(t *testing.T) {
	db, binding := grantWakeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Manager{store: db, runCtx: ctx}
	m.config.SessionRoot = t.TempDir()
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	schema := &ai.Schema{Type: "object"}
	authority := &embeddedTurnToolAuthority{role: "root", baseTools: []agent.AgentTool{{Name: "bash", Parameters: schema}, {Name: "scratchpad", Parameters: schema}}}
	runtime, err := m.buildClaudeCodeRuntime(ctx, store.ManagedAgentRecord{}, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, authority, "root prompt", func(*store.EmbeddedGoalAppliedReceipt) {})
	require.NoError(t, err)
	require.True(t, runtime.config.RootAuthority)
	grant, err := runtime.newGrant()
	require.NoError(t, err)
	defer grant.Revoke()
	data, err := os.ReadFile(grant.MCPConfigFile)
	require.NoError(t, err)
	var cfg struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &cfg))
	server := cfg.Servers["mecha"]
	status, result := mcpRequest(t, server.URL, strings.TrimPrefix(server.Headers["Authorization"], "Bearer "), "tools/list", nil)
	require.Equal(t, http.StatusOK, status)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"bash"`)
	require.Contains(t, string(raw), `"scratchpad"`)
	require.NotContains(t, string(raw), "mecha_worker_terminate")
	grant.Revoke()
	status, _ = mcpRequest(t, server.URL, strings.TrimPrefix(server.Headers["Authorization"], "Bearer "), "tools/list", nil)
	require.Equal(t, http.StatusUnauthorized, status)
}

func TestClaudeRootPromptNamesItsRole(t *testing.T) {
	prompt := buildEmbeddedEngineSystemPrompt("root", "root-id", "/project", true)
	require.Contains(t, prompt, "embedded root")
	require.NotContains(t, prompt, "embedded worker")
	require.Contains(t, prompt, "root-id")
	require.Contains(t, prompt, "/project")
}

func TestPiRootPromptRetainsWorkerIdentity(t *testing.T) {
	prompt := buildEmbeddedEngineSystemPrompt("root", "root-id", "/project", false)
	require.Contains(t, prompt, "embedded worker")
	require.NotContains(t, prompt, "embedded root")
	require.Equal(t, buildEmbeddedWorkerSystemPrompt("root", "root-id", "/project"), prompt)
}

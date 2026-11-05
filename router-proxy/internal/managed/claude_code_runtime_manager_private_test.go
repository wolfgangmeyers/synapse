package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// The wake fixture bypasses managed launch, which normally creates the canonical identity.
func grantWakeFixture(t *testing.T) (*store.Store, store.EmbeddedBinding) {
	t.Helper()
	db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(context.Background(), `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, raw.Close())
	return db, binding
}

func TestClaudeRuntimeGrantUsesPrivateListener(t *testing.T) {
	db, binding := grantWakeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &Manager{store: db, runCtx: ctx}
	m.config.SessionRoot = t.TempDir()
	m.config.RouterURL = "http://127.0.0.1:1" // Must not be used for Claude MCP.
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	authority := &embeddedTurnToolAuthority{tools: []agent.AgentTool{{Name: "scratchpad", Parameters: &ai.Schema{Type: "object"}}}}
	r, err := m.buildClaudeCodeRuntime(ctx, store.ManagedAgentRecord{}, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, authority, "prompt", func(*store.EmbeddedGoalAppliedReceipt) {})
	require.NoError(t, err)
	grant, err := r.newGrant()
	require.NoError(t, err)
	defer grant.Revoke()
	data, err := os.ReadFile(grant.MCPConfigFile)
	require.NoError(t, err)
	var config struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &config))
	server := config.Servers["mecha"]
	url, err := m.ClaudeCodeMCPURL()
	require.NoError(t, err)
	require.Equal(t, url, server.URL)
	token := strings.TrimPrefix(server.Headers["Authorization"], "Bearer ")
	require.NotEqual(t, server.Headers["Authorization"], token)
	status, result := mcpRequest(t, server.URL, token, "tools/list", nil)
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, result, "result")
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(encoded), "scratchpad")
	grant.Revoke()
	status, _ = mcpRequest(t, server.URL, token, "tools/list", nil)
	require.Equal(t, http.StatusUnauthorized, status)
	_, err = os.Stat(grant.MCPConfigFile)
	require.True(t, os.IsNotExist(err))
}

func TestClaudeRuntimeGrantListenerFailureFailsClosed(t *testing.T) {
	db, binding := grantWakeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{store: db, runCtx: ctx}
	m.config.SessionRoot = t.TempDir()
	p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
	r, err := m.buildClaudeCodeRuntime(ctx, store.ManagedAgentRecord{}, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, &embeddedTurnToolAuthority{}, "prompt", func(*store.EmbeddedGoalAppliedReceipt) {})
	require.NoError(t, err)
	cancel()
	grant, err := r.newGrant()
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, grant.MCPConfigFile)
	files, err := filepath.Glob(filepath.Join(m.config.SessionRoot, "claude-processes", "*"))
	require.NoError(t, err)
	require.Empty(t, files)
}

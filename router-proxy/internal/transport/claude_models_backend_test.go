package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

// Real HTTP -> manager -> SQLite, with only the external CLI replaced.
func TestClaudeBackendCatalogAndAdmission(t *testing.T) {
	db, processes, manager, server := newLaunchValidationServer(t, managed.CatalogFromModels(managed.Model{Provider: "claude-code", ID: "sonnet", Available: true}), &launchValidationRPCFactory{})
	ctx := context.Background()
	root := manager.AllowedWorkingDirectoryRoots()[0]
	binary, err := filepath.Abs(filepath.Join("..", "..", "..", "tests", "fixtures", "fake-claude", "claude.mjs"))
	require.NoError(t, err)
	t.Setenv("MECHA_CLAUDE_BIN", binary)
	script := filepath.Join(t.TempDir(), "script.json")
	require.NoError(t, os.WriteFile(script, []byte(`{"auth":{"loggedIn":false}}`), 0600))
	t.Setenv("FAKE_CLAUDE_SCRIPT", script)
	response, err := http.Get(server.URL + "/v1/models")
	require.NoError(t, err)
	defer response.Body.Close()
	var catalog struct {
		Models []managed.Model `json:"models"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&catalog))
	require.Len(t, catalog.Models, 4)
	body := `{"name":"claude-rejected","cwd":` + string(launchValidationJSON(root)) + `,"runtime":"embedded","model":{"provider":"claude-code","id":"sonnet"},"worktree":true,"worktree_branch":"must-not-exist"}`
	status, payload := launchValidationRequest(t, server, "123e4567-e89b-42d3-a456-426614174091", body)
	require.Equal(t, 422, status)
	require.Equal(t, "CLAUDE_UNAVAILABLE", payload["error"].(map[string]any)["code"])
	require.Contains(t, payload["error"].(map[string]any)["message"], "host")
	agents, err := db.ManagedAgents(ctx)
	require.NoError(t, err)
	require.Empty(t, agents)
	require.Zero(t, processes.count())
	// Roots use the same Claude availability preflight; RPC remains restricted.
	t.Setenv("MECHA_CLAUDE_BIN", "/no/such/claude")
	_, err = manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "root-rejected", Cwd: root, Runtime: "embedded", Model: &managed.Model{Provider: "claude-code", ID: "sonnet"}})
	require.ErrorContains(t, err, "Claude CLI unavailable")
	status, payload = launchValidationRequest(t, server, "123e4567-e89b-42d3-a456-426614174092", strings.ReplaceAll(strings.ReplaceAll(body, `"runtime":"embedded"`, `"runtime":"rpc"`), "claude-rejected", "rpc-rejected"))
	require.Equal(t, 422, status)
	require.Equal(t, "INVALID_ENGINE_SELECTION", payload["error"].(map[string]any)["code"])
	req, err := http.NewRequest("PUT", server.URL+"/v1/settings/default-model", strings.NewReader(`{"model":{"provider":"claude-code","id":"sonnet"}}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174093")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, 200, res.StatusCode)
	saved, err := db.GetDefaultModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, "claude-code", saved.Provider)
	require.Equal(t, "sonnet", saved.ID)
}

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func seedTestAgentForToolsHTTP(t *testing.T, s *store.Store, ctx context.Context, agentID string) {
	t.Helper()
	launchRecord := store.ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Tool Agent " + agentID,
		Cwd:              t.TempDir(),
		SelectedModel:    store.ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	op := store.ManagedOperation{
		OperationID:       "op-launch-" + agentID,
		Method:            "POST",
		CanonicalPath:     "/v1/managed-agents",
		RequestID:         "req-launch-" + agentID,
		CanonicalBodyHash: "hash-launch-" + agentID,
		State:             "terminal",
		ResponseStatus:    200,
		CreatedAt:         "2026-09-01T12:00:00Z",
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, launchRecord, op))
}

func TestToolsHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	s, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer s.Close()

	router, err := New(Options{Store: s})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	// 1. Test GET /v1/settings/tools returns 200 and list of tools.
	t.Run("GET /v1/settings/tools", func(t *testing.T) {
		resp, err := http.Get(server.URL + "/v1/settings/tools")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body struct {
			Tools []store.ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.NotEmpty(t, body.Tools)

		var foundBash bool
		for _, td := range body.Tools {
			if td.ToolName == "bash" {
				foundBash = true
				require.True(t, td.Enabled)
				require.Equal(t, "System & Shell", td.Category)
			}
		}
		require.True(t, foundBash)
	})

	// 2. Test PUT /v1/settings/tools updates default tool.
	t.Run("PUT /v1/settings/tools updates tool default", func(t *testing.T) {
		toolName := "bash"
		putBody, err := json.Marshal(map[string]any{
			"tool_name": toolName,
			"enabled":   false,
		})
		require.NoError(t, err)

		req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/tools", bytes.NewReader(putBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var resBody map[string]bool
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&resBody))
		require.True(t, resBody["ok"])

		// Verify GET reflects update
		getResp, err := http.Get(server.URL + "/v1/settings/tools")
		require.NoError(t, err)
		defer getResp.Body.Close()

		var getBody struct {
			Tools []store.ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getBody))
		for _, td := range getBody.Tools {
			if td.ToolName == "bash" {
				require.False(t, td.Enabled)
			}
		}

		// Re-enable bash for subsequent tests
		reEnableBody, _ := json.Marshal(map[string]any{
			"tool_name": "bash",
			"enabled":   true,
		})
		req, _ = http.NewRequest(http.MethodPut, server.URL+"/v1/settings/tools", bytes.NewReader(reEnableBody))
		req.Header.Set("Content-Type", "application/json")
		resp2, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp2.Body.Close()
	})

	// 3. Test PUT /v1/settings/tools updates category defaults.
	t.Run("PUT /v1/settings/tools updates category defaults", func(t *testing.T) {
		putBody, err := json.Marshal(map[string]any{
			"category": "File Operations",
			"enabled":  false,
		})
		require.NoError(t, err)

		req, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/tools", bytes.NewReader(putBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Verify GET reflects category disabled
		getResp, err := http.Get(server.URL + "/v1/settings/tools")
		require.NoError(t, err)
		defer getResp.Body.Close()

		var getBody struct {
			Tools []store.ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getBody))
		for _, td := range getBody.Tools {
			if td.Category == "File Operations" {
				require.False(t, td.Enabled, "tool %s should be disabled", td.ToolName)
			}
		}

		// Restore category defaults
		putBody, _ = json.Marshal(map[string]any{
			"category": "File Operations",
			"enabled":  true,
		})
		req, _ = http.NewRequest(http.MethodPut, server.URL+"/v1/settings/tools", bytes.NewReader(putBody))
		req.Header.Set("Content-Type", "application/json")
		resp2, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp2.Body.Close()
	})

	// 4. Test GET /v1/managed-agents/{id}/tools returns agent tools and workflow_override status.
	t.Run("GET /v1/managed-agents/{id}/tools returns agent tools and workflow_override status", func(t *testing.T) {
		agentID := "agent-tools-test-1"
		seedTestAgentForToolsHTTP(t, s, ctx, agentID)

		// Case A: No active workflow state
		resp, err := http.Get(fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var body agentToolsResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		require.Equal(t, agentID, body.AgentID)
		require.False(t, body.WorkflowOverride)
		require.Empty(t, body.ActiveWorkflowState)
		require.NotEmpty(t, body.Tools)

		for _, tool := range body.Tools {
			require.Equal(t, tool.Enabled, tool.EffectiveEnabled)
		}

		// Case B: With active workflow state that has allowed_tools defined
		wfState, err := s.CreateWorkflowState(ctx, store.WorkflowStateRequest{
			Name:         "Plan State",
			Description:  "Planning state with restricted tools",
			AllowedTools: []string{"read", "write"},
		})
		require.NoError(t, err)

		err = s.SetManagedAgentActiveWorkflowState(ctx, agentID, &wfState.ID)
		require.NoError(t, err)

		respWf, err := http.Get(fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID))
		require.NoError(t, err)
		defer respWf.Body.Close()
		require.Equal(t, http.StatusOK, respWf.StatusCode)

		var bodyWf agentToolsResponse
		require.NoError(t, json.NewDecoder(respWf.Body).Decode(&bodyWf))
		require.Equal(t, agentID, bodyWf.AgentID)
		require.True(t, bodyWf.WorkflowOverride)
		require.Equal(t, "Plan State", bodyWf.ActiveWorkflowState)

		for _, tool := range bodyWf.Tools {
			if tool.ToolName == "read" || tool.ToolName == "write" {
				require.True(t, tool.EffectiveEnabled, "%s should be effective enabled", tool.ToolName)
			} else if tool.ToolName == "mecha_workflow_transition" || tool.ToolName == "mecha_tool_timeout" {
				require.True(t, tool.EffectiveEnabled, "implicit control tool %s should be effective enabled", tool.ToolName)
			} else {
				require.False(t, tool.EffectiveEnabled, "%s should not be effective enabled under workflow override", tool.ToolName)
			}
		}

		// Cleanup workflow state
		_ = s.SetManagedAgentActiveWorkflowState(ctx, agentID, nil)
	})

	// 5. Test PUT /v1/managed-agents/{id}/tools updates agent tool override.
	t.Run("PUT /v1/managed-agents/{id}/tools updates agent tool override", func(t *testing.T) {
		agentID := "agent-tools-test-2"
		seedTestAgentForToolsHTTP(t, s, ctx, agentID)

		// Override single tool
		putBody, err := json.Marshal(map[string]any{
			"tool_name": "bash",
			"enabled":   false,
		})
		require.NoError(t, err)

		req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID), bytes.NewReader(putBody))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var resBody map[string]bool
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&resBody))
		require.True(t, resBody["ok"])

		// Check GET returns bash disabled for this agent
		getResp, err := http.Get(fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID))
		require.NoError(t, err)
		defer getResp.Body.Close()

		var body agentToolsResponse
		require.NoError(t, json.NewDecoder(getResp.Body).Decode(&body))
		for _, tool := range body.Tools {
			if tool.ToolName == "bash" || tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled)
				require.False(t, tool.EffectiveEnabled)
			} else {
				require.True(t, tool.Enabled)
			}
		}

		// Override category for agent
		putCatBody, err := json.Marshal(map[string]any{
			"category": "Subagent Lifecycle",
			"enabled":  false,
		})
		require.NoError(t, err)

		reqCat, err := http.NewRequest(http.MethodPut, fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID), bytes.NewReader(putCatBody))
		require.NoError(t, err)
		reqCat.Header.Set("Content-Type", "application/json")

		respCat, err := http.DefaultClient.Do(reqCat)
		require.NoError(t, err)
		defer respCat.Body.Close()
		require.Equal(t, http.StatusOK, respCat.StatusCode)

		// Check GET returns category disabled
		getRespCat, err := http.Get(fmt.Sprintf("%s/v1/managed-agents/%s/tools", server.URL, agentID))
		require.NoError(t, err)
		defer getRespCat.Body.Close()

		var bodyCat agentToolsResponse
		require.NoError(t, json.NewDecoder(getRespCat.Body).Decode(&bodyCat))
		for _, tool := range bodyCat.Tools {
			if tool.Category == "Subagent Lifecycle" || tool.ToolName == "bash" || tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled)
			} else {
				require.True(t, tool.Enabled)
			}
		}
	})

	// 6. Test 400 Bad Request on empty payload
	t.Run("400 Bad Request on missing tool_name and category", func(t *testing.T) {
		putBody, _ := json.Marshal(map[string]any{
			"enabled": true,
		})
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/tools", bytes.NewReader(putBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestToolsCatalog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	s, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer s.Close()

	router, err := New(Options{Store: s})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/tools/catalog")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Tools      []CatalogToolItem `json:"tools"`
		Categories []string          `json:"categories"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.Tools)
	require.NotEmpty(t, result.Categories)

	var foundBash, foundAdmin, foundAllowance bool
	for _, tool := range result.Tools {
		if tool.ToolName == "bash" {
			foundBash = true
			require.Equal(t, "System & Shell", tool.Category)
			require.False(t, tool.IsAdmin)
			require.True(t, tool.DefaultEnabled)
		}
		if tool.ToolName == "mecha_request_inline_tool_allowance" {
			foundAllowance = true
			require.Equal(t, "System & Shell", tool.Category)
			require.False(t, tool.IsAdmin)
			require.True(t, tool.DefaultEnabled)
			require.Equal(t, "Request a temporary exception for genuinely necessary inline use of a named tool", tool.Description)
		}
		if tool.Category == "Worker Administration" || tool.IsAdmin {
			foundAdmin = true
			require.True(t, tool.IsAdmin)
		}
	}
	require.True(t, foundBash)
	require.True(t, foundAdmin)
	require.True(t, foundAllowance)
}


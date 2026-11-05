package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func setupTestStoreForTools(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), "router.db")
	require.NoError(t, cloneLocalTemplateDB(t, dbPath))
	s, err := Open(ctx, dbPath, Options{Clock: clock, Migrations: []Migration{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx
}

func seedTestAgent(t *testing.T, s *Store, ctx context.Context, agentID string) {
	t.Helper()
	launchRecord := ManagedAgentRecord{
		AgentID:          agentID,
		Name:             "Tool Test Agent " + agentID,
		Cwd:              t.TempDir(),
		SelectedModel:    ManagedModel{Provider: "google", ID: "gemini-3.8-flash"},
		Runtime:          "embedded",
		DesiredState:     "running",
		LifecycleState:   "running",
		ActivityState:    "idle",
		ManifestRevision: 1,
		ManifestHash:     "manifest-sha",
		CreatedAt:        "2026-09-01T12:00:00Z",
		UpdatedAt:        "2026-09-01T12:00:00Z",
	}
	op := ManagedOperation{
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

func TestManagedTools(t *testing.T) {
	s, ctx := setupTestStoreForTools(t)

	t.Run("GetToolDefaults verifies initial seed", func(t *testing.T) {
		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)
		require.Len(t, defaults, len(NativeToolCatalog))

		categoryCounts := make(map[string]int)
		for _, tool := range defaults {
			if tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && !tool.Enabled) {
				require.False(t, tool.Enabled, "admin and workflow management tools should be disabled by default")
			} else {
				require.True(t, tool.Enabled, "all non-admin initial tools should be enabled by default")
			}
			require.Nil(t, tool.MCPServerID, "initial native tools should have nil MCPServerID")
			categoryCounts[tool.Category]++
		}

		require.Equal(t, 3, categoryCounts["File Operations"])
		require.Equal(t, 4, categoryCounts["System & Shell"])
		require.Equal(t, 5, categoryCounts["Subagent Lifecycle"])
		require.Equal(t, 5, categoryCounts["Messaging & Routing"])
		require.Equal(t, 16, categoryCounts["Workflow Management"])
		require.Equal(t, 7, categoryCounts["Knowledge Base & Search"])
		require.Equal(t, 5, categoryCounts["Worker Administration"])
	})

	t.Run("SetToolDefault enables and disables", func(t *testing.T) {
		err := s.SetToolDefault(ctx, "bash", false)
		require.NoError(t, err)

		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)

		var bashFound bool
		for _, tool := range defaults {
			if tool.ToolName == "bash" {
				bashFound = true
				require.False(t, tool.Enabled)
			}
		}
		require.True(t, bashFound)

		// Re-enable
		err = s.SetToolDefault(ctx, "bash", true)
		require.NoError(t, err)

		defaults, err = s.GetToolDefaults(ctx)
		require.NoError(t, err)
		for _, tool := range defaults {
			if tool.ToolName == "bash" {
				require.True(t, tool.Enabled)
			}
		}

		// Non-existent tool returns error
		err = s.SetToolDefault(ctx, "non_existent_tool", false)
		require.Error(t, err)
	})

	t.Run("SetCategoryDefaults updates all tools in category", func(t *testing.T) {
		err := s.SetCategoryDefaults(ctx, "File Operations", false)
		require.NoError(t, err)

		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)

		for _, tool := range defaults {
			if tool.Category == "File Operations" {
				require.False(t, tool.Enabled, "file ops tool %s should be disabled", tool.ToolName)
			} else if tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled, "admin tool %s should remain disabled", tool.ToolName)
			} else {
				require.True(t, tool.Enabled, "other category %s tool %s should remain enabled", tool.Category, tool.ToolName)
			}
		}

		// Re-enable category
		err = s.SetCategoryDefaults(ctx, "File Operations", true)
		require.NoError(t, err)

		defaults, err = s.GetToolDefaults(ctx)
		require.NoError(t, err)
		for _, tool := range defaults {
			if tool.Category == "File Operations" {
				require.True(t, tool.Enabled)
			}
		}
	})

	t.Run("RegisterMCPTools registers and updates mcp tools", func(t *testing.T) {
		serverID := "mcp-server-github"
		serverName := "GitHub MCP"
		toolNames := []string{"gh_issue_create", "gh_issue_list"}

		err := s.RegisterMCPTools(ctx, serverID, serverName, toolNames)
		require.NoError(t, err)

		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)
		require.Len(t, defaults, len(NativeToolCatalog)+2) // native tools + 2

		var foundCount int
		for _, tool := range defaults {
			if tool.ToolName == "gh_issue_create" || tool.ToolName == "gh_issue_list" {
				foundCount++
				require.Equal(t, "MCP: GitHub MCP", tool.Category)
				require.True(t, tool.Enabled)
				require.NotNil(t, tool.MCPServerID)
				require.Equal(t, serverID, *tool.MCPServerID)
			}
		}
		require.Equal(t, 2, foundCount)

		// Re-registering with new server name updates category
		err = s.RegisterMCPTools(ctx, serverID, "GitHub Extended", []string{"gh_issue_create"})
		require.NoError(t, err)

		defaults, err = s.GetToolDefaults(ctx)
		require.NoError(t, err)
		for _, tool := range defaults {
			if tool.ToolName == "gh_issue_create" {
				require.Equal(t, "MCP: GitHub Extended", tool.Category)
			}
		}
	})

	t.Run("GetAgentTools merges defaults with agent overrides", func(t *testing.T) {
		agentID := "agent-tool-test-1"
		seedTestAgent(t, s, ctx, agentID)

		// Initial agent tools should match defaults
		agentTools, err := s.GetAgentTools(ctx, agentID)
		require.NoError(t, err)

		for _, tool := range agentTools {
			if tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled)
			} else {
				require.True(t, tool.Enabled)
			}
		}

		// Override single tool for agent
		err = s.SetAgentTool(ctx, agentID, "bash", false)
		require.NoError(t, err)

		agentTools, err = s.GetAgentTools(ctx, agentID)
		require.NoError(t, err)

		for _, tool := range agentTools {
			if tool.ToolName == "bash" || tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled, "%s should be disabled for this agent", tool.ToolName)
			} else {
				require.True(t, tool.Enabled, "other tool %s should remain enabled", tool.ToolName)
			}
		}

		// Global default should still be enabled
		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)
		for _, tool := range defaults {
			if tool.ToolName == "bash" {
				require.True(t, tool.Enabled, "global default bash should still be enabled")
			}
		}

		// Disable entire category for agent
		err = s.SetAgentCategoryTools(ctx, agentID, "Subagent Lifecycle", false)
		require.NoError(t, err)

		agentTools, err = s.GetAgentTools(ctx, agentID)
		require.NoError(t, err)
		for _, tool := range agentTools {
			if tool.Category == "Subagent Lifecycle" || tool.ToolName == "bash" || tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled)
			} else {
				require.True(t, tool.Enabled)
			}
		}

		// DeleteAgentTools clears agent overrides back to defaults
		err = s.DeleteAgentTools(ctx, agentID)
		require.NoError(t, err)

		agentTools, err = s.GetAgentTools(ctx, agentID)
		require.NoError(t, err)
		for _, tool := range agentTools {
			if tool.Category == "Worker Administration" || (tool.Category == "Workflow Management" && strings.HasPrefix(tool.ToolName, "workflow_")) {
				require.False(t, tool.Enabled)
			} else {
				require.True(t, tool.Enabled, "tool %s should be restored to default enabled", tool.ToolName)
			}
		}
	})

	t.Run("Agent tools validation and non-existent agent errors", func(t *testing.T) {
		var notFound *ManagedAgentNotFoundError
		_, err := s.GetAgentTools(ctx, "non-existent-agent")
		require.ErrorAs(t, err, &notFound)

		err = s.SetAgentTool(ctx, "non-existent-agent", "bash", false)
		require.ErrorAs(t, err, &notFound)

		err = s.SetAgentCategoryTools(ctx, "non-existent-agent", "File Operations", false)
		require.ErrorAs(t, err, &notFound)

		// Empty IDs
		_, err = s.GetAgentTools(ctx, "")
		require.Error(t, err)

		err = s.SetAgentTool(ctx, "", "bash", false)
		require.Error(t, err)

		err = s.SetAgentTool(ctx, "some-agent", "", false)
		require.Error(t, err)

		err = s.SetAgentCategoryTools(ctx, "", "File Operations", false)
		require.Error(t, err)

		err = s.SetAgentCategoryTools(ctx, "some-agent", "", false)
		require.Error(t, err)

		err = s.DeleteAgentTools(ctx, "")
		require.Error(t, err)
	})

	t.Run("Cascade delete when agent is deleted", func(t *testing.T) {
		agentID := "agent-tool-cascade"
		createTestManagedAgent(t, ctx, s, agentID)

		err := s.SetAgentTool(ctx, agentID, "bash", false)
		require.NoError(t, err)

		var count int
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agent_tools WHERE agent_id = ?`, agentID).Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		// Delete agent from managed_agents
		_, err = s.db.ExecContext(ctx, `DELETE FROM managed_agents WHERE agent_id = ?`, agentID)
		require.NoError(t, err)

		// Verify cascade deleted from managed_agent_tools
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agent_tools WHERE agent_id = ?`, agentID).Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 0, count)
	})

	t.Run("SyncNativeToolDefaults syncs all catalog tools including inline allowance", func(t *testing.T) {
		defaults, err := s.GetToolDefaults(ctx)
		require.NoError(t, err)

		foundInlineAllowance := false
		for _, tool := range defaults {
			if tool.ToolName == "mecha_request_inline_tool_allowance" {
				foundInlineAllowance = true
				require.Equal(t, "System & Shell", tool.Category)
				require.True(t, tool.Enabled)
			}
		}
		require.True(t, foundInlineAllowance, "mecha_request_inline_tool_allowance must be present in defaults")

		// Re-running SyncNativeToolDefaults is idempotent
		err = s.SyncNativeToolDefaults(ctx)
		require.NoError(t, err)
	})
}


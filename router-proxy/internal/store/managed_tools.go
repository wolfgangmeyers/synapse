package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ToolDefinition represents a managed tool's metadata and enablement state.
type ToolDefinition struct {
	ToolName    string  `json:"tool_name"`
	Category    string  `json:"category"`
	Enabled     bool    `json:"enabled"`
	MCPServerID *string `json:"mcp_server_id,omitempty"`
}

// GetToolDefaults retrieves all global tool defaults ordered by category and tool_name.
func (s *Store) GetToolDefaults(ctx context.Context) ([]ToolDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tool_name, category, enabled, mcp_server_id
		FROM managed_tool_defaults
		ORDER BY category ASC, tool_name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tools []ToolDefinition
	for rows.Next() {
		var td ToolDefinition
		var enabledInt int
		var mcpServerID sql.NullString
		if err := rows.Scan(&td.ToolName, &td.Category, &enabledInt, &mcpServerID); err != nil {
			return nil, err
		}
		td.Enabled = enabledInt != 0
		if mcpServerID.Valid {
			td.MCPServerID = &mcpServerID.String
		}
		tools = append(tools, td)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []ToolDefinition{}
	}
	return tools, nil
}

// SetToolDefault updates the enabled status of a specific default tool.
func (s *Store) SetToolDefault(ctx context.Context, toolName string, enabled bool) error {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return errors.New("tool_name must not be empty")
	}

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		res, err := s.db.ExecContext(ctx, `
			UPDATE managed_tool_defaults
			SET enabled = ?, updated_at = ?
			WHERE tool_name = ?
		`, enabledInt, now, toolName)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return fmt.Errorf("tool %q not found in defaults", toolName)
		}
		return nil
	})
}

// SetCategoryDefaults updates the enabled status of all default tools in a given category.
func (s *Store) SetCategoryDefaults(ctx context.Context, category string, enabled bool) error {
	category = strings.TrimSpace(category)
	if category == "" {
		return errors.New("category must not be empty")
	}

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `
			UPDATE managed_tool_defaults
			SET enabled = ?, updated_at = ?
			WHERE category = ?
		`, enabledInt, now, category)
		return err
	})
}

// RegisterMCPTools registers a list of tools belonging to an MCP server into managed_tool_defaults.
// Category is set to "MCP: " + serverName, and default enabled = 1.
// If tool already exists, updates category, mcp_server_id, and updated_at (or inserts if absent).
func (s *Store) RegisterMCPTools(ctx context.Context, serverID string, serverName string, toolNames []string) error {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return errors.New("server_id must not be empty")
	}
	category := "MCP: " + strings.TrimSpace(serverName)

	return s.runImmediate(ctx, func() error {
		now := formatTimestamp(s.clock.Now())
		for _, name := range toolNames {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			_, err := s.db.ExecContext(ctx, `
				INSERT INTO managed_tool_defaults (tool_name, category, enabled, mcp_server_id, updated_at)
				VALUES (?, ?, 1, ?, ?)
				ON CONFLICT(tool_name) DO UPDATE SET
					category = excluded.category,
					mcp_server_id = excluded.mcp_server_id,
					updated_at = excluded.updated_at
			`, name, category, serverID, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// GetAgentTools retrieves the tool definitions for a specific agent, merging the global
// defaults with any agent-specific overrides in managed_agent_tools.
func (s *Store) GetAgentTools(ctx context.Context, agentID string) ([]ToolDefinition, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, errors.New("agent_id must not be empty")
	}

	// First verify agent exists
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id = ?`, agentID).Scan(&exists)
	if err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, &ManagedAgentNotFoundError{AgentID: agentID}
	}

	// Query defaults and left join agent overrides
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			d.tool_name,
			d.category,
			COALESCE(a.enabled, d.enabled) AS effective_enabled,
			d.mcp_server_id
		FROM managed_tool_defaults d
		LEFT JOIN managed_agent_tools a ON d.tool_name = a.tool_name AND a.agent_id = ?
		ORDER BY d.category ASC, d.tool_name ASC
	`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tools []ToolDefinition
	for rows.Next() {
		var td ToolDefinition
		var enabledInt int
		var mcpServerID sql.NullString
		if err := rows.Scan(&td.ToolName, &td.Category, &enabledInt, &mcpServerID); err != nil {
			return nil, err
		}
		td.Enabled = enabledInt != 0
		if mcpServerID.Valid {
			td.MCPServerID = &mcpServerID.String
		}
		tools = append(tools, td)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []ToolDefinition{}
	}
	return tools, nil
}

// SetAgentTool sets or updates an override for a specific agent and tool.
func (s *Store) SetAgentTool(ctx context.Context, agentID string, toolName string, enabled bool) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent_id must not be empty")
	}
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return errors.New("tool_name must not be empty")
	}

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	return s.runImmediate(ctx, func() error {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id = ?`, agentID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &ManagedAgentNotFoundError{AgentID: agentID}
		}

		now := formatTimestamp(s.clock.Now())
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO managed_agent_tools (agent_id, tool_name, enabled, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(agent_id, tool_name) DO UPDATE SET
				enabled = excluded.enabled,
				updated_at = excluded.updated_at
		`, agentID, toolName, enabledInt, now)
		return err
	})
}

// SetAgentCategoryTools sets overrides for all tools under a specific category for an agent.
func (s *Store) SetAgentCategoryTools(ctx context.Context, agentID string, category string, enabled bool) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent_id must not be empty")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		return errors.New("category must not be empty")
	}

	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	return s.runImmediate(ctx, func() error {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_agents WHERE agent_id = ?`, agentID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return &ManagedAgentNotFoundError{AgentID: agentID}
		}

		// Find all tools under this category in managed_tool_defaults
		rows, err := s.db.QueryContext(ctx, `SELECT tool_name FROM managed_tool_defaults WHERE category = ?`, category)
		if err != nil {
			return err
		}
		defer rows.Close()

		var toolNames []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			toolNames = append(toolNames, name)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		now := formatTimestamp(s.clock.Now())
		for _, name := range toolNames {
			_, err := s.db.ExecContext(ctx, `
				INSERT INTO managed_agent_tools (agent_id, tool_name, enabled, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(agent_id, tool_name) DO UPDATE SET
					enabled = excluded.enabled,
					updated_at = excluded.updated_at
			`, agentID, name, enabledInt, now)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteAgentTools deletes all agent-level tool overrides for an agent, reverting back to defaults.
func (s *Store) DeleteAgentTools(ctx context.Context, agentID string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent_id must not be empty")
	}

	return s.runImmediate(ctx, func() error {
		_, err := s.db.ExecContext(ctx, `DELETE FROM managed_agent_tools WHERE agent_id = ?`, agentID)
		return err
	})
}

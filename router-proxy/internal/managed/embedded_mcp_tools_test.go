package managed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestMCPToolsAddedToBuildSessionTools(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	// Register a connected MCP server in store
	s := &store.MCPServer{
		ID:       "weather-server",
		Name:     "Weather Service",
		URL:      "http://127.0.0.1:9999/mcp",
		AuthType: "none",
		Status:   "connected",
	}
	err = db.CreateMCPServer(ctx, s)
	require.NoError(t, err)

	// Save active tools for the server
	desc := "Fetch the current weather for a city"
	tools := []store.MCPToolRecord{
		{
			ID:               "weather-server:get_weather",
			ServerID:         "weather-server",
			Name:             "get_weather",
			PrefixedName:     "weather_get_weather",
			Description:      &desc,
			InputSchemaJSON:  `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`,
			DefinitionSHA256: "testsha",
		},
	}
	err = db.SaveMCPTools(ctx, "weather-server", tools)
	require.NoError(t, err)

	mcpMgr := mcp.NewManager(db, []byte("01234567890123456789012345678901"), nil)
	mgr := &EmbeddedManager{
		store: db,
	}
	mgr.SetMCPManager(mcpMgr)

	sessionTools := mgr.buildSessionTools(&SavedAgentSession{})
	require.Len(t, sessionTools, 1)

	tool := sessionTools[0]
	require.Equal(t, "weather_get_weather", tool.Name)
	require.Equal(t, desc, tool.Description)
	require.NotNil(t, tool.Parameters)
	require.Equal(t, "object", tool.Parameters.Type)
	require.Contains(t, tool.Parameters.Properties, "city")
	require.Equal(t, []string{"city"}, tool.Parameters.Required)
}

func TestMCPAgentToolExecutionSuccess(t *testing.T) {
	ctx := context.Background()

	// Mock remote MCP server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "initialize":
			resp := mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {"tools": {}},
					"serverInfo": {"name": "calc-service", "version": "1.0.0"}
				}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/call":
			resp := mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"content": [
						{"type": "text", "text": "Calculation result: 42"}
					],
					"isError": false
				}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "unknown method", http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	s := &store.MCPServer{
		ID:       "calc-server",
		Name:     "calc-server",
		URL:      mockServer.URL,
		AuthType: "none",
		Status:   "connected",
	}
	err = db.CreateMCPServer(ctx, s)
	require.NoError(t, err)

	desc := "Calculates expressions"
	tools := []store.MCPToolRecord{
		{
			ID:               "calc-server:calculate",
			ServerID:         "calc-server",
			Name:             "calculate",
			PrefixedName:     "calc_server_calculate",
			Description:      &desc,
			InputSchemaJSON:  `{"type":"object","properties":{"expr":{"type":"string"}}}`,
			DefinitionSHA256: "calcsha",
		},
	}
	err = db.SaveMCPTools(ctx, "calc-server", tools)
	require.NoError(t, err)

	mcpMgr := mcp.NewManager(db, []byte("01234567890123456789012345678901"), mockServer.Client())
	mgr := &EmbeddedManager{
		store: db,
	}
	mgr.SetMCPManager(mcpMgr)

	sessionTools := mgr.buildSessionTools(&SavedAgentSession{})
	require.Len(t, sessionTools, 1)

	tool := sessionTools[0]
	res, err := tool.Execute(ctx, "call_1", map[string]any{"expr": "6 * 7"}, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)

	textContent, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Equal(t, "Calculation result: 42", textContent.Text)
}

func TestMCPAgentToolExecutionErrorHandling(t *testing.T) {
	ctx := context.Background()

	// Mock server returning internal error
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			resp := mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {},
					"serverInfo": {"name": "err-service"}
				}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		// Return 500 error for tools/call
		http.Error(w, "internal server failure", http.StatusInternalServerError)
	}))
	defer mockServer.Close()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	s := &store.MCPServer{
		ID:       "err-server",
		Name:     "err-server",
		URL:      mockServer.URL,
		AuthType: "none",
		Status:   "connected",
	}
	err = db.CreateMCPServer(ctx, s)
	require.NoError(t, err)

	desc := "Always fails"
	tools := []store.MCPToolRecord{
		{
			ID:               "err-server:fail",
			ServerID:         "err-server",
			Name:             "fail",
			PrefixedName:     "err_server_fail",
			Description:      &desc,
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "errsha",
		},
	}
	err = db.SaveMCPTools(ctx, "err-server", tools)
	require.NoError(t, err)

	mcpMgr := mcp.NewManager(db, []byte("01234567890123456789012345678901"), mockServer.Client())
	mgr := &EmbeddedManager{
		store: db,
	}
	mgr.SetMCPManager(mcpMgr)

	sessionTools := mgr.buildSessionTools(&SavedAgentSession{})
	require.Len(t, sessionTools, 1)

	tool := sessionTools[0]
	res, err := tool.Execute(ctx, "call_err", map[string]any{}, nil)
	// Tool error must be returned cleanly without panicking
	require.Error(t, err)
	require.NotEmpty(t, res.Content)
	text, ok := res.Content[0].(ai.TextContent)
	require.True(t, ok)
	require.Contains(t, text.Text, "Error calling remote tool")
}

func TestMCPAgentToolServerDown(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()

	// Use closed server port
	s := &store.MCPServer{
		ID:       "down-server",
		Name:     "down-server",
		URL:      "http://127.0.0.1:65530/mcp",
		AuthType: "none",
		Status:   "connected",
	}
	err = db.CreateMCPServer(ctx, s)
	require.NoError(t, err)

	desc := "Server down tool"
	tools := []store.MCPToolRecord{
		{
			ID:               "down-server:test",
			ServerID:         "down-server",
			Name:             "test",
			PrefixedName:     "down_server_test",
			Description:      &desc,
			InputSchemaJSON:  `{}`,
			DefinitionSHA256: "downsha",
		},
	}
	err = db.SaveMCPTools(ctx, "down-server", tools)
	require.NoError(t, err)

	mcpMgr := mcp.NewManager(db, []byte("01234567890123456789012345678901"), nil)
	mgr := &EmbeddedManager{
		store: db,
	}
	mgr.SetMCPManager(mcpMgr)

	sessionTools := mgr.buildSessionTools(nil)
	require.Len(t, sessionTools, 1)

	tool := sessionTools[0]
	res, err := tool.Execute(ctx, "call_down", map[string]any{}, nil)
	require.Error(t, err)
	require.NotEmpty(t, res.Content)
}

func TestMCPToolsDisabledWhenManagerNil(t *testing.T) {
	mgr := &EmbeddedManager{}
	require.Nil(t, mgr.MCPManager())

	tools := mgr.buildSessionTools(nil)
	require.Empty(t, tools)
}

func TestMCPAgentToolSchemaFallback(t *testing.T) {
	mgr := &EmbeddedManager{}
	tool := mgr.embeddedMCPTool("srv", "my_tool", "my desc", "invalid json schema")

	require.Equal(t, "my_tool", tool.Name)
	require.Equal(t, "my desc", tool.Description)
	require.NotNil(t, tool.Parameters)
	require.Equal(t, "object", tool.Parameters.Type)
}

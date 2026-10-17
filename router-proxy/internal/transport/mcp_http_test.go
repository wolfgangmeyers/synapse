package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMCPServerTransportCRUD(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	secretKey, err := crypto.DeriveKey(dir)
	require.NoError(t, err)

	mgr := mcp.NewManager(durable, secretKey, http.DefaultClient)

	router, err := New(Options{
		Store:      durable,
		Host:       "127.0.0.1",
		Port:       8765,
		MCPManager: mgr,
	})
	require.NoError(t, err)
	defer router.Close(ctx)

	server := httptest.NewServer(router)
	defer server.Close()

	// 1. GET /v1/settings/mcp-servers - empty list
	resp, err := http.Get(server.URL + "/v1/settings/mcp-servers")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var listResult map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listResult))
	resp.Body.Close()
	servers, ok := listResult["servers"].([]any)
	require.True(t, ok)
	require.Empty(t, servers)

	// 2. Mock MCP Server for tool discovery
	mockMCPServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/mcp", r.URL.Path)
		var rpcReq map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&rpcReq))

		method, _ := rpcReq["method"].(string)
		id := rpcReq["id"]

		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"serverInfo": map[string]any{
						"name":    "mock-mcp-server",
						"version": "1.0.0",
					},
				},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "search",
							"description": "Search records",
							"inputSchema": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"query": map[string]any{"type": "string"},
								},
							},
						},
					},
				},
			})
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	defer mockMCPServer.Close()

	// 3. POST /v1/settings/mcp-servers - create none-auth server
	createPayload := map[string]any{
		"name":      "Test MCP",
		"url":       mockMCPServer.URL + "/mcp",
		"auth_mode": "none",
	}
	bodyBytes, _ := json.Marshal(createPayload)

	postResp, err := http.Post(server.URL+"/v1/settings/mcp-servers", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, postResp.StatusCode)

	var createResult map[string]any
	require.NoError(t, json.NewDecoder(postResp.Body).Decode(&createResult))
	postResp.Body.Close()

	serverObj, ok := createResult["server"].(map[string]any)
	require.True(t, ok)
	serverID, ok := serverObj["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, serverID)
	require.Equal(t, "connected", serverObj["status"])
	require.Equal(t, float64(1), serverObj["tools_count"])

	// 4. Duplicate URL conflict
	dupResp, err := http.Post(server.URL+"/v1/settings/mcp-servers", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, dupResp.StatusCode)
	dupResp.Body.Close()

	// 5. GET /v1/settings/mcp-servers/{id}
	getResp, err := http.Get(server.URL + "/v1/settings/mcp-servers/" + serverID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getResult))
	getResp.Body.Close()

	toolsObj, ok := getResult["tools"].([]any)
	require.True(t, ok)
	require.Len(t, toolsObj, 1)

	// 6. PUT /v1/settings/mcp-servers/{id}
	updatePayload := map[string]any{
		"name": "Updated Test MCP",
	}
	upBytes, _ := json.Marshal(updatePayload)
	putReq, err := http.NewRequest(http.MethodPut, server.URL+"/v1/settings/mcp-servers/"+serverID, bytes.NewReader(upBytes))
	require.NoError(t, err)
	putReq.Header.Set("Content-Type", "application/json")

	putResp, err := http.DefaultClient.Do(putReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putResp.StatusCode)

	var putResult map[string]any
	require.NoError(t, json.NewDecoder(putResp.Body).Decode(&putResult))
	putResp.Body.Close()

	updatedServer := putResult["server"].(map[string]any)
	require.Equal(t, "Updated Test MCP", updatedServer["name"])

	// 7. POST /v1/settings/mcp-servers/{id}/sync
	syncResp, err := http.Post(server.URL+"/v1/settings/mcp-servers/"+serverID+"/sync", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, syncResp.StatusCode)

	var syncResult map[string]any
	require.NoError(t, json.NewDecoder(syncResp.Body).Decode(&syncResult))
	syncResp.Body.Close()
	require.Len(t, syncResult["tools"].([]any), 1)

	// 8. DELETE /v1/settings/mcp-servers/{id}
	delReq, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/settings/mcp-servers/"+serverID, nil)
	require.NoError(t, err)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)
	delResp.Body.Close()

	// Verify not found after delete
	getAfterDel, err := http.Get(server.URL + "/v1/settings/mcp-servers/" + serverID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, getAfterDel.StatusCode)
	getAfterDel.Body.Close()
}

func TestMCPOAuthAndWebSocketFlow(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	secretKey, err := crypto.DeriveKey(dir)
	require.NoError(t, err)

	mgr := mcp.NewManager(durable, secretKey, http.DefaultClient)

	router, err := New(Options{
		Store:      durable,
		Host:       "127.0.0.1",
		Port:       8765,
		MCPManager: mgr,
	})
	require.NoError(t, err)
	defer router.Close(ctx)

	server := httptest.NewServer(router)
	defer server.Close()

	// Set up mock OAuth Auth Server & Token Endpoint & MCP endpoint
	var lastAuthCode string
	var lastResource string
	mockAS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			// Browser redirect flow: query params state, redirect_uri, code_challenge, resource
			query := r.URL.Query()
			state := query.Get("state")
			redirectURI := query.Get("redirect_uri")
			lastResource = query.Get("resource")
			lastAuthCode = "mock-auth-code-12345"

			targetURL, err := url.Parse(redirectURI)
			require.NoError(t, err)
			q := targetURL.Query()
			q.Set("code", lastAuthCode)
			q.Set("state", state)
			targetURL.RawQuery = q.Encode()

			http.Redirect(w, r, targetURL.String(), http.StatusFound)

		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-access-token-999",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"refresh_token": "mock-refresh-token-888",
			})

		case "/revoke":
			w.WriteHeader(http.StatusOK)

		case "/mcp":
			// Authenticated MCP call
			authHdr := r.Header.Get("Authorization")
			require.Equal(t, "Bearer mock-access-token-999", authHdr)

			var rpcReq map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&rpcReq))
			id := rpcReq["id"]
			method, _ := rpcReq["method"].(string)

			w.Header().Set("Content-Type", "application/json")
			if method == "initialize" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo":      map[string]any{"name": "oauth-mcp", "version": "1.0"},
					},
				})
			} else if method == "tools/list" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"result": map[string]any{
						"tools": []map[string]any{
							{"name": "oauth_action", "description": "Secured action", "inputSchema": map[string]any{"type": "object"}},
						},
					},
				})
			}

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockAS.Close()

	// Connect a WebSocket client to verify mcp_server_updated event
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/agents"
	wsConn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Register conversation sync so broadcast messages are delivered
	regPayload := map[string]any{
		"version":           "v1",
		"frame_id":          "browser-reg",
		"type":              "register",
		"agent_id":          "wolfgang",
		"instance_id":       "inst-1",
		"agent_role":        "user_interaction",
		"client_type":       "web",
		"conversation_sync": "v1",
	}
	regBytes, _ := json.Marshal(regPayload)
	require.NoError(t, wsConn.Write(ctx, websocket.MessageText, regBytes))

	// Read registration response
	_, regResp, err := wsConn.Read(ctx)
	require.NoError(t, err)
	var regFrame map[string]any
	require.NoError(t, json.Unmarshal(regResp, &regFrame))
	require.Equal(t, "registered", regFrame["type"])

	// Create OAuth MCP server
	authEndpoint := mockAS.URL + "/authorize"
	tokenEndpoint := mockAS.URL + "/token"
	revEndpoint := mockAS.URL + "/revoke"
	clientID := "client-test-id"

	createPayload := map[string]any{
		"name":                "OAuth MCP Server",
		"url":                 mockAS.URL + "/mcp",
		"auth_mode":           "oauth",
		"client_id":           clientID,
		"auth_endpoint":       authEndpoint,
		"token_endpoint":      tokenEndpoint,
		"revocation_endpoint": revEndpoint,
	}
	bodyBytes, _ := json.Marshal(createPayload)

	postResp, err := http.Post(server.URL+"/v1/settings/mcp-servers", "application/json", bytes.NewReader(bodyBytes))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, postResp.StatusCode)

	var createResult map[string]any
	require.NoError(t, json.NewDecoder(postResp.Body).Decode(&createResult))
	postResp.Body.Close()

	serverObj := createResult["server"].(map[string]any)
	serverID := serverObj["id"].(string)
	require.Equal(t, "needs_auth", serverObj["status"])

	// 1. Call POST /v1/settings/mcp-servers/{id}/auth/start
	authStartResp, err := http.Post(server.URL+"/v1/settings/mcp-servers/"+serverID+"/auth/start", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, authStartResp.StatusCode)

	var authStartResult map[string]any
	require.NoError(t, json.NewDecoder(authStartResp.Body).Decode(&authStartResult))
	authStartResp.Body.Close()

	authURLStr, ok := authStartResult["authorization_url"].(string)
	require.True(t, ok)
	require.NotEmpty(t, authURLStr)
	require.Contains(t, authURLStr, "resource="+url.QueryEscape(mockAS.URL+"/mcp"))

	// 2. Perform the authorization redirect: fetching authURLStr will redirect to /v1/mcp/oauth/callback
	clientNoRedirect := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	authGetResp, err := clientNoRedirect.Get(authURLStr)
	require.NoError(t, err)
	require.Equal(t, mockAS.URL+"/mcp", lastResource)
	require.Equal(t, http.StatusFound, authGetResp.StatusCode)
	redirectLocation := authGetResp.Header.Get("Location")
	authGetResp.Body.Close()

	// 3. Follow redirect to callback
	cbResp, err := http.Get(redirectLocation)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, cbResp.StatusCode)
	require.Equal(t, "text/html; charset=utf-8", cbResp.Header.Get("Content-Type"))

	var htmlBuf bytes.Buffer
	_, _ = htmlBuf.ReadFrom(cbResp.Body)
	cbResp.Body.Close()
	htmlStr := htmlBuf.String()
	require.Contains(t, htmlStr, "mcp_oauth_complete")
	require.Contains(t, htmlStr, serverID)
	require.Contains(t, htmlStr, "Authorization Complete")

	// 4. Verify server is now connected with discovered tools
	getResp, err := http.Get(server.URL + "/v1/settings/mcp-servers/" + serverID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getResult))
	getResp.Body.Close()

	connectedServer := getResult["server"].(map[string]any)
	require.Equal(t, "connected", connectedServer["status"])
	require.Equal(t, float64(1), connectedServer["tools_count"])

	tokenInfo := getResult["token"].(map[string]any)
	require.Equal(t, true, tokenInfo["has_refresh_token"])

	// Verify WebSocket notification was emitted
	// The client should receive mcp_server_updated event
	wsReadCtx, wsReadCancel := context.WithTimeout(ctx, 3*time.Second)
	defer wsReadCancel()
	_, wsMsg, err := wsConn.Read(wsReadCtx)
	require.NoError(t, err)
	var wsFrame map[string]any
	require.NoError(t, json.Unmarshal(wsMsg, &wsFrame))
	require.Equal(t, "mcp_server_updated", wsFrame["type"])

	// 5. Revoke tokens: POST /v1/settings/mcp-servers/{id}/auth/revoke
	revokeResp, err := http.Post(server.URL+"/v1/settings/mcp-servers/"+serverID+"/auth/revoke", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, revokeResp.StatusCode)

	var revokeResult map[string]any
	require.NoError(t, json.NewDecoder(revokeResp.Body).Decode(&revokeResult))
	revokeResp.Body.Close()

	revokedServer := revokeResult["server"].(map[string]any)
	require.Equal(t, "needs_auth", revokedServer["status"])

	// 6. Test callback error query parameter handling
	errCbResp, err := http.Get(server.URL + "/v1/mcp/oauth/callback?error=access_denied&error_description=User+cancelled")
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, errCbResp.StatusCode)
	var errHtmlBuf bytes.Buffer
	_, _ = errHtmlBuf.ReadFrom(errCbResp.Body)
	errCbResp.Body.Close()
	require.Contains(t, errHtmlBuf.String(), "Authorization Failed")
	require.Contains(t, errHtmlBuf.String(), "access_denied: User cancelled")
}

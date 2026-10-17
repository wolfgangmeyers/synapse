package mcp_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/crypto"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func generateTestKey(t *testing.T) []byte {
	key := make([]byte, crypto.KeySize)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func strPtr(s string) *string {
	return &s
}

func TestManagerLifecycleAndAuth(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenTestStore(t)
	key := generateTestKey(t)

	var asServer *httptest.Server
	asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			meta := map[string]any{
				"authorization_endpoint": asServer.URL + "/authorize",
				"token_endpoint":         asServer.URL + "/token",
				"registration_endpoint":  asServer.URL + "/register",
				"revocation_endpoint":    asServer.URL + "/revoke",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(meta)
			return
		}

		if r.URL.Path == "/register" {
			res := mcp.ClientRegistrationResult{
				ClientID:     "dyn-client-123",
				ClientSecret: "dyn-secret-abc",
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(res)
			return
		}

		if r.URL.Path == "/token" {
			grantType := r.Form.Get("grant_type")
			if grantType == "authorization_code" {
				require.Equal(t, "code-123", r.Form.Get("code"))
				require.NotEmpty(t, r.Form.Get("code_verifier"))
				res := mcp.TokenResponse{
					AccessToken:  "initial-token-1",
					RefreshToken: "refresh-token-1",
					TokenType:    "Bearer",
					ExpiresIn:    3600,
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(res)
				return
			}
		}

		if r.URL.Path == "/revoke" {
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	}))
	defer asServer.Close()

	mcpServerMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" || r.URL.Path == "/.well-known/oauth-protected-resource" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_servers": []string{asServer.URL},
			})
			return
		}

		var req mcp.JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" {
			res := mcp.InitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities:    map[string]any{},
				ServerInfo:      mcp.ServerInfo{Name: "test-server"},
			}
			raw, _ := json.Marshal(res)
			_ = json.NewEncoder(w).Encode(mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  raw,
			})
			return
		}

		if req.Method == "tools/list" {
			res := mcp.ListToolsResult{
				Tools: []mcp.MCPTool{
					{
						Name:        "search",
						Description: "search documents",
						InputSchema: json.RawMessage(`{"type":"object"}`),
					},
				},
			}
			raw, _ := json.Marshal(res)
			_ = json.NewEncoder(w).Encode(mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  raw,
			})
			return
		}

		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusOK)
			return
		}

		http.NotFound(w, r)
	}))
	defer mcpServerMock.Close()

	mgr := mcp.NewManager(st, key, http.DefaultClient)

	// 1. AddServer
	srv := &store.MCPServer{
		ID:       "srv-1",
		Name:     "Podclave Server",
		URL:      mcpServerMock.URL + "/mcp",
		AuthType: "oauth",
	}
	added, err := mgr.AddServer(ctx, srv)
	require.NoError(t, err)
	require.Equal(t, "srv-1", added.ID)
	require.Equal(t, asServer.URL+"/authorize", *added.AuthEndpoint)
	require.Equal(t, asServer.URL+"/token", *added.TokenEndpoint)

	// 2. StartAuth
	authURL, err := mgr.StartAuth(ctx, "srv-1", "http://localhost:8765/callback")
	require.NoError(t, err)
	require.Contains(t, authURL, asServer.URL+"/authorize")
	require.Contains(t, authURL, "dyn-client-123")
	require.Contains(t, authURL, "resource="+url.QueryEscape(srv.URL))

	// Extract state from authURL
	parsedURL, _ := http.NewRequest("GET", authURL, nil)
	state := parsedURL.URL.Query().Get("state")
	require.NotEmpty(t, state)

	// 3. CompleteAuth
	completed, err := mgr.CompleteAuth(ctx, state, "code-123")
	require.NoError(t, err)
	require.Equal(t, "connected", completed.Status)
	require.Equal(t, 1, completed.ToolsCount)

	// Verify active tools
	activeTools, err := mgr.ListActiveTools(ctx)
	require.NoError(t, err)
	require.Len(t, activeTools, 1)
	require.Equal(t, "search", activeTools[0].Name)
	require.Equal(t, "podclave_server_search", activeTools[0].PrefixedName)

	// 4. RevokeAuth
	err = mgr.RevokeAuth(ctx, "srv-1")
	require.NoError(t, err)

	updatedServer, err := st.GetMCPServer(ctx, "srv-1")
	require.NoError(t, err)
	require.Equal(t, "needs_auth", updatedServer.Status)

	// Active tools should now be empty since status != 'connected'
	activeTools, err = mgr.ListActiveTools(ctx)
	require.NoError(t, err)
	require.Len(t, activeTools, 0)
}

func TestManagerProactiveRefreshAndCallTool(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenTestStore(t)
	key := generateTestKey(t)

	var tokenRefreshCount int32

	var asServer *httptest.Server
	asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenRefreshCount, 1)
			res := mcp.TokenResponse{
				AccessToken:  "refreshed-token-xyz",
				RefreshToken: "refresh-token-xyz",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(res)
			return
		}
		http.NotFound(w, r)
	}))
	defer asServer.Close()

	mcpServerMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/call" {
			// Require the refreshed token
			authHeader := r.Header.Get("Authorization")
			if authHeader != "Bearer refreshed-token-xyz" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			res := mcp.ToolCallResult{
				Content: []mcp.ToolCallContent{
					{Type: "text", Text: "success from refreshed call"},
				},
			}
			raw, _ := json.Marshal(res)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mcp.JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  raw,
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mcpServerMock.Close()

	mgr := mcp.NewManager(st, key, http.DefaultClient)

	// Create connected server
	srv := &store.MCPServer{
		ID:            "srv-proactive",
		Name:          "Proactive Server",
		URL:           mcpServerMock.URL,
		AuthType:      "oauth",
		ClientID:      strPtr("client-1"),
		TokenEndpoint: strPtr(asServer.URL + "/token"),
		Status:        "connected",
	}
	require.NoError(t, st.CreateMCPServer(ctx, srv))

	// Store token that expires in 10 seconds (< 60s proactive window)
	expTime := time.Now().UTC().Add(10 * time.Second)
	refToken := "old-refresh-token"
	initToken := "old-access-token"
	require.NoError(t, st.SetMCPToken(ctx, &store.MCPToken{
		ServerID:     "srv-proactive",
		AccessToken:  &initToken,
		RefreshToken: &refToken,
		ExpiresAt:    &expTime,
	}, key))

	// CallTool should proactively refresh the token before making the call
	res, err := mgr.CallTool(ctx, "srv-proactive", "test_tool", nil)
	require.NoError(t, err)
	require.Equal(t, 1, int(atomic.LoadInt32(&tokenRefreshCount)))
	require.Equal(t, "success from refreshed call", res.Content[0].Text)
}

func TestManagerReactive401Retry(t *testing.T) {
	ctx := context.Background()
	st, _ := store.OpenTestStore(t)
	key := generateTestKey(t)

	var tokenRefreshCount int32
	var callCount int32

	var asServer *httptest.Server
	asServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/token" {
			atomic.AddInt32(&tokenRefreshCount, 1)
			res := mcp.TokenResponse{
				AccessToken:  "newly-refreshed-token",
				RefreshToken: "refreshed-refresh-token",
				TokenType:    "Bearer",
				ExpiresIn:    3600,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(res)
			return
		}
		http.NotFound(w, r)
	}))
	defer asServer.Close()

	mcpServerMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.Method == "tools/call" {
			count := atomic.AddInt32(&callCount, 1)
			// First call with stale token returns 401
			if count == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			// Second call with newly refreshed token succeeds
			if r.Header.Get("Authorization") == "Bearer newly-refreshed-token" {
				res := mcp.ToolCallResult{
					Content: []mcp.ToolCallContent{
						{Type: "text", Text: "retry succeeded!"},
					},
				}
				raw, _ := json.Marshal(res)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(mcp.JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  raw,
				})
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer mcpServerMock.Close()

	mgr := mcp.NewManager(st, key, http.DefaultClient)

	srv := &store.MCPServer{
		ID:            "srv-retry",
		Name:          "Retry Server",
		URL:           mcpServerMock.URL,
		AuthType:      "oauth",
		ClientID:      strPtr("client-1"),
		TokenEndpoint: strPtr(asServer.URL + "/token"),
		Status:        "connected",
	}
	require.NoError(t, st.CreateMCPServer(ctx, srv))

	// Store token that appears still valid (expires in 2 hours), so proactive refresh won't trigger
	expTime := time.Now().UTC().Add(2 * time.Hour)
	initAccess := "stale-access-token"
	initRefresh := "valid-refresh-token"
	require.NoError(t, st.SetMCPToken(ctx, &store.MCPToken{
		ServerID:     "srv-retry",
		AccessToken:  &initAccess,
		RefreshToken: &initRefresh,
		ExpiresAt:    &expTime,
	}, key))

	// First call will return 401, trigger reactive refresh, and replay successfully
	res, err := mgr.CallTool(ctx, "srv-retry", "test_retry", nil)
	require.NoError(t, err)
	require.Equal(t, 2, int(atomic.LoadInt32(&callCount)))
	require.Equal(t, 1, int(atomic.LoadInt32(&tokenRefreshCount)))
	require.Equal(t, "retry succeeded!", res.Content[0].Text)
}

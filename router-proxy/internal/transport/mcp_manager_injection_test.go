package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestMCPManagerInjectionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name               string
		managed, transport bool
	}{
		{"default", false, false}, {"managed", true, false}, {"transport", false, true}, {"transport overrides managed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
			require.NoError(t, err)
			defer db.Close()
			var managedMCP, transportMCP *mcp.Manager
			if tc.managed {
				managedMCP = mcp.NewManager(db, []byte("12345678901234567890123456789012"), nil)
			}
			if tc.transport {
				transportMCP = mcp.NewManager(db, []byte("abcdefghijklmnopqrstuvwxyz123456"), nil)
			}
			manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), MCPManager: managedMCP})
			require.NoError(t, err)
			defer manager.Close(ctx)
			router, err := New(Options{Store: db, Managed: manager, MCPManager: transportMCP})
			require.NoError(t, err)
			defer router.Close(ctx)
			selected := router.MCPManager()
			require.NotNil(t, selected)
			require.Same(t, selected, manager.MCPManager())
			if tc.transport {
				require.Same(t, transportMCP, selected)
			} else if tc.managed {
				require.Same(t, managedMCP, selected)
			}
		})
	}
}

func TestManagedInjectedMCPManagerRetainsCredentialAccess(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	token := "test-only-access-token"
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{}}})
	}))
	defer server.Close()
	require.NoError(t, db.CreateMCPServer(ctx, &store.MCPServer{ID: "credential-server", Name: "test", URL: server.URL, AuthType: "oauth"}))
	key := []byte("12345678901234567890123456789012")
	require.NoError(t, db.SetMCPToken(ctx, &store.MCPToken{ServerID: "credential-server", AccessToken: &token}, key))
	injected := mcp.NewManager(db, key, server.Client())
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), MCPManager: injected})
	require.NoError(t, err)
	defer manager.Close(ctx)
	router, err := New(Options{Store: db, Managed: manager})
	require.NoError(t, err)
	defer router.Close(ctx)
	_, err = router.MCPManager().CallTool(ctx, "credential-server", "test", nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer "+token, <-seen)
	require.Same(t, injected, manager.MCPManager())
}

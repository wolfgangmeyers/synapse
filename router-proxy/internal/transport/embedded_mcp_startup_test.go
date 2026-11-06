package transport

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/mcp"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMCPExplicitManagerWiring(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t)})
	require.NoError(t, err)
	defer manager.Close(ctx)
	supplied := mcp.NewManager(db, make([]byte, 32), nil)
	router, err := New(Options{Store: db, Managed: manager, MCPManager: supplied})
	require.NoError(t, err)
	defer router.Close(ctx)
	require.Same(t, supplied, router.MCPManager())
	require.Same(t, supplied, manager.MCPManager())
}

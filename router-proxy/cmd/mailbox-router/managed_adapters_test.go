package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHelperCatalogUsesManagedProtectedAuthStore(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	helper := filepath.Join(root, "auth-helper", "index.mjs")
	if _, err := os.Stat(filepath.Join(root, "auth-helper", "node_modules", "@earendil-works", "pi-coding-agent")); err != nil {
		t.Skip("managed helper dependencies are required")
	}
	home := t.TempDir()
	authPath := filepath.Join(home, ".pi", "agent", "auth.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0o700))
	require.NoError(t, os.WriteFile(authPath, []byte(`{"openai":{"type":"api_key","key":"synthetic-test-key"}}`), 0o600))
	catalog, err := (helperCatalog{Path: helper, AuthPath: authPath}).Catalog(context.Background())
	require.NoError(t, err)
	found := false
	for _, model := range catalog.Models {
		if model.Provider == "openai" && model.ID == "gpt-5.6-terra" {
			found = true
		}
		require.NotContains(t, model.Name, "synthetic-test-key")
	}
	require.True(t, found)
}

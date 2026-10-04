package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemOneConfigLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "router.sqlite")

	first, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)

	// Defaults should be empty/disabled
	cfg, err := first.GetSystemOneConfig(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
	require.Empty(t, cfg.Host)
	require.Zero(t, cfg.Port)

	// Save valid configuration
	err = first.SetSystemOneConfig(ctx, SystemOneConfig{
		Host:    "100.115.63.19",
		Port:    11434,
		Enabled: true,
	})
	require.NoError(t, err)

	// Verify updated
	cfg, err = first.GetSystemOneConfig(ctx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, "100.115.63.19", cfg.Host)
	require.Equal(t, 11434, cfg.Port)

	// Close and reopen to verify persistence
	require.NoError(t, first.Close())

	second, err := Open(ctx, dbPath, Options{})
	require.NoError(t, err)
	defer second.Close()

	cfg, err = second.GetSystemOneConfig(ctx)
	require.NoError(t, err)
	require.True(t, cfg.Enabled)
	require.Equal(t, "100.115.63.19", cfg.Host)
	require.Equal(t, 11434, cfg.Port)

	// Update to disabled
	err = second.SetSystemOneConfig(ctx, SystemOneConfig{
		Host:    "100.115.63.19",
		Port:    11434,
		Enabled: false,
	})
	require.NoError(t, err)

	cfg, err = second.GetSystemOneConfig(ctx)
	require.NoError(t, err)
	require.False(t, cfg.Enabled)
}

package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedContextThresholds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "router.sqlite")

	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()

	// 1. Initial state defaults
	cfg, err := s.GetContextThresholdConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, DefaultContextWarningPercent, cfg.WarningPercent)
	require.Equal(t, DefaultContextResetPercent, cfg.ResetPercent)
	require.Empty(t, cfg.ModelThresholds)

	// 2. Set valid configuration
	newCfg := ContextThresholdConfig{
		WarningPercent: 15,
		ResetPercent:   25,
		ModelThresholds: map[string]ModelContextThreshold{
			"google-vertex/gemini-3.8-flash": {
				WarningPercent: 12,
				ResetPercent:   22,
			},
			"claude-3-5-sonnet": {
				WarningPercent: 30,
				ResetPercent:   50,
			},
		},
	}
	err = s.SetContextThresholdConfig(ctx, newCfg)
	require.NoError(t, err)

	// 3. Read back
	cfg, err = s.GetContextThresholdConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 15, cfg.WarningPercent)
	require.Equal(t, 25, cfg.ResetPercent)
	require.Len(t, cfg.ModelThresholds, 2)
	require.Equal(t, 12, cfg.ModelThresholds["google-vertex/gemini-3.8-flash"].WarningPercent)
	require.Equal(t, 22, cfg.ModelThresholds["google-vertex/gemini-3.8-flash"].ResetPercent)

	// 4. Persistence across reopen
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()

	cfg, err = reopened.GetContextThresholdConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, 15, cfg.WarningPercent)
	require.Equal(t, 25, cfg.ResetPercent)
	require.Len(t, cfg.ModelThresholds, 2)

	// 5. Validation failure cases
	invalidCfgs := []ContextThresholdConfig{
		{WarningPercent: 0, ResetPercent: 20},
		{WarningPercent: 25, ResetPercent: 25},
		{WarningPercent: 30, ResetPercent: 20},
		{WarningPercent: 10, ResetPercent: 101},
		{
			WarningPercent: 10,
			ResetPercent:   20,
			ModelThresholds: map[string]ModelContextThreshold{
				"bad-model": {WarningPercent: 40, ResetPercent: 30},
			},
		},
	}
	for _, ic := range invalidCfgs {
		require.Error(t, reopened.SetContextThresholdConfig(ctx, ic))
	}
}

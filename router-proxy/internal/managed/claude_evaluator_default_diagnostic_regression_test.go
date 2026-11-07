package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeEvaluatorSelectionDiagnosticForMissingAndUnusableDefault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	m := &Manager{store: db}

	check := func() {
		_, err := m.claudeCodeEvaluatorModel(ctx)
		require.ErrorIs(t, err, errClaudeEvaluatorMissingDefault)
		require.Contains(t, claudeEvaluatorDiagnostic(err), "configure an authenticated router default")
	}
	check()
	require.NoError(t, db.SetDefaultModel(ctx, &store.ManagedModel{Provider: "google", ID: "gemini-2.5-flash"}))
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("HOME", t.TempDir())
	m.config.PiAgentDir = t.TempDir()
	m.config.ManagedHome = t.TempDir()
	check()
}

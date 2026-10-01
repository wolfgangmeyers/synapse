package managed

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestEmbeddedWorkerReadOwnPinnedRootAndEscapeDenial(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	workerDir := filepath.Join(base, "worker")
	parentDir := filepath.Join(base, "parent")
	require.NoError(t, os.Mkdir(workerDir, 0700))
	require.NoError(t, os.Mkdir(parentDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(workerDir, "note.txt"), []byte("worker\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(parentDir, "note.txt"), []byte("parent\n"), 0600))
	require.NoError(t, os.Symlink(filepath.Join(parentDir, "note.txt"), filepath.Join(workerDir, "escape.txt")))
	db, err := store.Open(ctx, filepath.Join(base, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest,
		Config:                Config{SessionRoot: filepath.Join(base, "sessions"), AllowedWorkingDirectoryRoots: []string{workerDir}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)
	_, err = m.Launch(ctx, LaunchRequest{Name: "isolated-worker", Cwd: workerDir, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "a5555555-5555-4555-8555-555555555582"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.embeddedOwners) == 1
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners["isolated-worker"]
	m.mu.Unlock()
	fd, err := pinEmbeddedReadRoot(workerDir)
	require.NoError(t, err)
	defer unix.Close(fd)
	tool := m.embeddedRootReadTool(owner, fd)
	for i, tc := range []struct {
		path string
		want string
	}{
		{"note.txt", "worker\n"},
		{"../parent/note.txt", ""},
		{"escape.txt", ""},
		{filepath.Join(parentDir, "note.txt"), ""},
	} {
		id := "worker-read-" + string(rune('a'+i))
		args := map[string]any{"path": tc.path}
		intent, err := json.Marshal(ai.ToolCall{ID: id, Name: "read", Arguments: args})
		require.NoError(t, err)
		require.NoError(t, db.RecordEmbeddedEffectIntent(ctx, owner.binding, id, intent))
		result, err := tool.Execute(ctx, id, args, nil)
		if tc.want == "" {
			require.Error(t, err, tc.path)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tc.want, result.Content[0].(ai.TextContent).Text)
	}
	stale := owner
	stale.binding.OwnerToken++
	_, err = m.embeddedRootReadTool(stale, fd).Execute(ctx, "worker-read-a", map[string]any{"path": "note.txt"}, nil)
	require.Error(t, err, "stale worker cannot read")
	_, err = tool.Execute(ctx, "worker-read-a", map[string]any{"path": "escape.txt"}, nil)
	require.ErrorContains(t, err, "conflicts with durable intent")
	bytes, err := os.ReadFile(filepath.Join(parentDir, "note.txt"))
	require.NoError(t, err)
	require.Equal(t, "parent\n", string(bytes))
}

package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestEmbeddedRootReadPinnedEscapeDenial(t *testing.T) {
	parent := t.TempDir() // Its parent may be sticky 1777.
	root := filepath.Join(parent, "project")
	require.NoError(t, os.Mkdir(root, 0700))
	outside := filepath.Join(parent, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "note.txt"), []byte("original\n"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
	fd, err := pinEmbeddedReadRoot(root)
	require.NoError(t, err)
	defer unix.Close(fd)
	res, err := readEmbeddedRootFile(context.Background(), fd, "note.txt", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "original\n", res.Content[0].(ai.TextContent).Text)
	for _, attack := range []string{"../outside.txt", outside, "escape.txt", "~/outside.txt", "file:///outside.txt", "a/../note.txt", ".", ""} {
		_, err := readEmbeddedRootFile(context.Background(), fd, attack, nil, nil)
		require.Error(t, err, attack)
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("a", embeddedReadLimit+1)), 0600))
	_, err = readEmbeddedRootFile(context.Background(), fd, "large.txt", nil, nil)
	require.Error(t, err)
	require.NoError(t, os.Chmod(root, 0755))
	fd2, err := pinEmbeddedReadRoot(root)
	require.NoError(t, err)
	unix.Close(fd2)
}

func TestEmbeddedRootReadImages(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fd, err := pinEmbeddedReadRoot(root)
	require.NoError(t, err)
	defer unix.Close(fd)

	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x01}
	jpgBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	webpBytes := []byte("RIFF....WEBP")
	gifBytes := []byte("GIF89a...")
	bmpBytes := []byte("BM....")

	require.NoError(t, os.WriteFile(filepath.Join(root, "test.png"), pngBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test.jpg"), jpgBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test.jpeg"), jpgBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test.webp"), webpBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test.gif"), gifBytes, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "test.bmp"), bmpBytes, 0600))

	// Test readEmbeddedRootFile on PNG
	res, err := readEmbeddedRootFile(ctx, fd, "test.png", nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	img, ok := res.Content[0].(ai.ImageContent)
	require.True(t, ok, "expected ai.ImageContent")
	require.Equal(t, "image/png", img.MimeType)
	require.Equal(t, base64.StdEncoding.EncodeToString(pngBytes), img.Data)

	// Test readEmbeddedRootFile on JPG
	res, err = readEmbeddedRootFile(ctx, fd, "test.jpg", nil, nil)
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	img, ok = res.Content[0].(ai.ImageContent)
	require.True(t, ok, "expected ai.ImageContent")
	require.Equal(t, "image/jpeg", img.MimeType)
	require.Equal(t, base64.StdEncoding.EncodeToString(jpgBytes), img.Data)

	// Test JPEG, WEBP, GIF, BMP MIME types
	for _, tc := range []struct {
		file     string
		wantMime string
		data     []byte
	}{
		{"test.jpeg", "image/jpeg", jpgBytes},
		{"test.webp", "image/webp", webpBytes},
		{"test.gif", "image/gif", gifBytes},
		{"test.bmp", "image/bmp", bmpBytes},
	} {
		r, err := readEmbeddedRootFile(ctx, fd, tc.file, nil, nil)
		require.NoError(t, err, tc.file)
		imgContent := r.Content[0].(ai.ImageContent)
		require.Equal(t, tc.wantMime, imgContent.MimeType)
		require.Equal(t, base64.StdEncoding.EncodeToString(tc.data), imgContent.Data)
	}

	// Test image exceeding 10 MiB limit fails
	oversizedPath := filepath.Join(root, "too_big.png")
	f, err := os.Create(oversizedPath)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(embeddedImageReadLimit+1))
	require.NoError(t, f.Close())
	_, err = readEmbeddedRootFile(ctx, fd, "too_big.png", nil, nil)
	require.Error(t, err, "reading image > 10 MiB must fail")

	// Test embeddedRootReadTool on image
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	m, err := NewManager(ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		Config:                Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t),
	})
	require.NoError(t, err)
	defer m.Close(ctx)

	_, err = m.Launch(ctx, LaunchRequest{Name: "test-worker", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "a6666666-6666-4666-8666-666666666682"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.embeddedOwners) == 1
	}, 4*time.Second, 10*time.Millisecond)
	m.mu.Lock()
	owner := m.embeddedOwners["test-worker"]
	m.mu.Unlock()

	// Record durable intent for read tool
	readID := "tool-read-image-1"
	readArgs := map[string]any{"path": "test.png"}
	intent, err := json.Marshal(ai.ToolCall{ID: readID, Name: "read", Arguments: readArgs})
	require.NoError(t, err)
	require.NoError(t, db.RecordEmbeddedEffectIntent(ctx, owner.binding, readID, intent))

	tool := m.embeddedRootReadTool(owner, fd)
	toolRes, err := tool.Execute(ctx, readID, readArgs, nil)
	require.NoError(t, err)
	require.Len(t, toolRes.Content, 1)
	toolImg, ok := toolRes.Content[0].(ai.ImageContent)
	require.True(t, ok, "tool result should contain ai.ImageContent")
	require.Equal(t, "image/png", toolImg.MimeType)
	require.Equal(t, base64.StdEncoding.EncodeToString(pngBytes), toolImg.Data)

	// Test NewEmbeddedSubagentReadTool on image
	subTool := NewEmbeddedSubagentReadTool(fd)
	subRes, err := subTool.Execute(ctx, "sub-read-1", map[string]any{"path": "test.png"}, nil)
	require.NoError(t, err)
	require.Len(t, subRes.Content, 1)
	subImg, ok := subRes.Content[0].(ai.ImageContent)
	require.True(t, ok, "subagent tool result should contain ai.ImageContent")
	require.Equal(t, "image/png", subImg.MimeType)
	require.Equal(t, base64.StdEncoding.EncodeToString(pngBytes), subImg.Data)
}

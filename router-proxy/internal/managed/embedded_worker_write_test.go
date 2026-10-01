package managed

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestEmbeddedWorkerWriteCreateOnlyAndDenials(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "worker")
	require.NoError(t, os.Mkdir(root, 0700))
	outside := filepath.Join(base, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "exists.txt"), []byte("original"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
	require.NoError(t, os.Symlink(base, filepath.Join(root, "escape-dir")))
	fd, err := pinEmbeddedReadRoot(root)
	require.NoError(t, err)
	defer unix.Close(fd)
	require.NoError(t, createEmbeddedWorkerFile(ctx, fd, "new/nested/created.txt", []byte("before\n")))
	text, err := os.ReadFile(filepath.Join(root, "new/nested/created.txt"))
	require.NoError(t, err)
	require.Equal(t, "before\n", string(text))
	for _, p := range []string{"exists.txt", "new/nested/created.txt", "escape.txt", "escape-dir/out.txt", "../outside.txt", outside, "~/outside.txt", "file:///outside.txt", "new/../exists.txt"} {
		require.Error(t, createEmbeddedWorkerFile(ctx, fd, p, []byte("changed")), p)
	}
	require.Error(t, createEmbeddedWorkerFile(ctx, fd, "oversize", []byte(strings.Repeat("x", embeddedWriteLimit+1))))
	_, err = os.Stat(filepath.Join(root, "oversize"))
	require.ErrorIs(t, err, os.ErrNotExist)
	text, err = os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(text))
	text, err = os.ReadFile(filepath.Join(root, "exists.txt"))
	require.NoError(t, err)
	require.Equal(t, "original", string(text))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, createEmbeddedWorkerFile(cancelled, fd, "cancelled.txt", nil), context.Canceled)
}

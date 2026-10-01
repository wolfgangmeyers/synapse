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

func TestEmbeddedWorkerEditUniqueExistingAndDenials(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	root := filepath.Join(base, "worker")
	require.NoError(t, os.Mkdir(root, 0700))
	outside := filepath.Join(base, "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("untouched"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "one.txt"), []byte("before\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "many.txt"), []byte("before before"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape.txt")))
	require.NoError(t, os.Symlink(base, filepath.Join(root, "escape-dir")))
	fd, err := pinEmbeddedReadRoot(root)
	require.NoError(t, err)
	defer unix.Close(fd)
	require.NoError(t, editEmbeddedWorkerFile(ctx, fd, "one.txt", "before", "after", nil))
	got, err := os.ReadFile(filepath.Join(root, "one.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(got))
	for _, tc := range []struct{ path, old, replacement string }{
		{"one.txt", "missing", "changed"}, {"many.txt", "before", "changed"},
		{"escape.txt", "untouched", "changed"}, {"escape-dir/out.txt", "untouched", "changed"},
		{"../outside.txt", "untouched", "changed"}, {outside, "untouched", "changed"},
		{"one.txt", "", "changed"}, {"one.txt", "after", strings.Repeat("x", embeddedReadLimit+1)},
		{"one.txt", "after", strings.Repeat("x", embeddedReadLimit)},
	} {
		require.Error(t, editEmbeddedWorkerFile(ctx, fd, tc.path, tc.old, tc.replacement, nil), tc.path)
	}
	got, err = os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(got))
	got, err = os.ReadFile(filepath.Join(root, "many.txt"))
	require.NoError(t, err)
	require.Equal(t, "before before", string(got))
	got, err = os.ReadFile(filepath.Join(root, "one.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(got))
	require.Error(t, editEmbeddedWorkerFile(ctx, fd, "one.txt", "after", "changed", func() error { return context.Canceled }))
	require.ErrorContains(t, editEmbeddedWorkerFile(ctx, fd, "one.txt", "after", "changed", func() error {
		return os.WriteFile(filepath.Join(root, "one.txt"), []byte("external"), 0600)
	}), "target changed")
	got, err = os.ReadFile(filepath.Join(root, "one.txt"))
	require.NoError(t, err)
	require.Equal(t, "external", string(got))
	require.NoError(t, os.WriteFile(filepath.Join(root, "one.txt"), []byte("after\n"), 0600))
	got, err = os.ReadFile(filepath.Join(root, "one.txt"))
	require.NoError(t, err)
	require.Equal(t, "after\n", string(got))
}

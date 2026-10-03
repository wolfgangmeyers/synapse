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
	text, err := readEmbeddedRootFile(context.Background(), fd, "note.txt", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "original\n", text)
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

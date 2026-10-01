//go:build embedded_private_status_probe

package managed

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func setupPrivateStatusProbeRetryTest(t *testing.T, m *Manager, base string, second int, failure string) string {
	if second != 403 || failure != "" {
		return ""
	}
	private := filepath.Join(base, "private-probe")
	require.NoError(t, os.Mkdir(private, 0700))
	path := filepath.Join(private, "status.jsonl")
	require.NoError(t, EnablePrivateStatusProbe(m, path))
	return path
}

func verifyPrivateStatusProbeRetryTest(t *testing.T, path string) {
	if path == "" {
		return
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "{\"ordinal\":1,\"status\":401}\n{\"ordinal\":2,\"status\":403}\n", string(content))
	for _, sentinel := range []string{"fake-secret-sentinel", "old-fake-bearer-401", "old-fake-refresh-401", "new-fake-bearer-401", "new-fake-refresh-401", "offline.invalid", "Authorization", "error"} {
		require.NotContains(t, string(content), sentinel)
	}
}

func TestPrivateStatusProbePathAndTransport(t *testing.T) {
	private := filepath.Join(t.TempDir(), "private")
	require.NoError(t, os.Mkdir(private, 0700))
	m := &Manager{}
	path := filepath.Join(private, "status.jsonl")
	require.Error(t, EnablePrivateStatusProbe(m, "relative.jsonl"))
	require.Nil(t, m.embeddedStatusObserver)
	public := filepath.Join(t.TempDir(), "public")
	require.NoError(t, os.Mkdir(public, 0755))
	require.NoError(t, os.Chmod(public, 0755)) // umask 077 must not turn the unsafe fixture private.
	publicInfo, err := os.Stat(public)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), publicInfo.Mode().Perm())
	require.Error(t, EnablePrivateStatusProbe(m, filepath.Join(public, "not-private.jsonl")))
	require.Nil(t, m.embeddedStatusObserver)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	require.Error(t, EnablePrivateStatusProbe(m, path))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(private, filepath.Join(filepath.Dir(private), "link")))
	require.Error(t, EnablePrivateStatusProbe(m, filepath.Join(filepath.Dir(private), "link", "trace")))
	require.NoError(t, EnablePrivateStatusProbe(m, path))
	m.embeddedStatusObserver(embeddedTransportTrace{Ordinal: 1, Category: embeddedTransportCategory(&net.DNSError{Err: "secret-sentinel", Name: "secret-sentinel"})})
	m.embeddedStatusObserver(embeddedTransportTrace{Ordinal: 2, Category: embeddedTransportCategory(errors.Join(syscall.ECONNRESET, errors.New("secret-sentinel")))})
	m.embeddedStatusObserver(embeddedTransportTrace{Ordinal: 3, Category: embeddedTransportCategory(errors.New("secret-sentinel"))})
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "{\"ordinal\":1,\"transport_class\":\"dns\"}\n{\"ordinal\":2,\"transport_class\":\"connection\"}\n{\"ordinal\":3,\"transport_class\":\"other\"}\n", string(content))
	require.False(t, strings.Contains(string(content), "secret-sentinel"))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

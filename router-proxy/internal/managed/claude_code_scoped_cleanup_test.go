package managed

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type scopedCloser struct{}

func (scopedCloser) Write(p []byte) (int, error) { return len(p), nil }
func (scopedCloser) Close() error                { return nil }

var _ io.WriteCloser = scopedCloser{}

func TestClaudeCloseUnknownOwnershipIsBoundedAndFenced(t *testing.T) {
	fenced := make(chan struct{}, 1)
	path := filepath.Join(t.TempDir(), "record")
	require.NoError(t, os.WriteFile(path, []byte("recovery authority"), 0600))
	p := &ClaudeCodeProcess{orphanPath: path, stdin: scopedCloser{}, orphanIdentity: ProcessIdentity{PID: -1}, done: make(chan struct{}), exited: make(chan struct{}), closeDone: make(chan struct{}), onUnconfirmedStop: func() { fenced <- struct{}{} }}
	start := time.Now()
	err := p.Close()
	require.Error(t, err)
	require.Less(t, time.Since(start), 10*time.Second)
	select {
	case <-fenced:
	default:
		t.Fatal("admission not fenced")
	}
	require.Error(t, p.Close())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "recovery authority", string(data))
}

func TestClaudePinnedConfirmationRemovesOnlyMatchingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record")
	id := ProcessIdentity{PID: 123, StartMarker: "start", Incarnation: "original"}
	write := func(identity ProcessIdentity) {
		b, err := json.Marshal(claudeCodeOrphanRecord{Version: 1, Boot: "boot", Identity: identity})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, b, 0600))
	}
	write(id)
	p := &ClaudeCodeProcess{orphanPath: path, orphanIdentity: id}
	p.forgetClaudeOrphanConfirmed()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err))
	replacement := id
	replacement.Incarnation = "replacement"
	write(replacement)
	p.forgetClaudeOrphanConfirmed()
	_, err = os.Stat(path)
	require.NoError(t, err)
}

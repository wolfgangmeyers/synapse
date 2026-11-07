package managed

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeChildConfigFencePreventsRelease(t *testing.T) {
	var m Manager
	cfg := claudeTestConfig(t, `{"turns":[]}`)
	m.fenceClaudeChildConfig(&cfg)
	released := false
	cfg.writeRelease = func(f *os.File) (int, error) { released = true; return f.Write([]byte("ready\n")) }
	cfg.BeforeRelease = func() error { m.claudeCodeProcesses.fence(); return nil }
	proc, err := StartClaudeCodeProcess(context.Background(), cfg)
	if proc != nil {
		defer proc.Close()
	}
	require.Error(t, err)
	require.False(t, released)
}

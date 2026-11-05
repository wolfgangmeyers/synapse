package managed

import (
	"context"
	"errors"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestClaudeInitialProbeConfirmedCleanupFallback(t *testing.T) {
	for _, tc := range []struct {
		started  bool
		stderr   string
		fallback bool
	}{
		{false, "", false},
		{true, "other failure", false},
		{true, "No conversation found with session ID: missing", true},
	} {
		done := closedChannel()
		proc := &ClaudeCodeProcess{stdin: poolTestStdin{}, done: done, exited: done, closeDone: make(chan struct{}), stderr: []byte(tc.stderr)}
		b := store.EmbeddedBinding{AgentID: "a"}
		revoked := 0
		pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{"a": {binding: b, process: proc, revoke: func() { revoked++ }}}}
		r := &claudeCodeRuntime{pool: pool, p: &embeddedPersistence{binding: b}}
		probeErr := errors.New("probe failed")
		fallback, err := r.failedInitialProbe(proc, tc.started, probeErr)
		require.Equal(t, tc.fallback, fallback)
		require.ErrorIs(t, err, probeErr)
		require.Equal(t, 1, revoked)
		require.Empty(t, pool.entries)
		require.NoError(t, r.closeScoped())
	}
}

func TestClaudeInitialProbeUnconfirmedCleanup(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "missing-resume"}[resumed], func(t *testing.T) {
			done := make(chan struct{})
			close(done)
			p := &ClaudeCodeProcess{stdin: poolTestStdin{}, done: done, exited: done, closeDone: make(chan struct{}), waitErr: errClaudeCodeGroupUnconfirmed}
			if resumed {
				p.stderr = []byte("No conversation found with session ID: missing")
			}
			b := store.EmbeddedBinding{AgentID: "probe-agent"}
			revoked := 0
			entry := &claudeCodeProcessEntry{binding: b, process: p, revoke: func() { revoked++ }}
			otherRevoked := 0
			other := &claudeCodeProcessEntry{revoke: func() { otherRevoked++ }}
			pool := &claudeCodeProcessPool{entries: map[string]*claudeCodeProcessEntry{b.AgentID: entry, "other": other}}
			r := &claudeCodeRuntime{pool: pool, p: &embeddedPersistence{binding: b}}
			probeErr := errors.New("probe failed")
			fallback, err := r.failedInitialProbe(p, resumed, probeErr)
			require.False(t, fallback)
			require.ErrorIs(t, err, probeErr)
			require.ErrorIs(t, err, errClaudeCodeGroupUnconfirmed)
			require.Equal(t, 1, revoked)
			require.Zero(t, otherRevoked)
			require.Same(t, other, pool.entries["other"])
			require.Same(t, entry, pool.entries[b.AgentID])
			require.True(t, pool.closed)
			_, acquireErr := pool.acquire(context.Background(), b, ClaudeCodeProcessConfig{}, func() (claudeCodeProcessGrant, error) {
				t.Fatal("fenced pool issued a new grant")
				return claudeCodeProcessGrant{}, nil
			})
			require.Error(t, acquireErr)
			require.ErrorIs(t, r.closeScoped(), errClaudeCodeGroupUnconfirmed)
			require.ErrorIs(t, pool.stop(b.AgentID), errClaudeCodeGroupUnconfirmed)
			require.Equal(t, 1, revoked)
		})
	}
}

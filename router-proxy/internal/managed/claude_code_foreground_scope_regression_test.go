package managed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A runtime represents one scoped execution. Closing it must reap before the
// next scoped execution can start, while successive runs share its process.
func TestClaudeForegroundScopeReapsBeforeNextScope(t *testing.T) {
	r, _, pool, _ := claudeRuntimeFixture(t, `{}`)
	_, err := r.Run(context.Background(), "first")
	require.NoError(t, err)
	first := r.process
	require.NotNil(t, first)
	_, err = r.Run(context.Background(), "same scope")
	require.NoError(t, err)
	require.Same(t, first, r.process)
	r.Close()
	select {
	case <-first.Done():
	default:
		t.Fatal("first execution returned before its process was reaped")
	}
	pool.mu.Lock()
	entry := pool.entries[r.p.binding.AgentID]
	pool.mu.Unlock()
	require.Nil(t, entry, "first process remains admitted")
	next, err := newClaudeCodeRuntime(context.Background(), claudeCodeRuntimeOptions{Pool: pool, Config: r.config, Persistence: r.p, BeforeSubmit: r.beforeSubmit, AfterEcho: r.afterEcho})
	require.NoError(t, err)
	defer next.Close()
	_, err = next.Run(context.Background(), "next scope")
	require.NoError(t, err)
	require.NotSame(t, first, next.process)
}

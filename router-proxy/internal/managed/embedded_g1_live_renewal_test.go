package managed

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A real SDK tool turn blocks inside Execute while the Manager renews its
// exact lease. Fixed clock jumps across the *original* expiry only after a
// heartbeat has extended it; B must still be unable to steal the live claim.
func TestEmbeddedG1LiveToolTurnRenewsOwner(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	root := t.TempDir()
	path := filepath.Join(root, "live.db")
	aDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer aDB.Close()
	bDB, err := store.Open(ctx, path, store.Options{Clock: clock})
	require.NoError(t, err)
	defer bDB.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls, executions atomic.Int32
	tool := g1Tool(func(context.Context) (agent.AgentToolResult, error) {
		executions.Add(1)
		close(entered)
		<-release
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "done"}}}, nil
	})
	m := g1Manager(t, aDB, root, tool, &calls)
	defer func() { close(release); require.NoError(t, m.Close(ctx)) }()
	binding := g1Launch(t, m, root)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("SDK did not enter external Execute")
	}
	owned := inspectManagedOwnerBinding(t, path, binding)
	require.NoError(t, clock.Advance(20*time.Second))
	// The real heartbeat runs every ten wall-clock seconds; advance the fixed
	// clock only after persisted renewal, not after a guessed sleep.
	require.Eventually(t, func() bool {
		check, e := sql.Open("sqlite", path)
		if e != nil {
			return false
		}
		defer check.Close()
		var expiry string
		if e = check.QueryRowContext(ctx, `SELECT owner_expires_at FROM embedded_sessions WHERE agent_id=? AND current=1`, binding.AgentID).Scan(&expiry); e != nil {
			return false
		}
		return expiry > clock.Now().Add(11*time.Second).UTC().Format("2006-01-02T15:04:05.000Z") && inspectManagedOwnerBinding(t, path, binding).OwnerToken == owned.OwnerToken
	}, 14*time.Second, 100*time.Millisecond)
	require.NoError(t, clock.Advance(11*time.Second))
	_, err = bDB.AcquireEmbeddedOwner(ctx, binding, "competing-owner", embeddedOwnerTTL)
	require.ErrorContains(t, err, "embedded owner lease held")
	require.EqualValues(t, 1, executions.Load())
}

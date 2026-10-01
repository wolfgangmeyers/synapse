package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedOwnerLeaseExpiryRenewalAndReopen(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "lease.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	binding := embeddedManagedAgent(t, a, "lease-agent")
	require.NoError(t, a.BindEmbeddedSession(ctx, binding, "root"))
	token, err := a.AcquireEmbeddedOwner(ctx, binding, "a", time.Second)
	require.NoError(t, err)
	require.Positive(t, token)
	_, err = b.AcquireEmbeddedOwner(ctx, binding, "b", time.Second)
	require.Error(t, err)
	binding.OwnerToken = token
	require.NoError(t, clock.Advance(500*time.Millisecond))
	require.NoError(t, a.RenewEmbeddedOwner(ctx, binding, "a", time.Second))
	require.NoError(t, clock.Advance(500*time.Millisecond))
	_, err = b.AcquireEmbeddedOwner(ctx, binding, "b", time.Second)
	require.Error(t, err)
	require.NoError(t, clock.Advance(time.Second))
	require.ErrorContains(t, a.RenewEmbeddedOwner(ctx, binding, "a", time.Second), "stale embedded owner")
	next, err := b.AcquireEmbeddedOwner(ctx, binding, "b", time.Second)
	require.NoError(t, err)
	require.Greater(t, next, token)
	require.NoError(t, b.Close())
	reopened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	_, err = reopened.AcquireEmbeddedOwner(ctx, binding, "a", time.Second)
	require.Error(t, err)
	require.NoError(t, clock.Advance(time.Second))
	final, err := reopened.AcquireEmbeddedOwner(ctx, binding, "a", time.Second)
	require.NoError(t, err)
	require.Greater(t, final, next)
}

func TestEmbeddedOwnerSameIdentityTakeoverRejectsStaleRenewAndSDKAppend(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "same-owner.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	base := embeddedManagedAgent(t, a, "same-owner-agent")
	require.NoError(t, a.BindEmbeddedSession(ctx, base, "root"))

	t1, err := a.AcquireEmbeddedOwner(ctx, base, "shared-owner", time.Second)
	require.NoError(t, err)
	require.Positive(t, t1)
	oldBinding := base
	oldBinding.OwnerToken = t1
	require.NoError(t, clock.Advance(time.Second)) // exactly at expiry, not after it
	t2, err := b.AcquireEmbeddedOwner(ctx, base, "shared-owner", time.Minute)
	require.NoError(t, err)
	require.Greater(t, t2, t1)
	newBinding := base
	newBinding.OwnerToken = t2

	type ownerSnapshot struct {
		id      string
		token   int64
		expires string
	}
	readOwner := func() ownerSnapshot {
		t.Helper()
		var snap ownerSnapshot
		require.NoError(t, b.db.QueryRowContext(ctx, `SELECT owner_id,owner_token,owner_expires_at FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, base.AgentID, base.SessionID, base.Epoch, base.Generation).Scan(&snap.id, &snap.token, &snap.expires))
		return snap
	}
	successor := readOwner() // read-only snapshot of B's persisted claim
	require.Equal(t, "shared-owner", successor.id)
	require.Equal(t, t2, successor.token)
	require.Equal(t, formatTimestamp(clock.Now().Add(time.Minute)), successor.expires)
	require.ErrorContains(t, a.RenewEmbeddedOwner(ctx, oldBinding, "shared-owner", time.Minute), "stale embedded owner")
	require.Equal(t, successor, readOwner(), "stale renewal must preserve B's owner ID, token, and expiry")

	message := ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "same-owner takeover"}}}
	require.ErrorContains(t, adapter(a, oldBinding).AppendMessage(ctx, message), "stale embedded owner")
	require.Zero(t, embeddedRedCount(t, b, "embedded_entries"))
	require.Zero(t, embeddedRedCount(t, b, "embedded_append_keys"))
	require.NoError(t, adapter(b, newBinding).AppendMessage(ctx, message))
	snapshot, err := b.ReopenEmbedded(ctx, newBinding)
	require.NoError(t, err)
	require.Len(t, snapshot.Entries, 1)
}

func TestEmbeddedOwnerLeaseConcurrentContendersAndFaultRollback(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "race.db")
	a, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer a.Close()
	b, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer b.Close()
	binding := embeddedManagedAgent(t, a, "race-agent")
	require.NoError(t, a.BindEmbeddedSession(ctx, binding, "root"))
	// An UPDATE trigger aborts the transaction, including its token increment.
	_, err = a.db.Exec(`CREATE TRIGGER lease_fault BEFORE UPDATE OF owner_token ON embedded_sessions BEGIN SELECT RAISE(ABORT,'injected lease fault'); END`)
	require.NoError(t, err)
	_, err = a.AcquireEmbeddedOwner(ctx, binding, "a", time.Second)
	require.ErrorContains(t, err, "injected lease fault")
	var token int64
	require.NoError(t, a.db.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id=?`, binding.AgentID).Scan(&token))
	require.Zero(t, token)
	_, err = a.db.Exec(`DROP TRIGGER lease_fault`)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, db := range []*Store{a, b} {
		wg.Add(1)
		go func(i int, db *Store) {
			defer wg.Done()
			<-start
			_, claimErr := db.AcquireEmbeddedOwner(ctx, binding, string(rune('a'+i)), time.Second)
			results <- claimErr
		}(i, db)
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		}
	}
	require.Equal(t, 1, wins)
	require.NoError(t, a.db.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id=?`, binding.AgentID).Scan(&token))
	require.EqualValues(t, 1, token)
}

package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// An explicit prompt is a checked SDK user, not a mail ACK. Only actual
// provider entry may promote it to an interrupted recovery candidate.
func TestEmbeddedExplicitPromptEnteredColdNotice(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "explicit.db")
	first, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	b := redBind(t, first, "explicit-root", "explicit-session", 1, "explicit-generation", "root")
	user, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "explicit work"}}})
	require.NoError(t, err)
	entry, err := first.AppendEmbeddedEntry(ctx, b, "user", user)
	require.NoError(t, err)
	n, err := first.PrepareEmbeddedInterruptedNotice(ctx, b, entry.Sequence)
	require.NoError(t, err)
	require.Nil(t, n, "saved user does not prove provider entry")
	require.Error(t, first.EnterEmbeddedNonMailProviderTurn(ctx, b, entry.Sequence, "wrong digest"))
	require.NoError(t, first.EnterEmbeddedNonMailProviderTurn(ctx, b, entry.Sequence, entry.Digest))
	require.NoError(t, first.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	cold, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	token, err := cold.AcquireEmbeddedOwner(ctx, b, "cold", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	snap, err := cold.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)
	n, err = cold.PrepareEmbeddedInterruptedNotice(ctx, b, entry.Sequence)
	require.NoError(t, err)
	require.NotNil(t, n)
	require.Equal(t, "pending", n.State)
	require.Contains(t, n.Text, "non-mail")
	require.Zero(t, embeddedRedCount(t, cold, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, cold, "embedded_reply_outbox"))
	_, err = cold.AppendEmbeddedInterruptedNotice(ctx, b, *n, entry.Sequence, entry.Digest, user)
	require.NoError(t, err)
	n, err = cold.PrepareEmbeddedInterruptedNotice(ctx, b, entry.Sequence)
	require.NoError(t, err)
	require.Equal(t, "appended", n.State)
	require.NoError(t, cold.StartEmbeddedInterruptedNotice(ctx, b, *n))
	require.Error(t, cold.StartEmbeddedInterruptedNotice(ctx, b, *n))
	require.NoError(t, cold.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	reopened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer reopened.Close()
	token, err = reopened.AcquireEmbeddedOwner(ctx, b, "third", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	n, err = reopened.PrepareEmbeddedInterruptedNotice(ctx, b, entry.Sequence)
	require.NoError(t, err)
	require.Equal(t, "started", n.State)
	require.Equal(t, 1, embeddedRedCount(t, reopened, "embedded_interrupted_notices"))
	require.Equal(t, 2, embeddedRedCount(t, reopened, "embedded_entries"))
}

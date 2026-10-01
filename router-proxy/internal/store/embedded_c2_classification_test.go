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

func TestEmbeddedC2ClassifiesSavedRolesAcrossIncarnations(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "c2.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	root := redBind(t, s, "c2-root", "root-session", 1, "root-runtime", "root")
	idle := redBind(t, s, "c2-idle", "idle-session", 1, "idle-runtime", "worker")
	active := redBind(t, s, "c2-active", "active-session", 1, "active-runtime", "worker")
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "original work"}}})
	require.NoError(t, err)
	user, err := s.AppendEmbeddedEntry(ctx, active, "user", body)
	require.NoError(t, err)
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, active, user.Sequence, user.Digest))
	first, err := s.MintEmbeddedProcessGeneration(ctx, "first")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	for i, key := range []string{"second", "third"} {
		s, err = Open(ctx, path, Options{Clock: clock})
		require.NoError(t, err)
		old := []EmbeddedBinding{root, idle, active}
		bindings := []*EmbeddedBinding{&root, &idle, &active}
		for _, b := range bindings {
			token, e := s.AcquireEmbeddedOwner(ctx, *b, key, time.Minute)
			require.NoError(t, e)
			b.OwnerToken = token
		}
		gen, e := s.MintEmbeddedProcessGeneration(ctx, key)
		require.NoError(t, e)
		require.Equal(t, first+int64(i)+1, gen)
		again, e := s.MintEmbeddedProcessGeneration(ctx, key)
		require.NoError(t, e)
		require.Equal(t, gen, again)
		for j, b := range bindings {
			decision, e := s.ClassifyEmbeddedRestart(ctx, *b, gen)
			require.NoError(t, e)
			same, e := s.ClassifyEmbeddedRestart(ctx, *b, gen)
			require.NoError(t, e)
			require.Equal(t, decision, same)
			expected := []string{"root-idle", "worker-idle", "interrupted"}[j]
			if i == 0 || j != 2 {
				require.Equal(t, expected, decision.Kind)
			} else {
				require.Equal(t, "neutral", decision.Kind)
			}
			if decision.Kind == "interrupted" {
				require.Equal(t, "appended", decision.Notice.State)
				require.Equal(t, "interrupted:c2-active:active-session:1:1", decision.Notice.ID)
			}
			_, e = s.ClassifyEmbeddedRestart(ctx, old[j], gen)
			require.Error(t, e)
		}
		require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
		require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"))
		if i == 0 {
			require.Equal(t, 2, embeddedRedCount(t, s, "embedded_entries"))
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, active, gen, "interrupted:c2-active:active-session:1:1"))
			require.Error(t, s.ClaimEmbeddedRestartWake(ctx, active, gen, "interrupted:c2-active:active-session:1:1"))
		}
		require.NoError(t, s.Close())
		require.NoError(t, clock.Advance(2*time.Minute))
	}
}

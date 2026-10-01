package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Construct the 076 notice entirely through supported Store APIs.
func restartNoticeFixture(t *testing.T, s *Store, b EmbeddedBinding) EmbeddedInterruptedNotice {
	t.Helper()
	ctx := context.Background()
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "source work"}}})
	require.NoError(t, err)
	source, err := s.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, source.Sequence, source.Digest))
	notice, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, source.Sequence)
	require.NoError(t, err)
	require.NotNil(t, notice)
	_, err = s.AppendEmbeddedInterruptedNotice(ctx, b, *notice, source.Sequence, source.Digest, body)
	require.NoError(t, err)
	notice, err = s.PrepareEmbeddedInterruptedNotice(ctx, b, source.Sequence)
	require.NoError(t, err)
	require.Equal(t, "appended", notice.State)
	return *notice
}

func TestEmbeddedRestartWakeStartedNoticeCannotReplayAfterTakeover(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "started.db")
	old, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	b := redBind(t, old, "started-root", "source-session", 1, "source-runtime", "root")
	notice := restartNoticeFixture(t, old, b)
	require.NoError(t, old.StartEmbeddedInterruptedNotice(ctx, b, notice))
	require.NoError(t, old.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	cold, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer cold.Close()
	token, err := cold.AcquireEmbeddedOwner(ctx, b, "new-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	g, err := cold.MintEmbeddedProcessGeneration(ctx, "new-process")
	require.NoError(t, err)
	before := embeddedRedCount(t, cold, "embedded_entries")
	_, err = cold.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "checked-projection")
	require.Error(t, err, "started notice must not become a new dispatch plan")
	require.Error(t, cold.ClaimEmbeddedRestartWake(ctx, b, g, notice.ID))
	require.Equal(t, before, embeddedRedCount(t, cold, "embedded_entries"), "no duplicate SDK user")
	require.Zero(t, embeddedRedCount(t, cold, "embedded_restart_wake_slots"))
	again, err := cold.PrepareEmbeddedInterruptedNotice(ctx, b, notice.SourceSequence)
	require.NoError(t, err)
	require.Equal(t, "started", again.State)
}

func TestEmbeddedRestartWakeInterruptedClaimOneWinnerAcrossHandles(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	defer s.Close()
	b := redBind(t, s, "claim-root", "claim-session", 1, "claim-runtime", "root")
	notice := restartNoticeFixture(t, s, b)
	g, err := s.MintEmbeddedProcessGeneration(ctx, "claim-process")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "checked-projection")
	require.NoError(t, err)
	other, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer other.Close()
	wrong := b
	wrong.SessionID = "other-session"
	require.Error(t, other.ClaimEmbeddedRestartWake(ctx, wrong, g, notice.ID))
	wrong = b
	wrong.Epoch++
	require.Error(t, other.ClaimEmbeddedRestartWake(ctx, wrong, g, notice.ID))
	wrong = b
	wrong.OwnerToken++
	require.Error(t, other.ClaimEmbeddedRestartWake(ctx, wrong, g, notice.ID))
	require.Error(t, other.ClaimEmbeddedRestartWake(ctx, b, g, "interrupted:claim-root:claim-session:1:999"), "wrong source sequence")
	before, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, notice.SourceSequence)
	require.NoError(t, err)
	require.Equal(t, "appended", before.State)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, handle := range []*Store{s, other} {
		wg.Add(1)
		go func(h *Store) { defer wg.Done(); <-start; results <- h.ClaimEmbeddedRestartWake(ctx, b, g, notice.ID) }(handle)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	require.Equal(t, 1, winners, "only one pre-provider dispatch permitted")
	wake, err := other.EmbeddedRestartWake(ctx, b, g, notice.ID)
	require.NoError(t, err)
	require.Equal(t, "claimed", wake.State)
	state, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, notice.SourceSequence)
	require.NoError(t, err)
	require.Equal(t, "started", state.State, "076 and 080 must commit together")
	require.Equal(t, b.OwnerToken, func() int64 {
		var owner int64
		require.NoError(t, s.db.QueryRowContext(ctx, `SELECT start_owner_token FROM embedded_interrupted_notices WHERE notice_id=? AND session_id=? AND epoch=? AND generation=? AND source_seq=?`, notice.ID, b.SessionID, b.Epoch, b.Generation, notice.SourceSequence).Scan(&owner))
		return owner
	}())
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_entries"), "claim must not append another SDK user")
	require.Error(t, s.StartEmbeddedInterruptedNotice(ctx, b, notice), "legacy 076 start cannot dispatch twice")
}

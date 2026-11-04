package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// A checked final is durable evidence even when the original process dies
// before Complete. Takeover must never reuse the dead process's owner token.
func TestEmbeddedWakeNewOwnerRecoversCheckedFinalWithoutDispatch(t *testing.T) {
	for _, withFinal := range []bool{true, false} {
		name := "no-final"
		if withFinal {
			name = "checked-final"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "takeover.db")
			s, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			b := redBind(t, s, "takeover-root", "session", 1, "runtime", "root")
			old := b
			g, err := s.MintEmbeddedProcessGeneration(ctx, "first-process")
			require.NoError(t, err)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "old-wake", "root-idle", "projection")
			require.NoError(t, err)
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "old-wake"))
			var final EmbeddedEntry
			if withFinal {
				final, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "old-wake", 0, embeddedGenesis, "old-final", wakeFinalFixture(t))
				require.NoError(t, err)
			}
			require.NoError(t, s.Close())
			require.NoError(t, clock.Advance(2*time.Minute))
			s, err = Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			defer s.Close()
			token, err := s.AcquireEmbeddedOwner(ctx, b, "second-process-owner", time.Minute)
			require.NoError(t, err)
			require.Positive(t, token)
			b.OwnerToken = token
			next, err := s.MintEmbeddedProcessGeneration(ctx, "second-process")
			require.NoError(t, err)
			require.Equal(t, g+1, next)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, next, "next-wake", "root-idle", "next-projection")
			require.NoError(t, err, "rotation auto-reconciles dead claimed wake")
			require.Error(t, s.CompleteEmbeddedRestartWake(ctx, old, g, "old-wake", final.Sequence, final.Digest))
			expected := "unknown"
			if withFinal {
				expected = "checked-complete"
			}
			require.Error(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "old-wake"))
			require.Error(t, s.CompleteEmbeddedRestartWake(ctx, old, g, "old-wake", final.Sequence, final.Digest))
			require.Error(t, s.ReconcileEmbeddedRestartWake(ctx, b, g, "old-wake"))
			var count int
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, b.AgentID).Scan(&count))
			require.Equal(t, 2, count)
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='last' AND wake_id='old-wake' AND state=?`, b.AgentID, expected).Scan(&count))
			require.Equal(t, 1, count)
			wantOrigins := 0
			if withFinal {
				wantOrigins = 1
			}
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND slot='last' AND wake_id='old-wake'`, b.AgentID).Scan(&count))
			require.Equal(t, wantOrigins, count)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, next, "old-wake", "root-idle", "projection")
			require.Error(t, err)
			require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id=?`, b.AgentID).Scan(&count))
			require.Equal(t, wantOrigins, count)
		})
	}
}

// Reopen the SQLite store and take ownership as a new router incarnation.
// Claude's persisted final does not prove that its process cleanup succeeded.
func TestEmbeddedWakeRestartClaudeFinalRemainsUnknown(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	b := redBind(t, s, "claude-wake", "session", 1, "runtime", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "first")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "wake", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "wake"))
	require.NoError(t, s.MarkClaudeBindingStarted(ctx, b))
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake", 0, embeddedGenesis, "final", wakeFinalFixture(t))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, clock.Advance(2*time.Minute))
	s, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()
	b.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, b, "new-owner", time.Minute)
	require.NoError(t, err)
	next, err := s.MintEmbeddedProcessGeneration(ctx, "second")
	require.NoError(t, err)
	decision, err := s.ClassifyEmbeddedRestart(ctx, b, next)
	require.NoError(t, err)
	require.True(t, decision.Suppressed)
	var state string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current'`, b.AgentID).Scan(&state))
	require.Equal(t, "unknown", state)
}

func TestEmbeddedWakeReconciliationErrorDoesNotRotateClaim(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "error.db"), Options{})
	require.NoError(t, err)
	defer s.Close()
	b := redBind(t, s, "error-wake", "session", 1, "runtime", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "first")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "wake", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "wake"))
	_, err = s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, "wake", 0, embeddedGenesis, "final", wakeFinalFixture(t))
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE embedded_restart_wake_checked_origins SET wake_id='wrong' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	next, err := s.MintEmbeddedProcessGeneration(ctx, "second")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, next, "next", "root-idle", "projection")
	require.Error(t, err)
	var state string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current'`, b.AgentID).Scan(&state))
	require.Equal(t, "claimed", state)
	_, err = s.ClassifyEmbeddedRestart(ctx, b, next)
	require.Error(t, err)
	require.NoError(t, s.db.QueryRow(`SELECT state FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current'`, b.AgentID).Scan(&state))
	require.Equal(t, "claimed", state)
}

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedRestartWakeInterruptedPlannedTransfersAcrossProcessGenerations(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "transfer.db")
			s, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			defer func() { _ = s.Close() }()
			b := redBind(t, s, "transfer-"+role, "transfer-session", 1, "runtime-one", role)
			notice := restartNoticeFixture(t, s, b)
			var previous int64
			for _, key := range []string{"first-" + role, "second-" + role, "third-" + role} {
				oldOwner := b
				if previous != 0 {
					require.NoError(t, s.Close())
					require.NoError(t, clock.Advance(2*time.Minute))
					s, err = Open(ctx, path, Options{Clock: clock})
					require.NoError(t, err)
					token, err := s.AcquireEmbeddedOwner(ctx, b, key+"-owner", time.Minute)
					require.NoError(t, err)
					b.OwnerToken = token
				}
				g, err := s.MintEmbeddedProcessGeneration(ctx, key)
				require.NoError(t, err)
				if previous != 0 {
					_, err = s.ReserveEmbeddedRestartWake(ctx, oldOwner, g, notice.ID, "interrupted", "checked-projection")
					require.Error(t, err, "old owner cannot transfer the plan")
					_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "different-projection")
					require.Error(t, err, "different checked projection cannot transfer the plan")
				}
				wake, err := s.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "checked-projection")
				require.NoError(t, err)
				require.Equal(t, "planned", wake.State)
				require.Equal(t, g, wake.ProcessGeneration)
				if previous != 0 {
					require.Error(t, s.ClaimEmbeddedRestartWake(ctx, b, previous, notice.ID), "stale generation must not dispatch")
					_, err = s.EmbeddedRestartWake(ctx, b, previous, notice.ID)
					require.Error(t, err)
				}
				var count int
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND wake_id=? AND state='planned' AND process_generation=?`, b.AgentID, notice.ID, g).Scan(&count))
				require.Equal(t, 1, count)
				require.NoError(t, s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=?`, b.AgentID).Scan(&count))
				require.LessOrEqual(t, count, 2)
				require.Equal(t, 2, embeddedRedCount(t, s, "embedded_entries"), "transfer cannot duplicate the SDK user")
				currentNotice, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, notice.SourceSequence)
				require.NoError(t, err)
				require.Equal(t, "appended", currentNotice.State, "transfer cannot start the provider")
				previous = g
			}
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, previous, notice.ID))
			require.Error(t, s.ClaimEmbeddedRestartWake(ctx, b, previous, notice.ID))
			started, err := s.PrepareEmbeddedInterruptedNotice(ctx, b, notice.SourceSequence)
			require.NoError(t, err)
			require.Equal(t, "started", started.State)
			require.Equal(t, 2, embeddedRedCount(t, s, "embedded_entries"))
		})
	}
}

func TestEmbeddedRestartWakeInterruptedTerminalCannotTransfer(t *testing.T) {
	for _, terminal := range []string{"unknown", "checked-complete"} {
		t.Run(terminal, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "terminal.db")
			s, err := Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			defer func() { _ = s.Close() }()
			b := redBind(t, s, "terminal-"+terminal, "session", 1, "runtime", "root")
			notice := restartNoticeFixture(t, s, b)
			g, err := s.MintEmbeddedProcessGeneration(ctx, terminal+"-first")
			require.NoError(t, err)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, notice.ID, "interrupted", "projection")
			require.NoError(t, err)
			require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, notice.ID))
			if terminal == "unknown" {
				require.NoError(t, s.ReconcileEmbeddedRestartWake(ctx, b, g, notice.ID))
			} else {
				seq, digest, err := s.embeddedHead(ctx, b)
				require.NoError(t, err)
				entry, err := s.AppendEmbeddedRestartWakeFinalAt(ctx, b, g, notice.ID, seq, digest, "final-"+terminal, wakeFinalFixture(t))
				require.NoError(t, err)
				require.NoError(t, s.CompleteEmbeddedRestartWake(ctx, b, g, notice.ID, entry.Sequence, entry.Digest))
			}
			require.NoError(t, s.Close())
			require.NoError(t, clock.Advance(2*time.Minute))
			s, err = Open(ctx, path, Options{Clock: clock})
			require.NoError(t, err)
			token, err := s.AcquireEmbeddedOwner(ctx, b, "next-owner", time.Minute)
			require.NoError(t, err)
			b.OwnerToken = token
			next, err := s.MintEmbeddedProcessGeneration(ctx, terminal+"-second")
			require.NoError(t, err)
			_, err = s.ReserveEmbeddedRestartWake(ctx, b, next, notice.ID, "interrupted", "projection")
			require.Error(t, err)
			require.Error(t, s.ClaimEmbeddedRestartWake(ctx, b, next, notice.ID))
			if terminal == "unknown" {
				require.Equal(t, 2, embeddedRedCount(t, s, "embedded_entries"))
			} else {
				require.Equal(t, 3, embeddedRedCount(t, s, "embedded_entries"))
			}
			var state string
			require.NoError(t, s.db.QueryRowContext(ctx, `SELECT state FROM embedded_restart_wake_slots WHERE agent_id=? AND wake_id=?`, b.AgentID, notice.ID).Scan(&state))
			require.Equal(t, terminal, state)
		})
	}
}

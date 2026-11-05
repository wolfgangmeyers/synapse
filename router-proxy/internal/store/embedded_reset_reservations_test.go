package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func resetReservationNext(b EmbeddedBinding) EmbeddedBinding {
	b.SessionID = fmt.Sprintf("reset-session-%d", b.Epoch+1)
	b.Epoch++
	b.OwnerToken = 0
	return b
}

func resetReservationMail(t *testing.T, s *Store, b EmbeddedBinding, id string) *EmbeddedMailAttempt {
	t.Helper()
	e := testEnvelope(id, "equal text")
	e.To = b.AgentID
	e.From = "sender-" + id // Distinct accepted inputs despite equal text (enqueue deduplicates sender/body).
	_, err := s.Enqueue(context.Background(), e)
	require.NoError(t, err)
	return reserveEmbeddedMailTest(t, s, b, id)
}

func resetReservationCount(t *testing.T, s *Store, a *EmbeddedMailAttempt) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE attempt_id=?`, a.AttemptID).Scan(&n))
	return n
}

func TestResetEmbeddedEpochReleasesOnlyUnconsumedReservations(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	// Ordinary and steer submissions share durable reservations, not an in-memory list.
	ordinary := resetReservationMail(t, s, b, "ordinary")
	steer := resetReservationMail(t, s, b, "steer")
	consumed := resetReservationMail(t, s, b, "consumed")
	consumedSteer := resetReservationMail(t, s, b, "consumed-steer")
	checkedOnly := resetReservationMail(t, s, b, "checked-without-ack")
	other := redBind(t, s, "other-agent", "other-session", 1, "other-generation", "worker")
	unrelated := resetReservationMail(t, s, other, "unrelated")
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, consumed.AttemptID, []byte(consumed.Body)))
	payload, err := embeddedMailUser(consumedSteer, s.clock.Now().UnixMilli())
	require.NoError(t, err)
	_, _, err = s.AppendEmbeddedSteerMailAndACK(ctx, b, consumedSteer.AttemptID, payload)
	require.NoError(t, err)
	// Model checked consumption with missing ACK defensively. Its append key is
	// authoritative; equal text in another entry must not protect unrelated mail.
	payload, err = embeddedMailUser(checkedOnly, s.clock.Now().UnixMilli())
	require.NoError(t, err)
	snap := redReopen(t, s, b)
	head := snap.Entries[len(snap.Entries)-1]
	_, err = s.AppendEmbeddedEntryAt(ctx, b, head.Sequence, head.Digest, "embedded-mail:"+checkedOnly.AttemptID, "user", payload)
	require.NoError(t, err)
	beforeOrdinary, err := s.MessageFor(ctx, ordinary.MessageID)
	require.NoError(t, err)
	beforeTarget, err := s.ResolveMessageTarget(ctx, ordinary.MessageID)
	require.NoError(t, err)
	next := resetReservationNext(b)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	for _, a := range []*EmbeddedMailAttempt{ordinary, steer} {
		require.Zero(t, resetReservationCount(t, s, a))
	}
	for _, a := range []*EmbeddedMailAttempt{consumed, consumedSteer, checkedOnly} {
		require.Equal(t, 1, resetReservationCount(t, s, a))
	}
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_acks"))
	require.Equal(t, 3, embeddedRedCount(t, s, "embedded_entries"))
	after, err := s.MessageFor(ctx, ordinary.MessageID)
	require.NoError(t, err)
	require.Equal(t, beforeOrdinary, after)
	target, err := s.ResolveMessageTarget(ctx, ordinary.MessageID)
	require.NoError(t, err)
	require.Equal(t, beforeTarget, target)
	next = claimEmbeddedFixture(t, s, next)
	replacement := reserveEmbeddedMailTest(t, s, next, ordinary.MessageID)
	require.NotEqual(t, ordinary.AttemptID, replacement.AttemptID)
	require.Equal(t, ordinary.MessageIncarnationID, replacement.MessageIncarnationID)
	require.Equal(t, ordinary.SnapshotID, replacement.SnapshotID)
	require.Equal(t, ordinary.AcceptedDigest, replacement.AcceptedDigest)
	// Both a late old callback and a retry of the committed reset leave the new
	// reservation untouched. Supplying the current binding cannot reuse an old ID.
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, ordinary.AttemptID, []byte(ordinary.Body)))
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, next, ordinary.AttemptID, []byte(ordinary.Body)))
	_, _, err = s.AppendEmbeddedSteerMailAndACK(ctx, b, steer.AttemptID, payload)
	require.Error(t, err)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	require.Equal(t, 1, resetReservationCount(t, s, replacement))
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_acks"))
	require.Equal(t, 3, embeddedRedCount(t, s, "embedded_entries"))
	// A subsequent actual reset releases that epoch's unconsumed replacement only.
	third := resetReservationNext(next)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, next, third, "root"))
	require.Zero(t, resetReservationCount(t, s, replacement))
	require.Error(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	require.Equal(t, 1, resetReservationCount(t, s, consumed))
	require.Equal(t, 1, resetReservationCount(t, s, unrelated))
	requireGoalForeignKeys(t, s)
}

func TestResetEmbeddedEpochRejectsStaleOwnerAndBinding(t *testing.T) {
	for _, field := range []string{"owner", "session", "epoch", "generation", "managed"} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "reserved")
			old := b
			switch field {
			case "owner":
				token, err := s.AcquireEmbeddedOwner(ctx, b, "replacement-owner", time.Minute)
				require.NoError(t, err)
				require.NotEqual(t, b.OwnerToken, token)
			case "session":
				old.SessionID = "wrong"
			case "epoch":
				old.Epoch++
			case "generation":
				old.Generation = "wrong"
			case "managed":
				_, err := s.db.Exec(`UPDATE managed_agents SET runtime_generation='replacement' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			}
			next := resetReservationNext(b)
			if old.Epoch >= next.Epoch {
				next.Epoch = old.Epoch + 1
			}
			require.Error(t, s.ResetEmbeddedSessionEpoch(ctx, old, next, "root"))
			require.Equal(t, 1, resetReservationCount(t, s, a))
			var epoch int64
			require.NoError(t, s.db.QueryRow(`SELECT epoch FROM embedded_sessions WHERE agent_id=? AND current=1`, b.AgentID).Scan(&epoch))
			require.Equal(t, b.Epoch, epoch)
		})
	}
}

func TestResetEmbeddedEpochReservationRollback(t *testing.T) {
	for _, fault := range []string{"delete", "insert"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "rollback")
			before, err := s.MessageFor(ctx, a.MessageID)
			require.NoError(t, err)
			if fault == "delete" {
				_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_reset BEFORE DELETE ON embedded_attempts BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
			} else {
				_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_reset BEFORE INSERT ON embedded_sessions BEGIN SELECT RAISE(ABORT,'reset fault'); END`)
			}
			require.NoError(t, err)
			next := resetReservationNext(b)
			require.ErrorContains(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"), "reset fault")
			require.Equal(t, 1, resetReservationCount(t, s, a))
			require.NoError(t, s.embeddedFence(ctx, b))
			var session string
			var epoch int64
			require.NoError(t, s.db.QueryRow(`SELECT pi_session_id,session_epoch FROM managed_agents WHERE agent_id=?`, b.AgentID).Scan(&session, &epoch))
			require.Equal(t, b.SessionID, session)
			require.Equal(t, b.Epoch, epoch)
			require.Equal(t, 1, embeddedRedCount(t, s, "embedded_sessions"))
			after, err := s.MessageFor(ctx, a.MessageID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = s.db.Exec(`DROP TRIGGER fail_reset`)
			require.NoError(t, err)
			require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
			require.Zero(t, resetReservationCount(t, s, a))
		})
	}
}

func TestResetEmbeddedEpochReopenAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	live := resetReservationMail(t, s, b, "live")
	expired := resetReservationMail(t, s, b, "expired")
	_, err := s.db.Exec(`UPDATE messages SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, expired.MessageID)
	require.NoError(t, err)
	before, err := s.MessageFor(ctx, expired.MessageID)
	require.NoError(t, err)
	next := resetReservationNext(b)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	next = claimEmbeddedFixture(t, reopened, next)
	replacement := reserveEmbeddedMailTest(t, reopened, next, live.MessageID)
	require.NotEqual(t, live.AttemptID, replacement.AttemptID)
	_, err = reopened.ReserveEmbeddedMail(ctx, next, expired.MessageID)
	require.Error(t, err)
	after, err := reopened.MessageFor(ctx, expired.MessageID)
	require.NoError(t, err)
	require.Equal(t, before, after, "reset must not resurrect or rewrite expired mail")
	require.Zero(t, resetReservationCount(t, reopened, expired))
	require.Zero(t, embeddedRedCount(t, reopened, "embedded_acks"))
}

func TestResetEmbeddedEpochCrossBindingAppendKeyDoesNotProveConsumption(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	a := resetReservationMail(t, s, b, "cross-binding-key")
	other := redBind(t, s, "other-key-agent", "other-key-session", 1, "other-key-generation", "worker")
	// Public checked append accepts arbitrary global keys. The key alone cannot
	// establish that this attempt was consumed under its reserved binding.
	_, err := s.AppendEmbeddedEntryAt(ctx, other, 0, embeddedGenesis, "embedded-mail:"+a.AttemptID, "user", []byte("foreign checked user"))
	require.NoError(t, err)
	next := resetReservationNext(b)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	require.Zero(t, resetReservationCount(t, s, a))
	require.Len(t, redReopen(t, s, other).Entries, 1)
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	requireGoalForeignKeys(t, s)
}

func TestResetEmbeddedEpochRetryChecksHistoricalOwner(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	next := resetReservationNext(b)
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"))
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"), "exact original arguments retry before acquiring next owner")
	owned := claimEmbeddedFixture(t, s, next)
	replacement := resetReservationMail(t, s, owned, "retry-replacement")
	require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, b, next, "root"), "exact original arguments retry after acquiring next owner")
	stale := b
	stale.OwnerToken++
	require.Error(t, s.ResetEmbeddedSessionEpoch(ctx, stale, next, "root"), "next epoch is not proof that this old owner performed the reset")
	staleNext := owned
	staleNext.OwnerToken++
	require.Error(t, s.ResetEmbeddedSessionEpoch(ctx, b, staleNext, "root"), "positive next-owner token remains fenced")
	wrong := b
	wrong.SessionID = "never-reset"
	require.Error(t, s.ResetEmbeddedSessionEpoch(ctx, wrong, next, "root"))
	require.Equal(t, 1, resetReservationCount(t, s, replacement))
	require.NoError(t, s.embeddedFence(ctx, owned))
}

func TestReleaseClaudeUnconsumedAttemptIsExactAndFenced(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	unread := resetReservationMail(t, s, b, "unread-release")
	consumed := resetReservationMail(t, s, b, "read-release")
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, consumed.AttemptID, []byte(consumed.Body)))
	stale := b
	stale.OwnerToken++
	require.Error(t, s.ReleaseEmbeddedUnconsumedAttempt(ctx, stale, unread.AttemptID))
	require.NoError(t, s.ReleaseEmbeddedUnconsumedAttempt(ctx, b, consumed.AttemptID))
	require.Equal(t, 1, resetReservationCount(t, s, consumed))
	require.NoError(t, s.ReleaseEmbeddedUnconsumedAttempt(ctx, b, unread.AttemptID))
	require.Zero(t, resetReservationCount(t, s, unread))
	replacement := reserveEmbeddedMailTest(t, s, b, unread.MessageID)
	require.NotEqual(t, unread.AttemptID, replacement.AttemptID)
	require.NoError(t, s.ReleaseEmbeddedUnconsumedAttempt(ctx, b, unread.AttemptID))
	require.Equal(t, 1, resetReservationCount(t, s, replacement))
}

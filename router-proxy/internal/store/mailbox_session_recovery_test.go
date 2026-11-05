package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Given an already-retired session persisted before upgrade, preserve its
// reservation without calling the new reset cleanup. This follows redBind's
// canonical legacy fixture: retire the exact old tuple, then bind the successor.
// It does not edit attempts, accepted envelopes, targets or effect evidence.
func mailboxLegacySuccessor(t *testing.T, s *Store, old, next EmbeddedBinding) EmbeddedBinding {
	t.Helper()
	_, err := s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, old.AgentID, old.SessionID, old.Epoch, old.Generation)
	require.NoError(t, err)
	return redBind(t, s, next.AgentID, next.SessionID, next.Epoch, next.Generation, "root")
}

// SCN-MSR-002/004/006/007: normal reset creates a new reservation, while a
// legacy already-retired reservation reconciles with its ID/key retained.
// Both paths exercise actual SQLite, including reopen and once-only ACK replay.
func TestMailboxSessionRecoveryUnstartedReservation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, reopen := range []bool{false, true} {
			name := "reset"
			if reopen {
				name = "reset-and-reopen"
			}
			if legacy {
				name = "legacy-" + name
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				s, old, path := embeddedMailFixture(t)
				first := reserveEmbeddedMailTest(t, s, old, "embedded-mail-g2")
				var originalKey string
				require.NoError(t, s.db.QueryRow(`SELECT append_key FROM embedded_attempts WHERE attempt_id=?`, first.AttemptID).Scan(&originalKey))
				later := testEnvelope("recovery-later", "later accepted body")
				later.To = old.AgentID
				_, err := s.Enqueue(ctx, later)
				require.NoError(t, err)
				target, err := s.ResolveMessageTarget(ctx, later.ID)
				require.NoError(t, err)
				require.NotNil(t, target)
				next := old
				next.SessionID, next.Epoch, next.OwnerToken = "recovered-session", old.Epoch+1, 0
				// Context reset changes the session epoch, not the runtime generation.
				if legacy {
					next = mailboxLegacySuccessor(t, s, old, next)
				} else {
					require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, old, next, "root"))
					next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, next, "recovery-owner", time.Minute)
					require.NoError(t, err)
				}
				if reopen {
					require.NoError(t, s.Close())
					s, err = Open(ctx, path, Options{})
					require.NoError(t, err)
				}
				defer s.Close()
				recovered, err := s.ReserveEmbeddedMail(ctx, next, first.MessageID)
				require.NoError(t, err, "retired epoch must not block the oldest unstarted accepted mail")
				require.NotNil(t, recovered)
				if legacy {
					require.Equal(t, first.AttemptID, recovered.AttemptID, "legacy reconciliation retains attempt identity")
				} else {
					require.NotEqual(t, first.AttemptID, recovered.AttemptID, "normal reset retires the old reservation")
				}
				var recoveredKey string
				require.NoError(t, s.db.QueryRow(`SELECT append_key FROM embedded_attempts WHERE attempt_id=?`, recovered.AttemptID).Scan(&recoveredKey))
				if legacy {
					require.Equal(t, originalKey, recoveredKey, "legacy reconciliation retains the original idempotency key")
				}
				require.Equal(t, first.MessageID, recovered.MessageID)
				require.Equal(t, first.MessageIncarnationID, recovered.MessageIncarnationID)
				require.Equal(t, first.SnapshotID, recovered.SnapshotID)
				require.Equal(t, first.AcceptedDigest, recovered.AcceptedDigest)
				require.Equal(t, first.Sender, recovered.Sender)
				require.Equal(t, first.Body, recovered.Body)
				require.Error(t, s.AppendEmbeddedMailAndACK(ctx, old, first.AttemptID, []byte(first.Body)), "late obsolete owner cannot consume")
				require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
				require.Empty(t, redReopen(t, s, next).Entries, "stale owner must not append")
				require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, next, recovered.AttemptID, []byte(recovered.Body)))
				second := reserveEmbeddedMailTest(t, s, next, later.ID)
				require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, next, second.AttemptID, []byte(second.Body)))
				// Identical committed ACK replay cannot append a second user message.
				require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, next, recovered.AttemptID, []byte(recovered.Body)))
				require.Equal(t, 2, embeddedRedCount(t, s, "embedded_acks"))
				require.Len(t, redReopen(t, s, next).Entries, 2)
				for _, id := range []string{first.MessageID, later.ID} {
					msg, e := s.MessageFor(ctx, id)
					require.NoError(t, e)
					require.Equal(t, "acknowledged", msg.State)
				}
			})
		}
	}
}

// SCN-MSR-005: never relocate cross-runtime or already-started reservations.
func TestMailboxSessionRecoveryFailsClosed(t *testing.T) {
	for _, guard := range []string{"current-old-row", "cross-generation", "committed-ack-and-append", "append-key-without-ack", "accepted-body-changed"} {
		t.Run(guard, func(t *testing.T) {
			ctx := context.Background()
			s, old, _ := embeddedMailFixture(t)
			first := reserveEmbeddedMailTest(t, s, old, "embedded-mail-g2")
			if guard == "committed-ack-and-append" {
				require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, old, first.AttemptID, []byte(first.Body)))
			}
			if guard == "append-key-without-ack" {
				var key string
				require.NoError(t, s.db.QueryRow(`SELECT append_key FROM embedded_attempts WHERE attempt_id=?`, first.AttemptID).Scan(&key))
				_, err := s.AppendEmbeddedEntryAt(ctx, old, 0, "genesis", key, "user", []byte(first.Body))
				require.NoError(t, err)
				require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
			}
			next := old
			next.SessionID, next.Epoch, next.OwnerToken = "guard-successor", old.Epoch+1, 0
			if guard == "cross-generation" {
				next.Generation = "different-runtime-generation"
			}
			next = mailboxLegacySuccessor(t, s, old, next)
			var err error
			if guard == "current-old-row" {
				_, err = s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id=?`, old.AgentID)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE embedded_sessions SET current=1 WHERE agent_id=? AND session_id=? AND epoch=?`, old.AgentID, old.SessionID, old.Epoch)
				require.NoError(t, err)
			}
			if guard == "accepted-body-changed" {
				_, err = s.db.Exec(`UPDATE messages SET body='tampered' WHERE id=?`, first.MessageID)
				require.NoError(t, err)
			}
			var before string
			require.NoError(t, s.db.QueryRow(`SELECT session_id FROM embedded_attempts WHERE attempt_id=?`, first.AttemptID).Scan(&before))
			acks, entries, keys := embeddedRedCount(t, s, "embedded_acks"), embeddedRedCount(t, s, "embedded_entries"), embeddedRedCount(t, s, "embedded_append_keys")
			for range 3 {
				attempt, err := s.ReserveEmbeddedMail(ctx, next, first.MessageID)
				require.True(t, err != nil || attempt == nil, "ineligible reservation must not be relocated")
			}
			var after string
			require.NoError(t, s.db.QueryRow(`SELECT session_id FROM embedded_attempts WHERE attempt_id=?`, first.AttemptID).Scan(&after))
			require.Equal(t, before, after)
			require.Equal(t, acks, embeddedRedCount(t, s, "embedded_acks"))
			require.Equal(t, entries, embeddedRedCount(t, s, "embedded_entries"))
			require.Equal(t, keys, embeddedRedCount(t, s, "embedded_append_keys"))
		})
	}
}

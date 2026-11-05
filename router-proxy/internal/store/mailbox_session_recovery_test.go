package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// SCN-MSR-002/004/006/007: a reservation without admission must not pin
// accepted mail to a retired epoch. Exercise the actual SQLite transaction,
// including reopen, rather than changing the reservation row in the fixture.
func TestMailboxSessionRecoveryUnstartedReservation(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		name := "reset"
		if reopen {
			name = "reset-and-reopen"
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
			require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, old, next, "root"))
			next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, next, "recovery-owner", time.Minute)
			require.NoError(t, err)
			if reopen {
				require.NoError(t, s.Close())
				s, err = Open(ctx, path, Options{})
				require.NoError(t, err)
			}
			defer s.Close()
			recovered, err := s.ReserveEmbeddedMail(ctx, next, first.MessageID)
			require.NoError(t, err, "retired epoch must not block the oldest unstarted accepted mail")
			require.NotNil(t, recovered)
			require.Equal(t, first.AttemptID, recovered.AttemptID, "recovery retains accepted attempt identity")
			var recoveredKey string
			require.NoError(t, s.db.QueryRow(`SELECT append_key FROM embedded_attempts WHERE attempt_id=?`, recovered.AttemptID).Scan(&recoveredKey))
			require.Equal(t, originalKey, recoveredKey, "recovery retains the original idempotency key")
			require.Equal(t, first.Body, recovered.Body)
			require.Error(t, s.AppendEmbeddedMailAndACK(ctx, old, first.AttemptID, []byte(first.Body)), "late obsolete owner cannot consume")
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
				_, err := s.db.Exec(`UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, old.AgentID)
				require.NoError(t, err)
				require.NoError(t, s.ResetEmbeddedSession(ctx, old, next, "root"))
			} else {
				require.NoError(t, s.ResetEmbeddedSessionEpoch(ctx, old, next, "root"))
			}
			var err error
			next.OwnerToken, err = s.AcquireEmbeddedOwner(ctx, next, "guard-owner", time.Minute)
			require.NoError(t, err)
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

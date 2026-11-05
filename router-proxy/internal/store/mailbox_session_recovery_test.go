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

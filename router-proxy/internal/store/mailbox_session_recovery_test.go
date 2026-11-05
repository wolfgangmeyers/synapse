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
			later := testEnvelope("recovery-later", "later accepted body")
			later.To = old.AgentID
			_, err := s.Enqueue(ctx, later)
			require.NoError(t, err)
			target, err := s.ResolveMessageTarget(ctx, later.ID)
			require.NoError(t, err)
			require.NotNil(t, target)
			next := old
			next.SessionID, next.Epoch, next.Generation, next.OwnerToken = "recovered-session", old.Epoch+1, "recovered-generation", 0
			_, err = s.db.ExecContext(ctx, `UPDATE managed_agents SET pi_session_id=?,session_epoch=?,runtime_generation=? WHERE agent_id=?`, next.SessionID, next.Epoch, next.Generation, old.AgentID)
			require.NoError(t, err)
			require.NoError(t, s.ResetEmbeddedSession(ctx, old, next, "root"))
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

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClaudeUnsubmittedSettlementAtomicFencedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	t.Run("rollback", func(t *testing.T) {
		s, b, _ := embeddedMailFixture(t)
		a := resetReservationMail(t, s, b, "settlement-rollback")
		_, err := s.db.Exec(`CREATE TRIGGER fail_dead_letter BEFORE UPDATE OF state ON messages WHEN NEW.state='dead_letter' BEGIN SELECT RAISE(ABORT,'injected'); END`)
		require.NoError(t, err)
		_, err = s.SettleClaudeUnsubmittedMail(ctx, b, a)
		require.Error(t, err)
		msg, err := s.MessageFor(ctx, a.MessageID)
		require.NoError(t, err)
		require.Equal(t, "queued", string(msg.State))
		require.Empty(t, historyRowsFor(t, s, b.AgentID))
		_, err = s.db.Exec(`DROP TRIGGER fail_dead_letter`)
		require.NoError(t, err)
		first, err := s.SettleClaudeUnsubmittedMail(ctx, b, a)
		require.NoError(t, err)
		second, err := s.SettleClaudeUnsubmittedMail(ctx, b, a)
		require.NoError(t, err)
		require.Equal(t, first.ID, second.ID)
		require.Len(t, historyRowsFor(t, s, b.AgentID), 1)
		require.NotNil(t, first.MessageContent)
		require.Contains(t, *first.MessageContent, "retry")
		require.Nil(t, first.SenderIdentity)
	})
	for _, kind := range []string{"owner", "attempt", "incarnation", "held", "sender"} {
		t.Run(kind, func(t *testing.T) {
			s, b, _ := embeddedMailFixture(t)
			a := resetReservationMail(t, s, b, "settlement-"+kind)
			switch kind {
			case "owner":
				b.OwnerToken++
			case "attempt":
				a.AttemptID = "stale"
			case "incarnation":
				a.MessageIncarnationID = "stale"
			case "held":
				require.NoError(t, s.MarkClaudeMailWriteStarted(ctx, b, a.AttemptID))
			case "sender":
				_, err := s.db.Exec(`UPDATE messages SET sender='replacement' WHERE id=?`, a.MessageID)
				require.NoError(t, err)
			}
			_, err := s.SettleClaudeUnsubmittedMail(ctx, b, a)
			require.Error(t, err)
			msg, err := s.MessageFor(ctx, a.MessageID)
			require.NoError(t, err)
			require.Equal(t, "queued", string(msg.State))
			require.Empty(t, historyRowsFor(t, s, b.AgentID))
		})
	}
}

func TestClaudeSettlementMissingAcceptedIdentityIsStale(t *testing.T) {
	for _, kind := range []string{"message", "target"} {
		for _, hold := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/hold=%v", kind, hold), func(t *testing.T) {
				ctx := context.Background()
				s, b, _ := embeddedMailFixture(t)
				a := resetReservationMail(t, s, b, "missing-"+kind)
				var err error
				if kind == "message" {
					_, err = s.db.Exec(`DELETE FROM messages WHERE id=?`, a.MessageID)
				} else {
					_, err = s.db.Exec(`DELETE FROM message_target_snapshots WHERE message_id=?`, a.MessageID)
				}
				require.NoError(t, err)
				if hold {
					_, err = s.HoldClaudeUncertainMail(ctx, b, a)
				} else {
					_, err = s.SettleClaudeUnsubmittedMail(ctx, b, a)
				}
				require.ErrorIs(t, err, ErrStaleEmbeddedFence)
			})
		}
	}
}

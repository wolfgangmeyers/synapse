package store

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestNextEmbeddedSteerMailFilters(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)

	// Clean out any existing queued messages for a deterministic test
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE recipient=?`, b.AgentID)
	require.NoError(t, err)

	enqueueWithAttrs := func(id, sender, sourceKind string) {
		e := protocol.Envelope{
			ID:        id,
			From:      sender,
			To:        b.AgentID,
			Body:      "test body for " + id,
			CreatedAt: formatTimestamp(s.clock.Now()),
		}
		var opts EnqueueOptions
		if sourceKind != "" {
			opts.Conversation = &ConversationOptions{
				Source: &protocol.MessageSource{
					Kind: sourceKind,
				},
			}
		}
		_, err := s.Enqueue(ctx, e, opts)
		require.NoError(t, err)
		// Ensure messages table has the expected source_kind and sender
		if sourceKind != "" {
			_, err = s.db.ExecContext(ctx, `UPDATE messages SET source_kind=? WHERE id=?`, sourceKind, id)
			require.NoError(t, err)
		}
		target, err := s.ResolveMessageTarget(ctx, id)
		require.NoError(t, err)
		require.NotNil(t, target)
	}

	t.Run("ExcludesSubagentNotices", func(t *testing.T) {
		subagentIDs := []string{
			"subagent-completed-sub1-1",
			"subagent-terminated-sub2-1",
			"subagent-interrupted-sub3-1",
		}
		for _, msgID := range subagentIDs {
			enqueueWithAttrs(msgID, "system", "system")

			// NextEmbeddedSteerMail must exclude subagent lifecycle notices
			id, err := s.NextEmbeddedSteerMail(ctx, b.AgentID)
			require.NoError(t, err)
			require.Empty(t, id, "NextEmbeddedSteerMail must NOT select messages with id starting with subagent-: %s", msgID)

			// NextEmbeddedMail MUST select them for normal turn admission when idle
			normalID, err := s.NextEmbeddedMail(ctx, b.AgentID)
			require.NoError(t, err)
			require.Equal(t, msgID, normalID, "NextEmbeddedMail MUST select subagent notices for normal turn admission when idle: %s", msgID)

			_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, msgID)
			require.NoError(t, err)
		}
	})

	t.Run("ExcludesRemindersBySourceKind", func(t *testing.T) {
		enqueueWithAttrs("msg-reminder-src", "supervisor", "reminder")
		id, err := s.NextEmbeddedSteerMail(ctx, b.AgentID)
		require.NoError(t, err)
		require.Empty(t, id, "NextEmbeddedSteerMail must NOT select messages with source_kind = 'reminder'")
		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, "msg-reminder-src")
		require.NoError(t, err)
	})

	t.Run("ExcludesRemindersBySender", func(t *testing.T) {
		enqueueWithAttrs("msg-reminder-sender", "reminders", "system")
		id, err := s.NextEmbeddedSteerMail(ctx, b.AgentID)
		require.NoError(t, err)
		require.Empty(t, id, "NextEmbeddedSteerMail must NOT select messages with sender = 'reminders'")
		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, "msg-reminder-sender")
		require.NoError(t, err)
	})

	t.Run("SelectsStandardUserMessages", func(t *testing.T) {
		enqueueWithAttrs("user-steer-msg-1", "user", "web_ui")
		id, err := s.NextEmbeddedSteerMail(ctx, b.AgentID)
		require.NoError(t, err)
		require.Equal(t, "user-steer-msg-1", id, "NextEmbeddedSteerMail MUST select standard user messages")

		// Mark it handled or delete to test next
		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, "user-steer-msg-1")
		require.NoError(t, err)

		enqueueWithAttrs("user-steer-msg-2", "user", "user")
		id, err = s.NextEmbeddedSteerMail(ctx, b.AgentID)
		require.NoError(t, err)
		require.Equal(t, "user-steer-msg-2", id, "NextEmbeddedSteerMail MUST select standard user messages")

		_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, "user-steer-msg-2")
		require.NoError(t, err)
	})

	t.Run("SelectsBackgroundBashCompletedMessages", func(t *testing.T) {
		enqueueWithAttrs("background-bash-completed-job-123", "system", "system")
		id, err := s.NextEmbeddedSteerMail(ctx, b.AgentID)
		require.NoError(t, err)
		require.Equal(t, "background-bash-completed-job-123", id, "NextEmbeddedSteerMail MUST select background-bash-completed-* messages")
	})
}

package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestAppendEmbeddedFinalSuppressesReplyWhenMailboxSendToSenderUsed(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-suppression.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker", "reply-session", 1, "reply-generation", "worker")

	// 1. Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-supervisor", "do task")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// 2. Assistant intermediate message with tool call to mecha_mailbox_send to supervisor
	toolCall := ai.ToolCall{
		ID:   "call-reply-1",
		Name: "mecha_mailbox_send",
		Arguments: map[string]any{
			"recipient": "supervisor",
			"body":      "I finished the task",
		},
	}
	assistantMsg := ai.AssistantMessage{
		StopReason: ai.StopToolUse,
		Content:    ai.ContentList{toolCall},
	}
	assistantBody, err := json.Marshal(assistantMsg)
	require.NoError(t, err)

	_, err = s.AppendEmbeddedEntry(ctx, b, "assistant", assistantBody)
	require.NoError(t, err)

	// Tool result
	resultMsg := ai.ToolResultMessage{
		ToolCallID: "call-reply-1",
		ToolName:   "mecha_mailbox_send",
		Content:    ai.ContentList{ai.TextContent{Text: `{"status":"accepted"}`}},
	}
	resultBody, err := json.Marshal(resultMsg)
	require.NoError(t, err)
	resultEntry, err := s.AppendEmbeddedEntry(ctx, b, "toolResult", resultBody)
	require.NoError(t, err)

	// 3. Final assistant message
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content:    ai.ContentList{ai.TextContent{Text: "Task complete summary"}},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, resultEntry.Sequence, resultEntry.Digest, finalMsg)
	require.NoError(t, err)
	require.Equal(t, "assistant", finalEntry.Kind)

	// Since mecha_mailbox_send was called to supervisor during this turn,
	// embedded_reply_outbox should NOT have any rows inserted!
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "reply outbox must be suppressed when mecha_mailbox_send to sender was used")
}

func TestAppendEmbeddedFinalDoesNotSuppressReplyWhenNoMailboxSendUsed(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "no-reply-suppression.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-2", "reply-session-2", 1, "reply-generation-2", "worker")

	// Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-sup-2", "plain task")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// Final assistant message without prior mecha_mailbox_send
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content:    ai.ContentList{ai.TextContent{Text: "Direct text response"}},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err)
	require.Equal(t, "assistant", finalEntry.Kind)

	// Outbox should have the reply for supervisor
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_reply_outbox"))
	reply, err := s.embeddedReplyTx(ctx, attempt.AttemptID)
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Equal(t, "supervisor", reply.To)
	require.Equal(t, "Direct text response", reply.Body)
}

func TestAppendEmbeddedFinalDoesNotSuppressReplyWhenMailboxSendSentToDifferentRecipient(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "different-recipient.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-3", "reply-session-3", 1, "reply-generation-3", "worker")

	// Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-sup-3", "coordinate with peer")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	_, err = s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)

	// Tool call to mecha_mailbox_send to "other-worker" (NOT supervisor)
	toolCall := ai.ToolCall{
		ID:   "call-reply-diff",
		Name: "mecha_mailbox_send",
		Arguments: map[string]any{
			"recipient": "other-worker",
			"body":      "Hey peer",
		},
	}
	assistantMsg := ai.AssistantMessage{
		StopReason: ai.StopToolUse,
		Content:    ai.ContentList{toolCall},
	}
	assistantBody, err := json.Marshal(assistantMsg)
	require.NoError(t, err)

	midEntry, err := s.AppendEmbeddedEntry(ctx, b, "assistant", assistantBody)
	require.NoError(t, err)

	// Final assistant message
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content:    ai.ContentList{ai.TextContent{Text: "Messaged peer"}},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, midEntry.Sequence, midEntry.Digest, finalMsg)
	require.NoError(t, err)
	require.Equal(t, "assistant", finalEntry.Kind)

	// Outbox should STILL have the reply for supervisor
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_reply_outbox"))
	reply, err := s.embeddedReplyTx(ctx, attempt.AttemptID)
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Equal(t, "supervisor", reply.To)
}

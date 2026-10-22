package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedReplyWithoutTextContent(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-no-text.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-empty-text", "reply-session-empty-text", 1, "reply-generation-1", "worker")

	// 1. Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-supervisor-no-text", "run command silently")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// 2. Final assistant message with no text content (e.g. empty content list)
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content:    ai.ContentList{},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err, "assistant message without text content must finalize successfully")
	require.Equal(t, "assistant", finalEntry.Kind)

	// 3. Verify reply outbox does NOT insert a fake fallback message text
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "assistant message without text must not insert fallback text into outbox")
}

func TestEmbeddedReplyWithoutTextContentWhenToolCallsPresent(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-tools-no-text.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-tools-no-text", "reply-session-tools-no-text", 1, "reply-generation-1", "worker")

	// 1. Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-supervisor-tools-no-text", "run command and finish")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	// 2. Intermediate assistant tool call (e.g. bash)
	toolCall := ai.ToolCall{
		ID:        "call-bash-1",
		Name:      "bash",
		Arguments: map[string]any{"command": "echo test"},
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
		ToolCallID: "call-bash-1",
		ToolName:   "bash",
		Content:    ai.ContentList{ai.TextContent{Text: "test output"}},
	}
	resultBody, err := json.Marshal(resultMsg)
	require.NoError(t, err)
	resultEntry, err := s.AppendEmbeddedEntry(ctx, b, "toolResult", resultBody)
	require.NoError(t, err)

	// 3. Final assistant message with no text content
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content:    ai.ContentList{},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, resultEntry.Sequence, resultEntry.Digest, finalMsg)
	require.NoError(t, err, "assistant message without text content must finalize successfully when tools were run")
	require.Equal(t, "assistant", finalEntry.Kind)

	// 4. Verify (Action completed without additional text) is NOT forced into reply outbox
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "reply outbox must not force fallback bubble when tool calls are present")
}

func TestEmbeddedReplyThinkingFallbackWhenTextEmpty(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-thinking-fallback.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-thinking", "reply-session-thinking", 1, "reply-generation-1", "worker")

	// 1. Inbound mail from "supervisor"
	mail := testEnvelope("mail-from-supervisor-thinking", "investigate something")
	mail.From = "supervisor"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// 2. Final assistant message with thinking content but zero text
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content: ai.ContentList{
			ai.ThinkingContent{Thinking: "Analyzing commits and determined the issue is resolved."},
		},
	})
	require.NoError(t, err)

	finalEntry, err := s.AppendEmbeddedFinalAndReply(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err, "assistant message with thinking content must finalize successfully")
	require.Equal(t, "assistant", finalEntry.Kind)

	// 3. Verify reply outbox is ZERO (assistant text is never auto-routed to outbox)
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "reply outbox must never auto-reply")
}

func TestEmbeddedReplyReminderRecordsHistoryWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-reminder.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-reminder", "reply-session-reminder", 1, "reply-generation-1", "worker")

	// 1. Inbound reminder message from "reminders"
	mail := testEnvelope("reminder-msg-1", "What animal has the most appendages?")
	mail.From = "reminders"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET source_kind='reminder' WHERE id=?`, mail.ID)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// 2. Assistant answers the reminder question
	replyText := "The animal with the most appendages is the millipede Eumillipes persephone."
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content: ai.ContentList{
			ai.TextContent{Text: replyText},
		},
	})
	require.NoError(t, err)

	finalEntry, historyEntry, err := s.AppendEmbeddedFinalAndReplyWithHistory(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err, "assistant reply to reminder must succeed")
	require.Equal(t, "assistant", finalEntry.Kind)

	// 3. Verify reply outbox is ZERO (no outbound mailbox message queued for "reminders")
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "reminder turn must not insert into embedded_reply_outbox")

	// 4. Verify managed_complete_history DOES contain the assistant's reply
	require.NotEmpty(t, historyEntry.ID, "historyEntry must have an ID")
	require.Equal(t, "Assistant response", historyEntry.Message)
	require.NotNil(t, historyEntry.Presentation)
	require.Equal(t, "user", historyEntry.Presentation.To)
	require.Equal(t, b.AgentID, historyEntry.Presentation.From)
	require.Equal(t, "message", historyEntry.Presentation.Kind)
	require.NotNil(t, historyEntry.MessageContent)
	require.Equal(t, replyText, *historyEntry.MessageContent)

	page, err := s.ManagedHistoryPage(ctx, b.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Entries)
	found := false
	for _, h := range page.Entries {
		if h.Presentation != nil && h.Presentation.To == "user" && h.MessageContent != nil && *h.MessageContent == replyText {
			found = true
			break
		}
	}
	require.True(t, found, "assistant response to reminder must be recorded in complete history")
}

func TestEmbeddedReplySystemMessageRecordsHistoryWithoutOutbox(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-system.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-system", "reply-session-system", 1, "reply-generation-1", "worker")

	// 1. Inbound system message from "system" with source_kind="system"
	mail := testEnvelope("system-msg-1", "[Subagent Completed] Task subagent-1 (test) finished with status completed: done.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*")
	mail.From = "system"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET source_kind='system' WHERE id=?`, mail.ID)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	// 2. Assistant acknowledges and continues without needing to mail system
	replyText := "Acknowledged system notice. Proceeding with next steps."
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content: ai.ContentList{
			ai.TextContent{Text: replyText},
		},
	})
	require.NoError(t, err)

	finalEntry, historyEntry, err := s.AppendEmbeddedFinalAndReplyWithHistory(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err, "assistant reply to system notification must succeed")
	require.Equal(t, "assistant", finalEntry.Kind)

	// 3. Verify reply outbox is ZERO (no outbound mailbox message auto-queued)
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "assistant text must never be automatically queued to outbox")

	// 4. Verify managed_complete_history targets "user", NOT "system"
	require.NotEmpty(t, historyEntry.ID, "historyEntry must have an ID")
	require.Equal(t, "user", historyEntry.Presentation.To)
	require.Equal(t, b.AgentID, historyEntry.Presentation.From)

	page, err := s.ManagedHistoryPage(ctx, b.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	for _, h := range page.Entries {
		if h.Presentation != nil {
			require.NotEqual(t, "system", h.Presentation.To, "no history entry may target system")
		}
	}
}

func TestEmbeddedReplySystemMessageRoutesToLastHumanRecipient(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-system-human.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-human", "reply-session-human", 1, "reply-generation-1", "worker")

	// 1. Prior human message from "wolfgang" via web_ui
	humanMail := testEnvelope("human-msg-1", "Hello worker, please do something.")
	humanMail.From = "wolfgang"
	humanMail.To = b.AgentID
	source := protocol.MessageSource{Kind: "web_ui"}
	humanMail.Source = &source
	humanBody := humanMail.Body
	_, err = s.Enqueue(ctx, humanMail, EnqueueOptions{Conversation: &ConversationOptions{Body: &humanBody, Source: &source}})
	require.NoError(t, err)

	// 2. Inbound system message from "system" with source_kind="system"
	sysMail := testEnvelope("system-msg-2", "[Subagent Completed] Task subagent-1 finished with status completed: done.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*")
	sysMail.From = "system"
	sysMail.To = b.AgentID
	_, err = s.Enqueue(ctx, sysMail)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET source_kind='system' WHERE id=?`, sysMail.ID)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, sysMail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)

	// 3. Assistant finishes turn responding to system notice
	replyText := "Subagent finished and I have verified the toggle buttons."
	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason: ai.StopStop,
		Content: ai.ContentList{
			ai.TextContent{Text: replyText},
		},
	})
	require.NoError(t, err)

	finalEntry, historyEntry, err := s.AppendEmbeddedFinalAndReplyWithHistory(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err)
	require.Equal(t, "assistant", finalEntry.Kind)

	// 4. Verify reply outbox is ZERO (no outbound mailbox message auto-queued)
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"), "assistant text must never be automatically queued to outbox")

	// 5. Verify managed_complete_history targets "wolfgang", NOT "system"
	require.NotEmpty(t, historyEntry.ID)
	require.Equal(t, "wolfgang", historyEntry.Presentation.To)
	require.Equal(t, b.AgentID, historyEntry.Presentation.From)

	page, err := s.ManagedHistoryPage(ctx, b.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	for _, h := range page.Entries {
		if h.Presentation != nil {
			require.NotEqual(t, "system", h.Presentation.To, "no history entry may target system")
		}
	}
}

func TestEmbeddedReplyOnAssistantStopErrorEmitsErrorReply(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "reply-error.db")
	s, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer s.Close()

	b := redBind(t, s, "reply-worker-error", "reply-session-error", 1, "reply-generation-1", "worker")

	mail := testEnvelope("mail-for-error-reply", "do work")
	mail.From = "sender"
	mail.To = b.AgentID
	_, err = s.Enqueue(ctx, mail)
	require.NoError(t, err)

	attempt := reserveEmbeddedMailTest(t, s, b, mail.ID)
	require.NoError(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))

	snap, err := s.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)

	finalMsg, err := json.Marshal(ai.AssistantMessage{
		StopReason:   ai.StopError,
		ErrorMessage: "vertex api error (status 404): model not found",
	})
	require.NoError(t, err)

	finalEntry, historyEntry, err := s.AppendEmbeddedFinalAndReplyWithHistory(ctx, b, attempt.AttemptID, snap.Entries[0].Sequence, snap.Entries[0].Digest, finalMsg)
	require.NoError(t, err)
	require.Equal(t, int64(2), finalEntry.Sequence)
	require.Equal(t, "assistant", finalEntry.Kind)

	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"))

	require.NotEmpty(t, historyEntry.ID)
	require.Equal(t, "error", historyEntry.Presentation.Outcome)
	require.Equal(t, "Turn failed: vertex api error (status 404): model not found", historyEntry.Message)
}

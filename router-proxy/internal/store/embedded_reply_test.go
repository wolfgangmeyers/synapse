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

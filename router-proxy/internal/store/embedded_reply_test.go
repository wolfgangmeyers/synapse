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

	// 3. Verify reply outbox row has the fallback message text
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_reply_outbox"))
	reply, err := s.embeddedReplyTx(ctx, attempt.AttemptID)
	require.NoError(t, err)
	require.NotNil(t, reply)
	require.Equal(t, "supervisor", reply.To)
	require.Equal(t, "(Action completed without additional text)", reply.Body)
}

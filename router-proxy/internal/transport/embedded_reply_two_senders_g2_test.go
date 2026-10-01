package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedReplyTwoSendersStableReplayConflict(t *testing.T) {
	f := startReplyFixture(t)
	ctx := context.Background()
	for i, sender := range []string{"sender-one", "sender-two"} {
		id := fmt.Sprintf("c%07d-1111-4111-8111-111111111111", i+1)
		payload := fmt.Sprintf(`{"id":%q,"from":%q,"to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id, sender)
		status, raw := f.call(http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusCreated, status, string(raw))
		require.Eventually(t, func() bool {
			status, raw := f.call(http.MethodGet, "/v1/conversations?owner="+sender+"&peer=mail-root", "", "")
			var result struct {
				Messages []struct{ From, To, Body string } `json:"messages"`
			}
			return status == http.StatusOK && json.Unmarshal(raw, &result) == nil && len(result.Messages) == 2 && result.Messages[1].To == sender && result.Messages[1].From == "mail-root" && result.Messages[1].Body == "answered"
		}, 3*time.Second, 10*time.Millisecond)
		status, raw = f.call(http.MethodPost, "/v1/messages", "", payload)
		require.Equal(t, http.StatusOK, status, string(raw))
		status, _ = f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":"wrong-sender","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id))
		require.Equal(t, http.StatusConflict, status)
	}
	require.EqualValues(t, 2, f.calls.Load())
	// Store's ordinary conversation idempotency rejects a changed timestamp on
	// the same reply ID. Only the frozen outbox envelope can be replayed.
	raw, err := f.db.PendingEmbeddedReplies(ctx, "mail-root")
	require.NoError(t, err)
	require.Empty(t, raw)
	history, err := f.db.ConversationHistory(ctx, "sender-one", "mail-root")
	require.NoError(t, err)
	reply := history[1]
	source := protocol.MessageSource{Kind: "agent"}
	_, err = f.db.Enqueue(ctx, protocol.Envelope{ID: reply.ID, From: reply.From, To: reply.To, Body: reply.Body, CreatedAt: "2031-01-01T00:00:00.000Z", Source: &source}, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &reply.Body, Source: &source}})
	require.Error(t, err)
}

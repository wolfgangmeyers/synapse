package store

import (
	"context"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestEnqueueConversationKeepsOriginalIdentityWhenDeliveryIsBatched(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "batch-1")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	body := "conversation body"
	first, err := store.Enqueue(context.Background(), testEnvelope("message-1", "queue body"), EnqueueOptions{Conversation: &ConversationOptions{Body: &body}})
	require.NoError(t, err)
	require.True(t, first.ConversationCreated)
	require.Equal(t, body, first.ConversationMessage.Body)
	second, err := store.Enqueue(context.Background(), testEnvelope("message-2", "different queue body"), EnqueueOptions{Conversation: &ConversationOptions{Body: &body}})
	require.NoError(t, err)
	require.True(t, second.ConversationCreated)
	require.Equal(t, first.Message.MessageIncarnationID, first.ConversationMessage.MessageIncarnationID)
	require.NotEqual(t, first.ConversationMessage.ID, second.ConversationMessage.ID)
	duplicate, err := store.Enqueue(context.Background(), testEnvelope("message-1", "queue body"), EnqueueOptions{Conversation: &ConversationOptions{Body: &body}})
	require.NoError(t, err)
	require.False(t, duplicate.ConversationCreated)
	require.Equal(t, first.ConversationMessage.Sequence, duplicate.ConversationMessage.Sequence)
}

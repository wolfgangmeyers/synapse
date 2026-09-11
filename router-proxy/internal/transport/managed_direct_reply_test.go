package transport

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPersistManagedDirectReplyCreatesOneConversationMessageAndBroadcasts(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	broadcasts := make(chan domain.ConversationMessage, 2)
	subscriber := &session{router: router, sendOverride: func(fields map[string]any) error {
		if fields["type"] == "conversation_message" {
			broadcasts <- fields["message"].(domain.ConversationMessage)
		}
		return nil
	}}
	router.mu.Lock()
	router.convos["wolfgang"] = map[string]*session{"browser": subscriber}
	router.mu.Unlock()

	attempt := store.ManagedDirectAttempt{
		MessageID: "direct-message",
		Message:   domain.Message{From: "wolfgang"},
		Fence:     store.ManagedRuntimeFence{AgentID: "managed-agent"},
	}
	reply := managed.DirectMailboxReply{EntryIdentity: "assistant-entry", Body: "managed reply works"}
	require.NoError(t, router.persistManagedDirectReply(context.Background(), attempt, reply))
	require.NoError(t, router.persistManagedDirectReply(context.Background(), attempt, reply))

	history, err := durable.ConversationHistory(context.Background(), "wolfgang", "managed-agent")
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "managed-agent", history[0].From)
	require.Equal(t, "wolfgang", history[0].To)
	require.Equal(t, "managed reply works", history[0].Body)

	broadcast := <-broadcasts
	require.Equal(t, history[0].ID, broadcast.ID)
	select {
	case duplicate := <-broadcasts:
		t.Fatalf("duplicate direct reply broadcast: %s", duplicate.ID)
	default:
	}
}

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestManagedHistoryKeepsQuietEligibilityAtWriteAndCapsComplete(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	body := "quiet message"
	source := protocol.MessageSource{Kind: "agent"}
	result, err := durable.Enqueue(ctx, protocol.Envelope{ID: "quiet-message", From: "worker", To: "supervisor", Body: body, CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source}, EnqueueOptions{Conversation: &ConversationOptions{Body: &body, Source: &source, QuietEligible: true}})
	require.NoError(t, err)
	require.True(t, result.ConversationCreated)
	page, err := durable.ManagedHistoryPage(ctx, "worker", "quiet", 0, 10)
	require.NoError(t, err)
	require.Equal(t, []ManagedHistoryEntry{{ID: "quiet-message", AgentID: "worker", At: result.ConversationMessage.RecordedAt, Level: "message", Message: body}}, page.Entries)

	for index := 0; index < maxManagedHistoryEntries+1; index++ {
		_, err := durable.AppendManagedCompleteHistory(ctx, "worker", "test", "info", fmt.Sprintf("event-%03d", index))
		require.NoError(t, err)
	}
	complete, err := durable.ManagedHistoryPage(ctx, "worker", "complete", 0, maxManagedHistoryEntries-1)
	require.NoError(t, err)
	require.Len(t, complete.Entries, maxManagedHistoryEntries-1)
	require.Equal(t, "event-200", complete.Entries[0].Message)
	require.Equal(t, "event-002", complete.Entries[len(complete.Entries)-1].Message)
	require.NotNil(t, complete.NextBefore)
	_, err = durable.ManagedHistoryPage(ctx, "worker", "complete", *complete.NextBefore, 10)
	require.NoError(t, err)
	quietAfterCompleteCap, err := durable.ManagedHistoryPage(ctx, "worker", "quiet", 0, 10)
	require.NoError(t, err)
	require.Equal(t, page.Entries, quietAfterCompleteCap.Entries, "complete-history retention must never evict quiet history")
}

func TestManagedHistoryPageScopesFactsAndQuietMailToItsAgent(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	_, err := durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "lifecycle", "info", "Alpha entered running")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-beta", "lifecycle", "info", "Beta entered running")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "managed_http", "info", "Managed POST completed")
	require.NoError(t, err)
	_, err = durable.AppendManagedCompleteHistory(ctx, "managed-alpha", "message", "info", "Message sent to another worker")
	require.NoError(t, err)

	alpha, err := durable.ManagedHistoryPage(ctx, "managed-alpha", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, alpha.Entries, 1)
	require.Equal(t, "managed-alpha", alpha.Entries[0].AgentID)
	require.Equal(t, "Alpha entered running", alpha.Entries[0].Message)

	body := "alpha quiet mail"
	source := protocol.MessageSource{Kind: "agent"}
	_, err = durable.Enqueue(ctx, protocol.Envelope{ID: "alpha-quiet", From: "managed-alpha", To: "supervisor", Body: body, CreatedAt: "2030-01-01T00:00:00.000Z", Source: &source}, EnqueueOptions{Conversation: &ConversationOptions{Body: &body, Source: &source, QuietEligible: true}})
	require.NoError(t, err)
	quiet, err := durable.ManagedHistoryPage(ctx, "managed-beta", "quiet", 0, 10)
	require.NoError(t, err)
	require.Empty(t, quiet.Entries)
}

func TestManagedQuietSendAuthorizationIsSingleUseAndCompleteHistoryProducerIsIdempotent(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, durable.ReserveManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor"))
	consumed, err := durable.ConsumeManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor")
	require.NoError(t, err)
	require.True(t, consumed)
	consumed, err = durable.ConsumeManagedQuietSendAuthorization(ctx, "control-1", "managed-agent", "supervisor")
	require.NoError(t, err)
	require.False(t, consumed)
	first, err := durable.RecordManagedCompleteHistory(ctx, "managed-agent", "control", "info", "Managed tool requested", "control:1")
	require.NoError(t, err)
	second, err := durable.RecordManagedCompleteHistory(ctx, "managed-agent", "control", "info", "Managed tool requested", "control:1")
	require.NoError(t, err)
	require.Equal(t, first, second)
	page, err := durable.ManagedHistoryPage(ctx, "managed-agent", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
}

func TestManagedHistoryRejectsInvalidInputs(t *testing.T) {
	durable := openTestStore(t)
	_, err := durable.AppendManagedCompleteHistory(context.Background(), "worker", "", "info", "message")
	require.Error(t, err)
	_, err = durable.AppendManagedCompleteHistory(context.Background(), "", "lifecycle", "info", "message")
	require.Error(t, err)
	_, err = durable.ManagedHistoryPage(context.Background(), "worker", "not-a-view", 0, 1)
	require.Error(t, err)
	_, err = durable.ManagedHistoryPage(context.Background(), "worker", "quiet", 0, maxManagedHistoryEntries+1)
	require.Error(t, err)
}

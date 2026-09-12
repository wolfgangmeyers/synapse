package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestCompleteHistoryPublisherReceivesOnlyAttributedDurableFacts(t *testing.T) {
	manager := &Manager{}
	published := make(chan store.ManagedHistoryEntry, 1)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) { published <- entry })

	entry := store.ManagedHistoryEntry{ID: "complete:1", AgentID: "managed-agent", Message: "Ran bash"}
	manager.publishCompleteHistory(entry)
	require.Equal(t, entry, <-published)

	manager.publishCompleteHistory(store.ManagedHistoryEntry{ID: "complete:2"})
	select {
	case unexpected := <-published:
		t.Fatalf("unattributed timeline fact was published: %+v", unexpected)
	default:
	}
}

func TestRootControlHistoryPublishesTheStructuredDurableFact(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manager := &Manager{store: durable}
	published := make(chan store.ManagedHistoryEntry, 1)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) { published <- entry })

	manager.recordRootControlHistory(context.Background(), "managed-agent", "bash", "success", "request-1")
	entry := <-published
	require.Equal(t, "Ran bash", entry.Message)
	require.Equal(t, &store.ManagedHistoryPresentation{Kind: "tool", ToolName: "bash", Outcome: "success", Summary: "Completed successfully"}, entry.Presentation)
}

package managed

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

func TestCompleteHistoryPublisherPanicLeavesCommittedFactReadable(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	manager := &Manager{store: durable}
	manager.SetCompleteHistoryPublisher(func(store.ManagedHistoryEntry) { panic("private publisher panic") })
	entry, err := durable.RecordManagedCompleteHistoryWithPresentation(context.Background(), "managed-agent", "managed_message", "info", "Message acknowledged", "publisher-panic", &store.ManagedHistoryPresentation{Kind: "message", Outcome: "success", Summary: "Inbound message acknowledged"})
	require.NoError(t, err)
	manager.publishCompleteHistory(entry)
	page, err := durable.ManagedHistoryPage(context.Background(), "managed-agent", "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	require.Equal(t, entry, page.Entries[0])
}

func prepareManagedReminderAcknowledgement(t *testing.T) (*Manager, *store.Store, store.ManagedRuntimeFence, store.ManagedDirectAttempt) {
	t.Helper()
	ctx := context.Background()
	manager, durable, _, fence, _ := readyGoalProjectionFixture(t)
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z")
	created, _, err := durable.CreateReminder(ctx, store.ReminderRequest{ID: "manager-reminder", Target: fence.AgentID, Creator: "sender", Body: "private reminder body", CreatedAt: now, NextDueAt: now})
	require.NoError(t, err)
	require.True(t, created)
	outcomes, err := durable.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.NotNil(t, outcomes[0].Message)
	_, err = durable.ResolveMessageTarget(ctx, outcomes[0].Message.ID)
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "cursor-1"))
	return manager, durable, fence, *attempt
}

func TestManagerAcknowledgementPublishesCommittedReminderExactlyOnce(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "recovered"}[recovered], func(t *testing.T) {
			manager, durable, fence, attempt := prepareManagedReminderAcknowledgement(t)
			published := make(chan store.ManagedHistoryEntry, 2)
			calls := 0
			manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
				calls++
				page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 10)
				require.NoError(t, err)
				require.Len(t, page.Entries, 1, "publication follows the durable acknowledgement commit")
				published <- entry
			})

			var changed bool
			if recovered {
				changed = manager.acknowledgeManagedDirectRecovered(attempt, "receipt-1")
			} else {
				changed = manager.acknowledgeManagedDirect(attempt, "receipt-1")
			}
			require.True(t, changed)
			entry := <-published
			require.Equal(t, 1, calls)
			require.Equal(t, "reminder", entry.Presentation.Kind)

			if recovered {
				changed = manager.acknowledgeManagedDirectRecovered(attempt, "receipt-1")
			} else {
				changed = manager.acknowledgeManagedDirect(attempt, "receipt-1")
			}
			require.False(t, changed)
			require.Equal(t, 1, calls)
			page, err := durable.ManagedHistoryPage(context.Background(), fence.AgentID, "complete", 0, 10)
			require.NoError(t, err)
			require.Len(t, page.Entries, 1)
			require.Equal(t, entry, page.Entries[0])
		})
	}
}

func TestParentCompactionManagerPublishesEachCreatedFactOnceAfterCommit(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)
	published := make(chan store.ManagedHistoryEntry, 4)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		require.NoError(t, err)
		require.NotEmpty(t, page.Entries, "publication follows the durable compaction transition")
		published <- entry
	})

	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(1), "state": "compacting"})
	start := <-published
	require.Equal(t, "compaction", start.Presentation.Kind)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)

	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(2), "state": "idle", "outcome": "completed"})
	end := <-published
	require.Equal(t, "compaction", end.Presentation.Kind)
	page, err = durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 2)

	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(2), "state": "idle", "outcome": "completed"})
	select {
	case duplicate := <-published:
		t.Fatalf("duplicate compaction transition was published: %+v", duplicate)
	default:
	}
}

func TestParentCompactionNotificationManagerPublishesOnlyWhenCreated(t *testing.T) {
	ctx := context.Background()
	manager, durable, binding, fence, _ := readyGoalProjectionFixture(t)
	published := make(chan store.ManagedHistoryEntry, 4)
	manager.SetCompleteHistoryPublisher(func(entry store.ManagedHistoryEntry) {
		page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
		require.NoError(t, err)
		require.NotEmpty(t, page.Entries, "publication follows the durable notification commit")
		published <- entry
	})
	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(1), "state": "compacting"})
	<-published
	manager.handleParentCompactionEvent(binding, map[string]any{"sequence": int64(2), "state": "idle", "outcome": "completed"})
	<-published

	manager.handleParentCompactionNotification(binding, map[string]any{"sequence": int64(3), "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash})
	notification := <-published
	require.Equal(t, "Post-compaction notification delivered", notification.Message)
	require.Equal(t, "message", notification.Presentation.Kind)
	page, err := durable.ManagedHistoryPage(ctx, fence.AgentID, "complete", 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 3)
	require.Equal(t, notification, page.Entries[0])

	manager.handleParentCompactionNotification(binding, map[string]any{"sequence": int64(3), "runtime_generation": fence.RuntimeGeneration, "session_epoch": fence.SessionEpoch, "manifest_sha256": fence.ManifestHash})
	select {
	case duplicate := <-published:
		t.Fatalf("duplicate post-compaction notification was published: %+v", duplicate)
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

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func reminderRequest(id string, interval *int64) ReminderRequest {
	return ReminderRequest{ID: id, Target: "agent-a", Creator: "creator", Body: "reminder body", CreatedAt: "2026-08-29T12:00:00.000Z", NextDueAt: "2026-08-29T12:00:00.000Z", IntervalMS: interval}
}

func TestCreateAndFireOneShotReminderQueuesAndLinksMessage(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	created, reminder, err := store.CreateReminder(context.Background(), reminderRequest("one-shot", nil))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "scheduled", reminder.State)
	var attempts []ReminderFireAttempt
	outcomes, err := store.FireDueReminders(context.Background(), func(attempt ReminderFireAttempt) { attempts = append(attempts, attempt) })
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, []ReminderFireAttempt{{ReminderID: "one-shot", ScheduledFor: "2026-08-29T12:00:00.000Z"}}, attempts)
	require.Equal(t, "message_queued", outcomes[0].Reminder.State)
	require.Equal(t, "reminder-one-shot-0", outcomes[0].Message.ID)
	require.Equal(t, "reminder", outcomes[0].Message.Source.Kind)
	require.Equal(t, outcomes[0].Message.ID, *outcomes[0].Reminder.LinkedMessageID)
	var count int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(*) FROM conversation_messages WHERE message_id = ?", outcomes[0].Message.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestFireDueRemindersKeepsActiveManagedRecurringReminderAndOffersDirectDelivery(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	fence := readyManagedDirectAgent(t, store, "agent-a")
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("managed-recurring", &interval)

	created, reminder, err := store.CreateReminder(ctx, request)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "scheduled", reminder.State)
	var connectedAt sql.NullString
	require.NoError(t, store.db.QueryRow(`SELECT last_connected_at FROM agents WHERE agent_id=?`, request.Target).Scan(&connectedAt))
	require.False(t, connectedAt.Valid, "managed compatibility rows do not carry legacy WebSocket leases")

	var attempts []ReminderFireAttempt
	outcomes, err := store.FireDueReminders(ctx, func(attempt ReminderFireAttempt) { attempts = append(attempts, attempt) })
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, []ReminderFireAttempt{{ReminderID: request.ID, ScheduledFor: request.NextDueAt}}, attempts)
	require.Equal(t, "scheduled", outcomes[0].Reminder.State)
	require.Equal(t, "2026-08-29T12:01:00.000Z", outcomes[0].Reminder.NextDueAt)
	require.Equal(t, 1, outcomes[0].Reminder.OccurrenceNumber)
	require.Equal(t, "reminder-managed-recurring-0", outcomes[0].Message.ID)

	stored, err := store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, outcomes[0].Message.ID, *stored.LinkedMessageID)
	var compatibilityRows int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE agent_id=?`, request.Target).Scan(&compatibilityRows))
	require.Equal(t, 1, compatibilityRows)

	snapshot, err := store.ResolveMessageTarget(ctx, outcomes[0].Message.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, "managed", snapshot.SourceKind)
	require.Equal(t, fence.AgentID, snapshot.OwnerIdentity)
	direct, err := store.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, direct)
	require.Equal(t, outcomes[0].Message.ID, direct.MessageID)
}

func TestOneShotReminderStillQueuesForManagedRecipientWithoutLegacyLease(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	_ = readyManagedDirectAgent(t, store, "agent-a")
	request := reminderRequest("managed-one-shot", nil)

	_, _, err := store.CreateReminder(ctx, request)
	require.NoError(t, err)
	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "message_queued", outcomes[0].Reminder.State)
	require.Equal(t, "reminder-managed-one-shot-0", outcomes[0].Message.ID)
}

func TestManagedRecurringReminderSkipsUnavailableProcessAndRecoversAfterStoreRestart(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	path := filepath.Join(t.TempDir(), "router.db")
	store, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	fence := readyManagedDirectAgent(t, store, "agent-a")
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("managed-restart", &interval)
	_, _, err = store.CreateReminder(ctx, request)
	require.NoError(t, err)
	require.NoError(t, store.MarkManagedStopped(ctx, fence.AgentID))

	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, outcomes)
	reminder, err := store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, "2026-08-29T12:01:00.000Z", reminder.NextDueAt)
	var compatibilityRows int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE agent_id=?`, request.Target).Scan(&compatibilityRows))
	require.Equal(t, 1, compatibilityRows)
	require.NoError(t, store.Close())

	store, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, clock.Advance(time.Minute))
	// A recovered runtime restores its direct-delivery fence before the next slot.
	_, err = store.db.Exec(`UPDATE managed_agents SET activity_state='idle',accepting_prompts=1,slot_held=1 WHERE agent_id=?`, fence.AgentID)
	require.NoError(t, err)
	outcomes, err = store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Equal(t, "reminder-managed-restart-0", outcomes[0].Message.ID)
}

func TestManagedRecurringReminderSkipsDeadManagedProcessWithoutLegacyCleanup(t *testing.T) {
	ctx := context.Background()
	for _, transition := range []struct {
		name  string
		apply func(*Store, string) error
	}{
		{"stopped", func(store *Store, agentID string) error { return store.MarkManagedStopped(ctx, agentID) }},
		{"failed", func(store *Store, agentID string) error {
			return store.MarkManagedFailed(ctx, agentID, "PROCESS_EXITED", "process exited")
		}},
	} {
		t.Run(transition.name, func(t *testing.T) {
			clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
			store := openTestStoreWithOptions(t, Options{Clock: clock})
			fence := readyManagedDirectAgent(t, store, "agent-a")
			interval := int64(time.Minute / time.Millisecond)
			request := reminderRequest("managed-"+transition.name, &interval)
			_, _, err := store.CreateReminder(ctx, request)
			require.NoError(t, err)
			require.NoError(t, transition.apply(store, fence.AgentID))
			outcomes, err := store.FireDueReminders(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, outcomes)
			reminder, err := store.ReminderFor(ctx, request.ID)
			require.NoError(t, err)
			require.Equal(t, "2026-08-29T12:01:00.000Z", reminder.NextDueAt)
			var agents, messages int
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE agent_id=?`, request.Target).Scan(&agents))
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE recipient=?`, request.Target).Scan(&messages))
			require.Equal(t, 1, agents)
			require.Zero(t, messages)
		})
	}
}

func TestRecurringReminderStillCleansUpGenuinelyOrphanedLegacyTarget(t *testing.T) {
	ctx := context.Background()
	for _, source := range []struct {
		name  string
		owner string
	}{
		{"legacy", ""},
		{"orphaned-managed-source", "missing-managed-agent"},
	} {
		t.Run(source.name, func(t *testing.T) {
			clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
			store := openTestStoreWithOptions(t, Options{Clock: clock})
			interval := int64(time.Minute / time.Millisecond)
			request := reminderRequest("orphaned-"+source.name, &interval)
			_, _, err := store.CreateReminder(ctx, request)
			require.NoError(t, err)
			if source.owner != "" {
				require.NoError(t, execTestData(store, `INSERT INTO recipient_sources (recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES (?,'managed',?,'active',?,?)`, request.Target, source.owner, request.CreatedAt, request.CreatedAt))
			}

			outcomes, err := store.FireDueReminders(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, outcomes)
			reminder, err := store.ReminderFor(ctx, request.ID)
			require.NoError(t, err)
			require.Nil(t, reminder)
			var agents int
			require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE agent_id=?`, request.Target).Scan(&agents))
			require.Zero(t, agents)
		})
	}
}

func TestManagedRecurringReminderUsesReplacementRecipientFence(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	oldFence := readyManagedDirectAgent(t, store, "agent-a")
	replacement := readyManagedDirectAgent(t, store, "replacement-agent")
	require.NoError(t, execTestData(store, `UPDATE recipient_sources SET owner_identity=? WHERE recipient_name=?`, replacement.AgentID, oldFence.AgentID))
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("replacement-fence", &interval)
	_, _, err := store.CreateReminder(ctx, request)
	require.NoError(t, err)

	outcomes, err := store.FireDueReminders(ctx, nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	snapshot, err := store.ResolveMessageTarget(ctx, outcomes[0].Message.ID)
	require.NoError(t, err)
	require.Equal(t, replacement.AgentID, snapshot.OwnerIdentity)
	oldAttempt, err := store.AllocateManagedDirectAttempt(ctx, oldFence)
	require.NoError(t, err)
	require.Nil(t, oldAttempt)
	replacementAttempt, err := store.AllocateManagedDirectAttempt(ctx, replacement)
	require.NoError(t, err)
	require.NotNil(t, replacementAttempt)
	require.Equal(t, outcomes[0].Message.ID, replacementAttempt.MessageID)
}

func TestManagedRecurringReminderFiresOnceAcrossConcurrentSchedulersAndRetainsAfterSettlement(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	fence := readyManagedDirectAgent(t, store, "agent-a")
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("managed-concurrent", &interval)
	_, _, err := store.CreateReminder(ctx, request)
	require.NoError(t, err)

	var attempts []ReminderFireAttempt
	var attemptsMu sync.Mutex
	var outcomes []ReminderFireOutcome
	var outcomesMu sync.Mutex
	var runErrs []error
	var errsMu sync.Mutex
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result, err := store.FireDueReminders(ctx, func(attempt ReminderFireAttempt) {
				attemptsMu.Lock()
				attempts = append(attempts, attempt)
				attemptsMu.Unlock()
			})
			outcomesMu.Lock()
			outcomes = append(outcomes, result...)
			outcomesMu.Unlock()
			errsMu.Lock()
			runErrs = append(runErrs, err)
			errsMu.Unlock()
		}()
	}
	workers.Wait()
	for _, err := range runErrs {
		require.NoError(t, err)
	}
	require.Len(t, attempts, 1)
	require.Len(t, outcomes, 1)
	var messages, directAttempts int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id=?`, "reminder-managed-concurrent-0").Scan(&messages))
	require.Equal(t, 1, messages)
	snapshot, err := store.ResolveMessageTarget(ctx, outcomes[0].Message.ID)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	direct, err := store.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, direct)
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE message_id=?`, direct.MessageID).Scan(&directAttempts))
	require.Equal(t, 1, directAttempts)
	require.NoError(t, store.MarkManagedDirectPrompted(ctx, *direct, "delivery-cursor"))
	require.NoError(t, store.AcknowledgeManagedDirect(ctx, *direct, "delivery-receipt", true))
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE message_id=?`, direct.MessageID).Scan(&directAttempts))
	require.Zero(t, directAttempts)
	reminder, err := store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, "scheduled", reminder.State)
	require.Nil(t, reminder.LinkedMessageID)
	require.NoError(t, clock.Advance(30*time.Second))
	_, err = store.Sweep(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	reminder, err = store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.NotNil(t, reminder)
}

func TestCancelManagedRecurringReminderLeavesNoScheduledReminder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	_ = readyManagedDirectAgent(t, store, "agent-a")
	interval := int64(time.Minute / time.Millisecond)
	request := reminderRequest("managed-cancel", &interval)
	_, _, err := store.CreateReminder(ctx, request)
	require.NoError(t, err)
	canceled, err := store.CancelReminder(ctx, request.ID)
	require.NoError(t, err)
	require.True(t, canceled)
	reminder, err := store.ReminderFor(ctx, request.ID)
	require.NoError(t, err)
	require.Nil(t, reminder)
}

func TestFireDueRemindersAdvancesRecurringRowsAndOrdersByDueTime(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), testUUID("0003"), testUUID("0004"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	interval := int64(time.Minute / time.Millisecond)
	first := reminderRequest("b-reminder", &interval)
	first.NextDueAt = "2026-08-29T12:00:02.000Z"
	second := reminderRequest("a-reminder", &interval)
	second.Body = "another reminder body"
	second.NextDueAt = "2026-08-29T12:00:01.000Z"
	_, _, err := store.CreateReminder(context.Background(), first)
	require.NoError(t, err)
	_, _, err = store.CreateReminder(context.Background(), second)
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(3*time.Second))
	var order []string
	outcomes, err := store.FireDueReminders(context.Background(), func(attempt ReminderFireAttempt) { order = append(order, attempt.ReminderID) })
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	require.Equal(t, []string{"a-reminder", "b-reminder"}, order)
	for _, outcome := range outcomes {
		require.Equal(t, "scheduled", outcome.Reminder.State)
		require.NotNil(t, outcome.Reminder.LinkedMessageID)
		require.Equal(t, int64(60000), *outcome.Reminder.IntervalMS)
		require.Equal(t, 1, outcome.Reminder.OccurrenceNumber)
	}
}

func TestFireDueRemindersRechecksRowsAddedDuringSweep(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	s := openTestStoreWithOptions(t, Options{Clock: clock})
	_, _, err := s.CreateReminder(context.Background(), reminderRequest("first", nil))
	require.NoError(t, err)
	var attempts []string
	createdSecond := false
	outcomes, err := s.FireDueRemindersWithBefore(context.Background(), func(attempt ReminderFireAttempt) {
		attempts = append(attempts, attempt.ReminderID)
	}, func(attempt ReminderFireAttempt) {
		if createdSecond || attempt.ReminderID != "first" {
			return
		}
		createdSecond = true
		_, _, err := s.CreateReminder(context.Background(), reminderRequest("second", nil))
		require.NoError(t, err)
	})
	require.NoError(t, err)
	require.Len(t, outcomes, 2)
	require.Equal(t, []string{"first", "second"}, attempts)
}

func TestCreateReminderIsIdempotentAndConflictsOnChangedEnvelope(t *testing.T) {
	store := openTestStore(t)
	request := reminderRequest("same", nil)
	created, _, err := store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	require.True(t, created)
	created, _, err = store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	require.False(t, created)
	request.Body = "changed"
	_, _, err = store.CreateReminder(context.Background(), request)
	var conflict *ReminderIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
}

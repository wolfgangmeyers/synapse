package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/scheduler"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// TestReminderLifecycleHTTPSchedulerRestartFunctional joins the public HTTP
// reminder API, the real router sweep, the durable SQLite transitions, and a
// deterministic recipient. Scheduler.Run and fixed-clock Advance are the
// synchronization barriers; there are no wall-clock sleeps in this test.
func TestReminderLifecycleHTTPSchedulerRestartFunctional(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	database := filepath.Join(t.TempDir(), "router.db")
	ids := deterministic.NewSequenceIDSource(
		"functional-id-01", "functional-id-02", "functional-id-03", "functional-id-04", "functional-id-05",
		"functional-id-06", "functional-id-07", "functional-id-08", "functional-id-09", "functional-id-10",
		"functional-id-11", "functional-id-12", "functional-id-13", "functional-id-14", "functional-id-15",
		"functional-id-16", "functional-id-17", "functional-id-18", "functional-id-19", "functional-id-20",
		"functional-id-21", "functional-id-22", "functional-id-23", "functional-id-24", "functional-id-25",
		"functional-id-26", "functional-id-27", "functional-id-28", "functional-id-29", "functional-id-30",
	)

	durable, err := store.Open(ctx, database, store.Options{Clock: clock, IDSource: ids})
	require.NoError(t, err)
	defer func() { require.NoError(t, durable.Close()) }()

	router, err := New(Options{Store: durable, Clock: clock, IDSource: ids})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()
	defer func() { require.NoError(t, router.Close(ctx)) }()

	recipient := reminderFunctionalRecipient{store: durable, agentID: "reminder-recipient", instanceID: "recipient-instance", leaseID: "recipient-lease"}

	oneShotDue := start.Add(time.Minute)
	oneShot := postReminder(t, server.URL, map[string]any{
		"id":          "functional-one-shot",
		"target":      recipient.agentID,
		"creator":     "functional-creator-one-shot",
		"body":        "one-shot body",
		"created_at":  reminderFunctionalTimestamp(start),
		"next_due_at": reminderFunctionalTimestamp(oneShotDue),
	})
	require.Equal(t, "functional-one-shot", oneShot.ID)
	require.Equal(t, recipient.agentID, oneShot.Target)
	require.Equal(t, "scheduled", oneShot.State)
	require.Nil(t, oneShot.LinkedMessageID)
	persistedOneShot, err := durable.ReminderFor(ctx, oneShot.ID)
	require.NoError(t, err)
	require.Equal(t, oneShot.ID, persistedOneShot.ID)
	require.Equal(t, recipient.agentID, persistedOneShot.Target)

	require.NoError(t, recipient.acquire(ctx, time.Hour))
	firstScheduler := newReminderFunctionalScheduler(t, clock, router)
	require.NoError(t, firstScheduler.Start(ctx, time.Hour))
	require.NoError(t, firstScheduler.Stop())
	require.NoError(t, clock.Advance(time.Minute))
	ran, err := firstScheduler.Run(ctx)
	require.NoError(t, err)
	require.True(t, ran)

	oneShotAfterFire := getReminder(t, server.URL, oneShot.ID)
	require.Equal(t, "message_queued", oneShotAfterFire.State)
	require.NotNil(t, oneShotAfterFire.LinkedMessageID)
	oneShotMessageID := *oneShotAfterFire.LinkedMessageID
	oneShotMessage := getMessage(t, server.URL, oneShotMessageID)
	require.Equal(t, oneShotMessageID, oneShotMessage.ID)
	require.Equal(t, "queued", oneShotMessage.State)
	recipient.deliverAndAck(t, ctx, oneShotMessageID)
	require.Equal(t, 1, recipient.deliveries)
	oneShotResponse := getReminderResponse(t, server.URL, oneShot.ID)
	require.Equal(t, http.StatusNotFound, oneShotResponse.status)
	oneShotHistory, err := durable.ConversationHistory(ctx, "reminders", recipient.agentID)
	require.NoError(t, err)
	require.Len(t, oneShotHistory, 1)

	recurringDue := clock.Now().Add(time.Minute)
	recurring := postReminder(t, server.URL, map[string]any{
		"id":          "functional-recurring",
		"target":      recipient.agentID,
		"creator":     "functional-creator-recurring",
		"body":        "recurring body",
		"created_at":  reminderFunctionalTimestamp(clock.Now()),
		"next_due_at": reminderFunctionalTimestamp(recurringDue),
		"interval_ms": int64(time.Minute / time.Millisecond),
	})
	require.Equal(t, "scheduled", recurring.State)
	require.Equal(t, 0, recurring.OccurrenceNumber)
	require.Nil(t, recurring.LinkedMessageID)
	persistedRecurring, err := durable.ReminderFor(ctx, recurring.ID)
	require.NoError(t, err)
	require.Equal(t, recurring.ID, persistedRecurring.ID)
	require.Equal(t, recipient.agentID, persistedRecurring.Target)

	require.NoError(t, clock.Advance(time.Minute))
	ran, err = firstScheduler.Run(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	firstOccurrence := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, "scheduled", firstOccurrence.State)
	require.Equal(t, 1, firstOccurrence.OccurrenceNumber)
	require.NotNil(t, firstOccurrence.LinkedMessageID)
	firstRecurringMessageID := *firstOccurrence.LinkedMessageID
	require.Equal(t, 1, countMessagesForTarget(t, durable, recipient.agentID, firstRecurringMessageID))

	// Restart the router and SQLite handle while this occurrence is linked but
	// still queued. The startup scheduler sweep must preserve its identity and
	// must not create a duplicate occurrence.
	require.NoError(t, firstScheduler.Stop())
	server.Close()
	require.NoError(t, router.Close(ctx))
	require.NoError(t, durable.Close())

	durable, err = store.Open(ctx, database, store.Options{Clock: clock, IDSource: ids})
	require.NoError(t, err)
	router, err = New(Options{Store: durable, Clock: clock, IDSource: ids})
	require.NoError(t, err)
	server = httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
		_ = router.Close(ctx)
		_ = durable.Close()
	})

	restartedScheduler := newReminderFunctionalScheduler(t, clock, router)
	require.NoError(t, restartedScheduler.Start(ctx, time.Hour))
	require.NoError(t, restartedScheduler.Stop())
	afterRestart := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, firstRecurringMessageID, dereferenceString(afterRestart.LinkedMessageID))
	require.Equal(t, 1, afterRestart.OccurrenceNumber)
	require.Equal(t, 1, countMessagesForTarget(t, durable, recipient.agentID, firstRecurringMessageID))

	recipient.store = durable
	recipient.deliverAndAck(t, ctx, firstRecurringMessageID)
	afterFirstAck := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, "scheduled", afterFirstAck.State)
	require.Equal(t, 1, afterFirstAck.OccurrenceNumber)
	require.Nil(t, afterFirstAck.LinkedMessageID)
	require.Equal(t, 2, recipient.deliveries)
	recurringHistoryAfterFirstAck, err := durable.ConversationHistory(ctx, "reminders", recipient.agentID)
	require.NoError(t, err)
	require.Len(t, recurringHistoryAfterFirstAck, 2)

	require.NoError(t, clock.Advance(time.Minute))
	ran, err = restartedScheduler.Run(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	secondOccurrence := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, 2, secondOccurrence.OccurrenceNumber)
	require.NotNil(t, secondOccurrence.LinkedMessageID)
	secondRecurringMessageID := *secondOccurrence.LinkedMessageID
	require.NotEqual(t, firstRecurringMessageID, secondRecurringMessageID)
	require.Equal(t, 1, countMessagesForTarget(t, durable, recipient.agentID, secondRecurringMessageID))

	// Re-running at the same controlled time is the exact duplicate guard.
	ran, err = restartedScheduler.Run(ctx)
	require.NoError(t, err)
	require.True(t, ran)
	unchanged := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, secondRecurringMessageID, dereferenceString(unchanged.LinkedMessageID))
	require.Equal(t, 2, unchanged.OccurrenceNumber)
	recipient.deliverAndAck(t, ctx, secondRecurringMessageID)
	require.Equal(t, 3, recipient.deliveries)
	recurringHistory, err := durable.ConversationHistory(ctx, "reminders", recipient.agentID)
	require.NoError(t, err)
	require.Len(t, recurringHistory, 3)
	final := getReminder(t, server.URL, recurring.ID)
	require.Equal(t, "scheduled", final.State)
	require.Nil(t, final.LinkedMessageID)
	require.Equal(t, 2, final.OccurrenceNumber)
}

type reminderFunctionalRecipient struct {
	store      *store.Store
	agentID    string
	instanceID string
	leaseID    string
	deliveries int
}

func (r *reminderFunctionalRecipient) acquire(ctx context.Context, duration time.Duration) error {
	_, err := r.store.AcquireAgentLease(ctx, store.LeaseRequest{AgentID: r.agentID, InstanceID: r.instanceID, LeaseID: r.leaseID, LeaseDuration: duration})
	return err
}

func (r *reminderFunctionalRecipient) deliverAndAck(t *testing.T, ctx context.Context, expectedMessageID string) {
	t.Helper()
	delivery, err := r.store.ClaimNextMessage(ctx, store.ClaimRequest{AgentID: r.agentID, InstanceID: r.instanceID, LeaseID: r.leaseID, DeliveryLeaseDuration: time.Hour})
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.Len(t, delivery.Items, 1)
	require.Equal(t, expectedMessageID, delivery.Items[0].MessageID)
	require.Equal(t, expectedMessageID, delivery.Message.ID)
	receipt := store.ReceiptRequest{AgentID: r.agentID, InstanceID: r.instanceID, LeaseID: r.leaseID, MessageID: expectedMessageID, AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items}
	_, err = r.store.RecordInjectionRequested(ctx, receipt)
	require.NoError(t, err)
	_, err = r.store.AcknowledgeDelivery(ctx, receipt)
	require.NoError(t, err)
	r.deliveries++
}

func newReminderFunctionalScheduler(t *testing.T, clock *deterministic.FixedClock, router *Router) *scheduler.Scheduler {
	t.Helper()
	result, err := scheduler.New(clock, func(ctx context.Context, _ time.Time) error {
		return router.runSweep(ctx)
	})
	require.NoError(t, err)
	return result
}

func postReminder(t *testing.T, baseURL string, payload map[string]any) domain.Reminder {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/reminders", bytes.NewReader(encoded))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, response.StatusCode, string(raw))
	var body struct {
		Reminder domain.Reminder `json:"reminder"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	return body.Reminder
}

func getReminder(t *testing.T, baseURL, id string) domain.Reminder {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/reminders/" + id)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body struct {
		Reminder domain.Reminder `json:"reminder"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return body.Reminder
}

type reminderHTTPResponse struct{ status int }

func getReminderResponse(t *testing.T, baseURL, id string) reminderHTTPResponse {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/reminders/" + id)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	return reminderHTTPResponse{status: response.StatusCode}
}

func getMessage(t *testing.T, baseURL, id string) domain.Message {
	t.Helper()
	response, err := http.Get(baseURL + "/v1/messages/" + id)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var body struct {
		Message domain.Message `json:"message"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	return body.Message
}

func countMessagesForTarget(t *testing.T, durable *store.Store, target, expectedID string) int {
	t.Helper()
	message, err := durable.MessageFor(context.Background(), expectedID)
	if err != nil || message == nil || message.To != target {
		return 0
	}
	return 1
}

func reminderFunctionalTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

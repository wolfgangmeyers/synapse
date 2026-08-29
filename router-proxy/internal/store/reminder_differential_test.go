package store

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

type reminderSnapshot struct {
	ReminderState string `json:"reminder_state"`
	LinkedID      string `json:"linked_id"`
	MessageState  string `json:"message_state"`
	MessageBody   string `json:"message_body"`
	SourceKind    string `json:"source_kind"`
}

func TestNodeAndGoOneShotReminderTransitionMatch(t *testing.T) {
	nodePath := filepath.Join(t.TempDir(), "node.db")
	node := runNodeReminderFixture(t, nodePath)
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "go.db"), Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("reminder-incarnation"))})
	require.NoError(t, err)
	_, _, err = store.CreateReminder(context.Background(), reminderRequest("one-shot", nil))
	require.NoError(t, err)
	outcomes, err := store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	goResult := reminderSnapshot{ReminderState: outcomes[0].Reminder.State, LinkedID: *outcomes[0].Reminder.LinkedMessageID, MessageState: outcomes[0].Message.State, MessageBody: outcomes[0].Message.Body, SourceKind: outcomes[0].Message.Source.Kind}
	require.Equal(t, node, goResult)
}

func runNodeReminderFixture(t *testing.T, databasePath string) reminderSnapshot {
	t.Helper()
	return runNodeJSON[reminderSnapshot](t, databasePath, `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
repository.createReminder({id:"one-shot", target:"agent-a", creator:"creator", body:"reminder body", created_at:now, next_due_at:now, interval_ms:null});
repository.fireDueReminders();
const reminder = repository.reminderFor("one-shot");
const message = repository.messageFor("reminder-one-shot-0");
console.log(JSON.stringify({reminder_state:reminder.state, linked_id:reminder.linked_message_id, message_state:message.state, message_body:message.body, source_kind:message.source.kind}));
repository.close();`)
}

type reminderControlSnapshot struct {
	ConflictCode  string `json:"conflict_code"`
	CancelCode    string `json:"cancel_code"`
	TimestampCode string `json:"timestamp_code"`
}

func TestNodeAndGoReminderValidationTransitionsMatch(t *testing.T) {
	node := runNodeJSON[reminderControlSnapshot](t, filepath.Join(t.TempDir(), "node.db"), `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
const request = {id:"same", target:"agent-a", creator:"creator", body:"body", created_at:now, next_due_at:now, interval_ms:null};
repository.createReminder(request);
let conflict_code = "";
try { repository.createReminder({...request, body:"changed"}); } catch (error) { conflict_code = error.code ?? "unknown"; }
let cancel_code = "";
try { repository.cancelReminder("bad/id"); } catch (error) { cancel_code = error.code ?? "unknown"; }
let timestamp_code = "";
try { repository.createReminder({...request, id:"too-late", next_due_at:"9999-12-31T23:59:59.999Z", interval_ms:1}); } catch (error) { timestamp_code = error.code ?? "unknown"; }
console.log(JSON.stringify({conflict_code, cancel_code, timestamp_code})); repository.close();`)
	store := openTestStore(t)
	request := reminderRequest("same", nil)
	_, _, err := store.CreateReminder(context.Background(), request)
	require.NoError(t, err)
	_, _, err = store.CreateReminder(context.Background(), ReminderRequest{ID: request.ID, Target: request.Target, Creator: request.Creator, Body: "changed", CreatedAt: request.CreatedAt, NextDueAt: request.NextDueAt})
	var conflict *ReminderIdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
	_, err = store.CancelReminder(context.Background(), "bad/id")
	var validation *ReminderValidationError
	require.ErrorAs(t, err, &validation)
	late := reminderRequest("too-late", nil)
	late.NextDueAt = "9999-12-31T23:59:59.999Z"
	lateInterval := int64(1)
	late.IntervalMS = &lateInterval
	_, _, err = store.CreateReminder(context.Background(), late)
	require.ErrorAs(t, err, &validation)
	require.Equal(t, node, reminderControlSnapshot{ConflictCode: conflict.Code(), CancelCode: validation.Code(), TimestampCode: validation.Code()})
}

type recurrenceSnapshot struct {
	State        string `json:"state"`
	NextDue      string `json:"next_due"`
	Occurrence   int    `json:"occurrence"`
	AttemptCount int    `json:"attempt_count"`
}

func TestNodeAndGoMissedRecurrenceSlotsMatch(t *testing.T) {
	node := runNodeJSON[recurrenceSnapshot](t, filepath.Join(t.TempDir(), "node.db"), `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
let now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
repository.createReminder({id:"missed", target:"agent-a", creator:"creator", body:"body", created_at:now, next_due_at:now, interval_ms:60000});
repository.acquireAgentLease({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", leaseDurationMs:60000});
now = "2026-08-29T12:05:00.000Z"; repository.fireDueReminders();
const reminder = repository.reminderFor("missed");
console.log(JSON.stringify({state:reminder.state, next_due:reminder.next_due_at, occurrence:reminder.occurrence_number, attempt_count:reminder.attempt_count})); repository.close();`)
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	store := openTestStoreWithOptions(t, Options{Clock: clock})
	interval := int64(60000)
	_, _, err := store.CreateReminder(context.Background(), ReminderRequest{ID: "missed", Target: "agent-a", Creator: "creator", Body: "body", CreatedAt: formatTimestamp(start), NextDueAt: formatTimestamp(start), IntervalMS: &interval})
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(5*time.Minute))
	_, err = store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	reminder, err := store.ReminderFor(context.Background(), "missed")
	require.NoError(t, err)
	require.Equal(t, node, recurrenceSnapshot{State: reminder.State, NextDue: reminder.NextDueAt, Occurrence: reminder.OccurrenceNumber, AttemptCount: reminder.AttemptCount})
}

type rearmSnapshot struct {
	ReminderState        string `json:"reminder_state"`
	LinkedID             string `json:"linked_id"`
	NextDue              string `json:"next_due"`
	Occurrence           int    `json:"occurrence"`
	AttemptCount         int    `json:"attempt_count"`
	MessageState         string `json:"message_state"`
	AttemptState         string `json:"attempt_state"`
	FailureReason        string `json:"failure_reason"`
	RetriedMessageState  string `json:"retried_message_state"`
	RetriedReminderState string `json:"retried_reminder_state"`
}

func TestNodeAndGoDeadLetterRecurringRearmMatch(t *testing.T) {
	node := runNodeJSON[rearmSnapshot](t, filepath.Join(t.TempDir(), "node.db"), `
const { openMessageRepository } = await import(process.env.MECHA_NODE_STORE_MODULE);
const now = "2026-08-29T12:00:00.000Z";
const repository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
repository.createReminder({id:"rearm", target:"agent-a", creator:"creator", body:"body", created_at:now, next_due_at:now, interval_ms:60000});
repository.acquireAgentLease({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", leaseDurationMs:60000});
repository.fireDueReminders(); repository.claimNextMessage({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", deliveryLeaseDurationMs:60000});
repository.recordInjectionRequested({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", messageId:"reminder-rearm-0", attemptNumber:1});
repository.nackDelivery({agentId:"agent-a", instanceId:"instance-a", leaseId:"lease-a", messageId:"reminder-rearm-0", attemptNumber:1, reason:"client_error", maxDeliveryAttempts:1});
repository.close();
const { DatabaseSync } = await import("node:sqlite"); let database = new DatabaseSync(process.env.MECHA_NODE_STORE_DATABASE);
const reminder = database.prepare("SELECT state, linked_message_id, next_due_at, occurrence_number, attempt_count FROM reminders WHERE id = ?").get("rearm");
const message = database.prepare("SELECT state FROM messages WHERE id = ?").get("reminder-rearm-0");
const attempt = database.prepare("SELECT state, failure_reason FROM delivery_attempts WHERE message_id = ?").get("reminder-rearm-0"); database.close();
const retryRepository = openMessageRepository({ databasePath: process.env.MECHA_NODE_STORE_DATABASE, clock: () => new Date(now) });
const retried = retryRepository.retryDeadLetter("reminder-rearm-0"); const retriedReminder = retryRepository.reminderFor("rearm"); retryRepository.close();
console.log(JSON.stringify({reminder_state:reminder?.state ?? "", linked_id:reminder?.linked_message_id ?? "", next_due:reminder?.next_due_at ?? "", occurrence:reminder?.occurrence_number ?? -1, attempt_count:reminder?.attempt_count ?? -1, message_state:message?.state ?? "", attempt_state:attempt?.state ?? "", failure_reason:attempt?.failure_reason ?? "", retried_message_state:retried?.state ?? "", retried_reminder_state:retriedReminder?.state ?? ""}));`)
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("0002"), "delivery-batch")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	interval := int64(60000)
	_, _, err := store.CreateReminder(context.Background(), ReminderRequest{ID: "rearm", Target: "agent-a", Creator: "creator", Body: "body", CreatedAt: formatTimestamp(start), NextDueAt: formatTimestamp(start), IntervalMS: &interval})
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = store.FireDueReminders(context.Background(), nil)
	require.NoError(t, err)
	_, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", DeliveryLeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = store.RecordInjectionRequested(context.Background(), ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "reminder-rearm-0", AttemptNumber: 1})
	require.NoError(t, err)
	_, err = store.NackDelivery(context.Background(), ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "reminder-rearm-0", AttemptNumber: 1}, "client_error", 1)
	require.NoError(t, err)
	reminder, err := store.ReminderFor(context.Background(), "rearm")
	require.NoError(t, err)
	var messageState, attemptState, reason string
	require.NoError(t, store.db.QueryRow("SELECT state FROM messages WHERE id='reminder-rearm-0'").Scan(&messageState))
	require.NoError(t, store.db.QueryRow("SELECT state, failure_reason FROM delivery_attempts WHERE message_id='reminder-rearm-0'").Scan(&attemptState, &reason))
	linked := ""
	if reminder.LinkedMessageID != nil {
		linked = *reminder.LinkedMessageID
	}
	require.Equal(t, node, rearmSnapshot{ReminderState: reminder.State, LinkedID: linked, NextDue: reminder.NextDueAt, Occurrence: reminder.OccurrenceNumber, AttemptCount: reminder.AttemptCount, MessageState: messageState, AttemptState: attemptState, FailureReason: reason, RetriedMessageState: "queued", RetriedReminderState: reminder.State})
	_, err = store.RetryDeadLetter(context.Background(), "reminder-rearm-0")
	require.NoError(t, err)
	retriedReminder, err := store.ReminderFor(context.Background(), "rearm")
	require.NoError(t, err)
	var retriedMessageState string
	require.NoError(t, store.db.QueryRow("SELECT state FROM messages WHERE id='reminder-rearm-0'").Scan(&retriedMessageState))
	require.Equal(t, "queued", retriedMessageState)
	require.Equal(t, reminder.State, retriedReminder.State)
	require.Equal(t, []string{testUUID("0002"), "delivery-batch"}, ids.Calls())
}

func runNodeJSON[T any](t *testing.T, databasePath, script string) T {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	moduleURL := (&url.URL{Scheme: "file", Path: filepath.Join(root, "harness", "db.js")}).String()
	command := exec.Command(node, "--input-type=module", "-e", script)
	command.Env = append(os.Environ(), "MECHA_NODE_STORE_MODULE="+moduleURL, "MECHA_NODE_STORE_DATABASE="+databasePath)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	var result T
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(output))), &result))
	return result
}

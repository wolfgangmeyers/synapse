package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func testEnvelope(id, body string) protocol.Envelope {
	return protocol.Envelope{ID: id, From: "sender", To: "agent-a", Body: body, CreatedAt: "2026-08-29T12:00:00.000Z"}
}
func testUUID(n string) string { return fmt.Sprintf("123e4567-e89b-42d3-a456-42661417%s", n) }

func readyManagedDirectFixture(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	durable, fence := readinessSourceFixture(t)
	policy := DefaultManagedReadinessPolicy()
	require.NoError(t, durable.ConfirmManagedPolicyApplied(context.Background(), fence, policy))
	snapshot := ManagedChildSnapshot{SnapshotID: "direct-snapshot", OwnerSessionID: "owner", Sequence: 0, Canonical: []byte(`{"owner_session_id":"owner","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"direct-snapshot"}`), SHA256: testManifestHash}
	require.NoError(t, durable.ConfirmManagedChildSnapshot(context.Background(), fence, snapshot))
	require.NoError(t, durable.MarkManagedReadyFromSources(context.Background(), fence))
	return durable, fence
}

func allocateTestDirectAttempt(t *testing.T, durable *Store, fence ManagedRuntimeFence, id string) *ManagedDirectAttempt {
	t.Helper()
	_, err := durable.Enqueue(context.Background(), protocol.Envelope{ID: id, From: "sender", To: fence.AgentID, Body: "body-" + id, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = durable.ResolveMessageTarget(context.Background(), id)
	require.NoError(t, err)
	attempt, err := durable.AllocateManagedDirectAttempt(context.Background(), fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	return attempt
}

func TestManagedDirectRetryBoundaries(t *testing.T) {
	require.Equal(t, time.Second, directRetryDelay(1))
	require.Equal(t, 5*time.Second, directRetryDelay(2))
	require.Equal(t, 30*time.Second, directRetryDelay(3))
	require.Equal(t, 120*time.Second, directRetryDelay(4))
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-retry")
	terminal, err := durable.ReleaseManagedDirect(context.Background(), *attempt, "prompt_failed", "")
	require.NoError(t, err)
	require.False(t, terminal)
	stored, err := durable.ManagedDirectAttempt(context.Background(), attempt.MessageID, 1)
	require.NoError(t, err)
	require.Equal(t, "released", stored.State)
	require.WithinDuration(t, time.Now().Add(time.Second), parseTimestamp(stored.NextAttemptAt), 2*time.Second)
}

func TestManagedDirectFifthAttemptTerminal(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-fifth")
	for number := 1; number <= 5; number++ {
		terminal, err := durable.ReleaseManagedDirect(context.Background(), *attempt, "prompt_failed", "")
		require.NoError(t, err)
		require.Equal(t, number == 5, terminal)
		if number == 5 {
			break
		}
		require.NoError(t, func() error {
			_, err := durable.db.Exec(`UPDATE managed_direct_delivery_attempts SET next_attempt_at = ? WHERE message_id = ? AND attempt_number = ?`, formatTimestamp(time.Now().Add(-time.Second)), attempt.MessageID, number)
			return err
		}())
		attempt, err = durable.AllocateManagedDirectAttempt(context.Background(), fence)
		require.NoError(t, err)
		require.NotNil(t, attempt)
	}
	message, err := durable.MessageFor(context.Background(), "direct-fifth")
	require.NoError(t, err)
	require.Equal(t, "dead_letter", message.State)
}

func TestManagedDirectExpiryAndTmuxIsolation(t *testing.T) {
	durable, fence := readyManagedDirectFixture(t)
	attempt := allocateTestDirectAttempt(t, durable, fence, "direct-expiry")
	require.NoError(t, func() error {
		_, err := durable.db.Exec(`UPDATE messages SET expires_at = ? WHERE id = ?`, formatTimestamp(time.Now().Add(-time.Second)), attempt.MessageID)
		return err
	}())
	_, err := durable.Sweep(context.Background(), DefaultRetentionPolicy)
	require.NoError(t, err)
	message, err := durable.MessageFor(context.Background(), attempt.MessageID)
	require.NoError(t, err)
	require.Nil(t, message)
	stored, err := durable.ManagedDirectAttempt(context.Background(), attempt.MessageID, attempt.AttemptNumber)
	require.Error(t, err)
	require.Nil(t, stored)
}

func TestTargetSnapshotSelectsDurableTmuxOwner(t *testing.T) {
	ctx := context.Background()
	durable := openTestStore(t)
	owner, err := durable.ReserveTmuxRecipientSource(ctx, "agent-a", "tmux-source-v1:agent-a")
	require.NoError(t, err)
	require.Equal(t, "tmux-source-v1:agent-a", owner)
	require.NoError(t, durable.ActivateTmuxRecipientSource(ctx, "agent-a", owner))
	result, err := durable.Enqueue(ctx, testEnvelope("snapshot-message", "body"))
	require.NoError(t, err)
	target, err := durable.ResolveMessageTarget(ctx, result.Message.ID)
	require.NoError(t, err)
	require.Equal(t, "tmux", target.SourceKind)
	require.Equal(t, owner, target.OwnerIdentity)
	_, err = durable.AcquireAgentLease(ctx, LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	delivery, err := durable.ClaimNextMessage(ctx, ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", SourceOwnerID: owner})
	require.NoError(t, err)
	require.Equal(t, "snapshot-message", delivery.Items[0].MessageID)
}

func TestEnqueueIsIdempotentAndSuppressesUnchangedLiveDuplicates(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"))
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	first, err := store.Enqueue(context.Background(), testEnvelope("message-1", "same"))
	require.NoError(t, err)
	require.True(t, first.Created)
	sameID, err := store.Enqueue(context.Background(), testEnvelope("message-1", "same"))
	require.NoError(t, err)
	require.False(t, sameID.Created)
	require.False(t, sameID.Deduped)
	require.Equal(t, first.Message.ID, sameID.Message.ID)
	duplicate, err := store.Enqueue(context.Background(), testEnvelope("message-2", "same"))
	require.NoError(t, err)
	require.False(t, duplicate.Created)
	require.True(t, duplicate.Deduped)
	require.Equal(t, "message-1", duplicate.Message.ID)
	changed, err := store.Enqueue(context.Background(), testEnvelope("message-3", "changed"))
	require.NoError(t, err)
	require.True(t, changed.Created)
	_, err = store.Enqueue(context.Background(), protocol.Envelope{ID: "message-1", From: "sender", To: "agent-a", Body: "changed", CreatedAt: "2026-08-29T12:00:00.000Z"})
	var conflict *IdempotencyConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestClaimBatchesRowsAndAckSettlesEveryItem(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), "batch-1")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	for i, body := range []string{"one", "two"} {
		_, err := store.Enqueue(context.Background(), testEnvelope("message-"+string(rune('1'+i)), body))
		require.NoError(t, err)
	}
	lease, err := store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Equal(t, "2026-08-29T12:00:30.000Z", lease.ExpiresAt)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, delivery.Items, 2)
	require.Contains(t, delivery.Message.Body, "Mailbox message batch")
	require.Equal(t, "message-1", delivery.Items[0].MessageID)
	require.Equal(t, "message-2", delivery.Items[1].MessageID)
	require.Equal(t, delivery.Items[0].AttemptNumber, delivery.AttemptNumber)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: delivery.AttemptNumber, DeliveryItems: delivery.Items}
	_, err = store.RecordInjectionRequested(context.Background(), receipt)
	require.NoError(t, err)
	_, err = store.AcknowledgeDelivery(context.Background(), receipt)
	require.NoError(t, err)
	for _, id := range []string{"message-1", "message-2"} {
		message, err := store.MessageFor(context.Background(), id)
		require.NoError(t, err)
		require.Nil(t, message)
	}
	_, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
}

func TestNackDeadLettersAndRetryRestoresTheMessage(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), "batch-1", "batch-2")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	delivery, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "message-1", AttemptNumber: 1, DeliveryItems: delivery.Items}
	require.NoError(t, func() error { _, err := store.RecordInjectionRequested(context.Background(), receipt); return err }())
	failed, err := store.NackDelivery(context.Background(), receipt, "client_error", 1)
	require.NoError(t, err)
	require.Equal(t, "dead_letter", failed.State)
	message, err := store.MessageFor(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "dead_letter", message.State)
	message, err = store.RetryDeadLetter(context.Background(), "message-1")
	require.NoError(t, err)
	require.Equal(t, "queued", message.State)
	delivery, err = store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Equal(t, 1, delivery.Items[0].AttemptNumber)
}

func TestClaimStopsAtSentinelBoundary(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"), testUUID("0003"), "batch-1", "batch-2", "batch-3")
	store := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	_, err := store.Enqueue(context.Background(), testEnvelope("normal-1", "one"))
	require.NoError(t, err)
	sentinel := testEnvelope("sentinel-1", "signal")
	sentinel.Sentinel = &protocol.SentinelDescriptor{Model: "model"}
	_, err = store.Enqueue(context.Background(), sentinel)
	require.NoError(t, err)
	_, err = store.Enqueue(context.Background(), testEnvelope("normal-2", "two"))
	require.NoError(t, err)
	_, err = store.AcquireAgentLease(context.Background(), LeaseRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	first, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "normal-1", first.Items[0].MessageID)
	receipt := ReceiptRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a", MessageID: "normal-1", AttemptNumber: 1, DeliveryItems: first.Items}
	_, err = store.RecordInjectionRequested(context.Background(), receipt)
	require.NoError(t, err)
	_, err = store.AcknowledgeDelivery(context.Background(), receipt)
	require.NoError(t, err)
	second, err := store.ClaimNextMessage(context.Background(), ClaimRequest{AgentID: "agent-a", InstanceID: "instance-a", LeaseID: "lease-a"})
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Equal(t, "sentinel-1", second.Items[0].MessageID)
}

type directTerminationFixture struct {
	store    *Store
	fence    ManagedRuntimeFence
	attempts map[string]*ManagedDirectAttempt
}

// newDirectTerminationFixture keeps volatile rows in every durable state while
// preserving ordinary rows in queued and historical states for cleanup tests.
func newDirectTerminationFixture(t *testing.T) directTerminationFixture {
	t.Helper()
	store, fence := readyManagedDirectFixture(t)
	attempts := map[string]*ManagedDirectAttempt{}
	for _, state := range []string{"pending", "prompted", "released", "acknowledged", "failed"} {
		attempts[state] = allocateTestDirectAttempt(t, store, fence, "termination-"+state)
	}
	require.NoError(t, store.MarkManagedDirectPrompted(context.Background(), *attempts["prompted"], "prompted-cursor"))
	terminal, err := store.ReleaseManagedDirect(context.Background(), *attempts["released"], "released", "")
	require.NoError(t, err)
	require.False(t, terminal)
	_, err = store.db.Exec(`UPDATE managed_direct_delivery_attempts SET state='acknowledged',acknowledged_at=? WHERE message_id=?`, formatTimestamp(time.Now()), attempts["acknowledged"].MessageID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE managed_direct_delivery_attempts SET state='failed',released_at=?,failure_code='failed' WHERE message_id=?`, formatTimestamp(time.Now()), attempts["failed"].MessageID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE messages SET state='injection_requested' WHERE id=?`, attempts["released"].MessageID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE messages SET state='dead_letter' WHERE id=?`, attempts["acknowledged"].MessageID)
	require.NoError(t, err)
	_, err = store.db.Exec(`UPDATE messages SET state='leased' WHERE id=?`, attempts["failed"].MessageID)
	require.NoError(t, err)
	for _, attempt := range attempts {
		require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, attempt.MessageID, attempt.AttemptNumber, "test", ""))
	}
	_, err = store.Enqueue(context.Background(), protocol.Envelope{ID: "termination-eligible", From: "sender", To: fence.AgentID, Body: "eligible", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = store.ResolveMessageTarget(context.Background(), "termination-eligible")
	require.NoError(t, err)
	return directTerminationFixture{store: store, fence: fence, attempts: attempts}
}

func readyManagedDirectAgent(t *testing.T, store *Store, agentID string) ManagedRuntimeFence {
	t.Helper()
	agent := ManagedAgentRecord{AgentID: agentID, Name: agentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "session-" + agentID, SessionEpoch: 1, ManifestRevision: 1, ManifestHash: testManifestHash}
	op := ManagedOperation{OperationID: "launch-" + agentID, Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-" + agentID, CanonicalBodyHash: "body-" + agentID, State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, store.ReserveManagedLaunch(context.Background(), agent, op))
	require.NoError(t, store.UpdateManagedAgentStarting(context.Background(), agentID, agent.PiSessionID, ProcessIdentityRecord{PID: 200, StartMarker: "marker-" + agentID, Incarnation: "incarnation-" + agentID}, "generation-"+agentID))
	fence := ManagedRuntimeFence{AgentID: agentID, PiSessionID: agent.PiSessionID, SessionEpoch: 1, ProcessPID: 200, ProcessStartMarker: "marker-" + agentID, ProcessIncarnation: "incarnation-" + agentID, RuntimeGeneration: "generation-" + agentID, ManifestRevision: 1, ManifestHash: testManifestHash}
	require.NoError(t, store.ConfirmManagedPolicyApplied(context.Background(), fence, DefaultManagedReadinessPolicy()))
	snapshotID := "snapshot-" + agentID
	snapshot := ManagedChildSnapshot{SnapshotID: snapshotID, OwnerSessionID: "owner-" + agentID, Sequence: 0, Canonical: []byte(fmt.Sprintf(`{"owner_session_id":"owner-%s","protocol":"mecha.children/v1","runs":[],"sequence":0,"snapshot_id":"%s"}`, agentID, snapshotID)), SHA256: testManifestHash}
	require.NoError(t, store.ConfirmManagedChildSnapshot(context.Background(), fence, snapshot))
	require.NoError(t, store.MarkManagedReadyFromSources(context.Background(), fence))
	return fence
}

func directRows(t *testing.T, store *Store, query string, args ...any) []string {
	t.Helper()
	rows, err := store.db.Query(query, args...)
	require.NoError(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	result := []string{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		require.NoError(t, rows.Scan(pointers...))
		parts := make([]string, len(values))
		for index, value := range values {
			parts[index] = fmt.Sprintf("%T:%v", value, value)
		}
		result = append(result, strings.Join(parts, "|"))
	}
	require.NoError(t, rows.Err())
	return result
}

// directTerminationLedger intentionally reads full direct-mail and ordinary
// rows, so mismatch and rollback tests catch partial writes and timestamp loss.
func directTerminationLedger(t *testing.T, store *Store, recipient, agentID string) map[string][]string {
	t.Helper()
	return map[string][]string{
		"source":      directRows(t, store, `SELECT * FROM recipient_sources WHERE recipient_name=?`, recipient),
		"attempts":    directRows(t, store, `SELECT * FROM managed_direct_delivery_attempts WHERE agent_id=? ORDER BY message_id,message_incarnation_id,attempt_number`, agentID),
		"diagnostics": directRows(t, store, `SELECT * FROM managed_direct_delivery_diagnostics WHERE agent_id=? ORDER BY diagnostic_id`, agentID),
		"messages":    directRows(t, store, `SELECT * FROM messages ORDER BY id`),
		"snapshots":   directRows(t, store, `SELECT * FROM message_target_snapshots ORDER BY snapshot_id`),
	}
}

func reserveDirectSourceForTest(ctx context.Context, store *Store, recipient, kind, owner string) error {
	return store.runImmediate(ctx, func() error {
		return store.reserveRecipientSourceTx(ctx, recipient, kind, owner)
	})
}

func directAttemptCount(t *testing.T, store *Store, agentID string) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_attempts WHERE agent_id=?`, agentID).Scan(&count))
	return count
}

func directDiagnosticCount(t *testing.T, store *Store, agentID string) int {
	t.Helper()
	var count int
	require.NoError(t, store.db.QueryRow(`SELECT COUNT(*) FROM managed_direct_delivery_diagnostics WHERE agent_id=?`, agentID).Scan(&count))
	return count
}

const directTerminationTestDeadline = 5 * time.Second

func directTerminationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), directTerminationTestDeadline)
	t.Cleanup(cancel)
	return ctx
}

func awaitDirectTerminationSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("%s: %v", name, ctx.Err())
	}
}

func awaitDirectTerminationResult(t *testing.T, ctx context.Context, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatalf("%s: %v", name, ctx.Err())
		return nil
	}
}

func waitDirectTerminationWorkers(t *testing.T, ctx context.Context, workers *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	awaitDirectTerminationSignal(t, ctx, done, "concurrent direct-mail producers did not finish")
}

func requireOldManagedRouteInvalid(t *testing.T, store *Store, attempt *ManagedDirectAttempt) {
	t.Helper()
	status, err := managedDirectRouteStatusTx(context.Background(), store.db, attempt.SnapshotID, attempt.Fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, managedDirectRouteInvalidTarget, status)
}

func requireDirectTerminationHistoricalMessages(t *testing.T, fixture directTerminationFixture, promptedSettled bool) {
	t.Helper()
	want := map[string]string{
		"pending":      "queued",
		"prompted":     "queued",
		"released":     "injection_requested",
		"acknowledged": "dead_letter",
		"failed":       "leased",
	}
	for state, wantState := range want {
		attempt := fixture.attempts[state]
		message, err := fixture.store.MessageFor(context.Background(), attempt.MessageID)
		require.NoError(t, err)
		if promptedSettled && state == "prompted" {
			require.Nil(t, message)
			_, err := snapshotForMessageTx(context.Background(), fixture.store.db, attempt.MessageID)
			require.ErrorIs(t, err, sql.ErrNoRows)
			continue
		}
		require.NotNil(t, message)
		require.Equal(t, wantState, message.State)
		snapshot, err := snapshotForMessageTx(context.Background(), fixture.store.db, attempt.MessageID)
		require.NoError(t, err)
		require.Equal(t, "managed", snapshot.SourceKind)
		require.Equal(t, fixture.fence.AgentID, snapshot.OwnerIdentity)
	}
	eligible, err := fixture.store.MessageFor(context.Background(), "termination-eligible")
	require.NoError(t, err)
	require.NotNil(t, eligible)
	require.Equal(t, "queued", eligible.State)
	snapshot, err := snapshotForMessageTx(context.Background(), fixture.store.db, eligible.ID)
	require.NoError(t, err)
	require.Equal(t, "managed", snapshot.SourceKind)
	require.Equal(t, fixture.fence.AgentID, snapshot.OwnerIdentity)
}

func requireDirectTerminationFinal(t *testing.T, fixture directTerminationFixture, laterClaim, promptedSettled bool) {
	t.Helper()
	require.Zero(t, directAttemptCount(t, fixture.store, fixture.fence.AgentID))
	require.Zero(t, directDiagnosticCount(t, fixture.store, fixture.fence.AgentID))
	requireDirectTerminationHistoricalMessages(t, fixture, promptedSettled)
	for _, attempt := range fixture.attempts {
		requireOldManagedRouteInvalid(t, fixture.store, attempt)
	}
	source, err := fixture.store.RecipientSource(context.Background(), fixture.fence.AgentID)
	require.NoError(t, err)
	if laterClaim {
		require.Equal(t, "managed", source.SourceKind)
		require.Equal(t, "later-agent", source.OwnerIdentity)
		require.Equal(t, "reserved", source.State)
		return
	}
	require.Equal(t, "managed", source.SourceKind)
	require.Equal(t, retiredManagedRecipientOwner(fixture.fence.AgentID), source.OwnerIdentity)
	require.Equal(t, "retired", source.State)
}

func TestManagedDirectRouteStatusTxHasOnlyThreeOutcomes(t *testing.T) {
	fixture := newDirectTerminationFixture(t)
	attempt := fixture.attempts["prompted"]
	status, err := managedDirectRouteStatusTx(context.Background(), fixture.store.db, attempt.SnapshotID, fixture.fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, managedDirectRouteActive, status)
	_, err = fixture.store.db.Exec(`UPDATE recipient_sources SET state='reserved' WHERE recipient_name=?`, fixture.fence.AgentID)
	require.NoError(t, err)
	status, err = managedDirectRouteStatusTx(context.Background(), fixture.store.db, attempt.SnapshotID, fixture.fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, managedDirectRouteUnavailable, status)
	_, err = fixture.store.db.Exec(`UPDATE recipient_sources SET state='active',owner_identity='later-agent' WHERE recipient_name=?`, fixture.fence.AgentID)
	require.NoError(t, err)
	status, err = managedDirectRouteStatusTx(context.Background(), fixture.store.db, attempt.SnapshotID, fixture.fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, managedDirectRouteInvalidTarget, status)
	status, err = managedDirectRouteStatusTx(context.Background(), fixture.store.db, "missing-snapshot", fixture.fence.AgentID)
	require.NoError(t, err)
	require.Equal(t, managedDirectRouteInvalidTarget, status)
}

func TestRetireManagedRecipientSourceForTerminationValidatesBeforeMutation(t *testing.T) {
	fixture := newDirectTerminationFixture(t)
	before := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
	longAgent := strings.Repeat("a", 129)
	longRecipient := strings.Repeat("a", 65)
	for _, candidate := range []struct{ agent, recipient string }{
		{"", fixture.fence.AgentID}, {"-invalid", fixture.fence.AgentID}, {"bad/agent", fixture.fence.AgentID}, {longAgent, fixture.fence.AgentID},
		{fixture.fence.AgentID, ""}, {fixture.fence.AgentID, "-invalid"}, {fixture.fence.AgentID, "bad/recipient"}, {fixture.fence.AgentID, longRecipient},
	} {
		err := fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), candidate.agent, candidate.recipient)
		require.Error(t, err)
		var mismatch *DirectMailSourceMismatchError
		require.False(t, errors.As(err, &mismatch))
		require.Equal(t, before, directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID))
	}
}

func TestRetireManagedRecipientSourceForTerminationCleansDirectStateAndPreservesHistory(t *testing.T) {
	for _, initialState := range []string{"active", "reserved"} {
		t.Run(initialState, func(t *testing.T) {
			fixture := newDirectTerminationFixture(t)
			_, err := fixture.store.db.Exec(`UPDATE recipient_sources SET state=?,updated_at='2000-01-01T00:00:00.000Z' WHERE recipient_name=?`, initialState, fixture.fence.AgentID)
			require.NoError(t, err)
			requireDirectTerminationHistoricalMessages(t, fixture, false)
			stale := *fixture.attempts["prompted"]
			otherFence := readyManagedDirectAgent(t, fixture.store, "other-agent")
			otherAttempt := allocateTestDirectAttempt(t, fixture.store, otherFence, "other-direct")
			require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), otherFence.AgentID, otherAttempt.MessageID, otherAttempt.AttemptNumber, "other", ""))
			if initialState == "active" {
				preTerminationAttempt, err := fixture.store.AllocateManagedDirectAttempt(context.Background(), fixture.fence)
				require.NoError(t, err)
				require.NotNil(t, preTerminationAttempt)
			}
			before := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			otherBefore := directTerminationLedger(t, fixture.store, otherFence.AgentID, otherFence.AgentID)

			require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID))
			source, err := fixture.store.RecipientSource(context.Background(), fixture.fence.AgentID)
			require.NoError(t, err)
			require.Empty(t, source.ConflictKind)
			require.Empty(t, source.ConflictOwner)
			require.NotEqual(t, "2000-01-01T00:00:00.000Z", source.UpdatedAt)
			_, due, err := fixture.store.NextManagedDirectRetry(context.Background(), fixture.fence.AgentID)
			require.NoError(t, err)
			require.False(t, due)
			requireDirectTerminationFinal(t, fixture, false, false)
			after := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			require.Equal(t, before["messages"], after["messages"])
			require.Equal(t, before["snapshots"], after["snapshots"])
			require.Equal(t, otherBefore["attempts"], directTerminationLedger(t, fixture.store, otherFence.AgentID, otherFence.AgentID)["attempts"])
			require.Equal(t, otherBefore["diagnostics"], directTerminationLedger(t, fixture.store, otherFence.AgentID, otherFence.AgentID)["diagnostics"])

			require.NoError(t, fixture.store.AcknowledgeManagedDirect(context.Background(), stale, "late-receipt", true))
			require.NoError(t, fixture.store.AcknowledgeManagedDirectRecovered(context.Background(), stale, "late-recovered-receipt"))
			attempt, err := fixture.store.AllocateManagedDirectAttempt(context.Background(), fixture.fence)
			require.NoError(t, err)
			require.Nil(t, attempt)
			require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), fixture.fence.AgentID, stale.MessageID, stale.AttemptNumber, "late", ""))
			require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), fixture.fence.AgentID, "", 0, "late", ""))
			requireDirectTerminationFinal(t, fixture, false, false)
		})
	}
}

func TestRetireManagedRecipientSourceForTerminationPromotesOnlyRecordedTmux(t *testing.T) {
	cases := []struct {
		name       string
		fixtureSQL string
	}{
		{"managed-primary", `UPDATE recipient_sources SET source_kind='managed',owner_identity='source-agent',state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-owner',updated_at='2000-01-01T00:00:00.000Z' WHERE recipient_name='source-agent'`},
		{"tmux-primary", `UPDATE recipient_sources SET source_kind='tmux',owner_identity='tmux-owner',state='conflicted',conflict_source_kind='managed',conflict_owner_identity='source-agent',updated_at='2000-01-01T00:00:00.000Z' WHERE recipient_name='source-agent'`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectTerminationFixture(t)
			_, err := fixture.store.db.Exec(test.fixtureSQL)
			require.NoError(t, err)
			before := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID))
			source, err := fixture.store.RecipientSource(context.Background(), fixture.fence.AgentID)
			require.NoError(t, err)
			require.Equal(t, "tmux", source.SourceKind)
			require.Equal(t, "tmux-owner", source.OwnerIdentity)
			require.Equal(t, "active", source.State)
			require.Empty(t, source.ConflictKind)
			require.Empty(t, source.ConflictOwner)
			require.Zero(t, directAttemptCount(t, fixture.store, fixture.fence.AgentID))
			require.Zero(t, directDiagnosticCount(t, fixture.store, fixture.fence.AgentID))
			after := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			require.Equal(t, before["messages"], after["messages"])
			require.Equal(t, before["snapshots"], after["snapshots"])
			for _, attempt := range fixture.attempts {
				requireOldManagedRouteInvalid(t, fixture.store, attempt)
			}
			messageID := "post-conflict-" + test.name
			_, err = fixture.store.Enqueue(context.Background(), protocol.Envelope{ID: messageID, From: "sender", To: fixture.fence.AgentID, Body: messageID, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
			require.NoError(t, err)
			target, err := fixture.store.ResolveMessageTarget(context.Background(), messageID)
			require.NoError(t, err)
			require.Equal(t, "tmux", target.SourceKind)
			require.Equal(t, "tmux-owner", target.OwnerIdentity)
		})
	}
}

func TestRetireManagedRecipientSourceForTerminationRejectsEveryOtherSourceShape(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"active-managed-other", `UPDATE recipient_sources SET source_kind='managed',owner_identity='other-agent',state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"reserved-managed-other", `UPDATE recipient_sources SET source_kind='managed',owner_identity='other-agent',state='reserved',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"retired-managed-raw-owner", `UPDATE recipient_sources SET source_kind='managed',owner_identity='source-agent',state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"retired-managed-other-sentinel", `UPDATE recipient_sources SET source_kind='managed',owner_identity='retired:other',state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"active-tmux", `UPDATE recipient_sources SET source_kind='tmux',owner_identity='tmux-owner',state='active',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"reserved-tmux", `UPDATE recipient_sources SET source_kind='tmux',owner_identity='tmux-owner',state='reserved',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"retired-tmux", `UPDATE recipient_sources SET source_kind='tmux',owner_identity='tmux-owner',state='retired',conflict_source_kind=NULL,conflict_owner_identity=NULL WHERE recipient_name='source-agent'`},
		{"conflicted-managed-other", `UPDATE recipient_sources SET source_kind='managed',owner_identity='other-agent',state='conflicted',conflict_source_kind='tmux',conflict_owner_identity='tmux-owner' WHERE recipient_name='source-agent'`},
		{"conflicted-tmux-other-secondary", `UPDATE recipient_sources SET source_kind='tmux',owner_identity='tmux-owner',state='conflicted',conflict_source_kind='managed',conflict_owner_identity='other-agent' WHERE recipient_name='source-agent'`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectTerminationFixture(t)
			_, err := fixture.store.db.Exec(test.query)
			require.NoError(t, err)
			before := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			err = fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID)
			var mismatch *DirectMailSourceMismatchError
			require.ErrorAs(t, err, &mismatch)
			require.Equal(t, "DIRECT_MAIL_SOURCE_MISMATCH", mismatch.Code())
			require.Equal(t, before, directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID))
		})
	}
}

func TestRetireManagedRecipientSourceForTerminationIsIdempotentAndReusesOnlyForAnotherOwner(t *testing.T) {
	fixture := newDirectTerminationFixture(t)
	require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID))
	require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID))
	stale := fixture.attempts["prompted"]
	requireOldManagedRouteInvalid(t, fixture.store, stale)
	var conflict *RecipientSourceConflictError
	err := reserveDirectSourceForTest(context.Background(), fixture.store, fixture.fence.AgentID, "managed", fixture.fence.AgentID)
	require.ErrorAs(t, err, &conflict)
	require.NoError(t, reserveDirectSourceForTest(context.Background(), fixture.store, fixture.fence.AgentID, "managed", "later-agent"))
	requireDirectTerminationFinal(t, fixture, true, false)
	require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), fixture.fence.AgentID, stale.MessageID, stale.AttemptNumber, "late", ""))
	require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), fixture.fence.AgentID, "", 0, "late", ""))
	requireDirectTerminationFinal(t, fixture, true, false)
}

func TestRetireManagedRecipientSourceForTerminationCleansReachableMissingSourceHistory(t *testing.T) {
	store, fence := readyManagedDirectFixture(t)
	states := map[string]string{
		"queued":              "queued",
		"injection-requested": "injection_requested",
		"dead-letter":         "dead_letter",
		"leased":              "leased",
	}
	for name, state := range states {
		messageID := "missing-source-" + name
		_, err := store.Enqueue(context.Background(), protocol.Envelope{ID: messageID, From: "sender", To: fence.AgentID, Body: messageID, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
		require.NoError(t, err)
		if state != "queued" {
			_, err = store.db.Exec(`UPDATE messages SET state=? WHERE id=?`, state, messageID)
			require.NoError(t, err)
		}
		require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, messageID, 1, "before-delete", ""))
	}
	require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, "", 0, "before-delete", ""))
	// Attempts require immutable snapshots, and snapshots retain their source by
	// foreign key. A valid missing-source store can therefore contain ordinary
	// history and diagnostics, but not an old direct attempt or snapshot.
	_, err := store.db.Exec(`DELETE FROM recipient_sources WHERE recipient_name=?`, fence.AgentID)
	require.NoError(t, err)
	before := directTerminationLedger(t, store, fence.AgentID, fence.AgentID)
	require.Empty(t, before["source"])
	require.Empty(t, before["attempts"])
	require.Empty(t, before["snapshots"])
	require.NotEmpty(t, before["diagnostics"])
	require.NoError(t, store.RetireManagedRecipientSourceForTermination(context.Background(), fence.AgentID, fence.AgentID))
	require.NoError(t, store.RetireManagedRecipientSourceForTermination(context.Background(), fence.AgentID, fence.AgentID))
	after := directTerminationLedger(t, store, fence.AgentID, fence.AgentID)
	require.Empty(t, after["source"])
	require.Empty(t, after["attempts"])
	require.Empty(t, after["diagnostics"])
	require.Equal(t, before["messages"], after["messages"])
	require.Empty(t, after["snapshots"])
}

func TestRetireManagedRecipientSourceForTerminationDoesNotRecreateDiagnosticAfterPriorSettlement(t *testing.T) {
	fixture := newDirectTerminationFixture(t)
	settled := *fixture.attempts["prompted"]
	require.NoError(t, fixture.store.AcknowledgeManagedDirect(context.Background(), settled, "receipt", true))
	message, err := fixture.store.MessageFor(context.Background(), settled.MessageID)
	require.NoError(t, err)
	require.Nil(t, message)
	require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID))
	require.NoError(t, fixture.store.RecordManagedDirectDiagnostic(context.Background(), fixture.fence.AgentID, settled.MessageID, settled.AttemptNumber, "late", ""))
	require.Zero(t, directDiagnosticCount(t, fixture.store, fixture.fence.AgentID))
}

func TestRetireManagedRecipientSourceForTerminationRollsBackEveryCleanupBoundary(t *testing.T) {
	cases := []struct {
		name string
		hook func(error) *managedDirectTerminationTestHook
	}{
		{"after-source-disposition", func(err error) *managedDirectTerminationTestHook {
			return &managedDirectTerminationTestHook{afterSourceDisposition: func() error { return err }}
		}},
		{"after-attempt-deletion", func(err error) *managedDirectTerminationTestHook {
			return &managedDirectTerminationTestHook{afterAttemptsDeleted: func() error { return err }}
		}},
		{"after-diagnostic-deletion", func(err error) *managedDirectTerminationTestHook {
			return &managedDirectTerminationTestHook{afterDiagnosticsDeleted: func() error { return err }}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDirectTerminationFixture(t)
			_, err := fixture.store.db.Exec(`UPDATE recipient_sources SET updated_at='2000-01-01T00:00:00.000Z' WHERE recipient_name=?`, fixture.fence.AgentID)
			require.NoError(t, err)
			before := directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID)
			injected := errors.New("direct-mail rollback")
			restore := fixture.store.setManagedDirectTerminationTestHook(test.hook(injected))
			err = fixture.store.RetireManagedRecipientSourceForTermination(context.Background(), fixture.fence.AgentID, fixture.fence.AgentID)
			restore()
			require.ErrorIs(t, err, injected)
			require.Equal(t, before, directTerminationLedger(t, fixture.store, fixture.fence.AgentID, fixture.fence.AgentID))
		})
	}
}

func TestRetireManagedRecipientSourceForTerminationGuardsMessageLessDiagnostics(t *testing.T) {
	store, fence := readyManagedDirectFixture(t)
	require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, "", 0, "active", ""))
	require.Equal(t, 1, directDiagnosticCount(t, store, fence.AgentID))
	_, err := store.db.Exec(`UPDATE recipient_sources SET state='reserved' WHERE recipient_name=?`, fence.AgentID)
	require.NoError(t, err)
	require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, "", 0, "reserved", ""))
	require.Equal(t, 2, directDiagnosticCount(t, store, fence.AgentID))
	_, err = store.db.Exec(`UPDATE recipient_sources SET owner_identity=?,state='retired' WHERE recipient_name=?`, retiredManagedRecipientOwner(fence.AgentID), fence.AgentID)
	require.NoError(t, err)
	require.NoError(t, store.RecordManagedDirectDiagnostic(context.Background(), fence.AgentID, "", 0, "retired", ""))
	require.Equal(t, 2, directDiagnosticCount(t, store, fence.AgentID))
}

func TestRetireManagedRecipientSourceForTerminationInterleavings(t *testing.T) {
	type companion struct {
		name            string
		run             func(context.Context, directTerminationFixture) error
		settlesPrompted bool
	}
	companions := []companion{
		{"allocate", func(ctx context.Context, f directTerminationFixture) error {
			_, err := f.store.AllocateManagedDirectAttempt(ctx, f.fence)
			return err
		}, false},
		{"release", func(ctx context.Context, f directTerminationFixture) error {
			_, err := f.store.ReleaseManagedDirect(ctx, *f.attempts["pending"], "late", "")
			return err
		}, false},
		{"acknowledge", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.AcknowledgeManagedDirect(ctx, *f.attempts["prompted"], "receipt", true)
		}, true},
		{"recovered-acknowledge", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.AcknowledgeManagedDirectRecovered(ctx, *f.attempts["prompted"], "receipt")
		}, true},
		{"diagnostic", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.RecordManagedDirectDiagnostic(ctx, f.fence.AgentID, f.attempts["pending"].MessageID, 1, "late", "")
		}, false},
		{"later-reservation", func(ctx context.Context, f directTerminationFixture) error {
			return reserveDirectSourceForTest(ctx, f.store, f.fence.AgentID, "managed", "later-agent")
		}, false},
	}
	requireAfterHook := func(t *testing.T, name string, err error) {
		t.Helper()
		if name != "release" {
			require.NoError(t, err)
			return
		}
		var inactive *ManagedDirectNotActiveError
		require.ErrorAs(t, err, &inactive)
	}
	requireBeforeHook := func(t *testing.T, name string, err error) {
		t.Helper()
		if name != "later-reservation" {
			require.NoError(t, err)
			return
		}
		var conflict *RecipientSourceConflictError
		require.ErrorAs(t, err, &conflict)
	}
	for _, companion := range companions {
		t.Run(companion.name, func(t *testing.T) {
			t.Run("hook-then-companion", func(t *testing.T) {
				ctx := directTerminationContext(t)
				fixture := newDirectTerminationFixture(t)
				require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(ctx, fixture.fence.AgentID, fixture.fence.AgentID))
				requireAfterHook(t, companion.name, companion.run(ctx, fixture))
				requireDirectTerminationFinal(t, fixture, companion.name == "later-reservation", false)
			})
			t.Run("companion-then-hook", func(t *testing.T) {
				ctx := directTerminationContext(t)
				fixture := newDirectTerminationFixture(t)
				requireBeforeHook(t, companion.name, companion.run(ctx, fixture))
				require.NoError(t, fixture.store.RetireManagedRecipientSourceForTermination(ctx, fixture.fence.AgentID, fixture.fence.AgentID))
				requireDirectTerminationFinal(t, fixture, false, companion.settlesPrompted)
			})
			t.Run("concurrent-after-source-disposition", func(t *testing.T) {
				ctx := directTerminationContext(t)
				fixture := newDirectTerminationFixture(t)
				entered := make(chan struct{})
				release := make(chan struct{})
				var releaseOnce sync.Once
				defer releaseOnce.Do(func() { close(release) })
				restore := fixture.store.setManagedDirectTerminationTestHook(&managedDirectTerminationTestHook{afterSourceDisposition: func() error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}})
				defer restore()
				hookDone := make(chan error, 1)
				go func() {
					hookDone <- fixture.store.RetireManagedRecipientSourceForTermination(ctx, fixture.fence.AgentID, fixture.fence.AgentID)
				}()
				awaitDirectTerminationSignal(t, ctx, entered, "termination hook did not reach source disposition")
				started := make(chan struct{})
				companionDone := make(chan error, 1)
				go func() {
					close(started)
					companionDone <- companion.run(ctx, fixture)
				}()
				awaitDirectTerminationSignal(t, ctx, started, "concurrent companion did not start")
				releaseOnce.Do(func() { close(release) })
				require.NoError(t, awaitDirectTerminationResult(t, ctx, hookDone, "termination hook did not finish"))
				requireAfterHook(t, companion.name, awaitDirectTerminationResult(t, ctx, companionDone, "concurrent companion did not finish"))
				requireDirectTerminationFinal(t, fixture, companion.name == "later-reservation", false)
			})
		})
	}
}

func TestRetireManagedRecipientSourceForTerminationMixedConcurrentProducers(t *testing.T) {
	type concurrentOperation struct {
		name string
		run  func(context.Context, directTerminationFixture) error
	}
	operations := []concurrentOperation{
		{"allocate", func(ctx context.Context, f directTerminationFixture) error {
			_, err := f.store.AllocateManagedDirectAttempt(ctx, f.fence)
			return err
		}},
		{"release", func(ctx context.Context, f directTerminationFixture) error {
			_, err := f.store.ReleaseManagedDirect(ctx, *f.attempts["pending"], "late", "")
			return err
		}},
		{"acknowledge", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.AcknowledgeManagedDirect(ctx, *f.attempts["prompted"], "receipt", true)
		}},
		{"recovered-acknowledge", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.AcknowledgeManagedDirectRecovered(ctx, *f.attempts["prompted"], "receipt")
		}},
		{"diagnostic", func(ctx context.Context, f directTerminationFixture) error {
			return f.store.RecordManagedDirectDiagnostic(ctx, f.fence.AgentID, f.attempts["pending"].MessageID, 1, "late", "")
		}},
		{"later-reservation", func(ctx context.Context, f directTerminationFixture) error {
			return reserveDirectSourceForTest(ctx, f.store, f.fence.AgentID, "managed", "later-agent")
		}},
	}
	for iteration := 0; iteration < 12; iteration++ {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			ctx := directTerminationContext(t)
			fixture := newDirectTerminationFixture(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			restore := fixture.store.setManagedDirectTerminationTestHook(&managedDirectTerminationTestHook{afterSourceDisposition: func() error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}})
			defer restore()
			hookDone := make(chan error, 1)
			go func() {
				hookDone <- fixture.store.RetireManagedRecipientSourceForTermination(ctx, fixture.fence.AgentID, fixture.fence.AgentID)
			}()
			awaitDirectTerminationSignal(t, ctx, entered, "termination hook did not reach source disposition")
			start := make(chan struct{})
			type result struct {
				name string
				err  error
			}
			results := make(chan result, len(operations))
			var workers sync.WaitGroup
			for _, operation := range operations {
				workers.Add(1)
				go func(candidate concurrentOperation) {
					defer workers.Done()
					select {
					case <-start:
						results <- result{name: candidate.name, err: candidate.run(ctx, fixture)}
					case <-ctx.Done():
						results <- result{name: candidate.name, err: ctx.Err()}
					}
				}(operation)
			}
			close(start)
			releaseOnce.Do(func() { close(release) })
			require.NoError(t, awaitDirectTerminationResult(t, ctx, hookDone, "termination hook did not finish"))
			waitDirectTerminationWorkers(t, ctx, &workers)
			close(results)
			for result := range results {
				if result.name != "release" {
					require.NoError(t, result.err, result.name)
					continue
				}
				var inactive *ManagedDirectNotActiveError
				require.ErrorAs(t, result.err, &inactive)
			}
			requireDirectTerminationFinal(t, fixture, true, false)
		})
	}
}

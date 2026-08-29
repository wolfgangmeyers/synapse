package transport

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestLifecycleSweepPublishesExpiredMessageAndDeliveryRecovery(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	var events []map[string]any
	router, err := New(Options{Store: durable, Clock: clock, SweepHeartbeatInterval: time.Hour, EventLogger: func(event string, fields map[string]any) {
		record := map[string]any{"event": event}
		for key, value := range fields {
			record[key] = value
		}
		events = append(events, record)
	}})
	require.NoError(t, err)

	_, err = durable.Enqueue(context.Background(), protocol.Envelope{ID: "expired", From: "sender", To: "worker", Body: "body", CreatedAt: "2026-08-29T12:00:00.000Z"})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(time.Hour+time.Second))
	require.NoError(t, router.Listen(context.Background()))
	defer router.Close(context.Background())
	result, err := router.sweep(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 3)
	require.Equal(t, "message_expired", events[0]["event"])
	require.Equal(t, "expired", events[0]["message_id"])
	require.Equal(t, "lifecycle_sweep_completed", events[1]["event"])
	require.Equal(t, 1, events[1]["expired_message_count"])
	require.Equal(t, 1, events[1]["stale_agent_count"])
	require.Equal(t, "router_started", events[2]["event"])
	require.Equal(t, 0, result.StaleAgents)

	identity := store.LeaseRequest{AgentID: "retry-worker", InstanceID: "retry-instance", LeaseID: "retry-lease", LeaseDuration: time.Minute}
	_, err = durable.AcquireAgentLease(context.Background(), identity)
	require.NoError(t, err)
	_, err = durable.Enqueue(context.Background(), protocol.Envelope{ID: "retry", From: "sender", To: identity.AgentID, Body: "body", CreatedAt: "2026-08-29T13:00:01.000Z"})
	require.NoError(t, err)
	delivery, err := durable.ClaimNextMessage(context.Background(), store.ClaimRequest{AgentID: identity.AgentID, InstanceID: identity.InstanceID, LeaseID: identity.LeaseID, DeliveryLeaseDuration: time.Second})
	require.NoError(t, err)
	require.NotNil(t, delivery)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err = router.sweep(context.Background())
	require.NoError(t, err)
	message, err := durable.MessageFor(context.Background(), "retry")
	require.NoError(t, err)
	require.Equal(t, "queued", message.State)
	seenTimeout := false
	for _, event := range events {
		if event["event"] == "delivery_timed_out" && event["message_id"] == "retry" {
			seenTimeout = true
		}
	}
	require.True(t, seenTimeout)
	lastSweep := events[len(events)-1]
	require.Equal(t, "lifecycle_sweep_completed", lastSweep["event"])
	require.Equal(t, 1, lastSweep["timed_out_delivery_count"])
}

func TestRouterSweepCleansAttachmentsOnStartupAndShutdown(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	directory := t.TempDir()
	attachmentStore, err := attachments.New(attachments.Options{Directory: filepath.Join(directory, "attachments"), Clock: clock, Retention: time.Hour})
	require.NoError(t, err)
	pending, err := attachmentStore.WriteTemp("old", 1, attachments.Input{MIMEType: "image/png", Bytes: tinyPNG()})
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(directory, "attachments", pending.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))

	durable, err := store.Open(context.Background(), filepath.Join(directory, "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock, Attachments: attachmentStore, SweepInterval: 250 * time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, router.Listen(context.Background()))
	_, err = os.Stat(filepath.Join(directory, "attachments", pending.ID))
	require.ErrorIs(t, err, os.ErrNotExist)

	periodic, err := attachmentStore.WriteTemp("periodic", 1, attachments.Input{MIMEType: "image/png", Bytes: tinyPNG()})
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(directory, "attachments", periodic.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, statErr := os.Stat(filepath.Join(directory, "attachments", periodic.ID))
		if os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("periodic attachment sweep did not remove expired file")
		}
		time.Sleep(time.Millisecond)
	}
	final, err := attachmentStore.WriteTemp("final", 1, attachments.Input{MIMEType: "image/png", Bytes: tinyPNG()})
	require.NoError(t, err)
	require.NoError(t, os.Chtimes(filepath.Join(directory, "attachments", final.ID), start.Add(-2*time.Hour), start.Add(-2*time.Hour)))
	require.NoError(t, router.Close(context.Background()))
	require.NoError(t, router.Close(context.Background()))
	_, err = os.Stat(filepath.Join(directory, "attachments", final.ID))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLifecyclePrunePublishesTerminalProjectionToObservers(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	seedLifecycleChild(t, database, start.Add(-8*24*time.Hour))

	var sent []map[string]any
	router, err := New(Options{Store: durable, Clock: clock, EventLogger: func(string, map[string]any) {}})
	require.NoError(t, err)
	observer := &session{router: router, agentID: "observer", conversationSync: "v1", lifecycleEnabled: true, sendOverride: func(fields map[string]any) error {
		sent = append(sent, fields)
		return nil
	}}
	router.mu.Lock()
	router.convos["observer"] = map[string]*session{"observer-instance": observer}
	router.lifecycleState["parent-a"] = lifecycleAgent{version: 1, subagents: []map[string]any{{"id": "child-a", "type": "worker"}}}
	router.mu.Unlock()

	_, err = router.sweep(context.Background())
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.Equal(t, "subagent_state_update", sent[0]["type"])
	agent := sent[0]["agent"].(map[string]any)
	require.Equal(t, "parent-a", agent["agent_id"])
	require.Equal(t, 0, agent["subagent_count"])
	require.Empty(t, agent["subagents"])
}

func TestLifecyclePublishDoesNotRevertNewerConcurrentState(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, EventLogger: func(string, map[string]any) {}})
	require.NoError(t, err)

	var sent []map[string]any
	observer := &session{router: router, agentID: "observer", conversationSync: "v1", lifecycleEnabled: true, sendOverride: func(fields map[string]any) error {
		sent = append(sent, fields)
		return nil
	}}
	router.mu.Lock()
	router.convos["observer"] = map[string]*session{"observer-instance": observer}
	router.lifecycleState["parent-a"] = lifecycleAgent{version: 2, subagents: []map[string]any{{"id": "new-child", "state": "terminal"}}}
	router.nextVersion = 2
	router.mu.Unlock()

	// This is the ordering produced when a newer lifecycle event commits while
	// an older sweep projection is still being prepared.
	router.publishLifecycleUpdates(context.Background(), []store.LifecycleUpdate{{ParentAgentID: "parent-a", RouterVersion: 1}})

	router.mu.RLock()
	state := router.lifecycleState["parent-a"]
	router.mu.RUnlock()
	require.Equal(t, int64(2), state.version)
	require.Empty(t, sent)
}

func TestExpiredLeaseNotifiesAndClosesOnlyMatchingCurrentSession(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	var notified int
	var notification map[string]any
	var closed int
	stale := &session{router: router, agentID: "stale", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: "lease-a", sendOverride: func(fields map[string]any) error { notified++; notification = fields; return nil }, closeOverride: func(websocket.StatusCode, string) error { closed++; return nil }}
	replacement := &session{router: router, agentID: "stale", instanceID: "instance-b", deliveryInstance: "instance-b", leaseID: "lease-b", sendOverride: func(map[string]any) error { return nil }, closeOverride: func(websocket.StatusCode, string) error { t.Fatal("replacement session was closed"); return nil }}
	router.mu.Lock()
	router.sessions["stale"] = stale
	router.mu.Unlock()
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "stale", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Second})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(2*time.Second))
	_, err = router.sweep(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, notified)
	require.Equal(t, "lease_expired", notification["code"])
	require.Equal(t, 1, closed)

	router.mu.Lock()
	router.sessions["stale"] = replacement
	router.mu.Unlock()
	router.closeExpiredLeaseSessions([]store.ExpiredLease{{AgentID: "stale", InstanceID: "instance-a", LeaseID: "lease-a"}})
}

func TestAttachmentSweepFailureDoesNotSuppressCoreSweep(t *testing.T) {
	start := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	directory := t.TempDir()
	attachmentStore, err := attachments.New(attachments.Options{Directory: filepath.Join(directory, "attachments"), Clock: clock})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Join(directory, "attachments"), 0o755))
	database := filepath.Join(directory, "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	_, err = durable.Enqueue(context.Background(), protocol.Envelope{ID: "expired", From: "sender", To: "worker", Body: "body", CreatedAt: timestamp(start)})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(time.Hour+time.Second))
	var events []string
	router, err := New(Options{Store: durable, Clock: clock, SweepHeartbeatInterval: time.Hour, Attachments: attachmentStore, EventLogger: func(event string, _ map[string]any) { events = append(events, event) }})
	require.NoError(t, err)
	_, err = router.sweep(context.Background())
	require.NoError(t, err)
	require.Contains(t, events, "router_error")
	require.Contains(t, events, "message_expired")
	require.Contains(t, events, "lifecycle_sweep_completed")
}

func TestConcurrentCloseWaitsForRouterStoppedRecord(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var events []string
	router, err := New(Options{Store: durable, EventLogger: func(event string, _ map[string]any) {
		if event != "router_stopped" {
			return
		}
		events = append(events, event)
		once.Do(func() { close(started) })
		<-release
	}})
	require.NoError(t, err)
	results := make(chan error, 2)
	go func() { results <- router.Close(context.Background()) }()
	<-started
	go func() { results <- router.Close(context.Background()) }()
	select {
	case <-results:
		t.Fatal("Close returned before router_stopped was released")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	require.Equal(t, []string{"router_stopped"}, events)
}

func seedLifecycleChild(t *testing.T, database string, seenAt time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	defer db.Close()
	epoch := "123e4567-e89b-12d3-a456-426614174000"
	old := timestamp(seenAt)
	_, err = db.Exec(`INSERT INTO subagent_lifecycle_producers (parent_agent_id, instance_id, parent_session_id, producer_epoch, lease_id, highest_client_sequence, last_seen_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)`, "parent-a", "instance-a", "session-a", epoch, "lease-a", old, old, old)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO subagent_lifecycle_state (parent_agent_id, subagent_id, instance_id, parent_session_id, producer_epoch, type, description, status, state, started_at, updated_at, router_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'running', ?, ?, 0)`, "parent-a", "child-a", "instance-a", "session-a", epoch, "worker", "child", "running", old, old)
	require.NoError(t, err)
}

func tinyPNG() []byte {
	return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1}
}

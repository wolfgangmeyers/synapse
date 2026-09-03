package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func TestLifecycleFrameNumericSpellingsMatchNode(t *testing.T) {
	for _, number := range []string{"1.0", "1e0", "9.007199254740991e15"} {
		value, ok := frameInt(map[string]json.RawMessage{"after_version": json.RawMessage(number)}, "after_version")
		require.True(t, ok, number)
		require.Equal(t, map[string]int64{"1.0": 1, "1e0": 1, "9.007199254740991e15": 9007199254740991}[number], value, number)
	}
	for _, number := range []string{"1.5", "9007199254740992", "9.007199254740992e15"} {
		_, ok := frameInt(map[string]json.RawMessage{"client_sequence": json.RawMessage(number)}, "client_sequence")
		require.False(t, ok, number)
	}

	frame := map[string]json.RawMessage{
		"agent_id":          json.RawMessage(`"parent-a"`),
		"instance_id":       json.RawMessage(`"instance-a"`),
		"parent_session_id": json.RawMessage(`"session-a"`),
		"lease_id":          json.RawMessage(`"lease-a"`),
		"producer_epoch":    json.RawMessage(`"123e4567-e89b-42d3-a456-426614174000"`),
		"client_sequence":   json.RawMessage("1.0"),
		"event_id":          json.RawMessage(`"event-a"`),
		"lifecycle_event":   json.RawMessage(`"started"`),
		"subagent":          json.RawMessage(`{"id":"child-a"}`),
	}
	candidate, err := lifecycleCandidateFromFrame(frame)
	require.NoError(t, err)
	require.Equal(t, int64(1), candidate.ClientSequence)
}

func TestTerminalOnlyLifecycleProducerSurvivesRouterRestartAndAgentsSnapshot(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{Clock: clock})
	require.NoError(t, err)

	base := store.LifecycleCandidate{
		ParentAgentID: "parent-a", InstanceID: "instance-a", ParentSessionID: "session-a", LeaseID: "lease-a",
		ProducerEpoch: "123e4567-e89b-42d3-a456-426614174000",
	}
	snapshot := base
	snapshot.ClientSequence, snapshot.EventID, snapshot.Snapshot = 1, "snapshot-1", true
	snapshot.Subagents = []store.LifecycleChild{{ID: "child-a", Type: "worker", Description: "child", Status: "running"}}
	_, err = durable.ApplySubagentLifecycle(context.Background(), snapshot, false)
	require.NoError(t, err)
	terminal := base
	terminal.ClientSequence, terminal.EventID, terminal.EventKind = 2, "terminal-1", "terminal"
	terminal.Subagent = store.LifecycleChild{ID: "child-a"}
	terminal.TerminalReason = "completed"
	_, err = durable.ApplySubagentLifecycle(context.Background(), terminal, false)
	require.NoError(t, err)
	lease, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "parent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)

	first, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	firstSession := &session{router: first, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: lease.LeaseID, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	first.mu.Lock()
	first.sessions[firstSession.agentID] = firstSession
	first.mu.Unlock()
	firstSnapshot := first.connectedAgents(context.Background(), false)
	require.Equal(t, int64(2), firstSnapshot["activity_snapshot_version"])
	firstAgent := firstSnapshot["agents"].([]map[string]any)[0]
	require.Equal(t, int64(2), firstAgent["subagent_version"])
	require.Equal(t, 0, firstAgent["subagent_count"])
	require.Empty(t, firstAgent["subagents"])

	require.NoError(t, first.Close(context.Background()))
	second, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	defer second.Close(context.Background())
	secondSession := &session{router: second, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: lease.LeaseID, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	second.mu.Lock()
	second.sessions[secondSession.agentID] = secondSession
	second.mu.Unlock()
	secondSnapshot := second.connectedAgents(context.Background(), false)
	require.Equal(t, int64(2), secondSnapshot["activity_snapshot_version"])
	secondAgent := secondSnapshot["agents"].([]map[string]any)[0]
	require.Equal(t, int64(2), secondAgent["subagent_version"])
	require.Equal(t, 0, secondAgent["subagent_count"])
	require.Empty(t, secondAgent["subagents"])
	require.NoError(t, durable.Close())
}

func TestLifecycleFramePersistsAndSuppressesDuplicateBroadcasts(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "parent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	var sent []map[string]any
	s := &session{
		router: router, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: "lease-a",
		lifecycleEnabled: true, sendOverride: func(fields map[string]any) error { sent = append(sent, fields); return nil },
	}
	router.mu.Lock()
	router.sessions[s.agentID] = s
	router.convos[s.agentID] = map[string]*session{s.instanceID: s}
	router.mu.Unlock()
	frame := func(eventID string, sequence int64, kind string) map[string]json.RawMessage {
		values := map[string]any{
			"type": "subagent_lifecycle_event", "frame_id": "frame-" + eventID, "agent_id": "parent-a",
			"instance_id": "instance-a", "parent_session_id": "session-a", "lease_id": "lease-a",
			"producer_epoch": "123e4567-e89b-42d3-a456-426614174000", "client_sequence": sequence,
			"event_id": eventID,
		}
		if kind == "" {
			// Node producers establish the producer with a snapshot before events.
			values["subagents"] = []map[string]any{}
		} else {
			values["lifecycle_event"] = kind
			values["subagent"] = map[string]any{"id": "child-a", "type": "worker", "description": "child", "status": "running"}
			if kind == "terminal" {
				values["terminal_reason"] = "completed"
			}
		}
		result := make(map[string]json.RawMessage, len(values))
		for key, value := range values {
			raw, marshalErr := json.Marshal(value)
			require.NoError(t, marshalErr)
			result[key] = raw
		}
		return result
	}
	require.NoError(t, router.lifecycleFrame(s, frame("snapshot-1", 1, "")))
	sent = nil
	require.NoError(t, router.lifecycleFrame(s, frame("event-1", 2, "started")))
	require.Len(t, sent, 2)
	require.Equal(t, "subagent_lifecycle_acknowledged", sent[0]["type"])
	require.Equal(t, "subagent_state_update", sent[1]["type"])
	sent = nil
	require.NoError(t, router.lifecycleFrame(s, frame("event-1", 2, "started")))
	require.Len(t, sent, 1)
	require.Equal(t, "duplicate", sent[0]["disposition"])

	sent = nil
	require.NoError(t, router.lifecycleFrame(s, frame("event-2", 3, "terminal")))
	require.Len(t, sent, 2)
	require.Equal(t, "applied", sent[0]["disposition"])
	require.Equal(t, "subagent_state_update", sent[1]["type"])
	require.Equal(t, int64(2), sent[0]["router_version"])

	sent = nil
	resync := frame("resync", 1, "started")
	resync["type"], _ = json.Marshal("subagent_state_resync")
	resync["after_version"], _ = json.Marshal(int64(1))
	delete(resync, "lifecycle_event")
	delete(resync, "subagent")
	require.NoError(t, router.syncResync(s, resync))
	require.Len(t, sent, 1)
	require.Equal(t, "subagent_state_replay", sent[0]["type"])
	updates := sent[0]["updates"].([]map[string]any)
	require.Len(t, updates, 1)
	require.Equal(t, int64(2), updates[0]["subagent_version"])
}

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

func TestLifecycleDiagnosticsUseOnlyBoundedIdentifiersCountsAndReasons(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	var events []map[string]any
	router, err := New(Options{Store: durable, Clock: clock, EventLogger: func(event string, fields map[string]any) {
		record := map[string]any{"event": event}
		for key, value := range fields {
			record[key] = value
		}
		events = append(events, record)
	}})
	require.NoError(t, err)
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "parent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	var sent []map[string]any
	sender := &session{
		router: router, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: "lease-a",
		lifecycleEnabled: true, sendOverride: func(fields map[string]any) error { sent = append(sent, fields); return nil },
	}
	observer := &session{router: router, agentID: "observer", instanceID: "observer-instance", lifecycleEnabled: true, sendOverride: func(map[string]any) error { return errors.New("observer write failed") }}
	router.mu.Lock()
	router.sessions[sender.agentID] = sender
	router.convos[sender.agentID] = map[string]*session{sender.instanceID: sender}
	router.convos[observer.agentID] = map[string]*session{observer.instanceID: observer}
	router.mu.Unlock()
	frame := func(eventID string, sequence int64, kind string) map[string]json.RawMessage {
		values := map[string]any{
			"type": "subagent_lifecycle_event", "frame_id": "frame-" + eventID, "agent_id": "parent-a",
			"instance_id": "instance-a", "parent_session_id": "session-a", "lease_id": "lease-a",
			"producer_epoch": "123e4567-e89b-42d3-a456-426614174000", "client_sequence": sequence,
			"event_id": eventID,
		}
		if kind == "" {
			values["subagents"] = []map[string]any{}
		} else {
			values["lifecycle_event"] = kind
			values["subagent"] = map[string]any{"id": "child-a", "type": "worker", "description": "private detail", "status": "running"}
		}
		result := make(map[string]json.RawMessage, len(values))
		for key, value := range values {
			raw, marshalErr := json.Marshal(value)
			require.NoError(t, marshalErr)
			result[key] = raw
		}
		return result
	}
	require.NoError(t, router.lifecycleFrame(sender, frame("snapshot-1", 1, "")))
	require.NoError(t, router.lifecycleFrame(sender, frame("event-1", 2, "started")))

	findEvent := func(name string) map[string]any {
		for _, event := range events {
			if event["event"] == name {
				return event
			}
		}
		t.Fatalf("missing event %q", name)
		return nil
	}
	ingested := findEvent("lifecycle_ingested")
	require.Equal(t, "parent-a", ingested["agent_id"])
	require.Equal(t, int64(1), ingested["client_sequence"])
	var persisted, acknowledged map[string]any
	for _, event := range events {
		if event["event"] == "lifecycle_persisted" && event["disposition"] == "applied" {
			persisted = event
		}
		if event["event"] == "lifecycle_acknowledged" && event["disposition"] == "applied" {
			acknowledged = event
		}
	}
	require.NotNil(t, persisted)
	require.Equal(t, int64(1), persisted["router_version"])
	require.NotNil(t, acknowledged)
	broadcast := findEvent("lifecycle_broadcast_failed")
	require.Equal(t, 2, broadcast["recipient_count"])
	require.Equal(t, 1, broadcast["failure_count"])
	require.Equal(t, "send_failed", broadcast["reason"])
	for _, event := range events {
		require.NotContains(t, event, "subagents")
		require.NotContains(t, event, "description")
		require.NotContains(t, event, "body")
	}
	require.NotEmpty(t, sent)
}

func TestLifecyclePersistenceFailureIsDiagnosticOnly(t *testing.T) {
	database := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), database, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "parent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	rawDB, err := sql.Open("sqlite", database)
	require.NoError(t, err)
	_, err = rawDB.Exec("DROP TABLE subagent_lifecycle_events")
	require.NoError(t, err)
	defer rawDB.Close()
	var events []map[string]any
	router, err := New(Options{Store: durable, EventLogger: func(event string, fields map[string]any) {
		events = append(events, map[string]any{"event": event, "fields": fields})
	}})
	require.NoError(t, err)
	sender := &session{router: router, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: "lease-a", sendOverride: func(map[string]any) error { return nil }}
	router.mu.Lock()
	router.sessions[sender.agentID] = sender
	router.mu.Unlock()
	frame := map[string]json.RawMessage{}
	for key, value := range map[string]any{
		"type": "subagent_lifecycle_snapshot", "frame_id": "frame-1", "agent_id": "parent-a", "instance_id": "instance-a", "parent_session_id": "session-a", "lease_id": "lease-a", "producer_epoch": "123e4567-e89b-42d3-a456-426614174000", "client_sequence": 1, "event_id": "snapshot-1", "subagents": []map[string]any{},
	} {
		raw, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		frame[key] = raw
	}
	require.Error(t, router.lifecycleFrame(sender, frame))
	found := false
	for _, event := range events {
		if event["event"] == "lifecycle_persist_failed" {
			fields := event["fields"].(map[string]any)
			require.Equal(t, "store_error", fields["reason"])
			found = true
		}
	}
	require.True(t, found)
}

func TestLifecycleAckFailureIsDiagnosticOnly(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	var events []map[string]any
	router, err := New(Options{Store: durable, EventLogger: func(event string, fields map[string]any) {
		events = append(events, map[string]any{"event": event, "fields": fields})
	}})
	require.NoError(t, err)
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "parent-a", InstanceID: "instance-a", LeaseID: "lease-a", LeaseDuration: time.Minute})
	require.NoError(t, err)
	sender := &session{router: router, agentID: "parent-a", instanceID: "instance-a", deliveryInstance: "instance-a", leaseID: "lease-a", sendOverride: func(map[string]any) error { return errors.New("ack write failed") }}
	router.mu.Lock()
	router.sessions[sender.agentID] = sender
	router.mu.Unlock()
	frame := map[string]json.RawMessage{}
	for key, value := range map[string]any{
		"type": "subagent_lifecycle_snapshot", "frame_id": "frame-1", "agent_id": "parent-a", "instance_id": "instance-a", "parent_session_id": "session-a", "lease_id": "lease-a", "producer_epoch": "123e4567-e89b-42d3-a456-426614174000", "client_sequence": 1, "event_id": "snapshot-1", "subagents": []map[string]any{},
	} {
		raw, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		frame[key] = raw
	}
	require.Error(t, router.lifecycleFrame(sender, frame))
	found := false
	for _, event := range events {
		if event["event"] == "lifecycle_ack_failed" {
			fields := event["fields"].(map[string]any)
			require.Equal(t, "send_failed", fields["reason"])
			found = true
		}
	}
	require.True(t, found)
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

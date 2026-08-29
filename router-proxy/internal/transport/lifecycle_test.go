package transport

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
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
	require.Equal(t, "router_started", events[0]["event"])
	require.Equal(t, "message_expired", events[1]["event"])
	require.Equal(t, "expired", events[1]["message_id"])
	require.Equal(t, "lifecycle_sweep_completed", events[2]["event"])
	require.Equal(t, 1, events[2]["expired_message_count"])
	require.Equal(t, 1, events[2]["stale_agent_count"])
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

func tinyPNG() []byte {
	return []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1}
}

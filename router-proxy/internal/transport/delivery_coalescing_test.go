package transport

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func testEnvelope(id, body string) protocol.Envelope {
	return protocol.Envelope{ID: id, From: "sender", To: "recipient", Body: body, CreatedAt: "2026-08-29T12:00:00.000Z"}
}

func TestDeliveryOfferCoalescesBurstIntoOneBatch(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	var offers atomic.Int32
	delivered := make(chan map[string]any, 1)
	session := newTransportSession(t, router, durable, "recipient", "instance", "lease", "agent", func(fields map[string]any) error {
		if fields["type"] == "message" {
			offers.Add(1)
			delivered <- fields
		}
		return nil
	})
	for index, body := range []string{"one", "two", "three"} {
		_, err := durable.Enqueue(context.Background(), testEnvelope("message-"+string(rune('1'+index)), body))
		require.NoError(t, err)
		router.offerAgent(session.agentID)
	}
	select {
	case fields := <-delivered:
		items, ok := fields["delivery_items"].([]protocol.DeliveryItem)
		require.True(t, ok)
		require.Len(t, items, 3)
	case <-time.After(time.Second):
		t.Fatal("coalesced delivery did not arrive")
	}
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, int32(1), offers.Load())
}

func TestDeliveryOfferCallbackUsesReplacementSession(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	oldDelivered := make(chan struct{}, 1)
	newDelivered := make(chan struct{}, 1)
	old := newTransportSession(t, router, durable, "recipient", "old", "lease", "agent", func(map[string]any) error {
		oldDelivered <- struct{}{}
		return nil
	})
	_, err := durable.Enqueue(context.Background(), testEnvelope("message-1", "body"))
	require.NoError(t, err)
	router.scheduleDeliveryOffer(old.agentID)
	replacement := &session{router: router, agentID: old.agentID, instanceID: old.instanceID, deliveryInstance: old.deliveryInstance, leaseID: old.leaseID, sendOverride: func(fields map[string]any) error {
		if fields["type"] == "message" {
			newDelivered <- struct{}{}
		}
		return nil
	}}
	router.mu.Lock()
	router.sessions[old.agentID] = replacement
	router.mu.Unlock()
	select {
	case <-newDelivered:
	case <-oldDelivered:
		t.Fatal("delivery callback used the replaced session")
	case <-time.After(time.Second):
		t.Fatal("replacement did not receive coalesced delivery")
	}
}

func TestFinalSessionCloseCancelsDeliveryOffer(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	var offers atomic.Int32
	session := newTransportSession(t, router, durable, "recipient", "instance", "lease", "agent", func(fields map[string]any) error {
		if fields["type"] == "message" {
			offers.Add(1)
		}
		return nil
	})
	router.scheduleDeliveryOffer(session.agentID)
	router.sessionClosed(session, true)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(0), offers.Load())
	router.deliveryOfferMu.Lock()
	require.Empty(t, router.pendingDeliveryOffers)
	router.deliveryOfferMu.Unlock()
}

func TestRouterShutdownClearsAndWaitsForDeliveryOffers(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	newTransportSession(t, router, durable, "recipient", "instance", "lease", "agent", func(map[string]any) error { return nil })
	router.scheduleDeliveryOffer("recipient")
	router.mu.Lock()
	delete(router.sessions, "recipient")
	router.mu.Unlock()
	require.NoError(t, router.Close(context.Background()))
	router.deliveryOfferMu.Lock()
	require.Empty(t, router.pendingDeliveryOffers)
	router.deliveryOfferMu.Unlock()
	require.NoError(t, durable.Close())
}

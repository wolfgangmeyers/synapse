package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// Exercise a durable write between a sampled projection and its next publication,
// with a separately reserved version in between. The published payload must be
// the latest committed state, not the stale sample relabeled with the new version.
func TestUILiveReservationInterleavedWithDurableChange(t *testing.T) {
	router, db, server := newUILiveTestRouter(t)
	ctx := context.Background()
	reserveUILiveAgent(t, db, "alpha")
	conn := dialUILive(t, server, "")
	defer conn.CloseNow()
	initial := readUILiveFrame(t, conn)
	readUILiveFrame(t, conn)

	sampled, err := router.uiLive.snapshot(ctx)
	require.NoError(t, err)
	reserved, err := db.ReserveProjectionVersion(ctx)
	require.NoError(t, err)
	require.NoError(t, db.MarkManagedFailed(ctx, "alpha", "FAIL", "failure"))
	require.NoError(t, router.uiLive.publishContext(ctx))
	frame := readUILiveFrame(t, conn)
	require.Equal(t, "managed_agents_snapshot", frame["type"])
	require.Greater(t, frame["projection_version"].(float64), float64(reserved))
	require.Greater(t, reserved, sampled.version)
	require.Equal(t, float64(sampled.version), initial["projection_version"])
	agents := frame["agents"].([]any)
	require.Len(t, agents, 1)
	require.Equal(t, "failed", agents[0].(map[string]any)["lifecycle_state"])
	entries, err := db.GetManagedCompleteHistory(ctx, "alpha")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// Block both peer write gates, then release exactly one. This does not rely on
// map iteration order: whichever peer receives the projection identifies itself.
func TestUILiveCancellationAfterOnePeerProjection(t *testing.T) {
	router, db, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	conns := []*websocket.Conn{dialUILive(t, server, ""), dialUILive(t, server, "")}
	for _, conn := range conns {
		defer conn.CloseNow()
		readUILiveFrame(t, conn)
		readUILiveFrame(t, conn)
	}
	require.Eventually(t, func() bool {
		router.uiLive.mu.Lock()
		defer router.uiLive.mu.Unlock()
		return len(router.uiLive.peers) == 2
	}, time.Second, time.Millisecond)
	router.uiLive.mu.Lock()
	peers := make([]*uiLiveClient, 0, 2)
	for peer := range router.uiLive.peers {
		peer.writeGate = make(chan struct{})
		peers = append(peers, peer)
	}
	router.uiLive.mu.Unlock()
	require.Len(t, peers, 2)
	require.NoError(t, db.MarkManagedFailed(context.Background(), "alpha", "FAIL", "failure"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- router.uiLive.publishContext(ctx) }()

	// An unbuffered gate lets the test observe which peer was selected first.
	selected := make(chan *uiLiveClient, 2)
	for _, peer := range peers {
		go func(peer *uiLiveClient) {
			select {
			case <-peer.writeGate:
				selected <- peer
			case <-ctx.Done():
			}
		}(peer)
	}
	var first *uiLiveClient
	select {
	case first = <-selected:
	case <-time.After(time.Second):
		t.Fatal("publication did not select a peer")
	}
	// The write completes before sendContext receives this release token.
	select {
	case first.writeGate <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("first projection write did not finish")
	}
	// Keep the second peer blocked: its gate receiver may already be waiting,
	// so do not provide its release token.
	frames := make(chan map[string]any, 2)
	for _, conn := range conns {
		go func(conn *websocket.Conn) {
			readCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			_, data, err := conn.Read(readCtx)
			if err == nil {
				var frame map[string]any
				if json.Unmarshal(data, &frame) == nil {
					frames <- frame
				}
			}
		}(conn)
	}
	select {
	case frame := <-frames:
		require.Equal(t, "managed_agent_delta", frame["type"])
	case <-time.After(time.Second):
		t.Fatal("first peer did not receive projection")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("publication did not stop")
	}
	entries, err := db.GetManagedCompleteHistory(context.Background(), "alpha")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Agent failed", entries[0].Message)
	router.uiLive.mu.Lock()
	remaining := len(router.uiLive.peers)
	router.uiLive.mu.Unlock()
	require.Zero(t, remaining)
	fresh := dialUILive(t, server, "")
	defer fresh.CloseNow()
	snapshot := readUILiveFrame(t, fresh)
	require.Equal(t, "failed", snapshot["agents"].([]any)[0].(map[string]any)["lifecycle_state"])
}

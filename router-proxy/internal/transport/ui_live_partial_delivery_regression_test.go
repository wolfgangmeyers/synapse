package transport

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestUILiveCancellationDisconnectsUnsentPeers(t *testing.T) {
	router, durable, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, durable, "alpha")
	first := dialUILive(t, server, "")
	defer first.CloseNow()
	second := dialUILive(t, server, "")
	defer second.CloseNow()
	initial := readUILiveFrame(t, first)
	readUILiveFrame(t, first)
	readUILiveFrame(t, second)
	readUILiveFrame(t, second)

	// Block every peer, so whichever map iteration selects first cannot send.
	router.uiLive.mu.Lock()
	for peer := range router.uiLive.peers {
		peer.writeGate <- struct{}{}
	}
	require.Len(t, router.uiLive.peers, 2)
	router.uiLive.mu.Unlock()
	require.NoError(t, durable.MarkManagedReady(context.Background(), "alpha"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- router.uiLive.publishContext(ctx) }()
	require.Eventually(t, func() bool {
		router.uiLive.mu.Lock()
		defer router.uiLive.mu.Unlock()
		return float64(router.uiLive.current.version) > initial["projection_version"].(float64)
	}, time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("publish did not stop")
	}
	for _, conn := range []*websocket.Conn{first, second} {
		readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err := conn.Read(readCtx)
		readCancel()
		require.Error(t, err)
		require.NotErrorIs(t, err, context.DeadlineExceeded)
	}
	// A retry need not resend an unchanged projection: reconnects get it whole.
	require.NoError(t, router.uiLive.publishContext(context.Background()))
	reconnected := dialUILive(t, server, "")
	defer reconnected.CloseNow()
	fresh := readUILiveFrame(t, reconnected)
	require.Equal(t, "managed_agents_snapshot", fresh["type"])
	require.Greater(t, fresh["projection_version"].(float64), initial["projection_version"].(float64))
}

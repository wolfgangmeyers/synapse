package transport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitForUILiveRegistration(t *testing.T, hub *uiLiveHub, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.peers) == count
	}, time.Second, time.Millisecond, "UI live peers did not register")
}

func TestUILiveCanceledProjectionPersistsHistory(t *testing.T) {
	router, db, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	conn := dialUILive(t, server, "")
	defer conn.CloseNow()
	readUILiveFrame(t, conn)
	readUILiveFrame(t, conn)
	waitForUILiveRegistration(t, router.uiLive, 1)
	router.uiLive.mu.Lock()
	for peer := range router.uiLive.peers {
		peer.writeGate <- struct{}{}
	}
	router.uiLive.mu.Unlock()
	require.NoError(t, db.MarkManagedFailed(context.Background(), "alpha", "FAIL", "failure"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- router.uiLive.publishContext(ctx) }()
	require.Eventually(t, func() bool {
		entries, err := db.GetManagedCompleteHistory(context.Background(), "alpha")
		return err == nil && len(entries) == 1
	}, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.NoError(t, router.uiLive.publishContext(context.Background()))
	entries, err := db.GetManagedCompleteHistory(context.Background(), "alpha")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Agent failed", entries[0].Message)
}

func TestUILiveCanceledHistoryDisconnectsPeer(t *testing.T) {
	router, db, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	conn := dialUILive(t, server, "")
	defer conn.CloseNow()
	readUILiveFrame(t, conn)
	readUILiveFrame(t, conn)
	waitForUILiveRegistration(t, router.uiLive, 1)
	router.uiLive.mu.Lock()
	for peer := range router.uiLive.peers {
		peer.writeGate <- struct{}{}
	}
	router.uiLive.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	router.uiLive.publishHistoryContext(ctx, "complete", map[string]any{"id": "complete:1", "agent_id": "alpha"})
	readCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, _, err := conn.Read(readCtx)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestUILiveReconnectRefreshesUnpublishedProjection(t *testing.T) {
	router, db, server := newUILiveTestRouter(t)
	reserveUILiveAgent(t, db, "alpha")
	first := dialUILive(t, server, "")
	defer first.CloseNow()
	initial := readUILiveFrame(t, first)
	readUILiveFrame(t, first)
	waitForUILiveRegistration(t, router.uiLive, 1)
	require.NoError(t, db.MarkManagedReady(context.Background(), "alpha"))
	second := dialUILive(t, server, "")
	defer second.CloseNow()
	fresh := readUILiveFrame(t, second)
	readUILiveFrame(t, second)
	require.Greater(t, fresh["projection_version"].(float64), initial["projection_version"].(float64))
	changed := readUILiveFrame(t, first)
	require.Equal(t, fresh["projection_version"], changed["projection_version"])
}

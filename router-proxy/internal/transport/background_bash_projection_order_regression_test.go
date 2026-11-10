package transport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A delayed publisher must not replace a projection already published by a
// newer, concurrent publisher.
func TestBackgroundProjectionDoesNotRegressPublishedVersion(t *testing.T) {
	router, db, _ := newUILiveTestRouter(t)
	old, err := db.ManagedAgentsProjectionSnapshot(context.Background())
	require.NoError(t, err)
	router.uiLive.mu.Lock()
	router.uiLive.current = &uiLiveProjection{version: old.ProjectionVersion + 10, agents: map[string]map[string]any{}}
	router.uiLive.mu.Unlock()
	router.uiLive.publish(context.Background())
	router.uiLive.mu.Lock()
	defer router.uiLive.mu.Unlock()
	require.Equal(t, old.ProjectionVersion+10, router.uiLive.current.version)
}

func TestBackgroundProjectionBlockedPublisherHonorsCancellation(t *testing.T) {
	router, _, _ := newUILiveTestRouter(t)
	router.uiLive.publishGate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- router.uiLive.publishContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("publication did not honor cancellation")
	}
	<-router.uiLive.publishGate
}

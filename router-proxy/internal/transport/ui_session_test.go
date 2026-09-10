package transport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestFailedUIRegistrationLeavesNoDurableOrInMemoryIdentity(t *testing.T) {
	durable, router := newTransportOrderingFixture(t)
	writeErr := errors.New("welcome frame failed")
	session := &session{
		router:       router,
		sendOverride: func(map[string]any) error { return writeErr },
		closeOverride: func(websocket.StatusCode, string) error {
			return nil
		},
	}
	frame := registrationFrameForOrdering("browser-label", "browser-instance", "user_interaction")
	require.ErrorIs(t, router.register(session, frame), writeErr)
	lease, err := durable.LeaseFor(context.Background(), "browser-label")
	require.NoError(t, err)
	require.Nil(t, lease)
	router.mu.RLock()
	_, retained := router.uiSessions[session]
	router.mu.RUnlock()
	require.False(t, retained)
}

func TestExpiredUISessionClosesAndLeavesNoInMemorySubscriber(t *testing.T) {
	_, router := newTransportOrderingFixture(t)
	errors := make(chan map[string]any, 1)
	closed := make(chan struct{}, 1)
	session := &session{
		router: router,
		sendOverride: func(fields map[string]any) error {
			errors <- fields
			return nil
		},
		closeOverride: func(websocket.StatusCode, string) error {
			closed <- struct{}{}
			return nil
		},
	}
	router.configureSession(session, registrationFrameForOrdering("web-ui", "browser-instance", "user_interaction"), "ui-lease", "browser-instance")
	now := router.now()
	session.setUILease(now.Add(-time.Second), now)
	router.mu.Lock()
	router.uiSessions[session] = struct{}{}
	router.mu.Unlock()

	router.closeExpiredUISessions(now)
	require.Equal(t, "lease_expired", (<-errors)["code"])
	select {
	case <-closed:
	default:
		t.Fatal("expired UI session was not closed")
	}

	// The socket reader performs this same cleanup after receiving the close.
	router.sessionClosed(session, true)
	router.mu.RLock()
	_, retained := router.uiSessions[session]
	router.mu.RUnlock()
	require.False(t, retained)
}

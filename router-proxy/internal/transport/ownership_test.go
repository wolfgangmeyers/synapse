package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestReplacementCloseCannotReleaseNewlyPublishedLease(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable})
	require.NoError(t, err)

	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{
		AgentID: "agent", InstanceID: "instance", LeaseID: "old-lease", LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	old := &session{
		router:        router,
		sendOverride:  func(map[string]any) error { return nil },
		closeOverride: func(websocket.StatusCode, string) error { return nil },
	}
	router.configureSession(old, registrationFrame("agent", "instance", "v1"), "old-lease", "instance")
	router.mu.Lock()
	router.sessions["agent"] = old
	router.convos["agent"] = map[string]*session{"instance": old}
	router.mu.Unlock()

	cleanupDone := make(chan struct{})
	registeredSendStarted := make(chan struct{})
	releaseRegisteredSend := make(chan struct{})
	newSession := &session{
		router: router,
		sendOverride: func(map[string]any) error {
			go func() {
				router.sessionClosed(old, true)
				close(cleanupDone)
			}()
			close(registeredSendStarted)
			<-releaseRegisteredSend
			return nil
		},
		closeOverride: func(websocket.StatusCode, string) error { return nil },
	}
	registerResult := make(chan error, 1)
	go func() { registerResult <- router.register(newSession, registrationFrame("agent", "instance", "v1")) }()
	<-registeredSendStarted
	select {
	case <-cleanupDone:
		t.Fatal("old socket cleanup ran before registration completed")
	default:
	}
	close(releaseRegisteredSend)
	require.NoError(t, <-registerResult)
	<-cleanupDone

	lease, err := durable.LeaseFor(context.Background(), "agent")
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.Equal(t, "old-lease", lease.LeaseID)
	router.mu.RLock()
	require.Same(t, newSession, router.sessions["agent"])
	require.Same(t, newSession, router.convos["agent"]["instance"])
	router.mu.RUnlock()
}

func registrationFrame(agentID, instanceID, conversationSync string) map[string]json.RawMessage {
	frame := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"type": "register", "frame_id": "register", "agent_id": agentID, "instance_id": instanceID,
		"conversation_sync": conversationSync,
	} {
		raw, _ := json.Marshal(value)
		frame[key] = raw
	}
	return frame
}

func TestRegisterWriteFailureRollsBackLeaseAndSessionOwnership(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	writeErr := errors.New("socket write failed")
	s := &session{router: router, sendOverride: func(map[string]any) error { return writeErr }, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	frame := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"type": "register", "frame_id": "register", "agent_id": "agent", "instance_id": "instance",
	} {
		raw, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		frame[key] = raw
	}

	require.ErrorIs(t, router.register(s, frame), writeErr)
	lease, err := durable.LeaseFor(context.Background(), "agent")
	require.NoError(t, err)
	require.Nil(t, lease)
	router.mu.RLock()
	_, hasSession := router.sessions["agent"]
	_, hasConversations := router.convos["agent"]
	router.mu.RUnlock()
	require.False(t, hasSession)
	require.False(t, hasConversations)
}

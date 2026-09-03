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

func TestConversationSyncRegistrationClearsModelBeforeRegistered(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "agent", InstanceID: "old-instance", LeaseID: "lease", LeaseDuration: time.Minute})
	require.NoError(t, err)

	old := &session{router: router, sendOverride: func(map[string]any) error { return nil }, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	router.configureSession(old, registrationFrame("agent", "old-instance", "v1"), "lease", "old-instance")
	router.mu.Lock()
	router.sessions["agent"] = old
	router.convos["agent"] = map[string]*session{"old-instance": old}
	router.models["agent"] = modelProjection{instanceID: "old-instance", leaseID: "lease", current: map[string]any{"provider": "old", "id": "stale"}}
	router.mu.Unlock()

	seenRegistered := make(chan bool, 1)
	replacement := &session{
		router: router,
		sendOverride: func(fields map[string]any) error {
			snapshot := router.connectedAgents(context.Background(), true)
			agent := snapshot["agents"].([]map[string]any)[0]
			_, hasCurrentModel := agent["current_model"]
			seenRegistered <- fields["type"] == "registered" && !hasCurrentModel
			return nil
		},
		closeOverride: func(websocket.StatusCode, string) error { return nil },
	}
	require.NoError(t, router.register(replacement, registrationFrame("agent", "new-instance", "v1")))
	require.True(t, <-seenRegistered)
}

func TestBlockedRegistrationDoesNotStopUnrelatedOwnership(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable})
	require.NoError(t, err)

	registrationStarted := make(chan struct{})
	releaseRegistration := make(chan struct{})
	blocked := &session{
		router: router,
		sendOverride: func(map[string]any) error {
			close(registrationStarted)
			<-releaseRegistration
			return nil
		},
		closeOverride: func(websocket.StatusCode, string) error { return nil },
	}
	blockedResult := make(chan error, 1)
	go func() { blockedResult <- router.register(blocked, registrationFrame("blocked", "instance", "")) }()
	<-registrationStarted

	other := &session{
		router:        router,
		sendOverride:  func(map[string]any) error { return nil },
		closeOverride: func(websocket.StatusCode, string) error { return nil },
	}
	otherResult := make(chan error, 1)
	go func() { otherResult <- router.register(other, registrationFrame("other", "instance", "")) }()
	select {
	case err := <-otherResult:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("unrelated registration waited on blocked socket I/O")
	}

	close(releaseRegistration)
	require.NoError(t, <-blockedResult)
}

func TestStaleCloseCannotReleaseReplacementLease(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	router, err := New(Options{Store: durable})
	require.NoError(t, err)
	_, err = durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: "agent", InstanceID: "instance", LeaseID: "old-lease", LeaseDuration: time.Minute})
	require.NoError(t, err)

	old := &session{router: router, sendOverride: func(map[string]any) error { return nil }, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	router.configureSession(old, registrationFrame("agent", "instance", ""), "old-lease", "instance")
	router.mu.Lock()
	router.sessions["agent"] = old
	router.mu.Unlock()

	registeredSendStarted := make(chan struct{})
	releaseRegisteredSend := make(chan struct{})
	cleanupDone := make(chan struct{})
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
	go func() { registerResult <- router.register(newSession, registrationFrame("agent", "instance", "")) }()
	<-registeredSendStarted
	select {
	case <-cleanupDone:
		t.Fatal("old socket cleanup ran before replacement registration completed")
	default:
	}
	close(releaseRegisteredSend)
	require.NoError(t, <-registerResult)
	<-cleanupDone

	lease, err := durable.LeaseFor(context.Background(), "agent")
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NotEqual(t, "old-lease", lease.LeaseID)
	require.Equal(t, "instance", lease.InstanceID)
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

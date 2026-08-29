package transport

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

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

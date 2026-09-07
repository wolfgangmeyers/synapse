package transport

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testSessionClock struct{ now time.Time }

func (c testSessionClock) Now() time.Time { return c.now }

func TestSessionRegistryStoresOnlyOpaqueProofsAndExpires(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{Clock: testSessionClock{now: time.Unix(100, 0)}, Random: bytes.NewReader(append(append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...), bytes.Repeat([]byte{3}, 32)...))})
	token, csrf, expires, err := registry.Issue()
	require.NoError(t, err)
	require.Len(t, token, 43)
	require.Len(t, csrf, 43)
	require.Equal(t, time.Unix(100, 0).UTC().Add(uiSessionLifetime), expires)
	session, err := registry.Authenticate(token)
	require.NoError(t, err)
	require.True(t, registry.ValidateCSRF(session, csrf))
	require.False(t, registry.ValidateCSRF(session, token))
	require.Equal(t, 1, registry.Count())
}

func TestSessionRegistryCapacityDoesNotEvictValidSession(t *testing.T) {
	registry := NewSessionRegistry(SessionRegistryOptions{Capacity: 1, Random: bytes.NewReader(bytes.Repeat([]byte{2}, 32*6))})
	token, _, _, err := registry.Issue()
	require.NoError(t, err)
	_, _, _, err = registry.Issue()
	require.Error(t, err)
	_, err = registry.Authenticate(token)
	require.NoError(t, err)
}

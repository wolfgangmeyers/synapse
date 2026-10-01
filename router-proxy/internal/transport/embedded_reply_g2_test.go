package transport

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/stretchr/testify/require"
)

// A completed SDK assistant, not HTTP acceptance or the inbound ACK, owns the reply.
func TestEmbeddedReplyHTTPCompletedSenderFetchRED(t *testing.T) {
	f := newEmbeddedHTTPG2Fixture(t)
	ctx := context.Background()
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", RequestID: "f1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, e := f.db.ManagedAgent(ctx, "mail-root")
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	id := "f2222222-2222-4222-8222-222222222222"
	payload := fmt.Sprintf(`{"id":%q,"from":"sender-one","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id)
	status, raw := f.call(http.MethodPost, "/v1/messages", "", payload)
	require.Equal(t, http.StatusCreated, status, string(raw))
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		h, e := f.db.ConversationHistory(ctx, "sender-one", "mail-root")
		return e == nil && len(h) == 2 && h[1].From == launched.Agent.AgentID && h[1].To == "sender-one" && h[1].Body == "answered"
	}, 3*time.Second, 10*time.Millisecond)
	f.manager.OfferDirect(launched.Agent.AgentID)
	status, raw = f.call(http.MethodPost, "/v1/messages", "", payload)
	require.Equal(t, http.StatusOK, status, string(raw))
	h, err := f.db.ConversationHistory(ctx, "sender-one", "mail-root")
	require.NoError(t, err)
	require.Len(t, h, 2)
	require.EqualValues(t, 1, f.calls.Load())
}

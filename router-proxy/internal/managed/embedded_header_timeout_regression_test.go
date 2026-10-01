package managed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Only the test boundary rewrites the pinned destination. The socket and its
// header deadline are the real router-owned transport, not a timed fake doer.
type failureLoopbackTransport struct {
	owned   *http.Transport
	target  *url.URL
	written chan struct{}
}

func (t failureLoopbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != embeddedCodexResponsesURL {
		return nil, errCodexTestDestination
	}
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			t.written <- struct{}{}
		}
	}})
	local := req.Clone(ctx)
	u := *req.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	local.URL = &u
	local.Host = t.target.Host
	return t.owned.RoundTrip(local)
}

func failureOwnedHTTP(t *testing.T, server *httptest.Server, budget time.Duration) failureLoopbackTransport {
	t.Helper()
	client := newEmbeddedCodexClient(nil)
	require.Zero(t, client.Timeout, "productive SSE has no blanket total deadline")
	owned, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Equal(t, embeddedCodexResponseHeaderTimeout, owned.ResponseHeaderTimeout)
	require.NotSame(t, http.DefaultTransport, owned)
	// Shorten only this private production clone for bounded automated tests.
	owned.ResponseHeaderTimeout = budget
	owned.Proxy = nil
	t.Cleanup(owned.CloseIdleConnections)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	return failureLoopbackTransport{owned: owned, target: target, written: make(chan struct{}, 4)}
}

func failureUseCodex(f *failureFixture, transport http.RoundTripper, refreshes *atomic.Int32) {
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	f.m.embeddedModelResolver = func(store.ManagedAgentRecord) *ai.Model { return &model }
	f.m.codexHTTPTransport = transport
	f.m.codexRefresh = func(context.Context, store.CodexCredential) (store.CodexCredential, error) {
		refreshes.Add(1)
		return store.CodexCredential{}, fmt.Errorf("unexpected fixture rotation")
	}
}

func TestEmbeddedCodexHeaderTimeoutReleasesLaneWithoutReplay(t *testing.T) {
	f := newFailureFixture(t)
	var sends, refreshes atomic.Int32
	headerRelease := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-headerRelease // deliberately accept the written body without headers
	}))
	defer server.Close()
	defer close(headerRelease)
	budget := 100 * time.Millisecond
	transport := failureOwnedHTTP(t, server, budget)
	failureUseCodex(f, transport, &refreshes)
	trace := make(chan embeddedTransportTrace, 4)
	f.m.embeddedStatusObserver = func(v embeddedTransportTrace) { trace <- v }
	owner := f.launch(t, "")
	id := "f1111111-1111-4111-8111-111111111111"
	done := make(chan error, 1)
	go func() {
		done <- f.m.AdmitPromptWithRequestID(context.Background(), owner.binding.AgentID, "header stall", id)
	}()
	select {
	case <-transport.written:
	case <-time.After(3 * time.Second):
		t.Fatal("request-written barrier not reached")
	}
	select {
	case err := <-done:
		require.ErrorContains(t, err, "turn outcome unknown")
	case <-time.After(10 * budget):
		t.Fatal("header withholding retained the agent lane")
	}
	f.laneBarrier(t, owner.binding.AgentID)
	require.ErrorContains(t, f.m.AdmitPromptWithRequestID(context.Background(), owner.binding.AgentID, "header stall", id), "embedded prompt outcome unknown", "same durable UNKNOWN request must be suppressed")
	require.EqualValues(t, 1, sends.Load())
	require.Zero(t, refreshes.Load(), "transport timeout is not a typed pre-output 401")
	select {
	case v := <-trace:
		require.Equal(t, embeddedTransportTrace{Ordinal: 1, Category: "timeout"}, v)
	default:
		t.Fatal("missing transport timeout trace")
	}
	select {
	case <-trace:
		t.Fatal("unexpected provider retry")
	default:
	}
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id=?`, owner.binding.AgentID))
	var state string
	require.NoError(t, f.raw.QueryRow(`SELECT state FROM embedded_prompt_requests WHERE request_id=?`, id).Scan(&state))
	require.Equal(t, "pending", state, "durable UNKNOWN claim must not be called complete")
	var body []byte
	require.NoError(t, f.raw.QueryRow(`SELECT body FROM embedded_entries WHERE agent_id=? AND kind='assistant'`, owner.binding.AgentID).Scan(&body))
	message, err := ai.UnmarshalMessage(body)
	require.NoError(t, err)
	assistant, ok := message.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopError, assistant.StopReason)
	require.Contains(t, assistant.ErrorMessage, "turn outcome unknown")
}

func TestEmbeddedCodexProductiveStreamOutlivesHeaderBudget(t *testing.T) {
	f := newFailureFixture(t)
	budget := 100 * time.Millisecond
	var sends, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"productive\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"item\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\ndata: {\"type\":\"response.content_part.added\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n")
		flusher.Flush() // prompt response headers, followed by ongoing progress
		for i := 0; i < 8; i++ {
			timer := time.NewTimer(budget / 3)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"a\"}\n\n")
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.done\",\"output_index\":0,\"content_index\":0,\"text\":\"aaaaaaaa\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"productive\",\"status\":\"completed\"}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()
	transport := failureOwnedHTTP(t, server, budget)
	failureUseCodex(f, transport, &refreshes)
	owner := f.launch(t, "")
	id := "f2222222-2222-4222-8222-222222222222"
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- f.m.AdmitPromptWithRequestID(context.Background(), owner.binding.AgentID, "productive stream", id)
	}()
	select {
	case <-transport.written:
	case <-time.After(3 * time.Second):
		t.Fatal("request not written")
	}
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("productive SSE did not settle")
	}
	require.Greater(t, time.Since(start), 2*budget)
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 1, sends.Load())
	require.Zero(t, refreshes.Load())
	var state string
	require.NoError(t, f.raw.QueryRow(`SELECT state FROM embedded_prompt_requests WHERE request_id=?`, id).Scan(&state))
	require.Equal(t, "complete", state)
	var body []byte
	require.NoError(t, f.raw.QueryRow(`SELECT body FROM embedded_entries WHERE agent_id=? AND kind='assistant'`, owner.binding.AgentID).Scan(&body))
	message, err := ai.UnmarshalMessage(body)
	require.NoError(t, err)
	assistant, ok := message.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, assistant.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: strings.Repeat("a", 8)}}, assistant.Content)
}

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
)

func TestManagedBridgeServerPhase2BComposedLoopback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer durable.Close()

	token := []byte(strings.Repeat("q", 32))
	verifier, err := managedbridge.NewVerifier(token)
	require.NoError(t, err)
	handler := NewManagedBridgeHandler(durable)
	server := httptest.NewServer(handler)
	defer server.Close()

	script := filepath.Join(t.TempDir(), "pi-helper.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done\n"), 0o700))
	config := managedbridge.Config{RouterURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/managed-bridge", TokenPath: filepath.Join(t.TempDir(), "unused-token"), Agent: "bridge.test", PiExecutable: script, PiDir: t.TempDir(), MaxStdoutLine: 4096, MaxStderrBytes: 4096, OutboxCapacity: 8, CommandWindow: 8, ReconnectInitial: time.Millisecond, ReconnectMax: 4 * time.Millisecond}
	bridge, err := managedbridge.NewBridge(config, verifier, managedbridge.Options{Random: func(int64) int64 { return 0 }})
	require.NoError(t, err)
	initial := managedbridgeToStoreFence(t, bridge.Fence())
	bridgeID := bridge.Fence().BridgeInstance
	operation := store.ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174290", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174291", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	agent := store.ManagedAgentRecord{AgentID: "bridge.test", Name: "bridge.test", SlotHeld: true, Cwd: t.TempDir(), SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: initial.PiSessionID, SessionEpoch: initial.SessionEpoch, ProcessPID: initial.ProcessPID, ProcessStartMarker: initial.ProcessStartMarker, ProcessIncarnation: initial.ProcessIncarnation, RuntimeGeneration: initial.RuntimeGeneration, ManifestRevision: initial.ManifestRevision, ManifestHash: initial.ManifestHash}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, operation))
	bytes, err := verifier.Bytes()
	require.NoError(t, err)
	_, err = durable.ReserveManagedBridge(ctx, store.ManagedBridgeReservation{AgentID: agent.AgentID, BridgeID: bridgeID, Verifier: bytes, Fence: initial})
	require.NoError(t, err)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- bridge.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("bridge did not stop")
		}
	}()

	waitUntil(t, 5*time.Second, func() bool {
		fence := managedbridgeToStoreFence(t, bridge.Fence())
		if fence.ProcessPID == initial.ProcessPID || fence.ProcessStartMarker == initial.ProcessStartMarker {
			return false
		}
		return durable.BindManagedBridgeRuntime(ctx, bridgeID, fence) == nil
	})
	waitUntil(t, 5*time.Second, func() bool { return bridge.Diagnostics().Connected })

	payload := []byte(`{"prompt":"phase2b"}`)
	hash, err := managedbridge.HashPayload(payload)
	require.NoError(t, err)
	fence := managedbridgeToStoreFence(t, bridge.Fence())
	_, err = durable.QueueManagedBridgeCommand(ctx, store.ManagedBridgeCommandInput{BridgeID: bridgeID, RequestID: "123e4567-e89b-42d3-a456-426614174292", Name: managedbridge.CommandPrompt, Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	handler.Wake(agent.AgentID, bridgeID)
	waitUntil(t, 5*time.Second, func() bool {
		cursors, cursorErr := durable.ManagedBridgeCursors(ctx, bridgeID)
		return cursorErr == nil && cursors.CommandAcked == 1 && cursors.EventAppliedNext == 2
	})
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.CommandNext)
	require.Equal(t, uint64(2), cursors.EventAdmittedNext)
	require.Equal(t, uint64(2), cursors.EventAppliedNext)
	eventPayload, err := durable.ReadManagedBridgeEventPayload(ctx, bridgeID, 1, fence)
	require.NoError(t, err)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal(eventPayload, &decoded))
	require.Equal(t, "phase2b", decoded["prompt"])
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("condition did not become true")
		}
	}
}

func managedbridgeFenceForTest(t *testing.T, agent string) managedbridge.RuntimeFence {
	t.Helper()
	sum := sha256.Sum256([]byte("mecha-managed-rpc-phase1"))
	return managedbridge.RuntimeFence{Agent: agent, BridgeInstance: "123e4567-e89b-42d3-a456-426614174293", PiSessionID: "123e4567-e89b-42d3-a456-426614174294", PiSessionEpoch: 1, RuntimeGeneration: 1, PiPID: 1, PiStartMarker: "pending", PiIncarnation: "00000000-0000-4000-8000-000000000000", ManifestRevision: 1, ManifestHash: hex.EncodeToString(sum[:])}
}

func managedbridgeToStoreFence(t *testing.T, fence managedbridge.RuntimeFence) store.ManagedRuntimeFence {
	t.Helper()
	return store.ManagedRuntimeFence{AgentID: fence.Agent, PiSessionID: fence.PiSessionID, SessionEpoch: int64(fence.PiSessionEpoch), ProcessPID: int(fence.PiPID), ProcessStartMarker: fence.PiStartMarker, ProcessIncarnation: fence.PiIncarnation, RuntimeGeneration: strconv.FormatUint(fence.RuntimeGeneration, 10), ManifestRevision: int(fence.ManifestRevision), ManifestHash: fence.ManifestHash}
}

type phase2BFixture struct {
	ctx      context.Context
	path     string
	store    *store.Store
	verifier managedbridge.Verifier
	bridge   *managedbridge.Bridge
	handler  *ManagedBridgeHandler
	server   *httptest.Server
	selected atomic.Value
	bridgeID string
	agentID  string
}

func newPhase2BFixture(t *testing.T, options ManagedBridgeHandlerOptions) *phase2BFixture {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{}
	if opts.Migrations == nil {
		if err := storetest.CloneTemplateDB(t, path); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	durable, err := store.Open(ctx, path, opts)
	require.NoError(t, err)
	token := []byte(strings.Repeat("r", 32))
	verifier, err := managedbridge.NewVerifier(token)
	require.NoError(t, err)
	script := filepath.Join(t.TempDir(), "pi-helper.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done\n"), 0o700))
	fixture := &phase2BFixture{ctx: ctx, path: path, store: durable, verifier: verifier, agentID: "bridge.test", bridgeID: "123e4567-e89b-42d3-a456-426614174280"}
	handler := NewManagedBridgeHandler(durable, options)
	fixture.handler = handler
	fixture.selected.Store(handler)
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		fixture.selected.Load().(*ManagedBridgeHandler).ServeHTTP(w, req)
	}))
	config := managedbridge.Config{RouterURL: "ws" + strings.TrimPrefix(fixture.server.URL, "http") + "/v1/managed-bridge", TokenPath: filepath.Join(t.TempDir(), "unused-token"), Agent: fixture.agentID, PiExecutable: script, PiDir: t.TempDir(), MaxStdoutLine: 4096, MaxStderrBytes: 4096, OutboxCapacity: 8, CommandWindow: 8, ReconnectInitial: time.Millisecond, ReconnectMax: 4 * time.Millisecond}
	fixture.bridge, err = managedbridge.NewBridge(config, verifier, managedbridge.Options{Random: func(int64) int64 { return 0 }})
	require.NoError(t, err)
	fixture.bridgeID = fixture.bridge.Fence().BridgeInstance
	initial := managedbridgeToStoreFence(t, fixture.bridge.Fence())
	op := store.ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174281", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174282", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	agent := store.ManagedAgentRecord{AgentID: fixture.agentID, Name: fixture.agentID, SlotHeld: true, Cwd: t.TempDir(), SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: initial.PiSessionID, SessionEpoch: initial.SessionEpoch, ProcessPID: initial.ProcessPID, ProcessStartMarker: initial.ProcessStartMarker, ProcessIncarnation: initial.ProcessIncarnation, RuntimeGeneration: initial.RuntimeGeneration, ManifestRevision: initial.ManifestRevision, ManifestHash: initial.ManifestHash}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))
	bytes, err := verifier.Bytes()
	require.NoError(t, err)
	_, err = durable.ReserveManagedBridge(ctx, store.ManagedBridgeReservation{AgentID: fixture.agentID, BridgeID: fixture.bridgeID, Verifier: bytes, Fence: initial})
	require.NoError(t, err)
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = fixture.bridge.Stop(stopCtx)
		_ = fixture.handler.Close(stopCtx)
		fixture.server.Close()
		_ = fixture.store.Close()
	})
	return fixture
}

func (f *phase2BFixture) start(t *testing.T) {
	t.Helper()
	initial := f.bridge.Fence()
	go f.bridge.Run(f.ctx)
	waitUntil(t, 5*time.Second, func() bool {
		fence := managedbridgeToStoreFence(t, f.bridge.Fence())
		if int64(fence.ProcessPID) == initial.PiPID || fence.ProcessStartMarker == initial.PiStartMarker {
			return false
		}
		err := f.store.BindManagedBridgeRuntime(f.ctx, f.bridgeID, fence)
		if err != nil {
			t.Logf("bind pending: id=%s fence=%+v err=%v", f.bridgeID, fence, err)
		}
		return err == nil
	})
	t.Logf("bound; diagnostics=%+v", f.bridge.Diagnostics())
	waitUntil(t, 5*time.Second, func() bool { return f.bridge.Diagnostics().Connected })
}

func phase2BCommand(t *testing.T, f *phase2BFixture, fence managedbridge.RuntimeFence, requestID string) store.ManagedBridgeCommand {
	t.Helper()
	payload := []byte(`{"prompt":"critical"}`)
	hash, err := managedbridge.HashPayload(payload)
	require.NoError(t, err)
	command, err := f.store.QueueManagedBridgeCommand(f.ctx, store.ManagedBridgeCommandInput{BridgeID: f.bridgeID, RequestID: requestID, Name: managedbridge.CommandPrompt, Payload: payload, PayloadHash: hash, Fence: managedbridgeToStoreFence(t, fence)})
	require.NoError(t, err)
	f.handler.Wake(f.agentID, f.bridgeID)
	return command
}

func TestManagedBridgeServerPhase2BEventAdmissionDropReplaysExactlyOnce(t *testing.T) {
	admitted := make(chan struct{})
	var once sync.Once
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{afterEventAdmission: func(session *managedBridgeSession) {
		once.Do(func() { close(admitted); session.stop() })
	}})
	f.start(t)
	phase2BCommand(t, f, f.bridge.Fence(), "123e4567-e89b-42d3-a456-426614174283")
	select {
	case <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("event was not durably admitted")
	}
	debugCursors, debugErr := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
	t.Logf("after hook cursors=%+v err=%v diagnostics=%+v", debugCursors, debugErr, f.bridge.Diagnostics())
	waitUntil(t, 5*time.Second, func() bool {
		cursors, err := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
		return err == nil && cursors.EventAdmittedNext == 2 && cursors.EventAppliedNext == 2
	})
	cursorsAfterAdmission, err := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
	require.NoError(t, err)
	t.Logf("after admission cursors: %+v diagnostics: %+v", cursorsAfterAdmission, f.bridge.Diagnostics())
	require.Equal(t, uint64(2), cursorsAfterAdmission.EventAppliedNext)
	replay, err := f.store.ReplayManagedBridgeEvents(f.ctx, f.bridgeID, 1, managedbridgeToStoreFence(t, f.bridge.Fence()))
	require.NoError(t, err)
	require.Len(t, replay, 1)
	cursors, err := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.EventAdmittedNext)
	require.Equal(t, uint64(2), cursors.EventAppliedNext)
}

func TestManagedBridgeServerPhase2BReopenedStoreReconnectKeepsFenceAndDeliversPendingOnce(t *testing.T) {
	activated := make(chan *managedBridgeSession, 2)
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{afterActivate: func(session *managedBridgeSession) { activated <- session }})
	f.start(t)
	select {
	case <-activated:
	case <-time.After(5 * time.Second):
		t.Fatal("initial bridge session was not activated")
	}
	before := f.bridge.Fence()
	oldHandler := f.handler
	oldStore := f.store
	require.NoError(t, oldHandler.Close(context.Background()))
	require.NoError(t, oldStore.Close())
	reopened, err := store.Open(f.ctx, f.path, store.Options{})
	require.NoError(t, err)
	replacement := NewManagedBridgeHandler(reopened, ManagedBridgeHandlerOptions{afterActivate: func(session *managedBridgeSession) { activated <- session }})
	f.store = reopened
	f.handler = replacement
	f.selected.Store(replacement)
	select {
	case <-activated:
	case <-time.After(5 * time.Second):
		t.Fatalf("replacement bridge session was not activated: diagnostics=%+v", f.bridge.Diagnostics())
	}
	phase2BCommand(t, f, before, "123e4567-e89b-42d3-a456-426614174284")
	waitUntil(t, 5*time.Second, func() bool {
		cursors, cursorErr := reopened.ManagedBridgeCursors(f.ctx, f.bridgeID)
		return cursorErr == nil && cursors.CommandAcked == 1 && cursors.EventAppliedNext == 2
	})
	after := f.bridge.Fence()
	require.Equal(t, before, after)
}

func rawPhase2BHandshake(t *testing.T, endpoint string, verifier managedbridge.Verifier, fence managedbridge.RuntimeFence) (*websocket.Conn, managedbridge.PostAuth) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	require.NoError(t, err)
	read := func() any {
		kind, data, readErr := conn.Read(ctx)
		require.NoError(t, readErr)
		require.Equal(t, websocket.MessageText, kind)
		frame, decodeErr := managedbridge.DecodeFrame(data)
		require.NoError(t, decodeErr)
		return frame
	}
	challenge, ok := read().(managedbridge.Challenge)
	require.True(t, ok)
	auth, err := managedbridge.AuthenticateFor(verifier, challenge, fence)
	require.NoError(t, err)
	authData, err := managedbridge.EncodeFrame(auth)
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, authData))
	accepted, ok := read().(managedbridge.Authenticated)
	require.True(t, ok)
	require.NoError(t, managedbridge.VerifyAuthenticated(verifier, challenge, auth, accepted))
	post := managedbridge.PostAuth{Agent: auth.Agent, BridgeInstance: auth.BridgeInstance, RouterInstanceNonce: auth.RouterInstanceNonce, ConnectionNonce: auth.ConnectionNonce, Fence: auth.Fence}
	resume := managedbridge.Resume{PostAuth: post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameResume), CommandCursor: accepted.CommandCursor, EventCursor: accepted.EventCursor}
	resumeData, err := managedbridge.EncodeFrame(resume)
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, websocket.MessageText, resumeData))
	return conn, post
}

func readPhase2BFrame(t *testing.T, conn *websocket.Conn) any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kind, data, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, kind)
	frame, err := managedbridge.DecodeFrame(data)
	require.NoError(t, err)
	return frame
}

func TestManagedBridgeServerPhase2BWinnerFencesStaleSocket(t *testing.T) {
	activated := make(chan *managedBridgeSession, 2)
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{afterActivate: func(session *managedBridgeSession) { activated <- session }})
	endpoint := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/managed-bridge"
	start := make(chan struct{})
	type result struct {
		conn *websocket.Conn
		post managedbridge.PostAuth
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			conn, post := rawPhase2BHandshake(t, endpoint, f.verifier, f.bridge.Fence())
			results <- result{conn: conn, post: post}
		}()
	}
	close(start)
	first := <-activated
	second := <-activated
	var left, right result
	for range 2 {
		value := <-results
		if value.post.ConnectionNonce == first.post.ConnectionNonce {
			left = value
		} else {
			right = value
		}
	}
	defer left.conn.Close(websocket.StatusNormalClosure, "done")
	defer right.conn.Close(websocket.StatusNormalClosure, "done")
	current := func() *managedBridgeSession {
		f.handler.registry.mu.Lock()
		defer f.handler.registry.mu.Unlock()
		return f.handler.registry.current[f.store][f.bridgeID]
	}
	require.Same(t, second, current())
	require.NotSame(t, first, current())

	payload := []byte(`{"value":true}`)
	hash, err := managedbridge.HashPayload(payload)
	require.NoError(t, err)
	_, err = f.store.QueueManagedBridgeCommand(f.ctx, store.ManagedBridgeCommandInput{BridgeID: f.bridgeID, RequestID: "123e4567-e89b-42d3-a456-426614174285", Name: managedbridge.CommandPrompt, Payload: payload, PayloadHash: hash, Fence: managedbridgeToStoreFence(t, f.bridge.Fence())})
	require.NoError(t, err)
	staleAck := managedbridge.Ack{PostAuth: left.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameAck), Domain: managedbridge.DomainCommands, Seq: 1}
	staleEvent := managedbridge.Event{PostAuth: left.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameEvent), Seq: 1, ID: "123e4567-e89b-42d3-a456-426614174286", Name: managedbridge.EventMessage, Payload: payload, Hash: hash}
	require.ErrorIs(t, first.handleAck(staleAck), managedbridge.ErrStaleFrame)
	require.ErrorIs(t, first.handleEvent(staleEvent), managedbridge.ErrStaleFrame)
	cursors, err := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
	require.NoError(t, err)
	require.Equal(t, uint64(0), cursors.CommandAcked)
	require.Equal(t, uint64(1), cursors.EventAdmittedNext)

	winnerEvent := staleEvent
	winnerEvent.PostAuth = right.post
	require.NoError(t, right.conn.Write(context.Background(), websocket.MessageText, mustManagedFrame(t, winnerEvent)))
	frame := readPhase2BFrame(t, right.conn)
	ack, ok := frame.(managedbridge.Ack)
	require.True(t, ok)
	require.Equal(t, managedbridge.DomainEvents, ack.Domain)
	winnerAck := staleAck
	winnerAck.PostAuth = right.post
	require.NoError(t, right.conn.Write(context.Background(), websocket.MessageText, mustManagedFrame(t, winnerAck)))
	waitUntil(t, 5*time.Second, func() bool {
		cursors, cursorErr := f.store.ManagedBridgeCursors(f.ctx, f.bridgeID)
		return cursorErr == nil && cursors.CommandAcked == 1 && cursors.EventAppliedNext == 2
	})
}

func mustManagedFrame(t *testing.T, frame any) []byte {
	t.Helper()
	data, err := managedbridge.EncodeFrame(frame)
	require.NoError(t, err)
	return data
}

func TestManagedBridgeServerPhase2BRejectsPrivateUnsafeInputs(t *testing.T) {
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{})
	endpoint := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/managed-bridge"
	require.False(t, managedBridgeRequestAllowed(&http.Request{Method: http.MethodGet, URL: &url.URL{}, RemoteAddr: "10.0.0.2:1234"}))
	require.False(t, managedBridgeRequestAllowed(&http.Request{Method: http.MethodGet, URL: &url.URL{}, RemoteAddr: "[::ffff:127.0.0.1]:1234"}))
	originHeaders := http.Header{}
	originHeaders.Set("Origin", "https://private.example")
	_, response, err := websocket.Dial(context.Background(), endpoint, &websocket.DialOptions{HTTPHeader: originHeaders})
	require.Error(t, err)
	require.NotNil(t, response)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)
	if response.Body != nil {
		_ = response.Body.Close()
	}

	wrong, err := managedbridge.NewVerifier([]byte(strings.Repeat("w", 32)))
	require.NoError(t, err)
	conn, _, err := websocket.Dial(context.Background(), endpoint, nil)
	require.NoError(t, err)
	challengeKind, challengeData, err := conn.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, websocket.MessageText, challengeKind)
	challenge, ok := mustDecodeManagedChallenge(t, challengeData)
	require.True(t, ok)
	auth, err := managedbridge.AuthenticateFor(wrong, challenge, f.bridge.Fence())
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, mustManagedFrame(t, auth)))
	_, _, err = conn.Read(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "wwww")
	_ = conn.Close(websocket.StatusNormalClosure, "done")

	conn, _, err = websocket.Dial(context.Background(), endpoint, nil)
	require.NoError(t, err)
	_, challengeData, err = conn.Read(context.Background())
	require.NoError(t, err)
	challenge, ok = mustDecodeManagedChallenge(t, challengeData)
	require.True(t, ok)
	stale := f.bridge.Fence()
	stale.ManifestHash = strings.Repeat("c", 64)
	auth, err = managedbridge.AuthenticateFor(f.verifier, challenge, stale)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, mustManagedFrame(t, auth)))
	_, _, err = conn.Read(context.Background())
	require.Error(t, err)
	_ = conn.Close(websocket.StatusNormalClosure, "done")

	conn, _ = rawPhase2BHandshake(t, endpoint, f.verifier, f.bridge.Fence())
	require.NoError(t, conn.Write(context.Background(), websocket.MessageBinary, []byte("private binary")))
	_, _, err = conn.Read(context.Background())
	require.Error(t, err)
}

func mustDecodeManagedChallenge(t *testing.T, data []byte) (managedbridge.Challenge, bool) {
	t.Helper()
	frame, err := managedbridge.DecodeFrame(data)
	require.NoError(t, err)
	challenge, ok := frame.(managedbridge.Challenge)
	return challenge, ok
}

func TestManagedBridgeServerPhase2BReplayGapNeedsReconstruction(t *testing.T) {
	var once sync.Once
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{afterActivate: func(session *managedBridgeSession) {
		once.Do(func() {
			_ = session.writeCurrent(managedbridge.ReplayGap{PostAuth: session.post, Protocol: managedbridge.ProtocolVersion, Type: string(managedbridge.FrameReplayGap), Domain: managedbridge.DomainEvents, Expected: 2, Floor: 2})
		})
	}})
	f.start(t)
	waitUntil(t, 5*time.Second, func() bool { return f.bridge.Diagnostics().NeedsReconstruction })
	reconstruction, ok := f.bridge.Reconstruction()
	require.True(t, ok)
	require.Equal(t, managedbridge.DomainEvents, reconstruction.Domain)
	require.Equal(t, uint64(2), reconstruction.Expected)
	require.Equal(t, uint64(2), reconstruction.Floor)
	diagnostics := f.bridge.Diagnostics()
	require.Equal(t, uint64(0), diagnostics.CommandCursor)
	require.Equal(t, uint64(0), diagnostics.EventCursor)
}

func TestManagedBridgeServerPhase2BShutdownJoinsPumps(t *testing.T) {
	f := newPhase2BFixture(t, ManagedBridgeHandlerOptions{})
	f.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.handler.Close(ctx))
	require.NoError(t, f.bridge.Stop(ctx))
	f.handler.mu.Lock()
	require.Empty(t, f.handler.sessions)
	f.handler.mu.Unlock()
}
